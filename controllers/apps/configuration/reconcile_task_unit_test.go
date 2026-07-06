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

package configuration

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestShouldFinalizeConfigRevision(t *testing.T) {
	const revision = "60"

	tests := []struct {
		name      string
		configMap *corev1.ConfigMap
		status    *appsv1alpha1.ConfigurationItemDetailStatus
		revision  string
		want      bool
	}{
		{
			name: "sync when status targets current revision but configmap still has old revision",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constant.ConfigurationRevision: "51",
					},
				},
			},
			status: &appsv1alpha1.ConfigurationItemDetailStatus{
				UpdateRevision: revision,
			},
			revision: revision,
			want:     true,
		},
		{
			name: "skip when configmap already has current revision",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constant.ConfigurationRevision: revision,
					},
				},
			},
			status: &appsv1alpha1.ConfigurationItemDetailStatus{
				UpdateRevision: revision,
			},
			revision: revision,
		},
		{
			name: "skip when status has not advanced to current revision",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constant.ConfigurationRevision: "51",
					},
				},
			},
			status: &appsv1alpha1.ConfigurationItemDetailStatus{
				UpdateRevision: "51",
			},
			revision: revision,
		},
		{
			name:     "skip without configmap",
			status:   &appsv1alpha1.ConfigurationItemDetailStatus{UpdateRevision: revision},
			revision: revision,
		},
		{
			name: "skip without status",
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constant.ConfigurationRevision: "51",
					},
				},
			},
			revision: revision,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFinalizeConfigRevision(tt.configMap, tt.status, tt.revision); got != tt.want {
				t.Fatalf("shouldFinalizeConfigRevision() = %v, want %v", got, tt.want)
			}
		})
	}
}
