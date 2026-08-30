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

package operations

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestForceStopQueueBlocking(t *testing.T) {
	forceStop := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "stop"},
		Spec: appsv1alpha1.OpsRequestSpec{
			Type:  appsv1alpha1.StopType,
			Force: true,
		},
	}
	stopBehaviour := OpsBehaviour{
		QueueByCluster:      true,
		ForceBypassOpsTypes: []appsv1alpha1.OpsType{appsv1alpha1.StartType},
	}

	tests := []struct {
		name     string
		queue    []appsv1alpha1.OpsRecorder
		blocking bool
	}{
		{
			name: "bypasses running start",
			queue: []appsv1alpha1.OpsRecorder{
				{Name: "start", Type: appsv1alpha1.StartType},
			},
		},
		{
			name: "waits for running horizontal scaling",
			queue: []appsv1alpha1.OpsRecorder{
				{Name: "hscale", Type: appsv1alpha1.HorizontalScalingType},
			},
			blocking: true,
		},
		{
			name: "waits for queued restart",
			queue: []appsv1alpha1.OpsRecorder{
				{Name: "start", Type: appsv1alpha1.StartType},
				{Name: "restart", Type: appsv1alpha1.RestartType, InQueue: true},
			},
			blocking: true,
		},
		{
			name: "ignores operations after itself",
			queue: []appsv1alpha1.OpsRecorder{
				{Name: "start", Type: appsv1alpha1.StartType},
				{Name: "stop", Type: appsv1alpha1.StopType, InQueue: true},
				{Name: "hscale", Type: appsv1alpha1.HorizontalScalingType, InQueue: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := existOtherQueueBlockingOps(tt.queue, forceStop, forceStop.Spec.Type, stopBehaviour); got != tt.blocking {
				t.Fatalf("existOtherQueueBlockingOps() = %v, want %v", got, tt.blocking)
			}
		})
	}
}

func TestForceQueueDefaultBehaviorIsPreserved(t *testing.T) {
	forceOps := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "force-hscale"},
		Spec: appsv1alpha1.OpsRequestSpec{
			Type:  appsv1alpha1.HorizontalScalingType,
			Force: true,
		},
	}
	queue := []appsv1alpha1.OpsRecorder{{Name: "restart", Type: appsv1alpha1.RestartType}}

	if existOtherQueueBlockingOps(queue, forceOps, forceOps.Spec.Type, OpsBehaviour{QueueByCluster: true}) {
		t.Fatal("force OpsRequest without a bypass allowlist should preserve the original bypass-all behavior")
	}
}
