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
	"testing"

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
