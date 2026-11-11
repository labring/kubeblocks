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
	"sigs.k8s.io/controller-runtime/pkg/client"
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

func TestPatroniRESTCredentialsFromPod(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "patroni-auth", Namespace: "default"},
		Data: map[string][]byte{
			"username": []byte("patroni-user"),
			"password": []byte("patroni-password"),
		},
	}
	secretRef := func(key string) corev1.EnvVar {
		return corev1.EnvVar{
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name},
					Key:                  key,
				},
			},
		}
	}

	tests := []struct {
		name             string
		envs             []corev1.EnvVar
		objects          []client.Object
		containerCount   int
		overrideUsername string
		expected         patroniRESTCredentials
		expectedErr      string
	}{{
		name: "no credentials",
	}, {
		name: "direct credentials",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, Value: "direct-user"},
			{Name: patroniRESTAPIPasswordEnv, Value: "direct-password"},
		},
		expected: patroniRESTCredentials{username: "direct-user", password: "direct-password"},
	}, {
		name: "secretKeyRef credentials",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, ValueFrom: secretRef("username").ValueFrom},
			{Name: patroniRESTAPIPasswordEnv, ValueFrom: secretRef("password").ValueFrom},
		},
		objects:  []client.Object{secret},
		expected: patroniRESTCredentials{username: "patroni-user", password: "patroni-password"},
	}, {
		name: "identical credentials in multiple containers",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, ValueFrom: secretRef("username").ValueFrom},
			{Name: patroniRESTAPIPasswordEnv, ValueFrom: secretRef("password").ValueFrom},
		},
		containerCount: 2,
		objects:        []client.Object{secret},
		expected:       patroniRESTCredentials{username: "patroni-user", password: "patroni-password"},
	}, {
		name: "conflicting credentials in multiple containers",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, Value: "direct-user"},
			{Name: patroniRESTAPIPasswordEnv, Value: "direct-password"},
		},
		containerCount:   2,
		overrideUsername: "conflicting-user",
		expectedErr:      "different values",
	}, {
		name: "partial credentials",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, Value: "direct-user"},
		},
		expectedErr: "must be set together",
	}, {
		name: "missing secret",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, ValueFrom: secretRef("username").ValueFrom},
			{Name: patroniRESTAPIPasswordEnv, ValueFrom: secretRef("password").ValueFrom},
		},
		expectedErr: `get secret "patroni-auth"`,
	}, {
		name: "missing secret key",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, ValueFrom: secretRef("missing-username").ValueFrom},
			{Name: patroniRESTAPIPasswordEnv, ValueFrom: secretRef("password").ValueFrom},
		},
		objects:     []client.Object{secret},
		expectedErr: `has no key "missing-username"`,
	}, {
		name: "empty secret value",
		envs: []corev1.EnvVar{
			{Name: patroniRESTAPIUsernameEnv, ValueFrom: secretRef("username").ValueFrom},
			{Name: patroniRESTAPIPasswordEnv, ValueFrom: secretRef("password").ValueFrom},
		},
		objects: []client.Object{
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secret.Name, Namespace: secret.Namespace},
				Data: map[string][]byte{
					"username": []byte("patroni-user"),
					"password": {},
				},
			},
		},
		expectedErr: "is empty",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containers := make([]corev1.Container, tt.containerCount)
			for i := range containers {
				envs := make([]corev1.EnvVar, len(tt.envs))
				copy(envs, tt.envs)
				if tt.overrideUsername != "" && i > 0 {
					envs[0].Value = tt.overrideUsername
					envs[0].ValueFrom = nil
				}
				containers[i] = corev1.Container{Env: envs}
			}
			if len(containers) == 0 {
				containers = []corev1.Container{{Env: tt.envs}}
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "leader", Namespace: "default"},
				Spec:       corev1.PodSpec{Containers: containers},
			}
			builder := fake.NewClientBuilder().WithScheme(rscheme)
			if len(tt.objects) > 0 {
				builder = builder.WithObjects(tt.objects...)
			}
			credentials, err := patroniRESTCredentialsFromPod(context.Background(), builder.Build(), pod)

			if tt.expectedErr != "" {
				require.ErrorContains(t, err, tt.expectedErr)
				require.NotContains(t, err.Error(), "patroni-password")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, credentials)
		})
	}
}

func TestHTTPPatroniConfigClientBasicAuth(t *testing.T) {
	t.Parallel()

	requests := make([]string, 0)
	config := patroniDynamicConfig{
		PostgreSQL: patroniPostgreSQLConfig{
			PgHBA: []string{"local all all trust"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "patroni-user" || password != "patroni-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config":
			require.NoError(t, json.NewEncoder(w).Encode(config))
		case r.Method == http.MethodPatch && r.URL.Path == "/config":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&config))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/reload":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &httpPatroniConfigClient{
		client:      server.Client(),
		credentials: patroniRESTCredentials{username: "patroni-user", password: "patroni-password"},
	}
	_, err := client.GetConfig(context.Background(), server.URL)
	require.NoError(t, err)
	require.NoError(t, client.PatchConfig(context.Background(), server.URL, patroniDynamicConfig{
		PostgreSQL: patroniPostgreSQLConfig{
			PgHBA: []string{"host all all 0.0.0.0/0 md5"},
		},
	}))
	require.NoError(t, client.Reload(context.Background(), server.URL))
	require.Equal(t, []string{
		"GET /config",
		"PATCH /config",
		"POST /reload",
	}, requests)
}

func TestHTTPPatroniConfigClientUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &httpPatroniConfigClient{
		client:      server.Client(),
		credentials: patroniRESTCredentials{username: "patroni-user", password: "invalid-password"},
	}
	_, err := client.GetConfig(context.Background(), server.URL)

	require.ErrorContains(t, err, "401 Unauthorized")
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
		require.Empty(t, r.Header.Get("Authorization"))
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
