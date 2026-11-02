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

package component

import (
	"reflect"
	"testing"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
)

func TestRSMCredentialConvertorLegacyConnectionCredential(t *testing.T) {
	const clusterName = "test-cluster"
	const componentName = "dn"
	const legacyCompDefName = "polardbx-dn"

	rsm, err := BuildRSMFrom(&SynthesizedComponent{
		ClusterName:        clusterName,
		Name:               componentName,
		CompDefName:        legacyCompDefName,
		ClusterCompDefName: legacyCompDefName,
		RSMSpec: &appsv1alpha1.RSMSpec{MembershipReconfiguration: &workloads.MembershipReconfiguration{
			MemberJoinAction: &workloads.Action{Command: []string{"join"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if rsm.Spec.Credential == nil {
		t.Fatal("legacy component without init account should use the cluster connection credential")
	}

	secretName := constant.GenerateDefaultConnCredential(clusterName)
	assertCredentialSecretRef(t, rsm.Spec.Credential.Username, secretName, constant.AccountNameForSecret)
	assertCredentialSecretRef(t, rsm.Spec.Credential.Password, secretName, constant.AccountPasswdForSecret)
}

func TestRSMCredentialConvertorPreservesExplicitInitAccount(t *testing.T) {
	const clusterName = "test-cluster"
	const componentName = "dn"
	const legacyCompDefName = "polardbx-dn"
	const initAccountName = "root"

	rsm, err := BuildRSMFrom(&SynthesizedComponent{
		ClusterName:        clusterName,
		Name:               componentName,
		CompDefName:        legacyCompDefName,
		ClusterCompDefName: legacyCompDefName,
		SystemAccounts: []appsv1alpha1.SystemAccount{{
			Name:        initAccountName,
			InitAccount: true,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if rsm.Spec.Credential == nil {
		t.Fatal("explicit init account should be used as the RSM credential")
	}

	secretName := constant.GenerateAccountSecretName(clusterName, componentName, initAccountName)
	assertCredentialSecretRef(t, rsm.Spec.Credential.Username, secretName, constant.AccountNameForSecret)
	assertCredentialSecretRef(t, rsm.Spec.Credential.Password, secretName, constant.AccountPasswdForSecret)
}

func TestRSMCredentialConvertorDoesNotFallbackForNewComponentDefinition(t *testing.T) {
	rsm, err := BuildRSMFrom(&SynthesizedComponent{
		ClusterName: "test-cluster",
		Name:        "dn",
		CompDefName: "polardbx-dn",
	}, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if rsm.Spec.Credential != nil {
		t.Fatalf("new ComponentDefinition without init account should not get a legacy credential fallback: %#v", rsm.Spec.Credential)
	}
}

func TestRSMCredentialConvertorDoesNotFallbackWithoutMembershipActions(t *testing.T) {
	rsm, err := BuildRSMFrom(&SynthesizedComponent{
		ClusterName:        "test-cluster",
		Name:               "dn",
		CompDefName:        "polardbx-dn",
		ClusterCompDefName: "polardbx-dn",
		RSMSpec:            &appsv1alpha1.RSMSpec{},
	}, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if rsm.Spec.Credential != nil {
		t.Fatalf("legacy component without membership actions should not receive a credential fallback: %#v", rsm.Spec.Credential)
	}
}

func assertCredentialSecretRef(t *testing.T, credential workloads.CredentialVar, secretName, key string) {
	t.Helper()
	if credential.Value != "" {
		t.Fatalf("credential value must remain unset, got non-empty literal")
	}
	if credential.ValueFrom == nil || credential.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("credential does not reference a Secret key")
	}
	ref := credential.ValueFrom.SecretKeyRef
	if ref.Name != secretName || ref.Key != key {
		t.Fatalf("unexpected credential Secret reference: got %s/%s, want %s/%s", ref.Name, ref.Key, secretName, key)
	}
}

func TestRSMMembershipReconfigurationConvertor(t *testing.T) {
	membershipReconfiguration := &workloads.MembershipReconfiguration{
		SwitchoverAction:  &workloads.Action{Image: "actions:v1", Command: []string{"switchover"}},
		MemberJoinAction:  &workloads.Action{Image: "actions:v1", Command: []string{"member-join"}},
		MemberLeaveAction: &workloads.Action{Image: "actions:v1", Command: []string{"member-leave"}},
		LogSyncAction:     &workloads.Action{Image: "actions:v1", Command: []string{"log-sync"}},
		PromoteAction:     &workloads.Action{Image: "actions:v1", Command: []string{"promote"}},
	}
	synthesizedComp := &SynthesizedComponent{
		RSMSpec: &appsv1alpha1.RSMSpec{MembershipReconfiguration: membershipReconfiguration},
	}

	rsm, err := BuildRSMFrom(synthesizedComp, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if !reflect.DeepEqual(rsm.Spec.MembershipReconfiguration, membershipReconfiguration) {
		t.Fatalf("membership reconfiguration was not propagated: got %#v, want %#v", rsm.Spec.MembershipReconfiguration, membershipReconfiguration)
	}
	if rsm.Spec.MembershipReconfiguration == membershipReconfiguration {
		t.Fatal("membership reconfiguration must be copied, not aliased")
	}

	// RSM mutations must not change the component definition's action configuration.
	rsm.Spec.MembershipReconfiguration.MemberJoinAction.Command[0] = "changed"
	if got := membershipReconfiguration.MemberJoinAction.Command[0]; got != "member-join" {
		t.Fatalf("source member-join command changed through RSM copy: got %q", got)
	}
}

func TestRSMMembershipReconfigurationConvertorUnset(t *testing.T) {
	rsm, err := BuildRSMFrom(&SynthesizedComponent{}, nil)
	if err != nil {
		t.Fatalf("BuildRSMFrom() error = %v", err)
	}
	if rsm.Spec.MembershipReconfiguration != nil {
		t.Fatalf("membership reconfiguration should be unset, got %#v", rsm.Spec.MembershipReconfiguration)
	}
}
