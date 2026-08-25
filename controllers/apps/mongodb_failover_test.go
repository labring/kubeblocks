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
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/controllers/apps/operations"
	opsutil "github.com/apecloud/kubeblocks/controllers/apps/operations/util"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
)

func TestReadyEndpointCount(t *testing.T) {
	endpoints := &corev1.Endpoints{
		Subsets: []corev1.EndpointSubset{
			{
				Addresses:         []corev1.EndpointAddress{{IP: "10.0.0.1"}},
				NotReadyAddresses: []corev1.EndpointAddress{{IP: "10.0.0.2"}},
			},
			{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.0.3"}},
			},
		},
	}

	if got := readyEndpointCount(endpoints); got != 2 {
		t.Fatalf("readyEndpointCount() = %d, want 2", got)
	}
}

func TestAutoFailoverPrimaryNotReady(t *testing.T) {
	condition := func(status corev1.ConditionStatus) *corev1.PodCondition {
		return &corev1.PodCondition{Type: corev1.PodReady, Status: status}
	}
	tests := []struct {
		name string
		cond *corev1.PodCondition
		want bool
	}{
		{name: "false", cond: condition(corev1.ConditionFalse), want: true},
		{name: "unknown after node loss", cond: condition(corev1.ConditionUnknown), want: true},
		{name: "true", cond: condition(corev1.ConditionTrue), want: false},
		{name: "missing condition", cond: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoFailoverPrimaryNotReady(tt.cond); got != tt.want {
				t.Fatalf("autoFailoverPrimaryNotReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrimaryServiceName(t *testing.T) {
	synthesized := &component.SynthesizedComponent{
		ClusterName: "demo",
		Name:        "mongodb",
		ComponentServices: []appsv1alpha1.ComponentService{
			{
				Service: appsv1alpha1.Service{
					Name:         "default",
					ServiceName:  "mongodb",
					RoleSelector: "primary",
				},
			},
		},
	}

	want := constant.GenerateComponentServiceName("demo", "mongodb", "mongodb")
	if got := primaryServiceName(synthesized, "PRIMARY"); got != want {
		t.Fatalf("primaryServiceName() = %q, want %q", got, want)
	}
}

func TestSameComponentSwitchover(t *testing.T) {
	ops := &appsv1alpha1.OpsRequest{
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName: "demo",
			SpecificOpsRequest: appsv1alpha1.SpecificOpsRequest{
				SwitchoverList: []appsv1alpha1.Switchover{
					{ComponentName: "other"},
					{ComponentName: "mongodb"},
				},
			},
		},
	}

	if !sameComponentSwitchover(ops, "demo", "mongodb") {
		t.Fatal("sameComponentSwitchover() = false, want true")
	}
	if sameComponentSwitchover(ops, "demo", "postgresql") {
		t.Fatal("sameComponentSwitchover() = true, want false")
	}
	if sameComponentSwitchover(ops, "other", "mongodb") {
		t.Fatal("sameComponentSwitchover() = true for another cluster, want false")
	}
}

func TestSelectAutoFailoverPrimaryPod(t *testing.T) {
	pod := func(name, currentRole, lastKnownRole string) corev1.Pod {
		labels := map[string]string{}
		annotations := map[string]string{}
		if currentRole != "" {
			labels[constant.RoleLabelKey] = currentRole
		}
		if lastKnownRole != "" {
			annotations[constant.LastKnownRoleAnnotationKey] = lastKnownRole
		}
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      labels,
				Annotations: annotations,
			},
		}
	}

	tests := []struct {
		name      string
		pods      []corev1.Pod
		wantPod   string
		wantState operations.AutoFailoverPrimarySelection
	}{
		{
			name: "current role label",
			pods: []corev1.Pod{
				pod("primary-0", "primary", ""),
				pod("secondary-1", "secondary", ""),
			},
			wantPod:   "primary-0",
			wantState: operations.AutoFailoverPrimaryCurrent,
		},
		{
			name: "current role label takes precedence over last known roles",
			pods: []corev1.Pod{
				pod("primary-0", "primary", "primary"),
				pod("old-primary-1", "", "primary"),
			},
			wantPod:   "primary-0",
			wantState: operations.AutoFailoverPrimaryCurrent,
		},
		{
			name: "last known role fallback",
			pods: []corev1.Pod{
				pod("old-primary-0", "", "PRIMARY"),
				pod("secondary-1", "secondary", "secondary"),
			},
			wantPod:   "old-primary-0",
			wantState: operations.AutoFailoverPrimaryLastKnown,
		},
		{
			name: "duplicate current roles fail closed",
			pods: []corev1.Pod{
				pod("primary-0", "primary", ""),
				pod("primary-1", "primary", ""),
			},
			wantState: operations.AutoFailoverPrimaryAmbiguous,
		},
		{
			name: "duplicate last known roles fail closed",
			pods: []corev1.Pod{
				pod("old-primary-0", "", "primary"),
				pod("old-primary-1", "", "primary"),
			},
			wantState: operations.AutoFailoverPrimaryAmbiguous,
		},
		{
			name: "no primary evidence",
			pods: []corev1.Pod{
				pod("secondary-0", "secondary", "secondary"),
			},
			wantState: operations.AutoFailoverPrimaryNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPod, gotState := operations.SelectAutoFailoverPrimaryPod(tt.pods, "primary")
			if gotState != tt.wantState {
				t.Fatalf("selection state = %v, want %v", gotState, tt.wantState)
			}
			if tt.wantPod == "" {
				if gotPod != nil {
					t.Fatalf("selected pod = %q, want nil", gotPod.Name)
				}
				return
			}
			if gotPod == nil || gotPod.Name != tt.wantPod {
				t.Fatalf("selected pod = %v, want %q", gotPod, tt.wantPod)
			}
		})
	}
}

func TestAutoFailoverSuppressed(t *testing.T) {
	stopped := true
	runningComp := &component.SynthesizedComponent{Replicas: 3}
	clusterWithOps := func(recorders []appsv1alpha1.OpsRecorder) *appsv1alpha1.Cluster {
		cluster := &appsv1alpha1.Cluster{}
		opsutil.SetOpsRequestToCluster(cluster, recorders)
		return cluster
	}

	tests := []struct {
		name     string
		transCtx *componentTransformContext
		want     bool
	}{
		{
			name: "idle cluster",
			transCtx: &componentTransformContext{
				Cluster:             &appsv1alpha1.Cluster{},
				SynthesizeComponent: runningComp,
			},
		},
		{
			name: "component stopped",
			transCtx: &componentTransformContext{
				Cluster: &appsv1alpha1.Cluster{},
				SynthesizeComponent: &component.SynthesizedComponent{
					Stop: &stopped,
				},
			},
			want: true,
		},
		{
			name: "replicas zero counts as stopped",
			transCtx: &componentTransformContext{
				Cluster:             &appsv1alpha1.Cluster{},
				SynthesizeComponent: &component.SynthesizedComponent{},
			},
			want: true,
		},
		{
			name: "rolling update must not suppress failover",
			transCtx: &componentTransformContext{
				Cluster:             &appsv1alpha1.Cluster{},
				SynthesizeComponent: runningComp,
				RunningWorkload: &workloads.InstanceSet{
					Status: workloads.InstanceSetStatus{
						CurrentRevision: "rev-a",
						UpdateRevision:  "rev-b",
					},
				},
			},
		},
		{
			name: "stuck planned ops must not suppress failover",
			transCtx: &componentTransformContext{
				Cluster: clusterWithOps([]appsv1alpha1.OpsRecorder{{
					Name: "upgrade-1",
					Type: appsv1alpha1.UpgradeType,
				}}),
				SynthesizeComponent: runningComp,
			},
		},
		{
			name:     "partial context",
			transCtx: &componentTransformContext{},
		},
		{
			name: "nil context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autoFailoverSuppressed(tt.transCtx); got != tt.want {
				t.Fatalf("autoFailoverSuppressed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAutoFailoverRetryDelay(t *testing.T) {
	tests := []struct {
		failedAttempt int
		want          time.Duration
	}{
		{failedAttempt: 0, want: 30 * time.Second},
		{failedAttempt: 1, want: time.Minute},
		{failedAttempt: 2, want: 2 * time.Minute},
		{failedAttempt: 3, want: 4 * time.Minute},
		{failedAttempt: 4, want: autoFailoverRetryMax},
		{failedAttempt: 100, want: autoFailoverRetryMax},
	}
	for _, tc := range tests {
		if got := autoFailoverRetryDelay(tc.failedAttempt); got != tc.want {
			t.Errorf("autoFailoverRetryDelay(%d) = %v, want %v", tc.failedAttempt, got, tc.want)
		}
	}
}

const (
	autoFailoverTestCluster = "demo"
	autoFailoverTestComp    = "mongodb"
	autoFailoverTestEpoch   = "ns/demo/mongodb/uid-1/2026-08-25T00:00:00Z"
)

// autoFailoverTestOps builds a switchover request for the test component. An
// empty epoch marks a request that was not issued by auto-failover, such as one
// a human created.
func autoFailoverTestOps(phase appsv1alpha1.OpsPhase, epoch string, attempt int) appsv1alpha1.OpsRequest {
	annotations := map[string]string{}
	if epoch != "" {
		annotations[operations.AutoFailoverAnnotation] = operations.AutoFailoverAnnotationValue
		annotations[operations.AutoFailoverEpochAnnotation] = epoch
		annotations[operations.AutoFailoverAttemptAnnotation] = strconv.Itoa(attempt)
	}
	return appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:       annotations,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: appsv1alpha1.OpsRequestSpec{
			ClusterName: autoFailoverTestCluster,
			Type:        appsv1alpha1.SwitchoverType,
			SpecificOpsRequest: appsv1alpha1.SpecificOpsRequest{
				SwitchoverList: []appsv1alpha1.Switchover{{ComponentName: autoFailoverTestComp}},
			},
		},
		Status: appsv1alpha1.OpsRequestStatus{Phase: phase},
	}
}

func TestEvalAutoFailoverAttempts(t *testing.T) {
	otherComp := autoFailoverTestOps(appsv1alpha1.OpsRunningPhase, "", 0)
	otherComp.Spec.SwitchoverList[0].ComponentName = "other"

	tests := []struct {
		name         string
		items        []appsv1alpha1.OpsRequest
		wantInFlight bool
		wantSettled  bool
		wantAttempt  int
	}{
		{
			name:        "no history issues the first attempt",
			wantAttempt: 0,
		},
		{
			name:        "a switchover for another component is ignored",
			items:       []appsv1alpha1.OpsRequest{otherComp},
			wantAttempt: 0,
		},
		{
			name:         "a running manual switchover blocks a new attempt",
			items:        []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsRunningPhase, "", 0)},
			wantInFlight: true,
		},
		{
			name:         "a pending switchover blocks a new attempt",
			items:        []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsPendingPhase, autoFailoverTestEpoch, 0)},
			wantInFlight: true,
		},
		{
			name:        "a succeeded attempt settles the epoch",
			items:       []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsSucceedPhase, autoFailoverTestEpoch, 0)},
			wantSettled: true,
		},
		{
			name:        "a cancelled attempt settles the epoch and is not retried",
			items:       []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsCancelledPhase, autoFailoverTestEpoch, 0)},
			wantSettled: true,
		},
		{
			name:        "a failed attempt bumps the counter",
			items:       []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsFailedPhase, autoFailoverTestEpoch, 0)},
			wantAttempt: 1,
		},
		{
			name:        "an aborted attempt counts as failed",
			items:       []appsv1alpha1.OpsRequest{autoFailoverTestOps(appsv1alpha1.OpsAbortedPhase, autoFailoverTestEpoch, 0)},
			wantAttempt: 1,
		},
		{
			name: "the counter is independent of list order",
			items: []appsv1alpha1.OpsRequest{
				autoFailoverTestOps(appsv1alpha1.OpsFailedPhase, autoFailoverTestEpoch, 1),
				autoFailoverTestOps(appsv1alpha1.OpsFailedPhase, autoFailoverTestEpoch, 0),
			},
			wantAttempt: 2,
		},
		{
			name: "a failure from an older epoch does not count",
			items: []appsv1alpha1.OpsRequest{
				autoFailoverTestOps(appsv1alpha1.OpsFailedPhase, "ns/demo/mongodb/uid-0/2026-08-24T00:00:00Z", 3),
			},
			wantAttempt: 0,
		},
		{
			name: "exhausting the budget stops further attempts",
			items: []appsv1alpha1.OpsRequest{
				autoFailoverTestOps(appsv1alpha1.OpsFailedPhase, autoFailoverTestEpoch, autoFailoverMaxAttempts-1),
			},
			wantAttempt: autoFailoverMaxAttempts,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evalAutoFailoverAttempts(
				tc.items,
				autoFailoverTestCluster,
				autoFailoverTestComp,
				autoFailoverTestEpoch,
			)
			if got.inFlight != tc.wantInFlight {
				t.Errorf("inFlight = %v, want %v", got.inFlight, tc.wantInFlight)
			}
			if got.settled != tc.wantSettled {
				t.Errorf("settled = %v, want %v", got.settled, tc.wantSettled)
			}
			if got.attempt != tc.wantAttempt {
				t.Errorf("attempt = %d, want %d", got.attempt, tc.wantAttempt)
			}
			if got.attempt > 0 && got.lastFailedAt.IsZero() {
				t.Error("lastFailedAt is zero although a failed attempt was counted")
			}
		})
	}
}
