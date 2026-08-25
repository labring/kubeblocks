/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/

package multicluster

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

func TestPlacementEventHandler(t *testing.T) {
	const placement = "worker-a"
	var got string
	delegate := handler.Funcs{
		CreateFunc: func(ctx context.Context, _ event.CreateEvent, _ workqueue.RateLimitingInterface) {
			got, _ = FromContext(ctx)
		},
	}
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	defer queue.ShutDown()

	wrapped := &placementEventHandler{placement: placement, delegate: delegate}
	wrapped.Create(context.Background(), event.CreateEvent{Object: &corev1.Endpoints{}}, queue)

	if got != placement {
		t.Fatalf("event placement = %q, want %q", got, placement)
	}
}
