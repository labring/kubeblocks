package apps

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/controllers/apps/operations"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

func TestTerminalOpsRequestDequeuesStaleClusterLock(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	const (
		namespace   = "default"
		clusterName = "restore-target"
		opsName     = "restore-ops"
	)
	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
			Annotations: map[string]string{
				"kubeblocks.io/ops-request": `[{"name":"restore-ops","type":"Restore"}]`,
			},
		},
	}
	ops := &appsv1alpha1.OpsRequest{
		ObjectMeta: metav1.ObjectMeta{Name: opsName, Namespace: namespace},
		Status:     appsv1alpha1.OpsRequestStatus{Phase: appsv1alpha1.OpsSucceedPhase},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	reconciler := &OpsRequestReconciler{Client: cli}
	reqCtx := intctrlutil.RequestCtx{Ctx: context.Background(), Log: logr.Discard()}
	opsRes := &operations.OpsResource{OpsRequest: ops, Cluster: cluster}

	if _, err := reconciler.handleOpsRequestByPhase(reqCtx, opsRes); err != nil {
		t.Fatalf("handle terminal OpsRequest: %v", err)
	}

	updatedCluster := &appsv1alpha1.Cluster{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: clusterName}, updatedCluster); err != nil {
		t.Fatalf("get cluster: %v", err)
	}
	if _, found := updatedCluster.Annotations["kubeblocks.io/ops-request"]; found {
		t.Fatal("stale terminal OpsRequest lock was not removed")
	}
}
