/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package component

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestBuildComponentAddsComponentDefinitionLabels(t *testing.T) {
	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: types.UID("cluster-uid")},
	}
	compSpec := &appsv1alpha1.ClusterComponentSpec{
		Name:         "postgresql",
		ComponentDef: "polardb-pg-ha-v1",
		Replicas:     2,
	}

	comp, err := BuildComponent(cluster, compSpec, nil)
	if err != nil {
		t.Fatalf("BuildComponent() error = %v", err)
	}
	if got, want := comp.Labels[constant.ComponentDefinitionLabelKey], compSpec.ComponentDef; got != want {
		t.Fatalf("ComponentDefinition label = %q, want %q", got, want)
	}
	if got, want := comp.Labels[constant.AppNameLabelKey], compSpec.ComponentDef; got != want {
		t.Fatalf("application name label = %q, want %q", got, want)
	}
}
