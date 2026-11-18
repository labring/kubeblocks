/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/

package replica

import (
	"encoding/json"
	"testing"

	"github.com/apecloud/kubeblocks/pkg/common"
	"github.com/apecloud/kubeblocks/pkg/lorry/dcs"
	"github.com/apecloud/kubeblocks/pkg/lorry/engines"
)

func TestBuildGlobalRoleSnapshotClearsOldLeader(t *testing.T) {
	manager := &engines.MockManager{}
	manager.CurrentMemberName = "mongodb-1"
	cluster := &dcs.Cluster{
		Members: []dcs.Member{
			{Name: "mongodb-0", UID: "old-uid", Role: "primary", PodIP: "10.0.0.1", LorryPort: "3501"},
			{Name: "mongodb-1", UID: "new-uid", Role: "primary", PodIP: "10.0.0.2", LorryPort: "3501"},
		},
	}

	value := (&CheckRole{}).buildGlobalRoleSnapshot(cluster, manager, "primary")
	snapshot := &common.GlobalRoleSnapshot{}
	if err := json.Unmarshal([]byte(value), snapshot); err != nil {
		t.Fatalf("unmarshal role snapshot: %v", err)
	}
	if len(snapshot.PodRoleNamePairs) != 2 {
		t.Fatalf("role pair count = %d, want 2", len(snapshot.PodRoleNamePairs))
	}
	if got := snapshot.PodRoleNamePairs[0]; got.PodName != "mongodb-1" || got.RoleName != "primary" {
		t.Fatalf("new primary pair = %#v", got)
	}
	if got := snapshot.PodRoleNamePairs[1]; got.PodName != "mongodb-0" || got.RoleName != "" || got.PodUID != "old-uid" {
		t.Fatalf("old primary pair = %#v", got)
	}
}
