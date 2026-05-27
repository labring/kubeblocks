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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
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

func TestInjectMySQLBinlogPathCompatInitContainer(t *testing.T) {
	t.Run("injects for ApeCloud MySQL workloads", func(t *testing.T) {
		runningITS := newBinlogCompatTestITS("mirror.local/apecloud/apecloud-mysql-server:8.0.30", corev1.PullIfNotPresent)
		protoITS := runningITS.DeepCopy()
		protoITS.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            "mysql",
			Image:           "docker.io/apecloud/apecloud-mysql-server:latest",
			ImagePullPolicy: corev1.PullAlways,
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "data",
				MountPath: "/data/mysql/",
			}},
		}}

		injectMySQLBinlogPathCompatInitContainer(&component.SynthesizedComponent{
			CharacterType:  constant.MySQLCharacterType,
			ClusterDefName: "apecloud-mysql",
		}, runningITS, protoITS)

		if len(protoITS.Spec.Template.Spec.InitContainers) != 1 {
			t.Fatalf("expected one init container, got %d", len(protoITS.Spec.Template.Spec.InitContainers))
		}
		initContainer := protoITS.Spec.Template.Spec.InitContainers[0]
		if initContainer.Name != mysqlBinlogPathCompatInitContainerName {
			t.Fatalf("unexpected init container name: %s", initContainer.Name)
		}
		if initContainer.Image != "mirror.local/apecloud/apecloud-mysql-server:8.0.30" {
			t.Fatalf("expected running image to be reused, got %s", initContainer.Image)
		}
		if initContainer.ImagePullPolicy != corev1.PullIfNotPresent {
			t.Fatalf("expected running image pull policy to be reused, got %s", initContainer.ImagePullPolicy)
		}
		if len(initContainer.Command) != 3 || initContainer.Command[2] != mysqlBinlogPathCompatScript {
			t.Fatalf("unexpected init command: %#v", initContainer.Command)
		}
		if len(initContainer.Env) != 1 || initContainer.Env[0].Name != "KB_COMPAT_MYSQL_DATA_ROOT" || initContainer.Env[0].Value != "/data/mysql" {
			t.Fatalf("unexpected env: %#v", initContainer.Env)
		}
		if len(initContainer.VolumeMounts) != 1 || initContainer.VolumeMounts[0].Name != "data" || initContainer.VolumeMounts[0].MountPath != "/data/mysql" {
			t.Fatalf("unexpected volume mounts: %#v", initContainer.VolumeMounts)
		}

		injectMySQLBinlogPathCompatInitContainer(&component.SynthesizedComponent{}, runningITS, protoITS)
		if len(protoITS.Spec.Template.Spec.InitContainers) != 1 {
			t.Fatalf("expected injection to be idempotent, got %d init containers", len(protoITS.Spec.Template.Spec.InitContainers))
		}
	})

	t.Run("skips non ApeCloud MySQL workloads", func(t *testing.T) {
		runningITS := newBinlogCompatTestITS("mysql:8.0", corev1.PullIfNotPresent)
		protoITS := runningITS.DeepCopy()
		protoITS.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name:      "data",
			MountPath: "/data/mysql",
		}}

		injectMySQLBinlogPathCompatInitContainer(&component.SynthesizedComponent{
			CharacterType: constant.MySQLCharacterType,
		}, runningITS, protoITS)

		if len(protoITS.Spec.Template.Spec.InitContainers) != 0 {
			t.Fatalf("expected no init containers, got %#v", protoITS.Spec.Template.Spec.InitContainers)
		}
	})
}

func newBinlogCompatTestITS(image string, pullPolicy corev1.PullPolicy) *workloads.InstanceSet {
	return &workloads.InstanceSet{
		Spec: workloads.InstanceSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "mysql",
						Image:           image,
						ImagePullPolicy: pullPolicy,
					}},
				},
			},
		},
	}
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
