/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package operations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/instanceset"
	"github.com/apecloud/kubeblocks/pkg/controller/multicluster"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	"github.com/apecloud/kubeblocks/pkg/dataprotection/utils"
)

// switchover constants
const (
	OpsReasonForSkipSwitchover = "SkipSwitchover"

	KBSwitchoverCandidateInstanceForAnyPod = "*"

	AutoFailoverAnnotation                      = "kubeblocks.io/auto-failover"
	AutoFailoverEpochAnnotation                 = "kubeblocks.io/auto-failover-epoch"
	AutoFailoverAttemptAnnotation               = "kubeblocks.io/auto-failover-attempt"
	AutoFailoverOldPrimaryPodAnnotation         = "kubeblocks.io/auto-failover-old-primary-pod"
	AutoFailoverOldPrimaryUIDAnnotation         = "kubeblocks.io/auto-failover-old-primary-uid"
	AutoFailoverNotReadySinceAnnotation         = "kubeblocks.io/auto-failover-not-ready-since"
	AutoFailoverPrimaryServiceAnnotation        = "kubeblocks.io/auto-failover-primary-service"
	AutoFailoverAnnotationValue                 = "true"
	AutoFailoverOpsTimeoutSeconds         int32 = 300
	autoFailoverValidationRequeueInterval       = time.Second

	KBJobTTLSecondsAfterFinished  = 5
	KBSwitchoverJobLabelKey       = "kubeblocks.io/switchover-job"
	KBSwitchoverJobLabelValue     = "kb-switchover-job"
	KBSwitchoverJobNamePrefix     = "kb-switchover-job"
	KBSwitchoverJobContainerName  = "kb-switchover-job-container"
	KBSwitchoverCheckJobKey       = "CheckJob"
	KBSwitchoverCheckRoleLabelKey = "CheckRoleLabel"

	KBSwitchoverCandidateName = "KB_SWITCHOVER_CANDIDATE_NAME"
	KBSwitchoverCandidateFqdn = "KB_SWITCHOVER_CANDIDATE_FQDN"

	// KBSwitchoverReplicationPrimaryPodIP and the others Replication and Consensus switchover constants will be deprecated in the future, use KBSwitchoverLeaderPodIP instead.
	KBSwitchoverReplicationPrimaryPodIP   = "KB_REPLICATION_PRIMARY_POD_IP"
	KBSwitchoverReplicationPrimaryPodName = "KB_REPLICATION_PRIMARY_POD_NAME"
	KBSwitchoverReplicationPrimaryPodFqdn = "KB_REPLICATION_PRIMARY_POD_FQDN"
	KBSwitchoverConsensusLeaderPodIP      = "KB_CONSENSUS_LEADER_POD_IP"
	KBSwitchoverConsensusLeaderPodName    = "KB_CONSENSUS_LEADER_POD_NAME"
	KBSwitchoverConsensusLeaderPodFqdn    = "KB_CONSENSUS_LEADER_POD_FQDN"

	KBSwitchoverLeaderPodIP   = "KB_LEADER_POD_IP"
	KBSwitchoverLeaderPodName = "KB_LEADER_POD_NAME"
	KBSwitchoverLeaderPodFqdn = "KB_LEADER_POD_FQDN"
)

type AutoFailoverPrimarySelection int

const (
	AutoFailoverPrimaryNotFound AutoFailoverPrimarySelection = iota
	AutoFailoverPrimaryCurrent
	AutoFailoverPrimaryLastKnown
	AutoFailoverPrimaryAmbiguous
)

// SelectAutoFailoverPrimaryPod prefers the current role label and falls back
// to the last role confirmed by the role event handler. The fallback keeps an
// unavailable former primary identifiable after its local role probe clears
// the live role label.
func SelectAutoFailoverPrimaryPod(
	pods []corev1.Pod,
	targetRole string,
) (*corev1.Pod, AutoFailoverPrimarySelection) {
	var current []*corev1.Pod
	for i := range pods {
		if strings.EqualFold(pods[i].Labels[constant.RoleLabelKey], targetRole) {
			current = append(current, &pods[i])
		}
	}
	switch len(current) {
	case 1:
		return current[0], AutoFailoverPrimaryCurrent
	case 0:
	default:
		return nil, AutoFailoverPrimaryAmbiguous
	}

	var lastKnown []*corev1.Pod
	for i := range pods {
		if strings.EqualFold(pods[i].Annotations[constant.LastKnownRoleAnnotationKey], targetRole) {
			lastKnown = append(lastKnown, &pods[i])
		}
	}
	switch len(lastKnown) {
	case 1:
		return lastKnown[0], AutoFailoverPrimaryLastKnown
	case 0:
		return nil, AutoFailoverPrimaryNotFound
	default:
		return nil, AutoFailoverPrimaryAmbiguous
	}
}

// needDoSwitchover checks whether we need to perform a switchover.
func needDoSwitchover(ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	synthesizedComp *component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover) (bool, error) {
	// get the Pod object whose current role label is primary
	pod, err := getServiceableNWritablePod(ctx, cli, *cluster, *synthesizedComp)
	if err != nil {
		return false, err
	}
	if pod == nil {
		return false, nil
	}
	switch switchover.InstanceName {
	case KBSwitchoverCandidateInstanceForAnyPod:
		return true, nil
	default:
		podList, err := component.GetComponentPodList(ctx, cli, *cluster, synthesizedComp.Name)
		if err != nil {
			return false, err
		}
		podParent, _ := instanceset.ParseParentNameAndOrdinal(pod.Name)
		siParent, o := instanceset.ParseParentNameAndOrdinal(switchover.InstanceName)
		if podParent != siParent || o < 0 || o >= len(podList.Items) {
			return false, errors.New("switchover.InstanceName is invalid")
		}
		// If the current instance is already the primary, then no switchover will be performed.
		if pod.Name == switchover.InstanceName {
			return false, nil
		}
	}
	return true, nil
}

func validateAutoFailover(
	ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	opsRequest *appsv1alpha1.OpsRequest,
	synthesizedComp *component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover,
) (bool, bool, *corev1.Pod, error) {
	if !isAutoFailoverOpsRequest(opsRequest) {
		return false, false, nil, nil
	}
	if switchover == nil || switchover.InstanceName != KBSwitchoverCandidateInstanceForAnyPod {
		return true, false, nil, intctrlutil.NewFatalError(
			"automatic failover requires a wildcard switchover",
		)
	}

	oldPrimaryName := opsRequest.Annotations[AutoFailoverOldPrimaryPodAnnotation]
	oldPrimaryUID := opsRequest.Annotations[AutoFailoverOldPrimaryUIDAnnotation]
	notReadySinceValue := opsRequest.Annotations[AutoFailoverNotReadySinceAnnotation]
	serviceName := opsRequest.Annotations[AutoFailoverPrimaryServiceAnnotation]
	if oldPrimaryName == "" || oldPrimaryUID == "" || notReadySinceValue == "" || serviceName == "" {
		return true, false, nil, intctrlutil.NewFatalError(
			"automatic failover validation annotations are incomplete",
		)
	}
	notReadySince, err := time.Parse(time.RFC3339Nano, notReadySinceValue)
	if err != nil {
		return true, false, nil, intctrlutil.NewFatalError(
			fmt.Sprintf("invalid automatic failover NotReady timestamp: %v", err),
		)
	}
	if remainingOpsRequestDuration(opsRequest) <= 0 {
		return true, false, nil, intctrlutil.NewFatalError(
			"automatic failover timed out",
		)
	}

	targetRole, err := serviceableNWritableRole(*synthesizedComp)
	if err != nil {
		return true, false, nil, intctrlutil.NewFatalError(err.Error())
	}
	dataCtx := ctx
	if placement := cluster.Annotations[constant.KBAppMultiClusterPlacementKey]; placement != "" {
		dataCtx = multicluster.IntoContext(ctx, placement)
	}
	podList := &corev1.PodList{}
	labels := constant.GetComponentWellKnownLabels(cluster.Name, synthesizedComp.Name)
	if err := cli.List(
		dataCtx,
		podList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(labels),
		multicluster.InDataContext(),
	); err != nil {
		return true, false, nil, err
	}
	primaryPod, selection := SelectAutoFailoverPrimaryPod(podList.Items, targetRole)
	switch selection {
	case AutoFailoverPrimaryNotFound:
		return true, false, nil, autoFailoverValidationWaitError(
			opsRequest,
			"waiting for the current primary role report",
		)
	case AutoFailoverPrimaryAmbiguous:
		return true, false, nil, autoFailoverValidationWaitError(
			opsRequest,
			"waiting for primary role reports to converge",
		)
	}

	if primaryPod.Name != oldPrimaryName || string(primaryPod.UID) != oldPrimaryUID {
		if primaryPod.DeletionTimestamp != nil || primaryPod.Status.Phase != corev1.PodRunning {
			return true, false, nil, autoFailoverValidationWaitError(
				opsRequest,
				"waiting for the new primary pod to run",
			)
		}
		readyCondition := intctrlutil.GetPodCondition(&primaryPod.Status, corev1.PodReady)
		if readyCondition == nil || readyCondition.Status != corev1.ConditionTrue {
			return true, false, nil, autoFailoverValidationWaitError(
				opsRequest,
				"waiting for the new primary pod to become ready",
			)
		}
		routed, err := autoFailoverPodHasReadyEndpoint(
			dataCtx,
			cli,
			cluster.Namespace,
			serviceName,
			primaryPod,
		)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, false, nil, autoFailoverValidationWaitError(
					opsRequest,
					"waiting for the primary service endpoints",
				)
			}
			return true, false, nil, err
		}
		if !routed {
			return true, false, nil, autoFailoverValidationWaitError(
				opsRequest,
				"waiting for the primary service to route to the new primary",
			)
		}
		// A different, ready primary behind the primary Service means MongoDB,
		// role reporting, and routing have converged. Do not step down the
		// healthy replacement primary.
		return true, false, nil, nil
	}
	if primaryPod.DeletionTimestamp != nil || primaryPod.Status.Phase != corev1.PodRunning {
		// The old primary can no longer run the step-down command. Keep the
		// request active while MongoDB's majority election and role reporting
		// converge instead of declaring success without observing a new primary.
		return true, false, nil, autoFailoverValidationWaitError(
			opsRequest,
			"waiting for a new primary after the old primary stopped",
		)
	}
	readyCondition := intctrlutil.GetPodCondition(&primaryPod.Status, corev1.PodReady)
	if readyCondition == nil ||
		(readyCondition.Status != corev1.ConditionFalse && readyCondition.Status != corev1.ConditionUnknown) {
		return true, false, nil, nil
	}
	// The transition timestamp may be rewritten by the node controller after a
	// node loss, so only verify it for the explicit False state.
	if readyCondition.Status == corev1.ConditionFalse && !readyCondition.LastTransitionTime.Time.Equal(notReadySince) {
		return true, false, nil, nil
	}

	endpointCount, err := autoFailoverReadyEndpointCount(
		dataCtx,
		cli,
		cluster.Namespace,
		serviceName,
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, false, nil, autoFailoverValidationWaitError(
				opsRequest,
				"waiting for the primary service endpoints",
			)
		}
		return true, false, nil, err
	}
	if endpointCount != 0 {
		return true, false, nil, nil
	}
	return true, true, primaryPod, nil
}

func isAutoFailoverOpsRequest(opsRequest *appsv1alpha1.OpsRequest) bool {
	return opsRequest != nil &&
		opsRequest.Annotations[AutoFailoverAnnotation] == AutoFailoverAnnotationValue
}

func autoFailoverValidationWaitError(opsRequest *appsv1alpha1.OpsRequest, message string) error {
	remaining := remainingOpsRequestDuration(opsRequest)
	if remaining <= 0 {
		return intctrlutil.NewFatalError(message + " before the automatic failover timeout")
	}
	requeueAfter := autoFailoverValidationRequeueInterval
	if remaining < requeueAfter {
		requeueAfter = remaining
	}
	return intctrlutil.NewRequeueError(requeueAfter, message)
}

func remainingOpsRequestDuration(opsRequest *appsv1alpha1.OpsRequest) time.Duration {
	if opsRequest == nil || opsRequest.Spec.TimeoutSeconds == nil || *opsRequest.Spec.TimeoutSeconds <= 0 {
		return 0
	}
	remaining := time.Duration(*opsRequest.Spec.TimeoutSeconds) * time.Second
	startedAt := opsRequest.Status.StartTimestamp.Time
	if startedAt.IsZero() {
		startedAt = opsRequest.CreationTimestamp.Time
	}
	if !startedAt.IsZero() {
		remaining -= time.Since(startedAt)
	}
	return remaining
}

func remainingOpsRequestSeconds(opsRequest *appsv1alpha1.OpsRequest) int64 {
	remaining := remainingOpsRequestDuration(opsRequest)
	if remaining <= 0 {
		return 0
	}
	return int64((remaining + time.Second - 1) / time.Second)
}

func autoFailoverReadyEndpointCount(
	ctx context.Context,
	cli client.Client,
	namespace string,
	serviceName string,
) (int, error) {
	endpoints := &corev1.Endpoints{}
	if err := cli.Get(
		ctx,
		types.NamespacedName{Namespace: namespace, Name: serviceName},
		endpoints,
		multicluster.InDataContext(),
	); err != nil {
		return 0, err
	}
	return readyEndpointCountForFailover(endpoints), nil
}

func autoFailoverPodHasReadyEndpoint(
	ctx context.Context,
	cli client.Client,
	namespace string,
	serviceName string,
	pod *corev1.Pod,
) (bool, error) {
	endpoints := &corev1.Endpoints{}
	if err := cli.Get(
		ctx,
		types.NamespacedName{Namespace: namespace, Name: serviceName},
		endpoints,
		multicluster.InDataContext(),
	); err != nil {
		return false, err
	}
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			if address.TargetRef != nil &&
				address.TargetRef.Name == pod.Name &&
				address.TargetRef.UID == pod.UID {
				return true, nil
			}
		}
	}
	return false, nil
}

func readyEndpointCountForFailover(endpoints *corev1.Endpoints) int {
	count := 0
	for _, subset := range endpoints.Subsets {
		count += len(subset.Addresses)
	}
	return count
}

// createSwitchoverJob creates a switchover job to do switchover.
func createSwitchoverJob(reqCtx intctrlutil.RequestCtx,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	opsRequest *appsv1alpha1.OpsRequest,
	synthesizedComp *component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover,
	primaryPod ...*corev1.Pod) error {
	switchoverJob, err := renderSwitchoverCmdJob(
		reqCtx.Ctx,
		cli,
		cluster,
		synthesizedComp,
		switchover,
		primaryPod...,
	)
	if err != nil {
		return err
	}
	if isAutoFailoverOpsRequest(opsRequest) {
		switchoverJob.Name = genSwitchoverJobNameForOpsRequest(
			cluster.Name,
			synthesizedComp.Name,
			cluster.Generation,
			opsRequest,
		)
		switchoverJob.Labels[constant.OpsRequestNameLabelKey] = opsRequest.Name
		if deadline := remainingOpsRequestSeconds(opsRequest); deadline > 0 {
			switchoverJob.Spec.ActiveDeadlineSeconds = pointer.Int64(deadline)
		}
	}
	// check the current generation switchoverJob whether exist
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: switchoverJob.Name}
	exists, _ := intctrlutil.CheckResourceExists(reqCtx.Ctx, cli, key, &batchv1.Job{})
	if !exists {
		// check the previous generation switchoverJob whether exist
		ml := getSwitchoverCmdJobLabel(cluster.Name, synthesizedComp.Name)
		previousJobs, err := component.GetJobWithLabels(reqCtx.Ctx, cli, cluster, ml)
		if err != nil {
			return err
		}
		if len(previousJobs) > 0 {
			// delete the previous generation switchoverJob
			reqCtx.Log.V(1).Info("delete previous generation switchoverJob", "job", previousJobs[0].Name)
			if err := component.CleanJobWithLabels(reqCtx.Ctx, cli, cluster, ml); err != nil {
				return err
			}
		}
		scheme, _ := appsv1alpha1.SchemeBuilder.Build()
		if err = utils.SetControllerReference(opsRequest, switchoverJob, scheme); err != nil {
			return err
		}
		// create the current generation switchoverJob
		if err := cli.Create(reqCtx.Ctx, switchoverJob); err != nil {
			return err
		}
		return nil
	}
	return nil
}

// checkPodRoleLabelConsistency checks whether the pod role label is consistent with the specified role label after switchover.
func checkPodRoleLabelConsistency(ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	synthesizedComp component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover,
	switchoverCondition *metav1.Condition) (bool, error) {
	if switchover == nil || switchoverCondition == nil {
		return false, nil
	}
	pod, err := getServiceableNWritablePod(ctx, cli, *cluster, synthesizedComp)
	if err != nil {
		return false, err
	}
	if pod == nil {
		return false, nil
	}
	var switchoverMessageMap map[string]SwitchoverMessage
	if err := json.Unmarshal([]byte(switchoverCondition.Message), &switchoverMessageMap); err != nil {
		return false, err
	}

	for _, switchoverMessage := range switchoverMessageMap {
		if switchoverMessage.GetComponentName() != switchover.GetComponentName() {
			continue
		}
		switch switchoverMessage.Switchover.InstanceName {
		case KBSwitchoverCandidateInstanceForAnyPod:
			if pod.Name != switchoverMessage.OldPrimary {
				return true, nil
			}
		default:
			if pod.Name == switchoverMessage.Switchover.InstanceName {
				return true, nil
			}
		}
	}
	return false, nil
}

// renderSwitchoverCmdJob renders and creates the switchover command jobs.
func renderSwitchoverCmdJob(ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	synthesizedComp *component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover,
	primaryPod ...*corev1.Pod) (*batchv1.Job, error) {
	if synthesizedComp.LifecycleActions == nil || synthesizedComp.LifecycleActions.Switchover == nil || switchover == nil {
		return nil, errors.New("switchover spec not found")
	}
	var pod *corev1.Pod
	if len(primaryPod) > 0 && primaryPod[0] != nil {
		pod = primaryPod[0]
	} else {
		var err error
		pod, err = getServiceableNWritablePod(ctx, cli, *cluster, *synthesizedComp)
		if err != nil {
			return nil, err
		}
		if pod == nil {
			return nil, errors.New("serviceable and writable pod not found")
		}
	}

	renderJobPodVolumes := func(scriptSpecSelectors []appsv1alpha1.ScriptSpecSelector) ([]corev1.Volume, []corev1.VolumeMount) {
		volumes := make([]corev1.Volume, 0)
		volumeMounts := make([]corev1.VolumeMount, 0)

		// find current pod's volume which mapped to configMapRefs
		findVolumes := func(tplSpec appsv1alpha1.ComponentTemplateSpec, scriptSpecSelector appsv1alpha1.ScriptSpecSelector) {
			if tplSpec.Name != scriptSpecSelector.Name {
				return
			}
			for _, podVolume := range pod.Spec.Volumes {
				if podVolume.Name == tplSpec.VolumeName {
					volumes = append(volumes, podVolume)
					break
				}
			}
		}

		// filter out the corresponding script configMap volumes from the volumes of the current leader pod based on the scriptSpecSelectors defined by the user.
		for _, scriptSpecSelector := range scriptSpecSelectors {
			for _, scriptSpec := range synthesizedComp.ScriptTemplates {
				findVolumes(scriptSpec, scriptSpecSelector)
			}
		}

		// find current pod's volumeMounts which mapped to volumes
		for _, volume := range volumes {
			for _, volumeMount := range pod.Spec.Containers[0].VolumeMounts {
				if volumeMount.Name == volume.Name {
					volumeMounts = append(volumeMounts, volumeMount)
					break
				}
			}
		}

		return volumes, volumeMounts
	}

	renderJob := func(switchoverSpec *appsv1alpha1.ComponentSwitchover, switchoverEnvs []corev1.EnvVar) (*batchv1.Job, error) {
		var (
			cmdExecutorConfig   *appsv1alpha1.Action
			scriptSpecSelectors []appsv1alpha1.ScriptSpecSelector
		)
		switch switchover.InstanceName {
		case KBSwitchoverCandidateInstanceForAnyPod:
			if switchoverSpec.WithoutCandidate != nil && switchoverSpec.WithoutCandidate.Exec != nil {
				cmdExecutorConfig = switchoverSpec.WithoutCandidate
			}
		default:
			if switchoverSpec.WithCandidate != nil && switchoverSpec.WithCandidate.Exec != nil {
				cmdExecutorConfig = switchoverSpec.WithCandidate
			}
		}
		scriptSpecSelectors = append(scriptSpecSelectors, switchoverSpec.ScriptSpecSelectors...)
		if cmdExecutorConfig == nil {
			return nil, errors.New("switchover exec action not found")
		}
		volumes, volumeMounts := renderJobPodVolumes(scriptSpecSelectors)

		// jobName named with generation to distinguish different switchover jobs.
		jobName := genSwitchoverJobName(cluster.Name, synthesizedComp.Name, cluster.Generation)
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      jobName,
				Labels:    getSwitchoverCmdJobLabel(cluster.Name, synthesizedComp.Name),
			},
			Spec: batchv1.JobSpec{
				BackoffLimit: pointer.Int32(2),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: cluster.Namespace,
						Name:      jobName,
					},
					Spec: corev1.PodSpec{
						Volumes:       volumes,
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:            KBSwitchoverJobContainerName,
								Image:           cmdExecutorConfig.Image,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Command:         cmdExecutorConfig.Exec.Command,
								Args:            cmdExecutorConfig.Exec.Args,
								Env:             switchoverEnvs,
								EnvFrom:         pod.Spec.Containers[0].EnvFrom,
								VolumeMounts:    volumeMounts,
							},
						},
					},
				},
			},
		}
		for i := range job.Spec.Template.Spec.Containers {
			intctrlutil.InjectZeroResourcesLimitsIfEmpty(&job.Spec.Template.Spec.Containers[i])
		}
		if err := component.BuildJobTolerations(job, cluster); err != nil {
			return nil, err
		}
		return job, nil
	}

	switchoverEnvs, err := buildSwitchoverEnvs(ctx, cli, cluster, synthesizedComp, switchover, pod)
	if err != nil {
		return nil, err
	}
	job, err := renderJob(synthesizedComp.LifecycleActions.Switchover, switchoverEnvs)
	if err != nil {
		return nil, err
	}
	return job, nil
}

// genSwitchoverJobName generates the switchover job name.
func genSwitchoverJobName(clusterName, componentName string, generation int64) string {
	return fmt.Sprintf("%s-%s-%s-%d", KBSwitchoverJobNamePrefix, clusterName, componentName, generation)
}

func genSwitchoverJobNameForOpsRequest(
	clusterName string,
	componentName string,
	generation int64,
	opsRequest *appsv1alpha1.OpsRequest,
) string {
	if !isAutoFailoverOpsRequest(opsRequest) {
		return genSwitchoverJobName(clusterName, componentName, generation)
	}
	sum := sha256.Sum256([]byte(opsRequest.Name))
	return fmt.Sprintf("%s-%x", KBSwitchoverJobNamePrefix, sum[:8])
}

// getSwitchoverCmdJobLabel gets the labels for job that execute the switchover commands.
func getSwitchoverCmdJobLabel(clusterName, componentName string) map[string]string {
	return map[string]string{
		constant.AppInstanceLabelKey:    clusterName,
		constant.KBAppComponentLabelKey: componentName,
		constant.AppManagedByLabelKey:   constant.AppName,
		KBSwitchoverJobLabelKey:         KBSwitchoverJobLabelValue,
	}
}

// buildSwitchoverCandidateEnv builds the candidate instance name environment variable for the switchover job.
func buildSwitchoverCandidateEnv(
	cluster *appsv1alpha1.Cluster,
	componentName string,
	switchover *appsv1alpha1.Switchover) []corev1.EnvVar {
	svcName := strings.Join([]string{cluster.Name, componentName, "headless"}, "-")
	if switchover == nil {
		return nil
	}
	if switchover.InstanceName == KBSwitchoverCandidateInstanceForAnyPod {
		return nil
	}
	return []corev1.EnvVar{
		{
			Name:  KBSwitchoverCandidateName,
			Value: switchover.InstanceName,
		},
		{
			Name:  KBSwitchoverCandidateFqdn,
			Value: fmt.Sprintf("%s.%s", switchover.InstanceName, svcName),
		},
	}
}

// buildSwitchoverEnvs builds the environment variables for the switchover job.
func buildSwitchoverEnvs(ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	synthesizeComp *component.SynthesizedComponent,
	switchover *appsv1alpha1.Switchover,
	primaryPod ...*corev1.Pod) ([]corev1.EnvVar, error) {
	if synthesizeComp == nil || synthesizeComp.LifecycleActions == nil ||
		synthesizeComp.LifecycleActions.Switchover == nil || switchover == nil {
		return nil, errors.New("switchover spec not found")
	}

	if synthesizeComp.LifecycleActions.Switchover.WithCandidate == nil && synthesizeComp.LifecycleActions.Switchover.WithoutCandidate == nil {
		return nil, errors.New("switchover spec withCandidate and withoutCandidate can't be nil at the same time")
	}

	// replace secret env and merge envs defined in SwitchoverSpec
	replaceSwitchoverConnCredentialEnv(synthesizeComp.LifecycleActions.Switchover, cluster.Name, synthesizeComp.Name)
	var switchoverEnvs []corev1.EnvVar
	switch switchover.InstanceName {
	case KBSwitchoverCandidateInstanceForAnyPod:
		if synthesizeComp.LifecycleActions.Switchover.WithoutCandidate != nil {
			switchoverEnvs = append(switchoverEnvs, synthesizeComp.LifecycleActions.Switchover.WithoutCandidate.Env...)
		}
	default:
		if synthesizeComp.LifecycleActions.Switchover.WithCandidate != nil {
			switchoverEnvs = append(switchoverEnvs, synthesizeComp.LifecycleActions.Switchover.WithCandidate.Env...)
		}
	}

	// inject the old primary info into the environment variable
	workloadEnvs, err := buildSwitchoverWorkloadEnvs(ctx, cli, cluster, synthesizeComp, primaryPod...)
	if err != nil {
		return nil, err
	}
	switchoverEnvs = append(switchoverEnvs, workloadEnvs...)

	// inject the candidate instance name into the environment variable if specify the candidate instance
	switchoverCandidateEnvs := buildSwitchoverCandidateEnv(cluster, synthesizeComp.Name, switchover)
	switchoverEnvs = append(switchoverEnvs, switchoverCandidateEnvs...)
	return switchoverEnvs, nil
}

// replaceSwitchoverConnCredentialEnv replaces the connection credential environment variables for the switchover job.
func replaceSwitchoverConnCredentialEnv(switchoverSpec *appsv1alpha1.ComponentSwitchover, clusterName, componentName string) {
	if switchoverSpec == nil {
		return
	}
	connCredentialMap := component.GetEnvReplacementMapForConnCredential(clusterName)
	replaceEnvVars := func(action *appsv1alpha1.Action) {
		if action != nil {
			action.Env = component.ReplaceSecretEnvVars(connCredentialMap, action.Env)
		}
	}
	replaceEnvVars(switchoverSpec.WithCandidate)
	replaceEnvVars(switchoverSpec.WithoutCandidate)
}

// buildSwitchoverWorkloadEnvs builds the replication or consensus workload environment variables for the switchover job.
func buildSwitchoverWorkloadEnvs(ctx context.Context,
	cli client.Client,
	cluster *appsv1alpha1.Cluster,
	synthesizeComp *component.SynthesizedComponent,
	primaryPod ...*corev1.Pod) ([]corev1.EnvVar, error) {
	var workloadEnvs []corev1.EnvVar
	var pod *corev1.Pod
	if len(primaryPod) > 0 && primaryPod[0] != nil {
		pod = primaryPod[0]
	} else {
		var err error
		pod, err = getServiceableNWritablePod(ctx, cli, *cluster, *synthesizeComp)
		if err != nil {
			return nil, err
		}
		if pod == nil {
			return nil, errors.New("serviceable and writable pod not found")
		}
	}
	svcName := strings.Join([]string{cluster.Name, synthesizeComp.Name, "headless"}, "-")

	workloadEnvs = append(workloadEnvs, []corev1.EnvVar{
		{
			Name:  KBSwitchoverLeaderPodIP,
			Value: pod.Status.PodIP,
		},
		{
			Name:  KBSwitchoverLeaderPodName,
			Value: pod.Name,
		},
		{
			Name:  KBSwitchoverLeaderPodFqdn,
			Value: fmt.Sprintf("%s.%s", pod.Name, svcName),
		},
	}...)

	// TODO(xingran): backward compatibility for the old env based on workloadType, it will be removed in the future
	workloadEnvs = append(workloadEnvs, []corev1.EnvVar{
		{
			Name:  KBSwitchoverReplicationPrimaryPodIP,
			Value: pod.Status.PodIP,
		},
		{
			Name:  KBSwitchoverReplicationPrimaryPodName,
			Value: pod.Name,
		},
		{
			Name:  KBSwitchoverReplicationPrimaryPodFqdn,
			Value: fmt.Sprintf("%s.%s", pod.Name, svcName),
		},
		{
			Name:  KBSwitchoverConsensusLeaderPodIP,
			Value: pod.Status.PodIP,
		},
		{
			Name:  KBSwitchoverConsensusLeaderPodName,
			Value: pod.Name,
		},
		{
			Name:  KBSwitchoverConsensusLeaderPodFqdn,
			Value: fmt.Sprintf("%s.%s", pod.Name, svcName),
		},
	}...)

	// add the first container's environment variables of the primary pod
	workloadEnvs = append(workloadEnvs, pod.Spec.Containers[0].Env...)
	return workloadEnvs, nil
}

// getServiceableNWritablePod returns the serviceable and writable pod of the component.
func getServiceableNWritablePod(ctx context.Context, cli client.Client, cluster appsv1alpha1.Cluster, synthesizeComp component.SynthesizedComponent) (*corev1.Pod, error) {
	targetRole, err := serviceableNWritableRole(synthesizeComp)
	if err != nil {
		return nil, err
	}

	podList, err := component.GetComponentPodListWithRole(ctx, cli, cluster, synthesizeComp.Name, targetRole)
	if err != nil {
		return nil, err
	}
	if len(podList.Items) != 1 {
		return nil, errors.New("component pod list is empty or has more than one serviceable and writable pod")
	}
	return &podList.Items[0], nil
}

func serviceableNWritableRole(synthesizeComp component.SynthesizedComponent) (string, error) {
	if synthesizeComp.Roles == nil {
		return "", errors.New("component does not support switchover")
	}
	targetRole := ""
	for _, role := range synthesizeComp.Roles {
		if !role.Serviceable || !role.Writable {
			continue
		}
		if targetRole != "" {
			return "", errors.New("component has more than one serviceable and writable role, does not support switchover")
		}
		targetRole = role.Name
	}
	if targetRole == "" {
		return "", errors.New("component has no serviceable and writable role, does not support switchover")
	}
	return targetRole, nil
}
