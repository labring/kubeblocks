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

package dataprotection

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dprestore "github.com/apecloud/kubeblocks/pkg/dataprotection/restore"
)

func TestParseRestoreJobUsesFullNameAnnotation(t *testing.T) {
	restoreName := strings.TrimSuffix(strings.Repeat("restore-", 12), "-")
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace:   "default",
		Labels:      dprestore.BuildRestoreLabels(restoreName),
		Annotations: dprestore.BuildRestoreAnnotations(restoreName),
	}}

	requests := (&RestoreReconciler{}).parseRestoreJob(context.Background(), job)
	if len(requests) != 1 {
		t.Fatalf("expected one reconcile request, got %d", len(requests))
	}
	if requests[0].Namespace != job.Namespace || requests[0].Name != restoreName {
		t.Fatalf("unexpected reconcile request: %#v", requests[0])
	}
}
