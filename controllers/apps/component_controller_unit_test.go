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
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

func TestIsIgnoredComponentPodUpdate(t *testing.T) {
	basePod := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-0",
				Namespace: "default",
				Labels: map[string]string{
					constant.AppManagedByLabelKey:   constant.AppName,
					constant.AppInstanceLabelKey:    "cluster",
					constant.KBAppComponentLabelKey: "mysql",
					constant.RoleLabelKey:           "leader",
					constant.AccessModeLabelKey:     "ReadWrite",
				},
				Annotations: map[string]string{
					constant.LastRoleSnapshotVersionAnnotationKey: "1",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{
					Type:   corev1.PodReady,
					Status: corev1.ConditionTrue,
				}},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*corev1.Pod)
		ignored bool
	}{
		{
			name: "ignores role snapshot version only",
			mutate: func(pod *corev1.Pod) {
				pod.Annotations[constant.LastRoleSnapshotVersionAnnotationKey] = "2"
			},
			ignored: true,
		},
		{
			name: "ignores handled role event annotation only",
			mutate: func(pod *corev1.Pod) {
				pod.Annotations[roleChangedEventHandledAnnotationKey] = "count-2"
			},
			ignored: true,
		},
		{
			name: "keeps other annotation updates",
			mutate: func(pod *corev1.Pod) {
				pod.Annotations["example.kubeblocks.io/other"] = "changed"
			},
			ignored: false,
		},
		{
			name: "keeps role label updates",
			mutate: func(pod *corev1.Pod) {
				pod.Labels[constant.RoleLabelKey] = "follower"
			},
			ignored: false,
		},
		{
			name: "keeps access mode updates",
			mutate: func(pod *corev1.Pod) {
				pod.Labels[constant.AccessModeLabelKey] = "Readonly"
			},
			ignored: false,
		},
		{
			name: "keeps pod readiness updates",
			mutate: func(pod *corev1.Pod) {
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
			},
			ignored: false,
		},
		{
			name: "keeps pod deletion updates",
			mutate: func(pod *corev1.Pod) {
				now := metav1.Now()
				pod.DeletionTimestamp = &now
			},
			ignored: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldPod := basePod()
			newPod := oldPod.DeepCopy()
			newPod.ResourceVersion = "2"
			tt.mutate(newPod)
			if got := isIgnoredComponentPodUpdate(oldPod, newPod); got != tt.ignored {
				t.Fatalf("isIgnoredComponentPodUpdate() = %v, want %v", got, tt.ignored)
			}
		})
	}
}

func TestPolarDBPostgreSQLWorkloadEventHandler(t *testing.T) {
	baseLabels := map[string]string{
		constant.AppManagedByLabelKey:   constant.AppName,
		constant.AppInstanceLabelKey:    "polardb-pg",
		constant.KBAppComponentLabelKey: "postgresql",
	}
	makeLabels := func(extra map[string]string) map[string]string {
		labels := map[string]string{}
		for k, v := range baseLabels {
			labels[k] = v
		}
		for k, v := range extra {
			labels[k] = v
		}
		return labels
	}

	tests := []struct {
		name      string
		labels    map[string]string
		wantCount int
	}{
		{
			name: "enqueues component from component definition label",
			labels: makeLabels(map[string]string{
				constant.ComponentDefinitionLabelKey: "polardb-postgresql-ha",
			}),
			wantCount: 1,
		},
		{
			name: "enqueues component from app component label",
			labels: makeLabels(map[string]string{
				constant.AppComponentLabelKey: "polardb-postgresql-ha",
			}),
			wantCount: 1,
		},
		{
			name: "ignores non polardb postgresql workload",
			labels: makeLabels(map[string]string{
				constant.ComponentDefinitionLabelKey: "postgresql",
			}),
		},
		{
			name: "ignores workload without component labels",
			labels: map[string]string{
				constant.ComponentDefinitionLabelKey: string(appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler),
			},
		},
	}

	r := &ComponentReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := r.polarDBPostgreSQLWorkloadEventHandler(context.Background(), &workloads.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "polardb-pg-postgresql",
					Namespace: "kb-polardb-pg",
					Labels:    tt.labels,
				},
			})
			if len(requests) != tt.wantCount {
				t.Fatalf("polarDBPostgreSQLWorkloadEventHandler() returned %d requests, want %d", len(requests), tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			if got, want := requests[0].NamespacedName.String(), "kb-polardb-pg/polardb-pg-postgresql"; got != want {
				t.Fatalf("polarDBPostgreSQLWorkloadEventHandler() request = %s, want %s", got, want)
			}
		})
	}
}

func TestShouldRequeuePendingPolarDBPostgreSQLComponent(t *testing.T) {
	tests := []struct {
		name string
		comp *appsv1alpha1.Component
		want bool
	}{
		{
			name: "requeues polardb postgresql with empty phase",
			comp: &appsv1alpha1.Component{
				Spec: appsv1alpha1.ComponentSpec{
					CompDef: "polardb-postgresql-ha",
				},
			},
			want: true,
		},
		{
			name: "requeues polardb postgresql while creating",
			comp: &appsv1alpha1.Component{
				Spec: appsv1alpha1.ComponentSpec{
					CompDef: "polardb-postgresql-ha",
				},
				Status: appsv1alpha1.ComponentStatus{
					Phase: appsv1alpha1.CreatingClusterCompPhase,
				},
			},
			want: true,
		},
		{
			name: "stops after polardb postgresql is running",
			comp: &appsv1alpha1.Component{
				Spec: appsv1alpha1.ComponentSpec{
					CompDef: "polardb-postgresql-ha",
				},
				Status: appsv1alpha1.ComponentStatus{
					Phase: appsv1alpha1.RunningClusterCompPhase,
				},
			},
		},
		{
			name: "ignores non polardb postgresql component",
			comp: &appsv1alpha1.Component{
				Spec: appsv1alpha1.ComponentSpec{
					CompDef: "postgresql",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRequeuePendingPolarDBPostgreSQLComponent(tt.comp); got != tt.want {
				t.Fatalf("shouldRequeuePendingPolarDBPostgreSQLComponent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClusterPlanBuilderEnqueuesNonPolarDBComponentReconcileEvent(t *testing.T) {
	drainEvents := func() {
		for {
			select {
			case <-componentReconcileEventCh:
			default:
				return
			}
		}
	}
	drainEvents()
	t.Cleanup(drainEvents)

	action := model.CREATE
	component := &appsv1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "polardb-mongo-compat-documentdb",
			Namespace: "default",
		},
		Spec: appsv1alpha1.ComponentSpec{
			CompDef: "polardb-mongo-documentdb",
		},
	}
	builder := &clusterPlanBuilder{
		transCtx: &clusterTransformContext{Logger: logr.Discard()},
	}
	builder.enqueueComponentReconcileEvent(&model.ObjectVertex{
		Obj:    component,
		Action: &action,
	})

	select {
	case reconcileEvent := <-componentReconcileEventCh:
		if got, want := reconcileEvent.Object.GetNamespace(), component.Namespace; got != want {
			t.Fatalf("component reconcile event namespace = %q, want %q", got, want)
		}
		if got, want := reconcileEvent.Object.GetName(), component.Name; got != want {
			t.Fatalf("component reconcile event name = %q, want %q", got, want)
		}
	default:
		t.Fatal("expected a component reconcile event for a non-PolarDB PostgreSQL component")
	}
}
