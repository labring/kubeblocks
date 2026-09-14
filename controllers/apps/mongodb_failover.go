/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/

package apps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/controllers/apps/operations"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/multicluster"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	mongodbServiceKind            = "mongodb"
	autoFailoverNotReadyThreshold = 30 * time.Second
	autoFailoverRetryBase         = 30 * time.Second
	autoFailoverRetryMax          = 5 * time.Minute
	// Each failed attempt leaves an OpsRequest behind, so retrying forever
	// would both flood etcd and slow down every later List. Give up instead
	// and surface an event for a human to pick up.
	autoFailoverMaxAttempts = 5
	autoFailoverNamePrefix  = "kb-mongodb-auto-failover-"
)

// reconcileMongoDBAutoFailover creates a wildcard switchover request when the
// primary has been unavailable long enough and no pod is left serving the
// writable role. The existing switchover implementation remains responsible
// for safe step-down or MongoDB's native majority election.
func (r *ComponentReconciler) reconcileMongoDBAutoFailover(
	ctx context.Context,
	transCtx *componentTransformContext,
) (time.Duration, bool, error) {
	if transCtx == nil || transCtx.Component == nil || transCtx.Cluster == nil ||
		transCtx.CompDef == nil || transCtx.SynthesizeComponent == nil {
		return 0, false, nil
	}
	if transCtx.Component.DeletionTimestamp != nil || transCtx.Cluster.DeletionTimestamp != nil {
		return 0, false, nil
	}
	if !strings.EqualFold(transCtx.CompDef.Spec.ServiceKind, mongodbServiceKind) {
		return 0, false, nil
	}
	if transCtx.SynthesizeComponent.LifecycleActions == nil ||
		transCtx.SynthesizeComponent.LifecycleActions.Switchover == nil ||
		transCtx.SynthesizeComponent.LifecycleActions.Switchover.WithoutCandidate == nil {
		return 0, false, nil
	}

	targetRole := ""
	for _, role := range transCtx.SynthesizeComponent.Roles {
		if !role.Serviceable || !role.Writable {
			continue
		}
		if targetRole != "" {
			return 0, false, nil
		}
		targetRole = role.Name
	}
	if targetRole == "" {
		return 0, false, nil
	}

	podList := &corev1.PodList{}
	podLabels := constant.GetComponentWellKnownLabels(
		transCtx.Cluster.Name,
		transCtx.SynthesizeComponent.Name,
	)
	err := transCtx.Client.List(
		transCtx.Context,
		podList,
		client.InNamespace(transCtx.Cluster.Namespace),
		client.MatchingLabels(podLabels),
		multicluster.InDataContext(),
	)
	if err != nil {
		return 0, false, err
	}
	primaryPod, selection := operations.SelectAutoFailoverPrimaryPod(podList.Items, targetRole)
	if selection != operations.AutoFailoverPrimaryCurrent &&
		selection != operations.AutoFailoverPrimaryLastKnown {
		return 0, false, nil
	}
	if primaryPod.DeletionTimestamp != nil || primaryPod.Status.Phase != corev1.PodRunning {
		return 0, false, nil
	}

	readyCondition := intctrlutil.GetPodCondition(&primaryPod.Status, corev1.PodReady)
	if !autoFailoverPrimaryNotReady(readyCondition) {
		return 0, false, nil
	}
	notReadySince := readyCondition.LastTransitionTime
	if notReadySince.IsZero() {
		return 0, false, nil
	}
	remaining := autoFailoverNotReadyThreshold - time.Since(notReadySince.Time)
	if remaining > 0 {
		return remaining, false, nil
	}

	// The primary service selects on the role label and only publishes ready
	// pods, so a ready pod holding the role means traffic is still served.
	// Deriving that from the pods already listed here avoids depending on the
	// service name, which differs between the generated ComponentDefinition
	// and the services an old-API cluster actually owns.
	if hasReadyRolePod(podList.Items, targetRole) {
		return 0, false, nil
	}

	if autoFailoverSuppressed(transCtx) {
		return 0, false, nil
	}

	epoch := autoFailoverEpoch(
		transCtx.Component.Namespace,
		transCtx.Cluster.Name,
		transCtx.SynthesizeComponent.Name,
		primaryPod.UID,
		notReadySince,
	)
	opsList := &appsv1alpha1.OpsRequestList{}
	if err = r.List(ctx, opsList,
		client.InNamespace(transCtx.Component.Namespace),
		client.MatchingLabels{
			constant.AppInstanceLabelKey:    transCtx.Cluster.Name,
			constant.OpsRequestTypeLabelKey: string(appsv1alpha1.SwitchoverType),
		},
	); err != nil {
		return 0, false, err
	}
	state := evalAutoFailoverAttempts(
		opsList.Items,
		transCtx.Cluster.Name,
		transCtx.SynthesizeComponent.Name,
		epoch,
	)
	if state.inFlight {
		// Re-check on a timer rather than relying only on a watch event: a
		// manually created switchover may carry no timeout and could stay
		// Running indefinitely.
		return autoFailoverRetryBase, false, nil
	}
	if state.settled {
		return 0, false, nil
	}
	if state.attempt >= autoFailoverMaxAttempts {
		if r.Recorder != nil {
			r.Recorder.Eventf(transCtx.Cluster, corev1.EventTypeWarning, "MongoDBAutoFailoverExhausted",
				"giving up automatic switchover of %s in component %s after %d attempts, manual intervention is required",
				primaryPod.Name, transCtx.SynthesizeComponent.Name, state.attempt)
		}
		return 0, false, nil
	}
	attempt := state.attempt
	if attempt > 0 && !state.lastFailedAt.IsZero() {
		retryAfter := autoFailoverRetryDelay(attempt - 1)
		if remaining := retryAfter - time.Since(state.lastFailedAt); remaining > 0 {
			return remaining, false, nil
		}
	}

	ops := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: transCtx.Component.Namespace,
			Name:      autoFailoverRequestName(epoch, attempt),
			Labels: map[string]string{
				constant.AppInstanceLabelKey:    transCtx.Cluster.Name,
				constant.OpsRequestTypeLabelKey: string(appsv1alpha1.SwitchoverType),
			},
			Annotations: map[string]string{
				operations.AutoFailoverAnnotation:              operations.AutoFailoverAnnotationValue,
				operations.AutoFailoverEpochAnnotation:         epoch,
				operations.AutoFailoverAttemptAnnotation:       strconv.Itoa(attempt),
				operations.AutoFailoverOldPrimaryPodAnnotation: primaryPod.Name,
				operations.AutoFailoverOldPrimaryUIDAnnotation: string(primaryPod.UID),
				operations.AutoFailoverNotReadySinceAnnotation: notReadySince.UTC().Format(time.RFC3339Nano),
			},
		},
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName: transCtx.Cluster.Name,
			Type:        appsv1alpha1.SwitchoverType,
			// A node-level failure keeps the cluster in Updating and stalls the
			// ops queue, so a queued switchover would never run. Force skips the
			// queue and the cluster-phase gate to restore the primary service.
			Force:          true,
			TimeoutSeconds: pointer.Int32(operations.AutoFailoverOpsTimeoutSeconds),
			SpecificOpsRequest: appsv1alpha1.SpecificOpsRequest{
				SwitchoverList: []appsv1alpha1.Switchover{{
					ComponentName: transCtx.SynthesizeComponent.Name,
					InstanceName:  operations.KBSwitchoverCandidateInstanceForAnyPod,
				}},
			},
		},
	}
	// The OpsRequest controller sets a plain owner reference to the same
	// cluster. Claiming a controller reference here would either be silently
	// overwritten by it, or fail with AlreadyOwnedError the day another
	// controller owns the request, which would block every failover. A plain
	// owner reference is all that garbage collection needs.
	if err = controllerutil.SetOwnerReference(transCtx.Cluster, ops, r.Scheme); err != nil {
		return 0, false, err
	}
	if err = r.Create(ctx, ops); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The API server is the idempotency boundary. The cache will observe
			// the existing request and its phase on a subsequent reconcile.
			return autoFailoverRetryBase, false, nil
		}
		return 0, false, err
	}
	if r.Recorder != nil {
		// The pod is read from the data context and may live in another cluster,
		// so anchor the event on the cluster and name the pod in the message.
		r.Recorder.Eventf(transCtx.Cluster, corev1.EventTypeWarning, "MongoDBAutoFailover",
			"created automatic switchover OpsRequest %s for NotReady primary %s", ops.Name, primaryPod.Name)
	}
	return 0, true, nil
}

// autoFailoverPrimaryNotReady reports whether the primary pod is not serving
// traffic. Both False and Unknown are accepted: a node-level failure usually
// leaves the pod Ready condition as Unknown (NodeLost) instead of False.
func autoFailoverPrimaryNotReady(readyCondition *corev1.PodCondition) bool {
	return readyCondition != nil &&
		(readyCondition.Status == corev1.ConditionFalse || readyCondition.Status == corev1.ConditionUnknown)
}

func autoFailoverEpoch(
	namespace string,
	clusterName string,
	componentName string,
	podUID types.UID,
	notReadySince metav1.Time,
) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s",
		namespace,
		clusterName,
		componentName,
		podUID,
		notReadySince.UTC().Format(time.RFC3339Nano),
	)
}

func autoFailoverRequestName(epoch string, attempt int) string {
	sum := sha256.Sum256([]byte(epoch))
	return fmt.Sprintf("%s%x-%d", autoFailoverNamePrefix, sum[:8], attempt)
}

func autoFailoverRetryDelay(failedAttempt int) time.Duration {
	delay := autoFailoverRetryBase
	for i := 0; i < failedAttempt && delay < autoFailoverRetryMax; i++ {
		delay *= 2
	}
	if delay > autoFailoverRetryMax {
		return autoFailoverRetryMax
	}
	return delay
}

// hasReadyRolePod reports whether any pod still carries the role and is ready,
// which is exactly what the role-selecting service would publish as an
// endpoint.
func hasReadyRolePod(pods []corev1.Pod, targetRole string) bool {
	for i := range pods {
		pod := &pods[i]
		if !strings.EqualFold(pod.Labels[constant.RoleLabelKey], targetRole) {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		readyCondition := intctrlutil.GetPodCondition(&pod.Status, corev1.PodReady)
		if readyCondition != nil && readyCondition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// autoFailoverSuppressed reports whether failing over is pointless rather than
// merely inconvenient. A planned operation that is rolling the primary already
// carries a DeletionTimestamp on that pod, so it is filtered earlier; checking
// the cluster ops queue here would instead let a stuck operation block recovery
// forever.
func autoFailoverSuppressed(transCtx *componentTransformContext) bool {
	return transCtx != nil &&
		transCtx.SynthesizeComponent != nil &&
		isCompStopped(transCtx.SynthesizeComponent)
}

// autoFailoverState summarises the switchover requests already issued for a
// component, so the caller can decide between waiting, giving up and issuing a
// new attempt.
type autoFailoverState struct {
	// inFlight means some switchover is still running for this component,
	// including one a human started, so nothing new may be issued.
	inFlight bool
	// settled means this exact failure was already resolved or deliberately
	// cancelled, and must not be retried under the same epoch.
	settled bool
	// attempt is the number to use for the next request, and doubles as the
	// count of attempts already made against this epoch.
	attempt      int
	lastFailedAt time.Time
}

func evalAutoFailoverAttempts(
	items []appsv1alpha1.OpsRequest,
	clusterName string,
	componentName string,
	epoch string,
) autoFailoverState {
	var state autoFailoverState
	for i := range items {
		ops := &items[i]
		if !sameComponentSwitchover(ops, clusterName, componentName) {
			continue
		}
		switch ops.Status.Phase {
		case "", appsv1alpha1.OpsPendingPhase, appsv1alpha1.OpsCreatingPhase,
			appsv1alpha1.OpsRunningPhase, appsv1alpha1.OpsCancellingPhase:
			state.inFlight = true
			return state
		}
		if ops.Annotations[operations.AutoFailoverAnnotation] != operations.AutoFailoverAnnotationValue ||
			ops.Annotations[operations.AutoFailoverEpochAnnotation] != epoch {
			continue
		}
		switch ops.Status.Phase {
		case appsv1alpha1.OpsSucceedPhase, appsv1alpha1.OpsCancelledPhase:
			state.settled = true
			return state
		}
		opsAttempt, parseErr := strconv.Atoi(ops.Annotations[operations.AutoFailoverAttemptAnnotation])
		if parseErr != nil || opsAttempt < 0 {
			opsAttempt = 0
		}
		if opsAttempt >= state.attempt {
			state.attempt = opsAttempt + 1
			state.lastFailedAt = ops.Status.CompletionTimestamp.Time
			if state.lastFailedAt.IsZero() {
				state.lastFailedAt = ops.CreationTimestamp.Time
			}
		}
	}
	return state
}

func sameComponentSwitchover(
	ops *appsv1alpha1.OpsRequest,
	clusterName string,
	componentName string,
) bool {
	if ops.Spec.ClusterName != clusterName {
		return false
	}
	for _, switchover := range ops.Spec.SwitchoverList {
		if switchover.ComponentName == componentName {
			return true
		}
	}
	return false
}
