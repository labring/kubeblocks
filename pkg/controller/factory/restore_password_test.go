/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package factory

import (
	"encoding/json"
	"testing"

	"github.com/apecloud/kubeblocks/pkg/constant"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

func TestGetRestoreSystemAccountPassword(t *testing.T) {
	const (
		encryptionKey = "restore-system-account-test-key"
		componentName = "polardb"
		accountName   = "postgres"
		password      = "restored-password"
	)
	previousKey := viper.GetString(constant.CfgKeyDPEncryptionKey)
	viper.Set(constant.CfgKeyDPEncryptionKey, encryptionKey)
	t.Cleanup(func() { viper.Set(constant.CfgKeyDPEncryptionKey, previousKey) })

	ciphertext, err := intctrlutil.NewEncryptor(encryptionKey).Encrypt([]byte(password))
	if err != nil {
		t.Fatalf("encrypt password: %v", err)
	}
	accountsJSON, err := json.Marshal(map[string]string{accountName: ciphertext})
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}
	restoreJSON, err := json.Marshal(map[string]map[string]string{
		componentName: {
			constant.EncryptedSystemAccounts: string(accountsJSON),
		},
	})
	if err != nil {
		t.Fatalf("marshal restore annotation: %v", err)
	}

	got, err := GetRestoreSystemAccountPassword(
		map[string]string{constant.RestoreFromBackupAnnotationKey: string(restoreJSON)},
		componentName,
		accountName,
	)
	if err != nil {
		t.Fatalf("get restored password: %v", err)
	}
	if got != password {
		t.Fatalf("got %q, want %q", got, password)
	}
}

func TestGetRestoreSystemAccountPasswordRejectsMalformedAccounts(t *testing.T) {
	annotations := map[string]string{
		constant.RestoreFromBackupAnnotationKey: `{"polardb":{"encryptedSystemAccounts":"not-json"}}`,
	}
	if _, err := GetRestoreSystemAccountPassword(annotations, "polardb", "postgres"); err == nil {
		t.Fatal("expected malformed account data to fail")
	}
}
