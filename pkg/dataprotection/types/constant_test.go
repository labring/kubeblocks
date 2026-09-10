/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package types

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestBackupPolicyLabelValue(t *testing.T) {
	validName := "polardb-mongo-backup-policy"
	if got := BackupPolicyLabelValue(validName); got != validName {
		t.Fatalf("expected valid name to be preserved, got %q", got)
	}

	longName := strings.TrimSuffix(strings.Repeat("polardb-mongo-compat-", 5), "-")
	labelValue := BackupPolicyLabelValue(longName)
	if labelValue == longName {
		t.Fatal("expected long backup policy name to be hashed")
	}
	if labelValue != BackupPolicyLabelValue(longName) {
		t.Fatal("expected backup policy label value to be stable")
	}
	if errs := validation.IsValidLabelValue(labelValue); len(errs) != 0 {
		t.Fatalf("expected valid label value, got %v", errs)
	}
}
