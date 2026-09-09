/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package restore

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestRestoreLabelValue(t *testing.T) {
	validName := "polardb-mongo-restore"
	if got := RestoreLabelValue(validName); got != validName {
		t.Fatalf("expected valid name to be preserved, got %q", got)
	}

	longName := strings.TrimSuffix(strings.Repeat("restore-", 12), "-")
	labelValue := RestoreLabelValue(longName)
	if labelValue == longName {
		t.Fatal("expected long restore name to be hashed")
	}
	if labelValue != RestoreLabelValue(longName) {
		t.Fatal("expected restore label value to be stable")
	}
	if errs := validation.IsValidLabelValue(labelValue); len(errs) != 0 {
		t.Fatalf("expected valid label value, got %v", errs)
	}
	if got := BuildRestoreAnnotations(longName)[DataProtectionRestoreNameAnnotationKey]; got != longName {
		t.Fatalf("expected full restore name annotation, got %q", got)
	}
}
