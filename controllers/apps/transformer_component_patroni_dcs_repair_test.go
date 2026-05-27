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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	cfgcore "github.com/apecloud/kubeblocks/pkg/configuration/core"
	ctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

func TestParsePgHBAContent(t *testing.T) {
	t.Parallel()

	content := `
# comment
local   all             all                                     trust
host    all             all             127.0.0.1/32            md5 # inline comment

host replication standby 0.0.0.0/0 md5
`
	require.Equal(t, []string{
		"local all all trust",
		"host all all 127.0.0.1/32 md5",
		"host replication standby 0.0.0.0/0 md5",
	}, parsePgHBAContent(content))
}

func TestMergePgHBARules(t *testing.T) {
	t.Parallel()

	current := []string{
		"local   all all trust",
		"host all all 127.0.0.1/32 md5",
	}
	expected := []string{
		"local all all trust",
		"host replication standby 0.0.0.0/0 md5",
		"host replication standby 0.0.0.0/0 md5",
	}

	merged, changed := mergePgHBARules(current, expected)
	require.True(t, changed)
	require.Equal(t, []string{
		"local all all trust",
		"host all all 127.0.0.1/32 md5",
		"host replication standby 0.0.0.0/0 md5",
	}, merged)
	require.Empty(t, missingPgHBARules(merged, expected))
}

func TestRepairPatroniPgHBA(t *testing.T) {
	t.Parallel()

	client := &fakePatroniConfigClient{
		config: patroniDynamicConfig{
			PostgreSQL: patroniPostgreSQLConfig{
				PgHBA: []string{"local all all trust"},
			},
		},
	}
	repaired, err := repairPatroniPgHBA(context.Background(), client, "http://pod:8008", []string{
		"local all all trust",
		"host replication standby 0.0.0.0/0 md5",
	}, false)

	require.NoError(t, err)
	require.True(t, repaired)
	require.True(t, client.patched)
	require.True(t, client.reloaded)
	require.Equal(t, []string{
		"local all all trust",
		"host replication standby 0.0.0.0/0 md5",
	}, client.config.PostgreSQL.PgHBA)
}

func TestRepairPatroniPgHBANoop(t *testing.T) {
	t.Parallel()

	client := &fakePatroniConfigClient{
		config: patroniDynamicConfig{
			PostgreSQL: patroniPostgreSQLConfig{
				PgHBA: []string{
					"local all all trust",
					"host replication standby 0.0.0.0/0 md5",
				},
			},
		},
	}
	repaired, err := repairPatroniPgHBA(context.Background(), client, "http://pod:8008", []string{
		"host replication standby 0.0.0.0/0 md5",
	}, false)

	require.NoError(t, err)
	require.False(t, repaired)
	require.False(t, client.patched)
	require.False(t, client.reloaded)
}

func TestRepairPatroniPgHBAReloadsAfterPreviousFailure(t *testing.T) {
	t.Parallel()

	client := &fakePatroniConfigClient{
		config: patroniDynamicConfig{
			PostgreSQL: patroniPostgreSQLConfig{
				PgHBA: []string{"host replication standby 0.0.0.0/0 md5"},
			},
		},
	}
	repaired, err := repairPatroniPgHBA(context.Background(), client, "http://pod:8008", []string{
		"host replication standby 0.0.0.0/0 md5",
	}, true)

	require.NoError(t, err)
	require.False(t, repaired)
	require.False(t, client.patched)
	require.True(t, client.reloaded)
}

func TestEnsurePgHBARemoteRules(t *testing.T) {
	t.Parallel()

	rules := ensurePgHBARemoteRules([]string{
		"local all all trust",
		"host all all 127.0.0.1/32 md5",
		"host replication all 127.0.0.1/32 md5",
	})

	require.Contains(t, rules, "host all all 0.0.0.0/0 md5")
	require.Contains(t, rules, "host replication standby 0.0.0.0/0 md5")
	require.Contains(t, rules, "host replication all 127.0.0.1/32 md5")
}

func TestPatroniRESTURL(t *testing.T) {
	t.Parallel()

	url, err := patroniRESTURL("10.0.0.10", 8008)
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.10:8008", url)

	url, err = patroniRESTURL("2001:db8::10", 8008)
	require.NoError(t, err)
	require.Equal(t, "http://[2001:db8::10]:8008", url)

	_, err = patroniRESTURL("not-an-ip", 8008)
	require.Error(t, err)
}

func TestHTTPPatroniConfigClient(t *testing.T) {
	t.Parallel()

	requests := make([]string, 0)
	config := patroniDynamicConfig{
		PostgreSQL: patroniPostgreSQLConfig{
			PgHBA: []string{"local all all trust"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config":
			require.NoError(t, json.NewEncoder(w).Encode(config))
		case r.Method == http.MethodPatch && r.URL.Path == "/config":
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&config))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/reload":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpPatroniConfigClient{client: server.Client()}
	got, err := client.GetConfig(context.Background(), server.URL)
	require.NoError(t, err)
	require.Equal(t, []string{"local all all trust"}, got.PostgreSQL.PgHBA)

	err = client.PatchConfig(context.Background(), server.URL, patroniDynamicConfig{
		PostgreSQL: patroniPostgreSQLConfig{
			PgHBA: []string{"host all all 0.0.0.0/0 md5"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, client.Reload(context.Background(), server.URL))
	require.Equal(t, []string{
		"GET /config",
		"PATCH /config",
		"POST /reload",
	}, requests)
	require.Equal(t, []string{"host all all 0.0.0.0/0 md5"}, config.PostgreSQL.PgHBA)
}

func TestComponentPatroniDCSRepairTransformer(t *testing.T) {
	t.Parallel()

	const (
		namespace     = "default"
		clusterName   = "test"
		componentName = "postgresql"
		compName      = "test-postgresql"
		configName    = "postgresql-configuration"
		leaderPodName = "test-postgresql-1"
	)

	scheme := rscheme
	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: namespace,
		},
	}
	comp := &appsv1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{
			Name:      compName,
			Namespace: namespace,
		},
		Status: appsv1alpha1.ComponentStatus{
			Phase: appsv1alpha1.RunningClusterCompPhase,
		},
	}
	compDef := &appsv1alpha1.ComponentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "postgresql"},
		Spec: appsv1alpha1.ComponentDefinitionSpec{
			ServiceKind: "PostgreSQL",
		},
	}
	leaderPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      leaderPodName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "postgresql",
				Ports: []corev1.ContainerPort{{
					Name:          "patroni",
					ContainerPort: 8010,
				}},
			}},
		},
		Status: corev1.PodStatus{PodIP: "10.0.0.10"},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfgcore.GetComponentCfgName(clusterName, componentName, configName),
			Namespace: namespace,
		},
		Data: map[string]string{
			pgHBAConfigFile: strings.Join([]string{
				"local all all trust",
				"host all all 0.0.0.0/0 md5",
				"host replication standby 0.0.0.0/0 md5",
			}, "\n"),
		},
	}
	runningITS := &workloads.InstanceSet{
		Status: workloads.InstanceSetStatus{
			AvailableReplicas: 2,
			MembersStatus: []workloads.MemberStatus{{
				PodName: leaderPodName,
				ReplicaRole: &workloads.ReplicaRole{
					Name:     "leader",
					IsLeader: true,
				},
			}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster, comp, compDef, leaderPod, configMap).
		Build()
	transCtx := &componentTransformContext{
		Context:       context.Background(),
		Client:        model.NewGraphClient(k8sClient),
		Logger:        log.Log,
		Cluster:       cluster,
		CompDef:       compDef,
		Component:     comp.DeepCopy(),
		ComponentOrig: comp.DeepCopy(),
		SynthesizeComponent: &ctrlcomp.SynthesizedComponent{
			Namespace:   namespace,
			ClusterName: clusterName,
			Name:        componentName,
			ConfigTemplates: []appsv1alpha1.ComponentConfigSpec{{
				ComponentTemplateSpec: appsv1alpha1.ComponentTemplateSpec{
					Name: configName,
				},
			}},
		},
		RunningWorkload: runningITS,
	}
	patroniClient := &fakePatroniConfigClient{
		config: patroniDynamicConfig{
			PostgreSQL: patroniPostgreSQLConfig{
				PgHBA: []string{"local all all trust"},
			},
		},
	}

	transformer := &componentPatroniDCSRepairTransformer{
		patroniClient: patroniClient,
	}
	err := transformer.Transform(transCtx, graph.NewDAG())

	require.NoError(t, err)
	require.True(t, patroniClient.patched)
	require.True(t, patroniClient.reloaded)
	require.Equal(t, "http://10.0.0.10:8010", patroniClient.baseURL)
	require.Empty(t, missingPgHBARules(
		patroniClient.config.PostgreSQL.PgHBA,
		parsePgHBAContent(configMap.Data[pgHBAConfigFile]),
	))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, patroniDCSRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestComponentPatroniDCSRepairTransformerFallbackPgHBA(t *testing.T) {
	t.Parallel()

	transCtx := &componentTransformContext{
		Context: context.Background(),
		Client:  model.NewGraphClient(fake.NewClientBuilder().WithScheme(rscheme).Build()),
		Cluster: &appsv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		},
		SynthesizeComponent: &ctrlcomp.SynthesizedComponent{
			Namespace:   "default",
			ClusterName: "test",
			Name:        "postgresql",
			ConfigTemplates: []appsv1alpha1.ComponentConfigSpec{{
				ComponentTemplateSpec: appsv1alpha1.ComponentTemplateSpec{
					Name: "postgresql-configuration",
				},
			}},
		},
	}

	rules, err := (&componentPatroniDCSRepairTransformer{}).expectedPgHBARules(transCtx)

	require.NoError(t, err)
	require.Equal(t, fallbackPatroniPgHBARules, rules)
}

type fakePatroniConfigClient struct {
	config   patroniDynamicConfig
	baseURL  string
	patched  bool
	reloaded bool
}

func (c *fakePatroniConfigClient) GetConfig(_ context.Context, baseURL string) (*patroniDynamicConfig, error) {
	c.baseURL = baseURL
	config := c.config
	return &config, nil
}

func (c *fakePatroniConfigClient) PatchConfig(
	_ context.Context,
	baseURL string,
	config patroniDynamicConfig,
) error {
	c.baseURL = baseURL
	c.patched = true
	c.config = config
	return nil
}

func (c *fakePatroniConfigClient) Reload(_ context.Context, baseURL string) error {
	c.baseURL = baseURL
	c.reloaded = true
	return nil
}

var _ patroniConfigClient = &fakePatroniConfigClient{}
