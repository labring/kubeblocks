/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package apps

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestSystemAccountReconcilerSkipsComponentDefinitionCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "component-definition-cluster", Namespace: "default"},
		Spec: appsv1alpha1.ClusterSpec{
			ComponentSpecs: []appsv1alpha1.ClusterComponentSpec{{
				Name:         "database",
				ComponentDef: "database-component-definition",
			}},
		},
		Status: appsv1alpha1.ClusterStatus{Phase: appsv1alpha1.RunningClusterPhase},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	reconciler := &SystemAccountReconciler{Client: cli}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}); err != nil {
		t.Fatalf("component-definition cluster should skip legacy account reconciliation: %v", err)
	}
}
