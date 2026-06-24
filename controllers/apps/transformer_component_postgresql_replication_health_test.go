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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

func TestEvaluatePostgreSQLReplicationHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		leaderState   postgreSQLLeaderState
		replicaStates map[string]postgreSQLReplicaState
		wantReady     bool
		wantReason    string
	}{
		{
			name: "ready",
			leaderState: postgreSQLLeaderState{
				TimelineID: 29,
				Replications: []postgreSQLReplicationConnection{{
					ApplicationName: "test_postgresql_0",
					State:           "streaming",
				}},
				Slots: []postgreSQLReplicationSlot{{
					SlotName: "test_postgresql_0",
					Active:   true,
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", ReceivedTLI: 29},
				},
			},
			wantReady:  true,
			wantReason: postgreSQLReplicationReasonReady,
		},
		{
			name: "selected primary is still in recovery",
			leaderState: postgreSQLLeaderState{
				InRecovery: true,
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming"},
				},
			},
			wantReason: postgreSQLReplicationReasonPrimaryInRecovery,
		},
		{
			name: "inactive slot",
			leaderState: postgreSQLLeaderState{
				Replications: []postgreSQLReplicationConnection{{
					ApplicationName: "test_postgresql_0",
					State:           "streaming",
				}},
				Slots: []postgreSQLReplicationSlot{{
					SlotName:             "test_postgresql_0",
					Active:               false,
					RetainedWALBytesText: "4096",
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming"},
				},
			},
			wantReason: postgreSQLReplicationReasonSlotInactive,
		},
		{
			name: "missing wal receiver",
			leaderState: postgreSQLLeaderState{
				Slots: []postgreSQLReplicationSlot{{
					SlotName: "test_postgresql_0",
					Active:   true,
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
				},
			},
			wantReason: postgreSQLReplicationReasonWALReceiverMissing,
		},
		{
			name: "wal receiver not streaming",
			leaderState: postgreSQLLeaderState{
				Slots: []postgreSQLReplicationSlot{{
					SlotName: "test_postgresql_0",
					Active:   true,
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver:     &postgreSQLWALReceiverStatus{Status: "stopped"},
				},
			},
			wantReason: postgreSQLReplicationReasonWALReceiverNotStreaming,
		},
		{
			name: "timeline mismatch",
			leaderState: postgreSQLLeaderState{
				TimelineID: 29,
				Slots: []postgreSQLReplicationSlot{{
					SlotName: "test_postgresql_0",
					Active:   true,
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver: &postgreSQLWALReceiverStatus{
						Status:      "streaming",
						ReceivedTLI: 21,
					},
				},
			},
			wantReason: postgreSQLReplicationReasonTimelineMismatch,
		},
		{
			name: "primary connection missing",
			leaderState: postgreSQLLeaderState{
				Slots: []postgreSQLReplicationSlot{{
					SlotName: "test_postgresql_0",
					Active:   true,
				}},
			},
			replicaStates: map[string]postgreSQLReplicaState{
				"test-postgresql-0": {
					InRecovery:      true,
					PrimaryConninfo: "application_name=test_postgresql_0",
					WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming"},
				},
			},
			wantReason: postgreSQLReplicationReasonWALReceiverMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluatePostgreSQLReplicationHealth("test-postgresql-1", tt.leaderState, tt.replicaStates)

			require.Equal(t, tt.wantReady, got.Ready)
			require.Equal(t, tt.wantReason, got.Reason)
			require.NotEmpty(t, got.Message)
		})
	}
}

func TestApplicationNameFromPrimaryConninfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		conninfo string
		want     string
	}{
		{
			conninfo: "host=leader port=5432 application_name=test_postgresql_0 user=standby",
			want:     "test_postgresql_0",
		},
		{
			conninfo: "host='leader pod' application_name='test-postgresql-0' password='do not log'",
			want:     "test-postgresql-0",
		},
		{
			conninfo: `application_name='test\'s replica' host=leader`,
			want:     "test's replica",
		},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, applicationNameFromPrimaryConninfo(tt.conninfo))
	}
}

func TestNormalizedPostgreSQLReplicaKeysPreferConfiguredNames(t *testing.T) {
	t.Parallel()

	keys := normalizedPostgreSQLReplicaKeys("test-postgresql-0", postgreSQLReplicaState{
		PrimaryConninfo: "application_name=configured_replica",
		WALReceiver:     &postgreSQLWALReceiverStatus{SlotName: "slot_replica"},
	})

	require.Equal(t, []string{"configured_replica", "slot_replica", "test_postgresql_0"}, keys)
}

func TestLookupPostgreSQLReplicationPrefersReplicaKeyOrder(t *testing.T) {
	t.Parallel()

	keys := []string{"configured_replica", "slot_replica", "test_postgresql_0"}
	replication := lookupPostgreSQLReplication([]postgreSQLReplicationConnection{
		{ApplicationName: "test_postgresql_0", State: "startup"},
		{ApplicationName: "configured_replica", State: "streaming"},
	}, keys)
	slot := lookupPostgreSQLSlot([]postgreSQLReplicationSlot{
		{SlotName: "test_postgresql_0", Active: false},
		{SlotName: "configured_replica", Active: true},
	}, keys)

	require.NotNil(t, replication)
	require.Equal(t, "configured_replica", replication.ApplicationName)
	require.NotNil(t, slot)
	require.Equal(t, "configured_replica", slot.SlotName)
}

func TestDecodePostgreSQLReplicationJSONRejectsEmptyAndLargeOutput(t *testing.T) {
	t.Parallel()

	var state postgreSQLLeaderState
	require.Error(t, decodePostgreSQLReplicationJSON("", &state))
	require.Error(t, decodePostgreSQLReplicationJSON(strings.Repeat("x", postgreSQLReplicationMaxJSONOutputBytes+1), &state))
	require.NoError(t, decodePostgreSQLReplicationJSON(`{"replications":[],"slots":[]}`, &state))
}

func TestComponentPostgreSQLReplicationHealthTransformerReady(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			Replications: []postgreSQLReplicationConnection{{
				ApplicationName: "test_postgresql_0",
				State:           "streaming",
			}},
			Slots: []postgreSQLReplicationSlot{{
				SlotName: "test_postgresql_0",
				Active:   true,
			}},
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", SlotName: "test_postgresql_0"},
		},
	})

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, graph.NewDAG())

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, postgreSQLReplicationReadyConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, postgreSQLReplicationReasonReady, cond.Reason)
	require.Equal(t, []string{leaderPodName, replicaPod}, runner.pods)
	require.Equal(t, postgreSQLReplicationLeaderSQL, runner.stdin[leaderPodName])
	require.Equal(t, postgreSQLReplicationReplicaSQL, runner.stdin[replicaPod])
}

func TestComponentPostgreSQLReplicationHealthTransformerSlotInactive(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	recorder := record.NewFakeRecorder(1)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.EventRecorder = recorder
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			Replications: []postgreSQLReplicationConnection{{
				ApplicationName: "test_postgresql_0",
				State:           "streaming",
			}},
			Slots: []postgreSQLReplicationSlot{{
				SlotName:             "test_postgresql_0",
				Active:               false,
				RetainedWALBytesText: "8192",
			}},
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", SlotName: "test_postgresql_0"},
		},
	})

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, graph.NewDAG())

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, postgreSQLReplicationReadyConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, postgreSQLReplicationReasonSlotInactive, cond.Reason)
	require.Contains(t, cond.Message, "retained WAL bytes=8192")
	event := <-recorder.Events
	require.Contains(t, event, postgreSQLReplicationReasonSlotInactive)
}

func TestComponentPostgreSQLReplicationHealthTransformerCheckFailed(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	runner := &fakePostgreSQLReplicationExecRunner{
		t:       t,
		results: map[string]string{},
		err:     errors.New("psql failed"),
	}

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, graph.NewDAG())

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, postgreSQLReplicationReadyConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, postgreSQLReplicationReasonHealthCheckFailed, cond.Reason)
	require.Contains(t, cond.Message, "psql failed")
	require.NotContains(t, cond.Message, "password=secret")
}

func TestComponentPostgreSQLReplicationHealthTransformerSkipsSinglePrimary(t *testing.T) {
	t.Parallel()

	const leaderPodName = "test-postgresql-0"

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).Transform(transCtx, graph.NewDAG())

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, postgreSQLReplicationReadyConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, postgreSQLReplicationReasonReplicasNotApplicable, cond.Reason)
}

func TestComponentPostgreSQLReplicationAutoRebuildWaitsForWindow(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "10m",
	}
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			Slots: []postgreSQLReplicationSlot{{
				SlotName: "test_postgresql_0",
				Active:   false,
			}},
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", SlotName: "test_postgresql_0"},
		},
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, dag)

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	pending := meta.FindStatusCondition(
		transCtx.Component.Status.Conditions,
		postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
	)
	require.NotNil(t, pending)
	require.Equal(t, metav1.ConditionTrue, pending.Status)
	require.Equal(t, postgreSQLReplicationReasonSlotInactive, pending.Reason)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildTransformCreatesOpsRequestAfterWindow(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
		Reason:             postgreSQLReplicationReasonSlotInactive,
		Message:            "slot inactive",
	})
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			Slots: []postgreSQLReplicationSlot{{
				SlotName: "test_postgresql_0",
				Active:   false,
			}},
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", SlotName: "test_postgresql_0"},
		},
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, dag)

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	opsRequests := graphOpsRequests(transCtx, dag)
	require.Len(t, opsRequests, 1)
	require.Equal(t, appsv1alpha1.RebuildInstanceType, opsRequests[0].Spec.Type)
	require.Equal(t, []appsv1alpha1.Instance{{Name: replicaPod}}, opsRequests[0].Spec.RebuildFrom[0].Instances)
}

func TestComponentPostgreSQLReplicationAutoRebuildCreatesOpsRequestForTimelineMismatch(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
		Reason:             postgreSQLReplicationReasonTimelineMismatch,
		Message:            "timeline mismatch",
	})
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			TimelineID: 29,
			Slots: []postgreSQLReplicationSlot{{
				SlotName: "test_postgresql_0",
				Active:   true,
			}},
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver: &postgreSQLWALReceiverStatus{
				Status:      "streaming",
				SlotName:    "test_postgresql_0",
				ReceivedTLI: 21,
			},
		},
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, dag)

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	opsRequests := graphOpsRequests(transCtx, dag)
	require.Len(t, opsRequests, 1)
	require.Equal(t, appsv1alpha1.RebuildInstanceType, opsRequests[0].Spec.Type)
	require.Equal(t, []appsv1alpha1.Instance{{Name: replicaPod}}, opsRequests[0].Spec.RebuildFrom[0].Instances)
}

func TestComponentPostgreSQLReplicationAutoRebuildSkipsPrimaryInRecovery(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	runner := newFakePostgreSQLReplicationExecRunner(t, map[string]any{
		leaderPodName: postgreSQLLeaderState{
			InRecovery: true,
		},
		replicaPod: postgreSQLReplicaState{
			InRecovery:      true,
			PrimaryConninfo: "application_name=test_postgresql_0",
			WALReceiver:     &postgreSQLWALReceiverStatus{Status: "streaming", SlotName: "test_postgresql_0"},
		},
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)

	err := (&componentPostgreSQLReplicationHealthTransformer{execRunner: runner}).Transform(transCtx, dag)

	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	require.Equal(t, postgreSQLReplicationHealthRequeueInterval, requeueAfter(t, err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, postgreSQLReplicationReadyConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, postgreSQLReplicationReasonPrimaryInRecovery, cond.Reason)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildCreatesOpsRequestAfterWindow(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 123)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonSlotInactive,
		Message:    "slot inactive",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	opsRequests := graphOpsRequests(transCtx, dag)
	require.Len(t, opsRequests, 1)
	opsRequest := opsRequests[0]
	require.Equal(t, appsv1alpha1.RebuildInstanceType, opsRequest.Spec.Type)
	require.Equal(t, transCtx.Cluster.Name, opsRequest.Spec.ClusterName)
	require.True(t, opsRequest.Spec.Force)
	require.True(t, opsRequest.Spec.EnqueueOnForce)
	require.NotNil(t, opsRequest.Spec.TimeoutSeconds)
	require.EqualValues(t, postgreSQLReplicationAutoRebuildTimeoutSeconds, *opsRequest.Spec.TimeoutSeconds)
	require.EqualValues(t, postgreSQLReplicationAutoRebuildTTLSecondsAfterSucceed, opsRequest.Spec.TTLSecondsAfterSucceed)
	require.EqualValues(
		t,
		postgreSQLReplicationAutoRebuildTTLSecondsAfterUnsuccessful,
		opsRequest.Spec.TTLSecondsAfterUnsuccessfulCompletion,
	)
	require.Equal(t, transCtx.Cluster.Name, opsRequest.Labels[constant.AppInstanceLabelKey])
	require.Equal(t, string(appsv1alpha1.RebuildInstanceType), opsRequest.Labels[constant.OpsRequestTypeLabelKey])
	require.Len(t, opsRequest.OwnerReferences, 1)
	require.Len(t, opsRequest.Spec.RebuildFrom, 1)
	require.Equal(t, transCtx.SynthesizeComponent.Name, opsRequest.Spec.RebuildFrom[0].ComponentName)
	require.True(t, opsRequest.Spec.RebuildFrom[0].InPlace)
	require.Equal(t, []appsv1alpha1.Instance{{Name: replicaPod}}, opsRequest.Spec.RebuildFrom[0].Instances)

	cooldown := meta.FindStatusCondition(
		transCtx.Component.Status.Conditions,
		postgreSQLReplicationAutoRebuildConditionType(replicaPod),
	)
	require.NotNil(t, cooldown)
	require.Equal(t, metav1.NewTime(now), cooldown.LastTransitionTime)
	require.Contains(t, cooldown.Message, opsRequest.Name)
}

func TestComponentPostgreSQLReplicationAutoRebuildDisabledByAnnotation(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 123)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildAnnotation:       "false",
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonSlotInactive,
		Message:    "slot inactive",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildDisabledByInvalidAnnotation(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 123)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildAnnotation:       "flase",
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonSlotInactive,
		Message:    "slot inactive",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildSkipsExistingCancellingOpsRequest(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 0)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonSlotInactive,
		Message:    "slot inactive",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	existingOpsRequest := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-rebuild",
			Namespace: transCtx.Cluster.Namespace,
			Labels: map[string]string{
				constant.AppInstanceLabelKey:    transCtx.Cluster.Name,
				constant.OpsRequestTypeLabelKey: string(appsv1alpha1.RebuildInstanceType),
			},
		},
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName: transCtx.Cluster.Name,
			Type:        appsv1alpha1.RebuildInstanceType,
		},
		Status: appsv1alpha1.OpsRequestStatus{
			Phase: appsv1alpha1.OpsCancellingPhase,
		},
	}
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	transCtx.Client = model.NewGraphClient(fake.NewClientBuilder().
		WithScheme(rscheme).
		WithObjects(existingOpsRequest).
		Build())
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildSkipsUnlabeledExistingOpsRequest(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 0)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation: "1m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonSlotInactive,
		Message:    "slot inactive",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	existingOpsRequest := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-unlabeled-rebuild",
			Namespace: transCtx.Cluster.Namespace,
		},
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName: transCtx.Cluster.Name,
			Type:        appsv1alpha1.RebuildInstanceType,
		},
		Status: appsv1alpha1.OpsRequestStatus{
			Phase: appsv1alpha1.OpsPendingPhase,
		},
	}
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	transCtx.Client = model.NewGraphClient(fake.NewClientBuilder().
		WithScheme(rscheme).
		WithObjects(existingOpsRequest).
		Build())
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestComponentPostgreSQLReplicationAutoRebuildSkipsDuringCooldown(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	now := time.Unix(1700000000, 0)
	transCtx := newPostgreSQLReplicationHealthTestContext(t, leaderPodName, replicaPod)
	transCtx.Component.Annotations = map[string]string{
		postgreSQLReplicationAutoRebuildWindowAnnotation:   "1m",
		postgreSQLReplicationAutoRebuildCooldownAnnotation: "30m",
	}
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonWALReceiverMissing,
		Message:    "wal receiver missing",
		ReplicaPod: replicaPod,
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)),
		Reason:             result.Reason,
		Message:            result.Message,
	})
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)),
		Reason:             result.Reason,
		Message:            "recent rebuild",
	})
	dag := initializedPostgreSQLReplicationHealthDAG(t, transCtx)
	runningITS := transCtx.RunningWorkload.(*workloads.InstanceSet)

	err := (&componentPostgreSQLReplicationHealthTransformer{}).
		maybeCreateAutoRebuildOpsRequest(transCtx, dag, runningITS, result, now)

	require.NoError(t, err)
	require.Empty(t, graphOpsRequests(transCtx, dag))
}

func TestPostgreSQLReplicationAutoRebuildPendingConditionResetsWhenReasonChanges(t *testing.T) {
	t.Parallel()

	const replicaPod = "test-postgresql-0"

	oldTransitionTime := metav1.NewTime(time.Now().Add(-time.Minute))
	transCtx := newPostgreSQLReplicationHealthTestContext(t, "test-postgresql-1", replicaPod)
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: oldTransitionTime,
		Reason:             postgreSQLReplicationReasonSlotInactive,
		Message:            "slot inactive",
	})
	result := postgreSQLReplicationHealthResult{
		Ready:      false,
		Reason:     postgreSQLReplicationReasonWALReceiverMissing,
		Message:    "wal receiver missing",
		ReplicaPod: replicaPod,
	}

	(&componentPostgreSQLReplicationHealthTransformer{}).recordAutoRebuildPendingCondition(transCtx, result)

	pending := meta.FindStatusCondition(
		transCtx.Component.Status.Conditions,
		postgreSQLReplicationAutoRebuildPendingConditionType(replicaPod),
	)
	require.NotNil(t, pending)
	require.Equal(t, postgreSQLReplicationReasonWALReceiverMissing, pending.Reason)
	require.True(t, pending.LastTransitionTime.After(oldTransitionTime.Time))
}

func TestShouldRecordPostgreSQLReplicationWarningOnlyWhenReasonChanges(t *testing.T) {
	t.Parallel()

	result := postgreSQLReplicationHealthResult{
		Ready:   false,
		Reason:  postgreSQLReplicationReasonSlotInactive,
		Message: "slot inactive",
	}
	require.True(t, shouldRecordPostgreSQLReplicationWarning(nil, result))
	require.False(t, shouldRecordPostgreSQLReplicationWarning([]metav1.Condition{{
		Type:   postgreSQLReplicationReadyConditionType,
		Status: metav1.ConditionFalse,
		Reason: postgreSQLReplicationReasonSlotInactive,
	}}, result))
	require.True(t, shouldRecordPostgreSQLReplicationWarning([]metav1.Condition{{
		Type:   postgreSQLReplicationReadyConditionType,
		Status: metav1.ConditionFalse,
		Reason: postgreSQLReplicationReasonWALReceiverMissing,
	}}, result))
	require.False(t, shouldRecordPostgreSQLReplicationWarning(nil, postgreSQLReplicationHealthResult{
		Ready:  true,
		Reason: postgreSQLReplicationReasonReady,
	}))
}

func TestPostgreSQLComponentRecognizesBuiltinHandler(t *testing.T) {
	t.Parallel()

	handler := appsv1alpha1.OfficialPostgresqlBuiltinActionHandler
	transCtx := &componentTransformContext{
		CompDef: &appsv1alpha1.ComponentDefinition{
			Spec: appsv1alpha1.ComponentDefinitionSpec{
				LifecycleActions: &appsv1alpha1.ComponentLifecycleActions{
					RoleProbe: &appsv1alpha1.RoleProbe{
						LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{
							BuiltinHandler: &handler,
						},
					},
				},
			},
		},
	}

	require.False(t, isPostgreSQLComponent(transCtx))
	require.True(t, isPostgreSQLComponentForReplicationHealth(transCtx))
}

func newPostgreSQLReplicationHealthTestContext(
	t *testing.T,
	leaderPodName string,
	replicaPodNames ...string,
) *componentTransformContext {
	t.Helper()
	return newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPodNames...)
}

func newFakePostgreSQLReplicationExecRunner(
	t *testing.T,
	states map[string]any,
) *fakePostgreSQLReplicationExecRunner {
	t.Helper()

	results := make(map[string]string, len(states))
	for podName, state := range states {
		data, err := json.Marshal(state)
		require.NoError(t, err)
		results[podName] = string(data)
	}
	return &fakePostgreSQLReplicationExecRunner{t: t, results: results}
}

func initializedPostgreSQLReplicationHealthDAG(
	t *testing.T,
	transCtx *componentTransformContext,
) *graph.DAG {
	t.Helper()

	dag := graph.NewDAG()
	require.NoError(t, (&componentInitTransformer{}).Transform(transCtx, dag))
	return dag
}

func graphOpsRequests(transCtx *componentTransformContext, dag *graph.DAG) []*appsv1alpha1.OpsRequest {
	graphClient := transCtx.Client.(model.GraphClient)
	objects := graphClient.FindAll(dag, &appsv1alpha1.OpsRequest{})
	opsRequests := make([]*appsv1alpha1.OpsRequest, 0, len(objects))
	for _, object := range objects {
		opsRequests = append(opsRequests, object.(*appsv1alpha1.OpsRequest))
	}
	return opsRequests
}

func requeueAfter(t *testing.T, err error) time.Duration {
	t.Helper()

	requeueErr, ok := err.(intctrlutil.RequeueError)
	require.True(t, ok)
	return requeueErr.RequeueAfter()
}

type fakePostgreSQLReplicationExecRunner struct {
	t       *testing.T
	results map[string]string
	err     error
	pods    []string
	stdin   map[string]string
}

func (r *fakePostgreSQLReplicationExecRunner) Exec(
	ctx context.Context,
	pod *corev1.Pod,
	command []string,
	stdin string,
) (string, string, error) {
	require.NoError(r.t, ctx.Err())
	require.Equal(r.t, postgreSQLReplicationPSQLCommand, command)
	if r.stdin == nil {
		r.stdin = make(map[string]string)
	}
	r.pods = append(r.pods, pod.Name)
	r.stdin[pod.Name] = stdin
	if r.err != nil {
		return "", "password=secret should not leak", r.err
	}
	return r.results[pod.Name], "", nil
}

var _ podExecRunner = &fakePostgreSQLReplicationExecRunner{}
