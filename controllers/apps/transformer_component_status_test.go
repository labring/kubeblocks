/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package apps

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
)

func TestBuildRoleProbeFailureCondition(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	secondary := func(name string) workloads.MemberStatus {
		return workloads.MemberStatus{
			PodName:     name,
			ReplicaRole: &workloads.ReplicaRole{Name: "secondary"},
		}
	}
	primary := func(name string) workloads.MemberStatus {
		return workloads.MemberStatus{
			PodName:     name,
			ReplicaRole: &workloads.ReplicaRole{Name: "primary", IsLeader: true},
		}
	}

	tests := []struct {
		name                string
		readyAt             time.Time
		members             []workloads.MemberStatus
		readyWithoutPrimary bool
		wantReason          string
	}{
		{
			name:       "waits for role probe timeout",
			readyAt:    now.Add(-30 * time.Second),
			members:    []workloads.MemberStatus{secondary("redis-0"), secondary("redis-1")},
			wantReason: "",
		},
		{
			name:       "reports incomplete roles",
			readyAt:    now.Add(-2 * time.Minute),
			members:    []workloads.MemberStatus{secondary("redis-0")},
			wantReason: roleProbeReasonFailed,
		},
		{
			name:    "reports a missing member role",
			readyAt: now.Add(-2 * time.Minute),
			members: []workloads.MemberStatus{
				secondary("redis-0"),
				{PodName: "redis-1"},
			},
			wantReason: roleProbeReasonFailed,
		},
		{
			name:       "reports no primary",
			readyAt:    now.Add(-2 * time.Minute),
			members:    []workloads.MemberStatus{secondary("redis-0"), secondary("redis-1")},
			wantReason: roleProbeReasonNoPrimary,
		},
		{
			name:       "accepts one primary",
			readyAt:    now.Add(-2 * time.Minute),
			members:    []workloads.MemberStatus{primary("redis-0"), secondary("redis-1")},
			wantReason: "",
		},
		{
			name:       "reports multiple primaries",
			readyAt:    now.Add(-2 * time.Minute),
			members:    []workloads.MemberStatus{primary("redis-0"), primary("redis-1")},
			wantReason: roleProbeReasonMultiPrimary,
		},
		{
			name:                "allows workloads ready without primary",
			readyAt:             now.Add(-2 * time.Minute),
			members:             []workloads.MemberStatus{secondary("redis-0"), secondary("redis-1")},
			readyWithoutPrimary: true,
			wantReason:          "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			its := newRoleProbeTestInstanceSet(2, tt.readyAt, tt.members)
			its.Status.ReadyWithoutPrimary = tt.readyWithoutPrimary
			condition := buildRoleProbeFailureCondition(its, newRoleProbeTestProbes(), its.Generation, now)

			if tt.wantReason == "" {
				if condition != nil {
					t.Fatalf("expected no failure condition, got %#v", condition)
				}
				return
			}
			if condition == nil {
				t.Fatalf("expected reason %q, got no condition", tt.wantReason)
			}
			if condition.Type != roleProbeConditionType ||
				condition.Status != metav1.ConditionFalse || condition.Reason != tt.wantReason {
				t.Fatalf("unexpected condition: %#v", condition)
			}
		})
	}
}

func TestReconcileStatusReportsMultiplePrimariesAsAbnormal(t *testing.T) {
	its := newRoleProbeTestInstanceSet(2, time.Now().Add(-2*time.Minute), []workloads.MemberStatus{
		{
			PodName:     "redis-0",
			ReplicaRole: &workloads.ReplicaRole{Name: "primary", IsLeader: true},
		},
		{
			PodName:     "redis-1",
			ReplicaRole: &workloads.ReplicaRole{Name: "primary", IsLeader: true},
		},
	})
	its.Name = "redis"
	its.Annotations = map[string]string{constant.KubeBlocksGenerationKey: "1"}
	its.Status.CurrentRevision = "revision"
	its.Status.UpdateRevision = "revision"
	its.Status.ObservedGeneration = its.Generation
	its.Status.ReadyReplicas = 2
	its.Status.UpdatedReplicas = 2
	its.Status.AvailableReplicas = 2

	transformer := &componentStatusTransformer{
		cluster:    &appsv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
		comp:       &appsv1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
		runningITS: its,
		synthesizeComp: &component.SynthesizedComponent{
			Replicas: 2,
			Probes:   newRoleProbeTestProbes(),
			Roles:    []appsv1alpha1.ReplicaRole{{Name: "primary", Serviceable: true, Writable: true}},
		},
	}

	if err := transformer.reconcileStatus(&componentTransformContext{}); err != nil {
		t.Fatalf("reconcileStatus() error = %v", err)
	}
	if transformer.comp.Status.Phase != appsv1alpha1.AbnormalClusterCompPhase {
		t.Fatalf("component phase = %s, want %s",
			transformer.comp.Status.Phase, appsv1alpha1.AbnormalClusterCompPhase)
	}
	for _, condition := range transformer.comp.Status.Conditions {
		if condition.Type == roleProbeConditionType && condition.Reason == roleProbeReasonMultiPrimary {
			return
		}
	}
	t.Fatalf("expected %s condition with reason %s, got %#v",
		roleProbeConditionType, roleProbeReasonMultiPrimary, transformer.comp.Status.Conditions)
}

func TestRoleProbeRequeueAfter(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	secondary := workloads.MemberStatus{
		PodName:     "redis-0",
		ReplicaRole: &workloads.ReplicaRole{Name: "secondary"},
	}
	primary := workloads.MemberStatus{
		PodName:     "redis-0",
		ReplicaRole: &workloads.ReplicaRole{Name: "primary", IsLeader: true},
	}

	tests := []struct {
		name    string
		readyAt time.Time
		members []workloads.MemberStatus
		want    time.Duration
	}{
		{
			name:    "requeues at the role probe deadline",
			readyAt: now.Add(-30 * time.Second),
			members: []workloads.MemberStatus{secondary},
			want:    30 * time.Second,
		},
		{
			name:    "does not requeue after the deadline",
			readyAt: now.Add(-2 * time.Minute),
			members: []workloads.MemberStatus{secondary},
		},
		{
			name:    "does not requeue with a confirmed primary",
			readyAt: now.Add(-30 * time.Second),
			members: []workloads.MemberStatus{primary, {
				PodName:     "redis-1",
				ReplicaRole: &workloads.ReplicaRole{Name: "secondary"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			its := newRoleProbeTestInstanceSet(2, tt.readyAt, tt.members)
			if got := roleProbeRequeueAfter(its, newRoleProbeTestProbes(), now); got != tt.want {
				t.Fatalf("roleProbeRequeueAfter() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIsComponentAvailableHandlesMissingRole(t *testing.T) {
	transformer := &componentStatusTransformer{
		cluster: &appsv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
		runningITS: &workloads.InstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{constant.KubeBlocksGenerationKey: "1"},
			},
			Status: workloads.InstanceSetStatus{
				CurrentRevision:   "revision",
				UpdateRevision:    "revision",
				AvailableReplicas: 1,
				MembersStatus:     []workloads.MemberStatus{{}},
			},
		},
		synthesizeComp: &component.SynthesizedComponent{
			Roles: []appsv1alpha1.ReplicaRole{{Name: "primary", Serviceable: true, Writable: true}},
		},
	}

	if transformer.isComponentAvailable() {
		t.Fatal("expected component without a reported role to be unavailable")
	}
}

func newRoleProbeTestInstanceSet(replicas int32, readyAt time.Time, members []workloads.MemberStatus) *workloads.InstanceSet {
	return &workloads.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 7},
		Spec: workloads.InstanceSetSpec{
			Replicas: &replicas,
			Roles: []workloads.ReplicaRole{
				{Name: "primary", IsLeader: true},
				{Name: "secondary"},
			},
		},
		Status: workloads.InstanceSetStatus{
			Replicas:      replicas,
			MembersStatus: members,
			Conditions: []metav1.Condition{
				{
					Type:               string(workloads.InstanceReady),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(readyAt),
				},
			},
		},
	}
}

func newRoleProbeTestProbes() *appsv1alpha1.ClusterDefinitionProbes {
	return &appsv1alpha1.ClusterDefinitionProbes{
		RoleProbe:                      &appsv1alpha1.ClusterDefinitionProbe{},
		RoleProbeTimeoutAfterPodsReady: 60,
	}
}
