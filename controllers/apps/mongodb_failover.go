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
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/multicluster"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	mongodbServiceKind            = "mongodb"
	autoFailoverNotReadyThreshold = 30 * time.Second
	autoFailoverRetryBase         = 30 * time.Second
	autoFailoverRetryMax          = 5 * time.Minute
	autoFailoverNamePrefix        = "kb-mongodb-auto-failover-"
)

// reconcileMongoDBAutoFailover creates a wildcard switchover request when the
// primary has been unavailable long enough and its primary service has no
// ready endpoints. The existing switchover implementation remains responsible
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

	serviceName := primaryServiceName(transCtx.SynthesizeComponent, targetRole)
	if serviceName == "" {
		return 0, false, nil
	}
	endpoints := &corev1.Endpoints{}
	if err = transCtx.Client.Get(transCtx.Context, types.NamespacedName{
		Namespace: transCtx.Component.Namespace,
		Name:      serviceName,
	}, endpoints, multicluster.InDataContext()); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if readyEndpointCount(endpoints) != 0 {
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
	attempt := 0
	var lastFailedAt time.Time
	for i := range opsList.Items {
		ops := &opsList.Items[i]
		if !sameComponentSwitchover(
			ops,
			transCtx.Cluster.Name,
			transCtx.SynthesizeComponent.Name,
		) {
			continue
		}
		switch ops.Status.Phase {
		case "", appsv1alpha1.OpsPendingPhase, appsv1alpha1.OpsCreatingPhase,
			appsv1alpha1.OpsRunningPhase, appsv1alpha1.OpsCancellingPhase:
			return 0, false, nil
		}
		if ops.Annotations[operations.AutoFailoverAnnotation] != operations.AutoFailoverAnnotationValue ||
			ops.Annotations[operations.AutoFailoverEpochAnnotation] != epoch {
			continue
		}
		if ops.Status.Phase == appsv1alpha1.OpsSucceedPhase {
			return 0, false, nil
		}
		opsAttempt, parseErr := strconv.Atoi(ops.Annotations[operations.AutoFailoverAttemptAnnotation])
		if parseErr != nil || opsAttempt < 0 {
			opsAttempt = 0
		}
		if opsAttempt >= attempt {
			attempt = opsAttempt + 1
			lastFailedAt = ops.Status.CompletionTimestamp.Time
			if lastFailedAt.IsZero() {
				lastFailedAt = ops.CreationTimestamp.Time
			}
		}
	}
	if attempt > 0 && !lastFailedAt.IsZero() {
		retryAfter := autoFailoverRetryDelay(attempt - 1)
		if remaining := retryAfter - time.Since(lastFailedAt); remaining > 0 {
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
				operations.AutoFailoverAnnotation:               operations.AutoFailoverAnnotationValue,
				operations.AutoFailoverEpochAnnotation:          epoch,
				operations.AutoFailoverAttemptAnnotation:        strconv.Itoa(attempt),
				operations.AutoFailoverOldPrimaryPodAnnotation:  primaryPod.Name,
				operations.AutoFailoverOldPrimaryUIDAnnotation:  string(primaryPod.UID),
				operations.AutoFailoverNotReadySinceAnnotation:  notReadySince.UTC().Format(time.RFC3339Nano),
				operations.AutoFailoverPrimaryServiceAnnotation: serviceName,
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
	if err = controllerutil.SetControllerReference(transCtx.Cluster, ops, r.Scheme); err != nil {
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
		r.Recorder.Eventf(primaryPod, corev1.EventTypeWarning, "MongoDBAutoFailover",
			"created automatic switchover OpsRequest %s", ops.Name)
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

func primaryServiceName(synthesizeComp *component.SynthesizedComponent, targetRole string) string {
	var fallback string
	for _, service := range synthesizeComp.ComponentServices {
		if !strings.EqualFold(service.RoleSelector, targetRole) {
			continue
		}
		if service.ServiceName == "" {
			continue
		}
		if service.Name == "default" {
			return constant.GenerateComponentServiceName(
				synthesizeComp.ClusterName,
				synthesizeComp.Name,
				service.ServiceName,
			)
		}
		if fallback == "" {
			fallback = constant.GenerateComponentServiceName(
				synthesizeComp.ClusterName,
				synthesizeComp.Name,
				service.ServiceName,
			)
		}
	}
	return fallback
}

func readyEndpointCount(endpoints *corev1.Endpoints) int {
	count := 0
	for _, subset := range endpoints.Subsets {
		count += len(subset.Addresses)
	}
	return count
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
