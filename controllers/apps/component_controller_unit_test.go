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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/apecloud/kubeblocks/pkg/constant"
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
