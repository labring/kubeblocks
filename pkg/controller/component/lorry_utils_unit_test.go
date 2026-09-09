/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package component

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestAppendPolarDBPostgreSQLHAEnv(t *testing.T) {
	tests := []struct {
		name          string
		envs          []corev1.EnvVar
		mainContainer *corev1.Container
		want          string
	}{
		{
			name: "defaults to disabled",
			want: "false",
		},
		{
			name: "preserves lorry environment",
			envs: []corev1.EnvVar{{Name: constant.KBEnvEnableHA, Value: "true"}},
			want: "true",
		},
		{
			name: "inherits database container environment",
			mainContainer: &corev1.Container{Env: []corev1.EnvVar{{
				Name: constant.KBEnvEnableHA, Value: "true",
			}}},
			want: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envs := appendPolarDBPostgreSQLHAEnv(tt.envs, tt.mainContainer)
			for _, env := range envs {
				if env.Name == constant.KBEnvEnableHA {
					if env.Value != tt.want {
						t.Fatalf("%s = %q, want %q", constant.KBEnvEnableHA, env.Value, tt.want)
					}
					return
				}
			}
			t.Fatalf("%s was not added", constant.KBEnvEnableHA)
		})
	}
}
