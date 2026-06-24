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

package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	ctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	postgreSQLReplicationReadyConditionType = "PostgreSQLReplicationReady"

	postgreSQLReplicationReasonReady                   = "ReplicationReady"
	postgreSQLReplicationReasonHealthCheckFailed       = "ReplicationHealthCheckFailed"
	postgreSQLReplicationReasonPrimaryNotFound         = "PrimaryNotFound"
	postgreSQLReplicationReasonPrimaryInRecovery       = "PrimaryInRecovery"
	postgreSQLReplicationReasonReplicasNotFound        = "ReplicasNotFound"
	postgreSQLReplicationReasonReplicasNotApplicable   = "ReplicasNotApplicable"
	postgreSQLReplicationReasonSlotInactive            = "SlotInactive"
	postgreSQLReplicationReasonWALReceiverMissing      = "WalReceiverMissing"
	postgreSQLReplicationReasonWALReceiverNotStreaming = "WalReceiverNotStreaming"
	postgreSQLReplicationReasonTimelineMismatch        = "TimelineMismatch"

	postgreSQLReplicationHealthRequeueInterval = 5 * time.Minute
	postgreSQLReplicationExecTimeout           = 5 * time.Second
	postgreSQLReplicationMaxJSONOutputBytes    = 64 * 1024

	postgreSQLReplicationAutoRebuildAnnotation         = "apps.kubeblocks.io/postgresql-replication-auto-rebuild"
	postgreSQLReplicationAutoRebuildWindowAnnotation   = "apps.kubeblocks.io/postgresql-replication-auto-rebuild-window"
	postgreSQLReplicationAutoRebuildCooldownAnnotation = "apps.kubeblocks.io/postgresql-replication-auto-rebuild-cooldown"

	postgreSQLReplicationDefaultAutoRebuildWindow   = 10 * time.Minute
	postgreSQLReplicationDefaultAutoRebuildCooldown = 30 * time.Minute

	postgreSQLReplicationAutoRebuildTimeoutSeconds              = 30 * 60
	postgreSQLReplicationAutoRebuildTTLSecondsAfterSucceed      = 60 * 60
	postgreSQLReplicationAutoRebuildTTLSecondsAfterUnsuccessful = 24 * 60 * 60

	postgreSQLReplicationAutoRebuildPendingConditionTypePrefix = "PostgreSQLReplicationAutoRebuildPending-"
	postgreSQLReplicationAutoRebuildConditionTypePrefix        = "PostgreSQLReplicationAutoRebuild-"
)

// componentPostgreSQLReplicationHealthTransformer reports PostgreSQL
// replication health from controller-side read-only SQL checks.
type componentPostgreSQLReplicationHealthTransformer struct {
	restConfig *rest.Config
	execRunner podExecRunner
}

var _ graph.Transformer = &componentPostgreSQLReplicationHealthTransformer{}

func (t *componentPostgreSQLReplicationHealthTransformer) Transform(
	ctx graph.TransformContext,
	dag *graph.DAG,
) error {
	transCtx, _ := ctx.(*componentTransformContext)
	if model.IsObjectDeleting(transCtx.ComponentOrig) {
		return nil
	}
	if !isPostgreSQLComponentForReplicationHealth(transCtx) {
		return nil
	}
	if transCtx.Component.Status.Phase != appsv1alpha1.RunningClusterCompPhase {
		return nil
	}

	runningITS, ok := transCtx.RunningWorkload.(*workloads.InstanceSet)
	if !ok || runningITS == nil || runningITS.Status.AvailableReplicas == 0 {
		return nil
	}

	now := time.Now()
	result := t.checkReplicationHealth(transCtx, runningITS)
	t.recordHealthResult(transCtx, result)
	if err := t.maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now); err != nil {
		return intctrlutil.NewDelayedRequeueError(postgreSQLReplicationHealthRequeueInterval, err.Error())
	}
	return postgreSQLReplicationHealthRequeue(result)
}

func isPostgreSQLComponentForReplicationHealth(transCtx *componentTransformContext) bool {
	return isPostgreSQLComponent(transCtx) ||
		(transCtx != nil &&
			transCtx.CompDef != nil &&
			isPostgreSQLLifecycleActions(transCtx.CompDef.Spec.LifecycleActions))
}

func isPostgreSQLBuiltinHandler(handler appsv1alpha1.BuiltinActionHandlerType) bool {
	switch strings.ToLower(strings.TrimSpace(string(handler))) {
	case string(appsv1alpha1.PostgresqlBuiltinActionHandler),
		string(appsv1alpha1.OfficialPostgresqlBuiltinActionHandler),
		string(appsv1alpha1.ApeCloudPostgresqlBuiltinActionHandler):
		return true
	default:
		return false
	}
}

func isPostgreSQLLifecycleActions(actions *appsv1alpha1.ComponentLifecycleActions) bool {
	if actions == nil {
		return false
	}
	handlers := []*appsv1alpha1.LifecycleActionHandler{
		actions.PostProvision,
		actions.PreTerminate,
		actions.MemberJoin,
		actions.MemberLeave,
		actions.Readonly,
		actions.Readwrite,
		actions.DataDump,
		actions.DataLoad,
		actions.Reconfigure,
		actions.AccountProvision,
	}
	for _, handler := range handlers {
		if handler != nil && handler.BuiltinHandler != nil && isPostgreSQLBuiltinHandler(*handler.BuiltinHandler) {
			return true
		}
	}
	return actions.RoleProbe != nil &&
		actions.RoleProbe.BuiltinHandler != nil &&
		isPostgreSQLBuiltinHandler(*actions.RoleProbe.BuiltinHandler)
}

func (t *componentPostgreSQLReplicationHealthTransformer) checkReplicationHealth(
	transCtx *componentTransformContext,
	runningITS *workloads.InstanceSet,
) postgreSQLReplicationHealthResult {
	pods, err := runningPostgreSQLPods(transCtx)
	if err != nil {
		return postgreSQLReplicationCheckFailed(fmt.Errorf("list component pods: %w", err))
	}

	leaderName := leaderMemberPodName(runningITS)
	if leaderName == "" {
		return postgreSQLReplicationHealthResult{
			Ready:   false,
			Reason:  postgreSQLReplicationReasonPrimaryNotFound,
			Message: "PostgreSQL primary pod is not found from InstanceSet member status",
		}
	}
	leaderPod := pods[leaderName]
	if leaderPod == nil {
		return postgreSQLReplicationHealthResult{
			Ready:   false,
			Reason:  postgreSQLReplicationReasonPrimaryNotFound,
			Message: fmt.Sprintf("PostgreSQL primary pod %q is not running", leaderName),
		}
	}

	replicaPods := replicaPodsForReplicationHealth(runningITS, pods)
	if len(replicaPods) == 0 {
		return postgreSQLReplicationHealthResult{
			Ready:   true,
			Reason:  postgreSQLReplicationReasonReplicasNotApplicable,
			Message: "PostgreSQL replication health check is not applicable because no running replica pods are found",
		}
	}

	runner := t.execRunner
	if runner == nil {
		runner = newKubePodExecRunner(t.restConfig)
	}
	leaderState, err := queryPostgreSQLReplicationLeader(transCtx.Context, runner, leaderPod)
	if err != nil {
		return postgreSQLReplicationCheckFailed(err)
	}

	replicaStates := make(map[string]postgreSQLReplicaState, len(replicaPods))
	for _, pod := range replicaPods {
		state, err := queryPostgreSQLReplicationReplica(transCtx.Context, runner, pod)
		if err != nil {
			return postgreSQLReplicationCheckFailed(err)
		}
		replicaStates[pod.Name] = state
	}
	return evaluatePostgreSQLReplicationHealth(leaderPod.Name, leaderState, replicaStates)
}

func (t *componentPostgreSQLReplicationHealthTransformer) recordHealthResult(
	transCtx *componentTransformContext,
	result postgreSQLReplicationHealthResult,
) {
	recordWarning := shouldRecordPostgreSQLReplicationWarning(transCtx.Component.Status.Conditions, result)
	status := metav1.ConditionFalse
	if result.Ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationReadyConditionType,
		Status:             status,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	if recordWarning && transCtx.EventRecorder != nil {
		transCtx.EventRecorder.Event(
			transCtx.Component,
			corev1.EventTypeWarning,
			result.Reason,
			result.Message,
		)
	}
	t.recordAutoRebuildPendingCondition(transCtx, result)
}

func (t *componentPostgreSQLReplicationHealthTransformer) recordAutoRebuildPendingCondition(
	transCtx *componentTransformContext,
	result postgreSQLReplicationHealthResult,
) {
	pendingType := postgreSQLReplicationAutoRebuildPendingConditionType(result.ReplicaPod)
	for _, condition := range transCtx.Component.Status.Conditions {
		if strings.HasPrefix(condition.Type, postgreSQLReplicationAutoRebuildPendingConditionTypePrefix) &&
			condition.Type != pendingType {
			meta.RemoveStatusCondition(&transCtx.Component.Status.Conditions, condition.Type)
		}
	}
	if !postgreSQLReplicationAutoRebuildAllowed(transCtx.Component) || !isPostgreSQLReplicationRebuildable(result) {
		if result.ReplicaPod != "" {
			meta.RemoveStatusCondition(
				&transCtx.Component.Status.Conditions,
				pendingType,
			)
		}
		return
	}
	if existing := meta.FindStatusCondition(transCtx.Component.Status.Conditions, pendingType); existing != nil &&
		(existing.Status != metav1.ConditionTrue ||
			existing.Reason != result.Reason) {
		meta.RemoveStatusCondition(&transCtx.Component.Status.Conditions, pendingType)
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               pendingType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             result.Reason,
		Message:            result.Message,
	})
}

func postgreSQLReplicationCheckFailed(err error) postgreSQLReplicationHealthResult {
	return postgreSQLReplicationHealthResult{
		Ready:   false,
		Reason:  postgreSQLReplicationReasonHealthCheckFailed,
		Message: fmt.Sprintf("PostgreSQL replication health check failed: %s", err.Error()),
	}
}

func shouldRecordPostgreSQLReplicationWarning(
	conditions []metav1.Condition,
	result postgreSQLReplicationHealthResult,
) bool {
	if result.Ready {
		return false
	}
	previous := meta.FindStatusCondition(conditions, postgreSQLReplicationReadyConditionType)
	return previous == nil || previous.Status != metav1.ConditionFalse || previous.Reason != result.Reason
}

func postgreSQLReplicationHealthRequeue(result postgreSQLReplicationHealthResult) error {
	return intctrlutil.NewDelayedRequeueError(postgreSQLReplicationHealthRequeueInterval, result.Message)
}

func (t *componentPostgreSQLReplicationHealthTransformer) maybeCreateAutoRebuildOpsRequest(
	transCtx *componentTransformContext,
	dag *graph.DAG,
	runningITS *workloads.InstanceSet,
	result postgreSQLReplicationHealthResult,
	now time.Time,
) error {
	if !postgreSQLReplicationAutoRebuildAllowed(transCtx.Component) ||
		!isPostgreSQLReplicationRebuildable(result) {
		return nil
	}
	if result.ReplicaPod == leaderMemberPodName(runningITS) {
		return nil
	}
	if !postgreSQLReplicationFailureWindowSatisfied(
		transCtx.Component.Status.Conditions,
		result,
		postgreSQLReplicationAutoRebuildWindow(transCtx.Component),
		now,
	) {
		return nil
	}
	if postgreSQLReplicationAutoRebuildInCooldown(
		transCtx.Component.Status.Conditions,
		result,
		postgreSQLReplicationAutoRebuildCooldown(transCtx.Component),
		now,
	) {
		return nil
	}
	if dag == nil || dag.Root() == nil {
		return fmt.Errorf("component dag is not initialized")
	}
	exists, err := t.hasRunningRebuildOpsRequest(transCtx)
	if err != nil {
		return fmt.Errorf("list running RebuildInstance OpsRequests: %w", err)
	}
	if exists || autoRebuildOpsRequestExistsInDAG(transCtx, dag) {
		return nil
	}

	graphCli, ok := transCtx.Client.(model.GraphClient)
	if !ok {
		return fmt.Errorf("component client does not support graph writes")
	}
	opsRequest := buildPostgreSQLReplicationAutoRebuildOpsRequest(transCtx, result.ReplicaPod, now)
	if err := intctrlutil.SetOwnerReference(transCtx.Cluster, opsRequest); err != nil {
		return fmt.Errorf("set owner reference for PostgreSQL replication auto rebuild OpsRequest: %w", err)
	}
	graphCli.Create(dag, opsRequest, inDataContext4G())
	recordPostgreSQLReplicationAutoRebuildCooldown(transCtx, result, opsRequest, now)
	if transCtx.EventRecorder != nil {
		transCtx.EventRecorder.Eventf(
			transCtx.Component,
			corev1.EventTypeNormal,
			"PostgreSQLReplicationAutoRebuild",
			"created RebuildInstance OpsRequest %q for PostgreSQL replica %q",
			opsRequest.Name,
			result.ReplicaPod,
		)
	}
	return nil
}

func recordPostgreSQLReplicationAutoRebuildCooldown(
	transCtx *componentTransformContext,
	result postgreSQLReplicationHealthResult,
	opsRequest *appsv1alpha1.OpsRequest,
	now time.Time,
) {
	conditionType := postgreSQLReplicationAutoRebuildConditionType(result.ReplicaPod)
	meta.RemoveStatusCondition(&transCtx.Component.Status.Conditions, conditionType)
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now),
		Reason:             result.Reason,
		Message: fmt.Sprintf(
			"created RebuildInstance OpsRequest %q for PostgreSQL replica %q",
			opsRequest.Name,
			result.ReplicaPod,
		),
	})
}

func postgreSQLReplicationAutoRebuildAllowed(component *appsv1alpha1.Component) bool {
	if component == nil || component.Annotations == nil {
		return true
	}
	value := strings.TrimSpace(component.Annotations[postgreSQLReplicationAutoRebuildAnnotation])
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

func isPostgreSQLReplicationRebuildable(result postgreSQLReplicationHealthResult) bool {
	if result.Ready || result.ReplicaPod == "" {
		return false
	}
	switch result.Reason {
	case postgreSQLReplicationReasonSlotInactive,
		postgreSQLReplicationReasonWALReceiverMissing,
		postgreSQLReplicationReasonWALReceiverNotStreaming,
		postgreSQLReplicationReasonTimelineMismatch:
		return true
	default:
		return false
	}
}

func postgreSQLReplicationFailureWindowSatisfied(
	conditions []metav1.Condition,
	result postgreSQLReplicationHealthResult,
	window time.Duration,
	now time.Time,
) bool {
	condition := meta.FindStatusCondition(
		conditions,
		postgreSQLReplicationAutoRebuildPendingConditionType(result.ReplicaPod),
	)
	if condition == nil ||
		condition.Status != metav1.ConditionTrue ||
		condition.Reason != result.Reason {
		return false
	}
	return !condition.LastTransitionTime.IsZero() &&
		!condition.LastTransitionTime.Time.After(now) &&
		now.Sub(condition.LastTransitionTime.Time) >= window
}

func postgreSQLReplicationAutoRebuildInCooldown(
	conditions []metav1.Condition,
	result postgreSQLReplicationHealthResult,
	cooldown time.Duration,
	now time.Time,
) bool {
	condition := meta.FindStatusCondition(conditions, postgreSQLReplicationAutoRebuildConditionType(result.ReplicaPod))
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.LastTransitionTime.IsZero() {
		return false
	}
	if condition.LastTransitionTime.Time.After(now) {
		return true
	}
	return now.Sub(condition.LastTransitionTime.Time) < cooldown
}

func postgreSQLReplicationAutoRebuildWindow(component *appsv1alpha1.Component) time.Duration {
	return postgreSQLReplicationDurationAnnotation(
		component,
		postgreSQLReplicationAutoRebuildWindowAnnotation,
		postgreSQLReplicationDefaultAutoRebuildWindow,
	)
}

func postgreSQLReplicationAutoRebuildCooldown(component *appsv1alpha1.Component) time.Duration {
	return postgreSQLReplicationDurationAnnotation(
		component,
		postgreSQLReplicationAutoRebuildCooldownAnnotation,
		postgreSQLReplicationDefaultAutoRebuildCooldown,
	)
}

func postgreSQLReplicationDurationAnnotation(
	component *appsv1alpha1.Component,
	key string,
	defaultValue time.Duration,
) time.Duration {
	if component == nil || component.Annotations == nil {
		return defaultValue
	}
	value := strings.TrimSpace(component.Annotations[key])
	if value == "" {
		return defaultValue
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return defaultValue
	}
	return duration
}

func (t *componentPostgreSQLReplicationHealthTransformer) hasRunningRebuildOpsRequest(
	transCtx *componentTransformContext,
) (bool, error) {
	opsRequestList := &appsv1alpha1.OpsRequestList{}
	if err := transCtx.Client.List(
		transCtx.Context,
		opsRequestList,
		client.InNamespace(transCtx.Cluster.Namespace),
		client.MatchingLabels{
			constant.AppInstanceLabelKey:    transCtx.Cluster.Name,
			constant.OpsRequestTypeLabelKey: string(appsv1alpha1.RebuildInstanceType),
		},
		inDataContext4C(),
	); err != nil {
		return false, err
	}
	for _, opsRequest := range opsRequestList.Items {
		if isRunningRebuildOpsRequestForCluster(&opsRequest, transCtx.Cluster.Name) {
			return true, nil
		}
	}

	opsRequestList = &appsv1alpha1.OpsRequestList{}
	if err := transCtx.Client.List(
		transCtx.Context,
		opsRequestList,
		client.InNamespace(transCtx.Cluster.Namespace),
		inDataContext4C(),
	); err != nil {
		return false, err
	}
	for _, opsRequest := range opsRequestList.Items {
		if isRunningRebuildOpsRequestForCluster(&opsRequest, transCtx.Cluster.Name) {
			return true, nil
		}
	}
	return false, nil
}

func isRunningRebuildOpsRequestForCluster(opsRequest *appsv1alpha1.OpsRequest, clusterName string) bool {
	if opsRequest == nil ||
		opsRequest.Spec.Type != appsv1alpha1.RebuildInstanceType ||
		opsRequest.Spec.GetClusterName() != clusterName {
		return false
	}
	return !opsRequest.IsComplete()
}

func autoRebuildOpsRequestExistsInDAG(transCtx *componentTransformContext, dag *graph.DAG) bool {
	if dag == nil {
		return false
	}
	graphCli, ok := transCtx.Client.(model.GraphClient)
	if !ok {
		return false
	}
	for _, object := range graphCli.FindAll(dag, &appsv1alpha1.OpsRequest{}) {
		opsRequest, ok := object.(*appsv1alpha1.OpsRequest)
		if !ok || opsRequest.Namespace != transCtx.Cluster.Namespace {
			continue
		}
		if opsRequest.Spec.GetClusterName() == transCtx.Cluster.Name &&
			opsRequest.Spec.Type == appsv1alpha1.RebuildInstanceType {
			return true
		}
	}
	return false
}

func buildPostgreSQLReplicationAutoRebuildOpsRequest(
	transCtx *componentTransformContext,
	podName string,
	now time.Time,
) *appsv1alpha1.OpsRequest {
	name := fmt.Sprintf(
		"pg-rebuild-%s-%s-%d",
		shortPostgreSQLReplicationHash(transCtx.Cluster.Name),
		shortPostgreSQLReplicationHash(podName),
		now.UnixNano(),
	)
	return &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: transCtx.Cluster.Namespace,
			Labels: map[string]string{
				constant.AppInstanceLabelKey:    transCtx.Cluster.Name,
				constant.OpsRequestTypeLabelKey: string(appsv1alpha1.RebuildInstanceType),
			},
		},
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName:                           transCtx.Cluster.Name,
			Type:                                  appsv1alpha1.RebuildInstanceType,
			Force:                                 true,
			TimeoutSeconds:                        pointer.Int32(postgreSQLReplicationAutoRebuildTimeoutSeconds),
			TTLSecondsAfterSucceed:                postgreSQLReplicationAutoRebuildTTLSecondsAfterSucceed,
			TTLSecondsAfterUnsuccessfulCompletion: postgreSQLReplicationAutoRebuildTTLSecondsAfterUnsuccessful,
			// Force allows rebuilding an abnormal replica; EnqueueOnForce keeps
			// the auto-repair inside the normal OpsRequest queue.
			EnqueueOnForce: true,
			SpecificOpsRequest: appsv1alpha1.SpecificOpsRequest{
				RebuildFrom: []appsv1alpha1.RebuildInstance{{
					ComponentOps: appsv1alpha1.ComponentOps{
						ComponentName: transCtx.SynthesizeComponent.Name,
					},
					Instances: []appsv1alpha1.Instance{{
						Name: podName,
					}},
					InPlace: true,
				}},
			},
		},
	}
}

func shortPostgreSQLReplicationHash(value string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	return fmt.Sprintf("%08x", hash.Sum32())
}

func postgreSQLReplicationAutoRebuildConditionType(podName string) string {
	return postgreSQLReplicationAutoRebuildConditionTypePrefix + shortPostgreSQLReplicationHash(podName)
}

func postgreSQLReplicationAutoRebuildPendingConditionType(podName string) string {
	return postgreSQLReplicationAutoRebuildPendingConditionTypePrefix + shortPostgreSQLReplicationHash(podName)
}

var postgreSQLReplicationPSQLCommand = []string{
	"psql",
	"-U",
	"postgres",
	"-v",
	"ON_ERROR_STOP=1",
	"-X",
	"-A",
	"-t",
	"-q",
	"-f",
	"-",
}

const postgreSQLReplicationLeaderSQL = `
SELECT json_build_object(
  'in_recovery', pg_is_in_recovery(),
  'timeline_id', (pg_control_checkpoint()).timeline_id,
  'replications', COALESCE((
    SELECT json_agg(json_build_object(
      'application_name', application_name,
      'state', state,
      'sent_lsn', sent_lsn::text,
      'write_lsn', write_lsn::text,
      'flush_lsn', flush_lsn::text,
      'replay_lsn', replay_lsn::text,
      'sync_state', sync_state
    ))
    FROM pg_stat_replication
  ), '[]'::json),
  'slots', COALESCE((
    SELECT json_agg(json_build_object(
      'slot_name', slot_name,
      'active', active,
      'restart_lsn', restart_lsn::text,
      'retained_wal_bytes',
        CASE
          WHEN pg_is_in_recovery() OR restart_lsn IS NULL THEN NULL
          ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)::text
        END
    ))
    FROM pg_replication_slots
    WHERE slot_type = 'physical'
  ), '[]'::json)
);
`

const postgreSQLReplicationReplicaSQL = `
SELECT json_build_object(
  'in_recovery', pg_is_in_recovery(),
  'primary_conninfo', current_setting('primary_conninfo', true),
  'receive_lsn', pg_last_wal_receive_lsn()::text,
  'replay_lsn', pg_last_wal_replay_lsn()::text,
  'wal_receiver', (
    SELECT json_build_object(
      'status', status,
      'sender_host', sender_host,
      'slot_name', slot_name,
      'latest_end_lsn', latest_end_lsn::text,
      'received_tli', received_tli
    )
    FROM pg_stat_wal_receiver
    LIMIT 1
  )
);
`

func runningPostgreSQLPods(transCtx *componentTransformContext) (map[string]*corev1.Pod, error) {
	labels := constant.GetComponentWellKnownLabels(transCtx.Cluster.Name, transCtx.SynthesizeComponent.Name)
	pods, err := ctrlcomp.ListPodOwnedByComponent(
		transCtx.Context,
		transCtx.Client,
		transCtx.SynthesizeComponent.Namespace,
		labels,
		inDataContext4C(),
	)
	if err != nil {
		return nil, err
	}

	runningPods := make(map[string]*corev1.Pod, len(pods))
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		runningPods[pod.Name] = pod
	}
	return runningPods, nil
}

func replicaPodsForReplicationHealth(
	runningITS *workloads.InstanceSet,
	runningPods map[string]*corev1.Pod,
) []*corev1.Pod {
	leaderName := leaderMemberPodName(runningITS)
	replicas := make([]*corev1.Pod, 0)
	for _, member := range runningITS.Status.MembersStatus {
		if member.PodName == "" || member.PodName == leaderName {
			continue
		}
		if pod := runningPods[member.PodName]; pod != nil {
			replicas = append(replicas, pod)
		}
	}
	sort.Slice(replicas, func(i, j int) bool {
		return replicas[i].Name < replicas[j].Name
	})
	return replicas
}

func queryPostgreSQLReplicationLeader(
	ctx context.Context,
	runner podExecRunner,
	pod *corev1.Pod,
) (postgreSQLLeaderState, error) {
	var state postgreSQLLeaderState
	stdout, err := execPostgreSQLReplicationSQL(ctx, runner, pod, postgreSQLReplicationLeaderSQL)
	if err != nil {
		return state, err
	}
	if err := decodePostgreSQLReplicationJSON(stdout, &state); err != nil {
		return state, fmt.Errorf("decode PostgreSQL primary replication state from pod %q: %w", pod.Name, err)
	}
	return state, nil
}

func queryPostgreSQLReplicationReplica(
	ctx context.Context,
	runner podExecRunner,
	pod *corev1.Pod,
) (postgreSQLReplicaState, error) {
	var state postgreSQLReplicaState
	stdout, err := execPostgreSQLReplicationSQL(ctx, runner, pod, postgreSQLReplicationReplicaSQL)
	if err != nil {
		return state, err
	}
	if err := decodePostgreSQLReplicationJSON(stdout, &state); err != nil {
		return state, fmt.Errorf("decode PostgreSQL replica replication state from pod %q: %w", pod.Name, err)
	}
	return state, nil
}

func execPostgreSQLReplicationSQL(
	ctx context.Context,
	runner podExecRunner,
	pod *corev1.Pod,
	sql string,
) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, postgreSQLReplicationExecTimeout)
	defer cancel()

	stdout, _, err := runner.Exec(execCtx, pod, postgreSQLReplicationPSQLCommand, sql)
	if err != nil {
		return "", fmt.Errorf("exec PostgreSQL replication health SQL on pod %q: %w", pod.Name, err)
	}
	return strings.TrimSpace(stdout), nil
}

func decodePostgreSQLReplicationJSON(output string, target any) error {
	if len(output) > postgreSQLReplicationMaxJSONOutputBytes {
		return fmt.Errorf("output exceeds %d bytes", postgreSQLReplicationMaxJSONOutputBytes)
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return fmt.Errorf("empty output")
	}
	if err := json.Unmarshal([]byte(output), target); err != nil {
		return err
	}
	return nil
}

type postgreSQLLeaderState struct {
	InRecovery   bool                              `json:"in_recovery"`
	TimelineID   int64                             `json:"timeline_id"`
	Replications []postgreSQLReplicationConnection `json:"replications"`
	Slots        []postgreSQLReplicationSlot       `json:"slots"`
}

type postgreSQLReplicationConnection struct {
	ApplicationName string `json:"application_name"`
	State           string `json:"state"`
	SentLSN         string `json:"sent_lsn"`
	WriteLSN        string `json:"write_lsn"`
	FlushLSN        string `json:"flush_lsn"`
	ReplayLSN       string `json:"replay_lsn"`
	SyncState       string `json:"sync_state"`
}

type postgreSQLReplicationSlot struct {
	SlotName             string `json:"slot_name"`
	Active               bool   `json:"active"`
	RestartLSN           string `json:"restart_lsn"`
	RetainedWALBytesText string `json:"retained_wal_bytes"`
}

type postgreSQLReplicaState struct {
	InRecovery      bool                         `json:"in_recovery"`
	PrimaryConninfo string                       `json:"primary_conninfo"`
	ReceiveLSN      string                       `json:"receive_lsn"`
	ReplayLSN       string                       `json:"replay_lsn"`
	WALReceiver     *postgreSQLWALReceiverStatus `json:"wal_receiver"`
}

type postgreSQLWALReceiverStatus struct {
	Status       string `json:"status"`
	SenderHost   string `json:"sender_host"`
	SlotName     string `json:"slot_name"`
	LatestEndLSN string `json:"latest_end_lsn"`
	ReceivedTLI  int64  `json:"received_tli"`
}

type postgreSQLReplicationHealthResult struct {
	Ready      bool
	Reason     string
	Message    string
	ReplicaPod string
}

func evaluatePostgreSQLReplicationHealth(
	leaderPodName string,
	leaderState postgreSQLLeaderState,
	replicaStates map[string]postgreSQLReplicaState,
) postgreSQLReplicationHealthResult {
	if len(replicaStates) == 0 {
		return postgreSQLReplicationHealthResult{
			Ready:   false,
			Reason:  postgreSQLReplicationReasonReplicasNotFound,
			Message: "PostgreSQL replica states are not found",
		}
	}
	if leaderState.InRecovery {
		return postgreSQLReplicationHealthResult{
			Ready:  false,
			Reason: postgreSQLReplicationReasonPrimaryInRecovery,
			Message: fmt.Sprintf(
				"PostgreSQL primary pod %q from InstanceSet member status is still in recovery",
				leaderPodName,
			),
		}
	}

	replicaNames := make([]string, 0, len(replicaStates))
	for podName := range replicaStates {
		replicaNames = append(replicaNames, podName)
	}
	sort.Strings(replicaNames)

	for _, podName := range replicaNames {
		replicaState := replicaStates[podName]
		keys := normalizedPostgreSQLReplicaKeys(podName, replicaState)
		slot := lookupPostgreSQLSlot(leaderState.Slots, keys)
		if slot != nil && !slot.Active {
			retainedWAL, _ := parsePostgreSQLRetainedWALBytes(slot.RetainedWALBytesText)
			message := fmt.Sprintf(
				"PostgreSQL replica %q replication slot %q is inactive on primary %q",
				podName,
				slot.SlotName,
				leaderPodName,
			)
			if retainedWAL > 0 {
				message = fmt.Sprintf("%s, retained WAL bytes=%d", message, retainedWAL)
			}
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonSlotInactive,
				Message:    message,
				ReplicaPod: podName,
			}
		}
		if !replicaState.InRecovery {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonWALReceiverMissing,
				Message:    fmt.Sprintf("PostgreSQL replica %q is not in recovery", podName),
				ReplicaPod: podName,
			}
		}
		if replicaState.WALReceiver == nil {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonWALReceiverMissing,
				Message:    fmt.Sprintf("PostgreSQL replica %q has no WAL receiver", podName),
				ReplicaPod: podName,
			}
		}
		if !strings.EqualFold(strings.TrimSpace(replicaState.WALReceiver.Status), "streaming") {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonWALReceiverNotStreaming,
				ReplicaPod: podName,
				Message: fmt.Sprintf(
					"PostgreSQL replica %q WAL receiver is %q",
					podName,
					replicaState.WALReceiver.Status,
				),
			}
		}
		if leaderState.TimelineID > 0 &&
			replicaState.WALReceiver.ReceivedTLI > 0 &&
			leaderState.TimelineID != replicaState.WALReceiver.ReceivedTLI {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonTimelineMismatch,
				ReplicaPod: podName,
				Message: fmt.Sprintf(
					"PostgreSQL replica %q WAL receiver timeline %d differs from primary %q timeline %d",
					podName,
					replicaState.WALReceiver.ReceivedTLI,
					leaderPodName,
					leaderState.TimelineID,
				),
			}
		}
		replication := lookupPostgreSQLReplication(leaderState.Replications, keys)
		if replication == nil {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonWALReceiverMissing,
				ReplicaPod: podName,
				Message: fmt.Sprintf(
					"PostgreSQL primary %q has no replication connection for replica %q",
					leaderPodName,
					podName,
				),
			}
		}
		if !strings.EqualFold(strings.TrimSpace(replication.State), "streaming") {
			return postgreSQLReplicationHealthResult{
				Ready:      false,
				Reason:     postgreSQLReplicationReasonWALReceiverNotStreaming,
				ReplicaPod: podName,
				Message: fmt.Sprintf(
					"PostgreSQL replica %q primary connection is %q on primary %q",
					podName,
					replication.State,
					leaderPodName,
				),
			}
		}
	}

	return postgreSQLReplicationHealthResult{
		Ready:   true,
		Reason:  postgreSQLReplicationReasonReady,
		Message: "PostgreSQL replication is streaming for all running replicas",
	}
}

func normalizedPostgreSQLReplicaKeys(podName string, replicaState postgreSQLReplicaState) []string {
	keys := make([]string, 0, 3)
	appendKey := func(key string) {
		key = normalizePostgreSQLReplicationName(key)
		if key == "" {
			return
		}
		for _, existing := range keys {
			if existing == key {
				return
			}
		}
		keys = append(keys, key)
	}

	appendKey(applicationNameFromPrimaryConninfo(replicaState.PrimaryConninfo))
	if replicaState.WALReceiver != nil {
		appendKey(replicaState.WALReceiver.SlotName)
	}
	appendKey(podName)
	return keys
}

func lookupPostgreSQLReplication(
	replications []postgreSQLReplicationConnection,
	keys []string,
) *postgreSQLReplicationConnection {
	for _, key := range keys {
		for i := range replications {
			if key == normalizePostgreSQLReplicationName(replications[i].ApplicationName) {
				return &replications[i]
			}
		}
	}
	return nil
}

func lookupPostgreSQLSlot(slots []postgreSQLReplicationSlot, keys []string) *postgreSQLReplicationSlot {
	for _, key := range keys {
		for i := range slots {
			if key == normalizePostgreSQLReplicationName(slots[i].SlotName) {
				return &slots[i]
			}
		}
	}
	return nil
}

func normalizePostgreSQLReplicationName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "-", "_")
	return name
}

func applicationNameFromPrimaryConninfo(conninfo string) string {
	fields := parsePostgreSQLConninfo(conninfo)
	return fields["application_name"]
}

func parsePostgreSQLConninfo(conninfo string) map[string]string {
	fields := make(map[string]string)
	i := 0
	for i < len(conninfo) {
		for i < len(conninfo) && conninfo[i] == ' ' {
			i++
		}
		if i >= len(conninfo) {
			break
		}

		keyStart := i
		for i < len(conninfo) && conninfo[i] != '=' && conninfo[i] != ' ' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(conninfo[keyStart:i]))
		for i < len(conninfo) && conninfo[i] == ' ' {
			i++
		}
		if i >= len(conninfo) || conninfo[i] != '=' {
			for i < len(conninfo) && conninfo[i] != ' ' {
				i++
			}
			continue
		}
		i++
		for i < len(conninfo) && conninfo[i] == ' ' {
			i++
		}

		var value strings.Builder
		if i < len(conninfo) && conninfo[i] == '\'' {
			i++
			for i < len(conninfo) {
				switch conninfo[i] {
				case '\\':
					i++
					if i < len(conninfo) {
						value.WriteByte(conninfo[i])
						i++
					}
				case '\'':
					i++
					goto parsedValue
				default:
					value.WriteByte(conninfo[i])
					i++
				}
			}
		} else {
			for i < len(conninfo) && conninfo[i] != ' ' {
				value.WriteByte(conninfo[i])
				i++
			}
		}

	parsedValue:
		if key != "" {
			fields[key] = value.String()
		}
	}
	return fields
}

func parsePostgreSQLRetainedWALBytes(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	bytes, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		return bytes, true
	}
	asFloat, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return int64(asFloat), true
}
