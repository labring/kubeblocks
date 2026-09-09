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

package apps

import (
	"testing"

	"github.com/stretchr/testify/require"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestDefaultBackupMethodByServiceKindSupportsPolarDBPostgreSQL(t *testing.T) {
	handler := appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler
	compDefName := "polardb-postgresql-ha"
	transformer := &clusterBackupPolicyTransformer{
		clusterTransformContext: &clusterTransformContext{
			ComponentDefs: map[string]*appsv1alpha1.ComponentDefinition{
				compDefName: {
					Spec: appsv1alpha1.ComponentDefinitionSpec{
						LifecycleActions: &appsv1alpha1.ComponentLifecycleActions{
							RoleProbe: &appsv1alpha1.RoleProbe{
								LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{
									BuiltinHandler: &handler,
								},
							},
						},
					},
				},
			},
		},
	}

	method := transformer.defaultBackupMethodByServiceKind(componentItem{
		compSpec: &appsv1alpha1.ClusterComponentSpec{
			Name:         "postgresql",
			ComponentDef: compDefName,
		},
	})

	require.Equal(t, defaultPostgresBackupMethod, method)
}
