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
	"strings"
	"testing"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
)

func TestValidateLifecycleActionBuiltInHandlersSupportsPolarDBPostgreSQL(t *testing.T) {
	handler := appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler
	reconciler := &ComponentDefinitionReconciler{}

	lifecycleActions := &appsv1alpha1.ComponentLifecycleActions{
		RoleProbe: &appsv1alpha1.RoleProbe{
			LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{
				BuiltinHandler: &handler,
			},
		},
		MemberJoin:  &appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &handler},
		MemberLeave: &appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &handler},
		Readonly:    &appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &handler},
		Readwrite:   &appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &handler},
	}

	if err := reconciler.validateLifecycleActionBuiltInHandlers(lifecycleActions); err != nil {
		t.Fatalf("validateLifecycleActionBuiltInHandlers() error = %v", err)
	}
}

func TestGetBuiltinActionHandlersIncludesPolarDBPostgreSQL(t *testing.T) {
	found := false
	for _, handler := range getBuiltinActionHandlers() {
		if handler == appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("getBuiltinActionHandlers() does not include %q", appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler)
	}
}

func TestValidateLifecycleActionBuiltInHandlersRejectsUnsupportedHandlersOutsideRoleProbe(t *testing.T) {
	supported := appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler
	unsupported := appsv1alpha1.BuiltinActionHandlerType("not-supported")
	reconciler := &ComponentDefinitionReconciler{}

	lifecycleActions := &appsv1alpha1.ComponentLifecycleActions{
		RoleProbe: &appsv1alpha1.RoleProbe{
			LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{
				BuiltinHandler: &supported,
			},
		},
		MemberJoin: &appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &unsupported},
	}

	err := reconciler.validateLifecycleActionBuiltInHandlers(lifecycleActions)
	if err == nil {
		t.Fatal("validateLifecycleActionBuiltInHandlers() expected error, got nil")
	}
	if !strings.Contains(err.Error(), string(unsupported)) {
		t.Fatalf("validateLifecycleActionBuiltInHandlers() error = %v, want unsupported handler %q", err, unsupported)
	}
}
