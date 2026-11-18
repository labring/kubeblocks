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

package operations

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	opsutil "github.com/apecloud/kubeblocks/controllers/apps/operations/util"
)

func TestDequeueAutoFailoverFailureKeepsQueuedRequests(t *testing.T) {
	const (
		namespace     = "default"
		clusterName   = "mongodb"
		autoFailover  = "mongodb-auto-failover"
		queuedRequest = "user-upgrade"
	)

	scheme := runtime.NewScheme()
	if err := appsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      clusterName,
		},
	}
	opsutil.SetOpsRequestToCluster(cluster, []appsv1alpha1.OpsRecorder{
		{
			Name:    autoFailover,
			Type:    appsv1alpha1.SwitchoverType,
			InQueue: false,
		},
		{
			Name:    queuedRequest,
			Type:    appsv1alpha1.UpgradeType,
			InQueue: true,
		},
	})
	failed := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      autoFailover,
			Annotations: map[string]string{
				AutoFailoverAnnotation: AutoFailoverAnnotationValue,
			},
		},
		Status: appsv1alpha1.OpsRequestStatus{
			Phase: appsv1alpha1.OpsFailedPhase,
		},
	}
	queued := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      queuedRequest,
		},
		Status: appsv1alpha1.OpsRequestStatus{
			Phase: appsv1alpha1.OpsPendingPhase,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, failed, queued).
		Build()
	opsRes := &OpsResource{
		Cluster:    cluster,
		OpsRequest: failed,
	}

	if err := DequeueOpsRequestInClusterAnnotation(context.Background(), cli, opsRes); err != nil {
		t.Fatal(err)
	}

	gotQueued := &appsv1alpha1.OpsRequest{}
	if err := cli.Get(
		context.Background(),
		client.ObjectKeyFromObject(queued),
		gotQueued,
	); err != nil {
		t.Fatal(err)
	}
	if gotQueued.Status.Phase != appsv1alpha1.OpsPendingPhase {
		t.Fatalf("queued request phase = %q, want %q", gotQueued.Status.Phase, appsv1alpha1.OpsPendingPhase)
	}

	gotCluster := &appsv1alpha1.Cluster{}
	if err := cli.Get(
		context.Background(),
		client.ObjectKeyFromObject(cluster),
		gotCluster,
	); err != nil {
		t.Fatal(err)
	}
	queue, err := opsutil.GetOpsRequestSliceFromCluster(gotCluster)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].Name != queuedRequest || !queue[0].InQueue {
		t.Fatalf("cluster queue = %#v, want only queued request %q", queue, queuedRequest)
	}
}
