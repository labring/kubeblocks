/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package apps

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dpv1alpha1 "github.com/apecloud/kubeblocks/apis/dataprotection/v1alpha1"
	dptypes "github.com/apecloud/kubeblocks/pkg/dataprotection/types"
)

func TestKindsForCompDeleteIncludesPodDisruptionBudget(t *testing.T) {
	t.Parallel()

	for _, kind := range kindsForCompDelete() {
		if _, ok := kind.(*policyv1.PodDisruptionBudgetList); ok {
			return
		}
	}

	t.Fatalf("expected component delete kinds to include PodDisruptionBudgetList")
}

func TestRemoveOrphanBackupJobFinalizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		job                  *batchv1.Job
		backup               *dpv1alpha1.Backup
		wantFinalizerRemoved bool
	}{
		{
			name: "removes finalizer when labeled backup job has no backup",
			job: newBackupJob("job-with-missing-backup", map[string]string{
				dptypes.BackupNameLabelKey:      "missing-backup",
				dptypes.BackupNamespaceLabelKey: "default",
			}, nil),
			wantFinalizerRemoved: true,
		},
		{
			name: "keeps finalizer when backup still exists",
			job: newBackupJob("job-with-existing-backup", map[string]string{
				dptypes.BackupNameLabelKey:      "existing-backup",
				dptypes.BackupNamespaceLabelKey: "default",
			}, nil),
			backup: newBackup("existing-backup"),
		},
		{
			name: "removes finalizer when owner referenced backup is missing",
			job: newBackupJob("job-with-owner-ref", nil, []metav1.OwnerReference{
				{
					APIVersion: dpv1alpha1.GroupVersion.String(),
					Kind:       dptypes.BackupKind,
					Name:       "owner-ref-backup",
				},
			}),
			wantFinalizerRemoved: true,
		},
		{
			name: "keeps finalizer when job is not associated with a backup",
			job:  newBackupJob("plain-job", nil, nil),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			objects := []client.Object{tt.job}
			if tt.backup != nil {
				objects = append(objects, tt.backup)
			}
			cli := newDeletionCompatibilityFakeClient(t, objects...)
			if err := removeOrphanBackupJobFinalizer(ctx, cli, tt.job, nil); err != nil {
				t.Fatalf("remove orphan backup job finalizer: %v", err)
			}

			got := &batchv1.Job{}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(tt.job), got); err != nil {
				t.Fatalf("get job: %v", err)
			}

			hasFinalizer := controllerutil.ContainsFinalizer(got, dptypes.DataProtectionFinalizerName)
			if tt.wantFinalizerRemoved && hasFinalizer {
				t.Fatalf("expected dataprotection finalizer to be removed")
			}
			if !tt.wantFinalizerRemoved && !hasFinalizer {
				t.Fatalf("expected dataprotection finalizer to be kept")
			}
		})
	}
}

func newDeletionCompatibilityFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}
	if err := dpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add dataprotection scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()
}

func newBackupJob(name string, labels map[string]string, owners []metav1.OwnerReference) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            name,
			Labels:          labels,
			OwnerReferences: owners,
			Finalizers: []string{
				dptypes.DataProtectionFinalizerName,
			},
		},
	}
}

func newBackup(name string) *dpv1alpha1.Backup {
	return &dpv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
		},
	}
}
