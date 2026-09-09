/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package apps

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestValidateCompReplicasAllowsStopToScaleToZero(t *testing.T) {
	comp := &appsv1alpha1.Component{}
	comp.Spec.Replicas = 0
	compDef := &appsv1alpha1.ComponentDefinition{
		Spec: appsv1alpha1.ComponentDefinitionSpec{
			ReplicasLimit: &appsv1alpha1.ReplicasLimit{MinReplicas: 1, MaxReplicas: 2},
		},
	}

	if err := validateCompReplicas(comp, compDef, nil); err == nil {
		t.Fatal("expected normal scale-to-zero to be rejected")
	}

	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{constant.SnapShotForStartAnnotationKey: `{"documentdb":1}`},
		},
	}
	if err := validateCompReplicas(comp, compDef, cluster); err != nil {
		t.Fatalf("expected Stop scale-to-zero to be accepted: %v", err)
	}
}
