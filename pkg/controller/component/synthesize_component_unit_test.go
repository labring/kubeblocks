/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package component

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestBuildLabelsUsesComponentDefinitionSpecLabels(t *testing.T) {
	compDef := &appsv1alpha1.ComponentDefinition{
		Spec: appsv1alpha1.ComponentDefinitionSpec{
			Labels: map[string]string{patroniManagedLabelKey: "true"},
		},
	}
	comp := &appsv1alpha1.Component{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{"app.kubernetes.io/instance": "pg-ha"},
	}}
	synthesized := &SynthesizedComponent{
		ClusterName: "pg-ha",
		ClusterUID:  "cluster-uid",
		Name:        "postgresql",
	}

	buildLabels(compDef, comp, synthesized)

	if got := synthesized.Labels[patroniManagedLabelKey]; got != "true" {
		t.Fatalf("patroni managed label = %q, want true", got)
	}
}
