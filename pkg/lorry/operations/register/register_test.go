/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package register

import "testing"

func TestSQLOperationsAreNotRegistered(t *testing.T) {
	registered := Operations()
	for _, name := range []string{"query", "exec"} {
		if _, ok := registered[name]; ok {
			t.Fatalf("SQL operation %q must not be exposed by the Lorry HTTP API", name)
		}
	}
}
