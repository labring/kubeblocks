/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
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
		{
			name:          "official postgresql",
			characterType: string(models.OfficialPostgreSQL),
			want:          true,
		},
		{
			name:          "polardb postgresql",
			characterType: string(models.PolarDBPostgreSQL),
			want:          true,
		},
		{
			name:          "apecloud postgresql",
			characterType: string(models.ApecloudPostgreSQL),
			want:          false,
		},
		{
			name:          "legacy postgresql consensus",
			characterType: string(models.PostgreSQL),
			workloadType:  Consensus,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsHAAvailable(tt.characterType, tt.workloadType))
		})
	}
}
