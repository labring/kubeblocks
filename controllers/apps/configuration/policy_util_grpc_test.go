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

package configuration

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cfgproto "github.com/apecloud/kubeblocks/pkg/configuration/proto"
)

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

type stubReconfigureClient struct {
	onlineUpgradeParams func(context.Context, *cfgproto.OnlineUpgradeParamsRequest) (*cfgproto.OnlineUpgradeParamsResponse, error)
	stopContainer       func(context.Context, *cfgproto.StopContainerRequest) (*cfgproto.StopContainerResponse, error)
}

func (c *stubReconfigureClient) OnlineUpgradeParams(ctx context.Context, request *cfgproto.OnlineUpgradeParamsRequest, _ ...grpc.CallOption) (*cfgproto.OnlineUpgradeParamsResponse, error) {
	return c.onlineUpgradeParams(ctx, request)
}

func (c *stubReconfigureClient) StopContainer(ctx context.Context, request *cfgproto.StopContainerRequest, _ ...grpc.CallOption) (*cfgproto.StopContainerResponse, error) {
	return c.stopContainer(ctx, request)
}

func TestCommonOnlineUpdateWithPodClosesClient(t *testing.T) {
	rpcErr := errors.New("rpc failed")
	tests := []struct {
		name        string
		client      cfgproto.ReconfigureClient
		ctx         func() context.Context
		createErr   error
		wantErr     error
		wantErrText string
		wantCloses  int
	}{
		{
			name: "success",
			client: &stubReconfigureClient{onlineUpgradeParams: func(context.Context, *cfgproto.OnlineUpgradeParamsRequest) (*cfgproto.OnlineUpgradeParamsResponse, error) {
				return &cfgproto.OnlineUpgradeParamsResponse{}, nil
			}},
			ctx:        context.Background,
			wantCloses: 1,
		},
		{
			name: "RPC error",
			client: &stubReconfigureClient{onlineUpgradeParams: func(context.Context, *cfgproto.OnlineUpgradeParamsRequest) (*cfgproto.OnlineUpgradeParamsResponse, error) {
				return nil, rpcErr
			}},
			ctx:        context.Background,
			wantErr:    rpcErr,
			wantCloses: 1,
		},
		{
			name: "service error",
			client: &stubReconfigureClient{onlineUpgradeParams: func(context.Context, *cfgproto.OnlineUpgradeParamsRequest) (*cfgproto.OnlineUpgradeParamsResponse, error) {
				return &cfgproto.OnlineUpgradeParamsResponse{ErrMessage: "invalid configuration"}, nil
			}},
			ctx:         context.Background,
			wantErrText: "invalid configuration",
			wantCloses:  1,
		},
		{
			name: "canceled context",
			client: &stubReconfigureClient{onlineUpgradeParams: func(ctx context.Context, _ *cfgproto.OnlineUpgradeParamsRequest) (*cfgproto.OnlineUpgradeParamsResponse, error) {
				return nil, ctx.Err()
			}},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr:    context.Canceled,
			wantCloses: 1,
		},
		{
			name:       "create error",
			ctx:        context.Background,
			createErr:  rpcErr,
			wantErr:    rpcErr,
			wantCloses: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closes := 0
			create := func(string) (cfgproto.ReconfigureClient, io.Closer, error) {
				if test.createErr != nil {
					return nil, nil, test.createErr
				}
				return test.client, closerFunc(func() error { closes++; return nil }), nil
			}

			err := commonOnlineUpdateWithPod(testReconfigurePod(), test.ctx(), create, "config", map[string]string{"key": "value"})

			assertReconfigureError(t, err, test.wantErr, test.wantErrText)
			if closes != test.wantCloses {
				t.Fatalf("close count = %d, want %d", closes, test.wantCloses)
			}
		})
	}
}

func TestCommonStopContainerWithPodClosesClient(t *testing.T) {
	rpcErr := errors.New("rpc failed")
	tests := []struct {
		name        string
		client      cfgproto.ReconfigureClient
		createErr   error
		wantErr     error
		wantErrText string
		wantCloses  int
	}{
		{
			name: "success",
			client: &stubReconfigureClient{stopContainer: func(context.Context, *cfgproto.StopContainerRequest) (*cfgproto.StopContainerResponse, error) {
				return &cfgproto.StopContainerResponse{}, nil
			}},
			wantCloses: 1,
		},
		{
			name: "RPC error",
			client: &stubReconfigureClient{stopContainer: func(context.Context, *cfgproto.StopContainerRequest) (*cfgproto.StopContainerResponse, error) {
				return nil, rpcErr
			}},
			wantErr:    rpcErr,
			wantCloses: 1,
		},
		{
			name: "service error",
			client: &stubReconfigureClient{stopContainer: func(context.Context, *cfgproto.StopContainerRequest) (*cfgproto.StopContainerResponse, error) {
				return &cfgproto.StopContainerResponse{ErrMessage: "failed to stop container"}, nil
			}},
			wantErrText: "failed to stop container",
			wantCloses:  1,
		},
		{
			name:       "create error",
			createErr:  rpcErr,
			wantErr:    rpcErr,
			wantCloses: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closes := 0
			create := func(string) (cfgproto.ReconfigureClient, io.Closer, error) {
				if test.createErr != nil {
					return nil, nil, test.createErr
				}
				return test.client, closerFunc(func() error { closes++; return nil }), nil
			}

			err := commonStopContainerWithPod(testReconfigurePod(), context.Background(), []string{"database"}, create)

			assertReconfigureError(t, err, test.wantErr, test.wantErrText)
			if closes != test.wantCloses {
				t.Fatalf("close count = %d, want %d", closes, test.wantCloses)
			}
		})
	}
}

func assertReconfigureError(t *testing.T, got, want error, wantText string) {
	t.Helper()

	if want != nil && !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
	if wantText != "" && (got == nil || !strings.Contains(got.Error(), wantText)) {
		t.Fatalf("error = %v, want containing %q", got, wantText)
	}
	if want == nil && wantText == "" && got != nil {
		t.Fatalf("unexpected error: %v", got)
	}
}

func testReconfigurePod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod"},
		Status: corev1.PodStatus{
			PodIP: "127.0.0.1",
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:        "database",
				ContainerID: "containerd://container-id",
			}},
		},
	}
}
