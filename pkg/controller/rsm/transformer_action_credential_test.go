/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

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

package rsm

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestMembershipActionReceivesCredentialSecretRefs(t *testing.T) {
	const credentialSecretName = "test-cluster-conn-credential"
	rsm := &workloads.ReplicatedStateMachine{
		Spec: workloads.ReplicatedStateMachineSpec{
			Credential: &workloads.Credential{
				Username: workloads.CredentialVar{ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: credentialSecretName},
						Key:                  "username",
					},
				}},
				Password: workloads.CredentialVar{ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: credentialSecretName},
						Key:                  "password",
					},
				}},
			},
			MembershipReconfiguration: &workloads.MembershipReconfiguration{
				MemberJoinAction: &workloads.Action{Command: []string{"join"}},
			},
		},
	}

	template := buildActionPodTemplate(rsm, nil, jobTypeMemberJoinNotifying)
	if len(template.Spec.Containers) != 1 {
		t.Fatalf("expected one membership action container, got %d", len(template.Spec.Containers))
	}

	for _, test := range []struct {
		name string
		key  string
	}{
		{name: usernameCredentialVarName, key: constant.AccountNameForSecret},
		{name: passwordCredentialVarName, key: constant.AccountPasswdForSecret},
	} {
		t.Run(test.name, func(t *testing.T) {
			var env *corev1.EnvVar
			for i := range template.Spec.Containers[0].Env {
				if template.Spec.Containers[0].Env[i].Name == test.name {
					env = &template.Spec.Containers[0].Env[i]
					break
				}
			}
			if env == nil {
				t.Fatalf("action container is missing %s", test.name)
			}
			if env.Value != "" {
				t.Fatal("credential must be passed as a Secret reference, not a literal value")
			}
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Fatal("credential environment variable does not reference a Secret key")
			}
			ref := env.ValueFrom.SecretKeyRef
			if ref.Name != credentialSecretName || ref.Key != test.key {
				t.Fatalf("unexpected Secret reference: got %s/%s, want %s/%s", ref.Name, ref.Key, credentialSecretName, test.key)
			}
		})
	}
}

func TestMemberJoinActionRetriesAreBounded(t *testing.T) {
	rsm := &workloads.ReplicatedStateMachine{
		Spec: workloads.ReplicatedStateMachineSpec{
			MembershipReconfiguration: &workloads.MembershipReconfiguration{
				MemberJoinAction: &workloads.Action{Command: []string{"join"}},
				SwitchoverAction: &workloads.Action{Command: []string{"switchover"}},
			},
		},
	}

	joinJob := buildAction(rsm, "join-job", jobTypeMemberJoinNotifying, jobScenarioMembership, "leader", "target")
	if joinJob.Spec.BackoffLimit == nil || *joinJob.Spec.BackoffLimit != 3 {
		t.Fatalf("member-join Job backoffLimit must be 3, got %v", joinJob.Spec.BackoffLimit)
	}
	if got := joinJob.Spec.Template.Spec.RestartPolicy; got != corev1.RestartPolicyNever {
		t.Fatalf("member-join Pod restartPolicy must be Never, got %q", got)
	}

	switchoverJob := buildAction(rsm, "switchover-job", jobTypeSwitchover, jobScenarioMembership, "leader", "target")
	if switchoverJob.Spec.BackoffLimit != nil {
		t.Fatalf("non-join Job backoffLimit should retain the existing default, got %d", *switchoverJob.Spec.BackoffLimit)
	}
	if got := switchoverJob.Spec.Template.Spec.RestartPolicy; got != corev1.RestartPolicyOnFailure {
		t.Fatalf("non-join Pod restartPolicy should retain OnFailure, got %q", got)
	}
}
