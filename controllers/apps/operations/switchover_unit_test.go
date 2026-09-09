package operations

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestSwitchoverJobFailed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "default"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
		}}},
	}
	completeJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "complete", Namespace: "default"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		}}},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(failedJob, completeJob).Build()
	cluster := &appsv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: "default"}}

	failed, err := switchoverJobFailed(context.Background(), cli, cluster, "failed")
	if err != nil || !failed {
		t.Fatalf("failed job = (%t, %v), want (true, nil)", failed, err)
	}

	failed, err = switchoverJobFailed(context.Background(), cli, cluster, "complete")
	if err != nil || failed {
		t.Fatalf("complete job = (%t, %v), want (false, nil)", failed, err)
	}
}
