/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
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
