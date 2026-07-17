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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

func TestMemberJoinEnabled(t *testing.T) {
	t.Run("explicit member join", func(t *testing.T) {
		op := &componentWorkloadOps{
			synthesizeComp: &component.SynthesizedComponent{
				LifecycleActions: &appsv1alpha1.ComponentLifecycleActions{
					MemberJoin: &appsv1alpha1.LifecycleActionHandler{},
				},
			},
		}
		if !op.memberJoinEnabled() {
			t.Fatal("expected member join to be enabled")
		}
	})

	t.Run("mongodb role probe builtin", func(t *testing.T) {
		handler := appsv1alpha1.MongoDBBuiltinActionHandler
		op := &componentWorkloadOps{
			synthesizeComp: &component.SynthesizedComponent{
				CharacterType: constant.MongoDBCharacterType,
				LifecycleActions: &appsv1alpha1.ComponentLifecycleActions{
					RoleProbe: &appsv1alpha1.RoleProbe{
						LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{
							BuiltinHandler: &handler,
						},
					},
				},
			},
		}
		if !op.memberJoinEnabled() {
			t.Fatal("expected member join to be enabled for MongoDB role probe builtin")
		}
	})

	t.Run("non-mongodb without member join", func(t *testing.T) {
		op := &componentWorkloadOps{
			synthesizeComp: &component.SynthesizedComponent{
				CharacterType: constant.MySQLCharacterType,
			},
		}
		if op.memberJoinEnabled() {
			t.Fatal("expected member join to be disabled")
		}
	})

	t.Run("mongodb missing role probe builtin", func(t *testing.T) {
		op := &componentWorkloadOps{
			synthesizeComp: &component.SynthesizedComponent{
				CharacterType: constant.MongoDBCharacterType,
				LifecycleActions: &appsv1alpha1.ComponentLifecycleActions{
					RoleProbe: &appsv1alpha1.RoleProbe{},
				},
			},
		}
		if op.memberJoinEnabled() {
			t.Fatal("expected member join to be disabled without MongoDB builtin role probe")
		}
	})
}

func TestDetectPodsToMemberJoin(t *testing.T) {
	t.Run("no leader present", func(t *testing.T) {
		op := newMemberJoinOps(3, []workloads.MemberStatus{{PodName: "pod-0"}}, "pod-0", "pod-1")
		pods := []*corev1.Pod{newTestPod("pod-1", corev1.PodRunning, true)}
		if got := op.detectPodsToMemberJoin(pods); got.Len() != 0 {
			t.Fatalf("expected no pods to join, got %v", sets.List(got))
		}
	})

	t.Run("replicas already satisfied", func(t *testing.T) {
		op := newMemberJoinOps(1, []workloads.MemberStatus{{
			PodName:     "pod-0",
			ReplicaRole: &workloads.ReplicaRole{IsLeader: true},
		}}, "pod-0", "pod-1")
		pods := []*corev1.Pod{newTestPod("pod-1", corev1.PodRunning, true)}
		if got := op.detectPodsToMemberJoin(pods); got.Len() != 0 {
			t.Fatalf("expected no pods to join, got %v", sets.List(got))
		}
	})

	t.Run("filters pod states and labels", func(t *testing.T) {
		op := newMemberJoinOps(3, []workloads.MemberStatus{{
			PodName:     "pod-0",
			ReplicaRole: &workloads.ReplicaRole{IsLeader: true},
		}}, "pod-0", "pod-1", "pod-2", "pod-3", "pod-4", "pod-5")

		pod0 := newTestPod("pod-0", corev1.PodRunning, true)
		pod1 := newTestPod("pod-1", corev1.PodRunning, true)
		pod2 := newTestPod("pod-2", corev1.PodRunning, false)
		pod3 := newTestPod("pod-3", corev1.PodPending, false)
		pod4 := newTestPod("pod-4", corev1.PodRunning, true)
		pod4.Labels = map[string]string{constant.RoleLabelKey: "leader"}
		pod5 := newTestPod("pod-5", corev1.PodRunning, true)
		ts := metav1.NewTime(time.Now())
		pod5.DeletionTimestamp = &ts
		podX := newTestPod("pod-x", corev1.PodRunning, true)

		got := op.detectPodsToMemberJoin([]*corev1.Pod{pod0, pod1, pod2, pod3, pod4, pod5, podX})
		if got.Len() != 2 || !got.Has("pod-1") || !got.Has("pod-2") {
			t.Fatalf("unexpected pods to join: %v", sets.List(got))
		}
		if got.Has("pod-3") || got.Has("pod-4") || got.Has("pod-5") {
			t.Fatalf("unexpected pods included: %v", sets.List(got))
		}
	})
}

func TestUpdatePVCSizeRestoresPVPolicyAndClearsRecoveryMarkers(t *testing.T) {
	const (
		namespace = "default"
		pvcName   = "data-test-0"
		pvName    = "pv-data-test-0"
	)

	storage := resource.MustParse("2Gi")
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: pvName,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storage,
				},
			},
		},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvName,
			Namespace: namespace,
			Labels: map[string]string{
				constant.PVCNameLabelKey: pvcName,
			},
			Annotations: map[string]string{
				constant.PVLastClaimPolicyAnnotationKey: string(corev1.PersistentVolumeReclaimDelete),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			ClaimRef: &corev1.ObjectReference{
				Namespace: namespace,
				Name:      pvcName,
			},
		},
	}
	vctProto := &corev1.PersistentVolumeClaimTemplate{
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storage,
				},
			},
		},
	}

	dag := graph.NewDAG()
	protoITS := &workloads.InstanceSet{}
	dag.AddVertex(&model.ObjectVertex{Obj: protoITS})
	op := &componentWorkloadOps{
		reqCtx: intctrlutil.RequestCtx{
			Ctx: context.Background(),
		},
		cli:      fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pv).Build(),
		dag:      dag,
		protoITS: protoITS,
	}

	if err := op.updatePVCSize(
		types.NamespacedName{Namespace: namespace, Name: pvcName},
		pvc,
		false,
		vctProto,
	); err != nil {
		t.Fatalf("updatePVCSize failed: %v", err)
	}

	restoredPV := findPatchedPV(dag, pvName)
	if restoredPV == nil {
		t.Fatal("expected a PV patch to restore reclaim policy")
	}
	if restoredPV.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		t.Fatalf("expected reclaim policy %q, got %q",
			corev1.PersistentVolumeReclaimDelete, restoredPV.Spec.PersistentVolumeReclaimPolicy)
	}
	if _, ok := restoredPV.Labels[constant.PVCNameLabelKey]; ok {
		t.Fatalf("expected recovery label %q to be removed", constant.PVCNameLabelKey)
	}
	if _, ok := restoredPV.Annotations[constant.PVLastClaimPolicyAnnotationKey]; ok {
		t.Fatalf("expected recovery annotation %q to be removed", constant.PVLastClaimPolicyAnnotationKey)
	}
}

func TestUpdatePVCSizeIgnoresStaleRecoveryMarkers(t *testing.T) {
	const (
		namespace = "default"
		pvcName   = "data-test-0"
		pvName    = "pv-data-test-0"
	)

	storage := resource.MustParse("2Gi")
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvName,
			Namespace: namespace,
			Labels: map[string]string{
				constant.PVCNameLabelKey: pvcName,
			},
			Annotations: map[string]string{
				constant.PVLastClaimPolicyAnnotationKey: string(corev1.PersistentVolumeReclaimDelete),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			ClaimRef: &corev1.ObjectReference{
				Namespace:       namespace,
				Name:            pvcName,
				UID:             "current-pvc",
				ResourceVersion: "123",
			},
		},
	}
	vctProto := &corev1.PersistentVolumeClaimTemplate{
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storage,
				},
			},
		},
	}

	dag := graph.NewDAG()
	protoITS := &workloads.InstanceSet{}
	dag.AddVertex(&model.ObjectVertex{Obj: protoITS})
	op := &componentWorkloadOps{
		reqCtx: intctrlutil.RequestCtx{
			Ctx: context.Background(),
		},
		cli:      fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pv).Build(),
		dag:      dag,
		protoITS: protoITS,
	}

	if err := op.updatePVCSize(
		types.NamespacedName{Namespace: namespace, Name: pvcName},
		&corev1.PersistentVolumeClaim{},
		true,
		vctProto,
	); err != nil {
		t.Fatalf("updatePVCSize failed: %v", err)
	}

	if patchedPV := findPatchedPV(dag, pvName); patchedPV != nil {
		t.Fatal("expected stale recovery markers not to trigger a PV patch")
	}
}

func findPatchedPV(dag *graph.DAG, pvName string) *corev1.PersistentVolume {
	for _, vertex := range dag.Vertices() {
		objVertex, ok := vertex.(*model.ObjectVertex)
		if !ok || objVertex.Action == nil || *objVertex.Action != model.PATCH {
			continue
		}
		candidate, ok := objVertex.Obj.(*corev1.PersistentVolume)
		if ok && candidate.Name == pvName {
			return candidate
		}
	}
	return nil
}

func newMemberJoinOps(replicas int32, members []workloads.MemberStatus, desired ...string) *componentWorkloadOps {
	return &componentWorkloadOps{
		runningITS: &workloads.InstanceSet{
			Spec: workloads.InstanceSetSpec{Replicas: int32Ptr(replicas)},
			Status: workloads.InstanceSetStatus{
				MembersStatus: members,
			},
		},
		desiredCompPodNameSet: sets.New[string](desired...),
	}
}

func newTestPod(name string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}}
	}
	return pod
}

func int32Ptr(value int32) *int32 {
	return &value
}
