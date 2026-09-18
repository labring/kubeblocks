/*
Copyright (C) 2026 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package apps

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestSyncRoleLabelSelectorFollowsDesiredComponentReplicas(t *testing.T) {
	cluster := &appsv1alpha1.Cluster{
		Spec: appsv1alpha1.ClusterSpec{
			ComponentSpecs: []appsv1alpha1.ClusterComponentSpec{
				{
					Name:         "postgresql",
					ComponentDef: "polardb-pg-ha-v2",
					Replicas:     1,
				},
			},
		},
	}
	transformer := &clusterBackupPolicyTransformer{
		clusterTransformContext: &clusterTransformContext{Cluster: cluster},
		backupPolicy: &appsv1alpha1.BackupPolicy{
			ComponentDefs: []string{"polardb-pg-ha-v2"},
		},
	}
	target := &dpv1alpha1.BackupTarget{
		PodSelector: &dpv1alpha1.PodSelector{
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{constant.RoleLabelKey: "secondary"},
			},
		},
	}

	transformer.syncRoleLabelSelector(target, "secondary")
	if _, ok := target.PodSelector.MatchLabels[constant.RoleLabelKey]; ok {
		t.Fatal("single replica backup target must not select a secondary")
	}

	cluster.Spec.ComponentSpecs[0].Replicas = 2
	transformer.syncRoleLabelSelector(target, "secondary")
	if got := target.PodSelector.MatchLabels[constant.RoleLabelKey]; got != "secondary" {
		t.Fatalf("scale-out backup target role = %q, want secondary", got)
	}
}

func TestBuildBackupPolicyAppliesSingleReplicaRoleFallback(t *testing.T) {
	cluster := &appsv1alpha1.Cluster{
		Spec: appsv1alpha1.ClusterSpec{
			ComponentSpecs: []appsv1alpha1.ClusterComponentSpec{
				{
					Name:         "postgresql",
					ComponentDef: "polardb-pg-ha-v2",
					Replicas:     1,
				},
			},
		},
	}
	transformer := &clusterBackupPolicyTransformer{
		clusterTransformContext: &clusterTransformContext{
			Cluster:     cluster,
			OrigCluster: cluster,
		},
		backupPolicyTpl: &appsv1alpha1.BackupPolicyTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "pg-backup-template"},
		},
		backupPolicy: &appsv1alpha1.BackupPolicy{
			ComponentDefs: []string{"polardb-pg-ha-v2"},
			Target: appsv1alpha1.TargetInstance{
				Role: "secondary",
			},
		},
	}

	policy := transformer.buildBackupPolicy(&cluster.Spec.ComponentSpecs[0], "pg-backup")
	if _, ok := policy.Spec.Target.PodSelector.MatchLabels[constant.RoleLabelKey]; ok {
		t.Fatal("new single replica backup policy must not select a secondary")
	}
}
