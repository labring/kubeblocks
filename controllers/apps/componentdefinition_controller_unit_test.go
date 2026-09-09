/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
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
			LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &handler},
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
	for _, handler := range getBuiltinActionHandlers() {
		if handler == appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler {
			return
		}
	}
	t.Fatalf("getBuiltinActionHandlers() does not include %q", appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler)
}

func TestValidateLifecycleActionBuiltInHandlersRejectsUnsupportedActionHandler(t *testing.T) {
	supported := appsv1alpha1.PolarDBPostgresqlBuiltinActionHandler
	unsupported := appsv1alpha1.BuiltinActionHandlerType("not-supported")
	reconciler := &ComponentDefinitionReconciler{}

	lifecycleActions := &appsv1alpha1.ComponentLifecycleActions{
		RoleProbe: &appsv1alpha1.RoleProbe{
			LifecycleActionHandler: appsv1alpha1.LifecycleActionHandler{BuiltinHandler: &supported},
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
