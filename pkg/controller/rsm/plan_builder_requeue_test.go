/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/

package rsm

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
)

func TestPlanNeedsMemberUpdateRequeue(t *testing.T) {
	strategy := workloads.SerialUpdateStrategy
	rsm := &workloads.ReplicatedStateMachine{
		Spec: workloads.ReplicatedStateMachineSpec{
			MemberUpdateStrategy: &strategy,
			RsmTransformPolicy:   workloads.ToSts,
		},
		Status: workloads.ReplicatedStateMachineStatus{
			StatefulSetStatus: appsv1.StatefulSetStatus{
				Replicas:        2,
				UpdatedReplicas: 1,
				CurrentRevision: "old",
				UpdateRevision:  "new",
			},
		},
	}
	plan := &Plan{transCtx: &rsmTransformContext{rsm: rsm}}

	if !plan.NeedsMemberUpdateRequeue() {
		t.Fatal("expected a revision mismatch to request another reconciliation")
	}

	rsm.Status.CurrentRevision = rsm.Status.UpdateRevision
	rsm.Status.UpdatedReplicas = rsm.Status.Replicas
	if plan.NeedsMemberUpdateRequeue() {
		t.Fatal("did not expect a converged update to request another reconciliation")
	}
	rsm.Status.CurrentRevision = "old"
	if plan.NeedsMemberUpdateRequeue() {
		t.Fatal("did not expect an OnDelete current revision to keep requeuing after all members updated")
	}

	rsm.Spec.RsmTransformPolicy = workloads.ToPod
	if plan.NeedsMemberUpdateRequeue() {
		t.Fatal("did not expect a direct-Pod RSM to use the OnDelete retry path")
	}
}

func TestRolelessSerialUpdateUsesKubernetesReadiness(t *testing.T) {
	strategy := workloads.SerialUpdateStrategy
	rsm := workloads.ReplicatedStateMachine{
		Spec: workloads.ReplicatedStateMachineSpec{
			MemberUpdateStrategy: &strategy,
		},
		Status: workloads.ReplicatedStateMachineStatus{
			StatefulSetStatus: appsv1.StatefulSetStatus{UpdateRevision: "new"},
		},
	}
	ready := corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}
	pod0 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ferretdb-0",
			Labels: map[string]string{appsv1.StatefulSetRevisionLabel: "new"},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{ready}},
	}
	pod1 := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ferretdb-1",
			Labels: map[string]string{appsv1.StatefulSetRevisionLabel: "old"},
		},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{ready}},
	}

	podsToUpdate, err := newUpdatePlan(rsm, []corev1.Pod{pod0, pod1}).execute()
	if err != nil {
		t.Fatalf("build update plan: %v", err)
	}
	if len(podsToUpdate) != 1 || podsToUpdate[0].Name != pod1.Name {
		t.Fatalf("expected only %s to be updated, got %#v", pod1.Name, podsToUpdate)
	}
}
