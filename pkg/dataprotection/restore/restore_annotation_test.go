/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package restore

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestGetRestoreFromBackupAnnotationKeepsComponentSystemAccounts(t *testing.T) {
	accountsJSON, err := json.Marshal(map[string]map[string]string{
		"polardb": {"postgres": "encrypted-postgres-password"},
		"other":   {"root": "encrypted-root-password"},
	})
	if err != nil {
		t.Fatalf("marshal backup accounts: %v", err)
	}
	backup := &dpv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "polardb-backup",
			Namespace: "default",
			Labels: map[string]string{
				constant.KBAppComponentLabelKey: "polardb",
			},
			Annotations: map[string]string{
				constant.EncryptedSystemAccountsAnnotationKey: string(accountsJSON),
			},
		},
	}

	annotation, err := GetRestoreFromBackupAnnotation(
		backup,
		[]appsv1alpha1.ClusterComponentSpec{{Name: "polardb"}},
		"Serial",
		"",
		false,
	)
	if err != nil {
		t.Fatalf("build restore annotation: %v", err)
	}
	components := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(annotation), &components); err != nil {
		t.Fatalf("decode restore annotation: %v", err)
	}
	componentAccounts := map[string]string{}
	if err := json.Unmarshal([]byte(components["polardb"][constant.EncryptedSystemAccounts]), &componentAccounts); err != nil {
		t.Fatalf("decode component accounts: %v", err)
	}
	if got := componentAccounts["postgres"]; got != "encrypted-postgres-password" {
		t.Fatalf("got %q, want component postgres account", got)
	}
	if _, found := componentAccounts["root"]; found {
		t.Fatal("restore annotation leaked another component's system account")
	}
}
