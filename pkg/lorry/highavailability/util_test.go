/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package highavailability

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/apecloud/kubeblocks/pkg/lorry/engines/models"
)

func TestIsHAAvailableForPostgreSQLHandlers(t *testing.T) {
	tests := []struct {
		name          string
		characterType string
		workloadType  string
		want          bool
	}{
		{name: "official postgresql", characterType: string(models.OfficialPostgreSQL), want: true},
		{name: "polardb postgresql", characterType: string(models.PolarDBPostgreSQL), want: true},
		{name: "apecloud postgresql", characterType: string(models.ApecloudPostgreSQL), want: true},
		{name: "legacy postgresql consensus", characterType: string(models.PostgreSQL), workloadType: Consensus, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsHAAvailable(tt.characterType, tt.workloadType))
		})
	}
}
