/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package dataprotection

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
	dpbackup "github.com/apecloud/kubeblocks/pkg/dataprotection/backup"
	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

func TestSetEncryptedSystemAccountsAnnotation(t *testing.T) {
	const (
		encryptionKey = "backup-system-account-test-key"
		password      = "source-postgres-password"
	)
	previousKey := viper.GetString(constant.CfgKeyDPEncryptionKey)
	viper.Set(constant.CfgKeyDPEncryptionKey, encryptionKey)
	t.Cleanup(func() { viper.Set(constant.CfgKeyDPEncryptionKey, previousKey) })

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := appsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}
	cluster := &appsv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "polardb", Namespace: "default"}}
	labels := constant.GetClusterWellKnownLabels(cluster.Name)
	labels[constant.KBAppComponentLabelKey] = "polardb"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "polardb-account-postgres", Namespace: "default", Labels: labels},
		Data: map[string][]byte{
			constant.AccountNameForSecret:   []byte("postgres"),
			constant.AccountPasswdForSecret: []byte(password),
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, secret).Build()
	request := &dpbackup.Request{
		Backup:     &dpv1alpha1.Backup{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}},
		RequestCtx: intctrlutil.RequestCtx{Ctx: context.Background()},
		Client:     cli,
	}

	if err := setEncryptedSystemAccountsAnnotation(request, cluster); err != nil {
		t.Fatalf("set encrypted account annotation: %v", err)
	}
	accountsByComponent := map[string]map[string]string{}
	if err := json.Unmarshal([]byte(request.Backup.Annotations[constant.EncryptedSystemAccountsAnnotationKey]), &accountsByComponent); err != nil {
		t.Fatalf("decode encrypted account annotation: %v", err)
	}
	ciphertext := accountsByComponent["polardb"]["postgres"]
	got, err := intctrlutil.NewEncryptor(encryptionKey).Decrypt([]byte(ciphertext))
	if err != nil {
		t.Fatalf("decrypt account password: %v", err)
	}
	if got != password {
		t.Fatalf("got %q, want %q", got, password)
	}
}
