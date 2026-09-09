/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package apps

import "testing"

func TestSystemAccountExists(t *testing.T) {
	accounts := []map[string]any{
		{"roleName": "kbadmin"},
		{"userName": "kbprobe"},
	}

	for _, accountName := range []string{"kbadmin", "kbprobe"} {
		if !systemAccountExists(accounts, accountName) {
			t.Fatalf("expected %q to be found", accountName)
		}
	}
	if systemAccountExists(accounts, "kbdataprotection") {
		t.Fatal("unexpected system account found")
	}
}
