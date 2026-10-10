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

package dataprotection

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	storagev1alpha1 "github.com/apecloud/kubeblocks/apis/storage/v1alpha1"
)

func TestBackupRepoReconcilerMapProviderToReposListsExistingRepos(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := dpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dataprotection scheme: %v", err)
	}
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add storage scheme: %v", err)
	}

	repoA := &dpv1alpha1.BackupRepo{
		ObjectMeta: metav1.ObjectMeta{Name: "repo-a"},
		Spec: dpv1alpha1.BackupRepoSpec{
			StorageProviderRef: "provider-a",
		},
	}
	repoB := &dpv1alpha1.BackupRepo{
		ObjectMeta: metav1.ObjectMeta{Name: "repo-b"},
		Spec: dpv1alpha1.BackupRepoSpec{
			StorageProviderRef: "provider-a",
		},
	}
	repoC := &dpv1alpha1.BackupRepo{
		ObjectMeta: metav1.ObjectMeta{Name: "repo-c"},
		Spec: dpv1alpha1.BackupRepoSpec{
			StorageProviderRef: "provider-b",
		},
	}

	want := []client.ObjectKey{
		client.ObjectKeyFromObject(repoA),
		client.ObjectKeyFromObject(repoB),
	}

	tests := []struct {
		name     string
		provider client.Object
	}{
		{
			name: "dataprotection provider",
			provider: &dpv1alpha1.StorageProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "provider-a"},
			},
		},
		{
			name: "legacy provider",
			provider: &storagev1alpha1.StorageProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "provider-a"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &BackupRepoReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(repoC.DeepCopy(), repoB.DeepCopy(), repoA.DeepCopy()).
					Build(),
			}
			got := reconciler.mapProviderToRepos(context.Background(), tt.provider)

			gotKeys := make([]client.ObjectKey, 0, len(got))
			for _, req := range got {
				gotKeys = append(gotKeys, req.NamespacedName)
			}

			if !reflect.DeepEqual(gotKeys, want) {
				t.Fatalf("mapProviderToRepos() = %#v, want %#v", gotKeys, want)
			}
		})
	}
}
