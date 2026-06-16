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
	"fmt"
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
	"github.com/apecloud/kubeblocks/pkg/constant"
	ctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

func TestParseStandbyPasswordFromPgpass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{
			name: "standby entry",
			content: strings.Join([]string{
				"# comment",
				"localhost:5432:*:postgres:root",
				"leader:5432:*:standby:secret",
			}, "\n"),
			want: "secret",
		},
		{
			name:    "escaped delimiters",
			content: `leader\:0:5432:*:standby:se\:cret\\`,
			want:    `se:cret\`,
		},
		{
			name:    "preserve password spaces",
			content: "leader:5432:*:standby: secret ",
			want:    " secret ",
		},
		{
			name:    "missing standby",
			content: "localhost:5432:*:postgres:root",
			wantErr: true,
		},
		{
			name:    "empty standby password",
			content: "localhost:5432:*:standby:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStandbyPasswordFromPgpass(tt.content)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestConsistentStandbyPassword(t *testing.T) {
	t.Parallel()

	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}},
	}
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			"postgresql-0": "localhost:5432:*:standby:secret",
			"postgresql-1": "localhost:5432:*:standby:secret",
		},
	}

	password, err := consistentStandbyPassword(context.Background(), runner, pods)

	require.NoError(t, err)
	require.Equal(t, "secret", password)
	require.Equal(t, [][]string{
		{"cat", standbyPgpassPath},
		{"cat", standbyPgpassPath},
	}, runner.commands)
}

func TestConsistentStandbyPasswordInconsistent(t *testing.T) {
	t.Parallel()

	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}},
	}
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			"postgresql-0": "localhost:5432:*:standby:secret-a",
			"postgresql-1": "localhost:5432:*:standby:secret-b",
		},
	}

	_, err := consistentStandbyPassword(context.Background(), runner, pods)

	require.Error(t, err)
	require.True(t, isInconsistentStandbyPasswordError(err))
}

func TestShouldSkipStandbyPasswordRepair(t *testing.T) {
	t.Parallel()

	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}},
	}
	runner := &fakePodExecRunner{
		pgMode: map[string]string{
			"postgresql-0": "",
			"postgresql-1": "standby",
		},
	}

	skip, err := shouldSkipStandbyPasswordRepair(context.Background(), runner, pods)

	require.NoError(t, err)
	require.True(t, skip)
	require.Equal(t, [][]string{
		{"sh", "-c", readPostgreSQLModeEnvCommand},
		{"sh", "-c", readPostgreSQLModeEnvCommand},
	}, runner.commands)
}

func TestEnsureLeaderStandbyPassword(t *testing.T) {
	t.Parallel()

	leaderPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}}
	runner := &fakePodExecRunner{ensureResult: "f"}

	repaired, err := ensureLeaderStandbyPassword(context.Background(), runner, leaderPod, "secret")

	require.NoError(t, err)
	require.True(t, repaired)
	require.Equal(t, "secret", runner.stdin)
	require.Equal(t, []string{"sh", "-c", ensureStandbyPasswordScript}, runner.lastCommand)
}

func TestEnsureLeaderStandbyPasswordNoop(t *testing.T) {
	t.Parallel()

	leaderPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}}
	runner := &fakePodExecRunner{ensureResult: "t"}

	repaired, err := ensureLeaderStandbyPassword(context.Background(), runner, leaderPod, "secret")

	require.NoError(t, err)
	require.False(t, repaired)
}

func TestEnsureLeaderStandbyPasswordRejectsNewline(t *testing.T) {
	t.Parallel()

	leaderPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgresql-1"}}
	runner := &fakePodExecRunner{ensureResult: "t"}

	_, err := ensureLeaderStandbyPassword(context.Background(), runner, leaderPod, "sec\nret")

	require.Error(t, err)
	require.Empty(t, runner.lastCommand)
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformer(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			replicaPod:    "localhost:5432:*:standby:secret",
			leaderPodName: "localhost:5432:*:standby:leader-secret",
		},
		ensureResult: "f",
	}

	transformer := &componentPostgreSQLStandbyPasswordRepairTransformer{execRunner: runner}
	err := transformer.Transform(transCtx, graph.NewDAG())

	require.NoError(t, err)
	require.Equal(t, leaderPodName, runner.ensurePod)
	require.Equal(t, "secret", runner.stdin)
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerRunsWhileNotRunning(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		phase appsv1alpha1.ClusterComponentPhase
	}{
		{
			name:  "updating",
			phase: appsv1alpha1.UpdatingClusterCompPhase,
		},
		{
			name:  "abnormal",
			phase: appsv1alpha1.AbnormalClusterCompPhase,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const (
				leaderPodName = "test-postgresql-1"
				replicaPod    = "test-postgresql-0"
			)

			transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
			transCtx.Component.Status.Phase = tc.phase
			runner := &fakePodExecRunner{
				pgpass: map[string]string{
					replicaPod:    "localhost:5432:*:standby:secret",
					leaderPodName: "localhost:5432:*:postgres:ignored",
				},
				ensureResult: "f",
			}

			err := (&componentPostgreSQLStandbyPasswordRepairTransformer{
				execRunner: runner,
			}).Transform(transCtx, graph.NewDAG())

			require.NoError(t, err)
			require.Equal(t, leaderPodName, runner.ensurePod)
			require.Equal(t, "secret", runner.stdin)
		})
	}
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerUnavailableDoesNotRequeue(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	testCases := []struct {
		name   string
		runner *fakePodExecRunner
	}{
		{
			name: "missing pgpass",
			runner: &fakePodExecRunner{
				missingPgpass: map[string]bool{
					replicaPod: true,
				},
			},
		},
		{
			name: "standby entry missing",
			runner: &fakePodExecRunner{
				pgpass: map[string]string{
					replicaPod: "localhost:5432:*:postgres:secret",
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
			err := (&componentPostgreSQLStandbyPasswordRepairTransformer{
				execRunner: tc.runner,
			}).Transform(transCtx, graph.NewDAG())

			require.NoError(t, err)
			require.Empty(t, tc.runner.ensurePod)
			cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
			require.NotNil(t, cond)
			require.Equal(t, metav1.ConditionFalse, cond.Status)
			require.Equal(t, standbyPasswordRepairReasonUnavailable, cond.Reason)
		})
	}
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerSkipsSingleReplica(t *testing.T) {
	t.Parallel()

	const leaderPodName = "test-postgresql-0"

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName)
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			leaderPodName: "localhost:5432:*:standby:secret",
		},
	}

	err := (&componentPostgreSQLStandbyPasswordRepairTransformer{
		execRunner: runner,
	}).Transform(transCtx, graph.NewDAG())

	require.NoError(t, err)
	require.Empty(t, runner.ensurePod)
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, standbyPasswordRepairReasonSkipped, cond.Reason)
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerPgpassExecFailureRequeues(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
	runner := &fakePodExecRunner{
		pgpassError: map[string]error{
			replicaPod: fmt.Errorf("exec failed"),
		},
		pgpassStderr: map[string]string{
			replicaPod: "container not found",
		},
	}

	err := (&componentPostgreSQLStandbyPasswordRepairTransformer{
		execRunner: runner,
	}).Transform(transCtx, graph.NewDAG())

	require.Error(t, err)
	require.True(t, intctrlutil.IsDelayedRequeueError(err))
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, standbyPasswordRepairReasonFailed, cond.Reason)
}

func TestComponentPostgreSQLStandbyPasswordRepairConditionUsesStatusVertex(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			replicaPod:    "localhost:5432:*:standby:secret",
			leaderPodName: "localhost:5432:*:postgres:ignored",
		},
		ensureResult: "f",
	}
	dag := graph.NewDAG()

	require.NoError(t, (&componentInitTransformer{}).Transform(transCtx, dag))
	require.NoError(t, (&componentStatusTransformer{}).Transform(transCtx, dag))
	require.Equal(t, appsv1alpha1.RunningClusterCompPhase, transCtx.Component.Status.Phase)
	require.NoError(t, (&componentPostgreSQLStandbyPasswordRepairTransformer{
		execRunner: runner,
	}).Transform(transCtx, dag))

	require.Equal(t, leaderPodName, runner.ensurePod)
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)

	graphClient := transCtx.Client.(model.GraphClient)
	vertex := graphClient.FindMatchedVertex(dag, transCtx.Component)
	require.NotNil(t, vertex)
	objectVertex := vertex.(*model.ObjectVertex)
	require.Equal(t, model.STATUS, *objectVertex.Action)
	component := objectVertex.Obj.(*appsv1alpha1.Component)
	cond = meta.FindStatusCondition(component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerSkipsStandbyMode(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPod    = "test-postgresql-0"
	)

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPod)
	runner := &fakePodExecRunner{
		pgMode: map[string]string{
			leaderPodName: "standby",
			replicaPod:    "standby",
		},
	}

	transformer := &componentPostgreSQLStandbyPasswordRepairTransformer{execRunner: runner}
	err := transformer.Transform(transCtx, graph.NewDAG())

	require.NoError(t, err)
	require.Empty(t, runner.ensurePod)
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, standbyPasswordRepairReasonSkipped, cond.Reason)
}

func TestComponentPostgreSQLStandbyPasswordRepairTransformerInconsistent(t *testing.T) {
	t.Parallel()

	const (
		leaderPodName = "test-postgresql-1"
		replicaPodA   = "test-postgresql-0"
		replicaPodB   = "test-postgresql-2"
	)

	transCtx := newPostgreSQLStandbyPasswordRepairTestContext(t, leaderPodName, replicaPodA, replicaPodB)
	runner := &fakePodExecRunner{
		pgpass: map[string]string{
			leaderPodName: "localhost:5432:*:standby:leader-secret",
			replicaPodA:   "localhost:5432:*:standby:secret-a",
			replicaPodB:   "localhost:5432:*:standby:secret-b",
		},
	}

	transformer := &componentPostgreSQLStandbyPasswordRepairTransformer{execRunner: runner}
	err := transformer.Transform(transCtx, graph.NewDAG())

	require.NoError(t, err)
	require.Empty(t, runner.ensurePod)
	cond := meta.FindStatusCondition(transCtx.Component.Status.Conditions, standbyPasswordRepairConditionType)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, standbyPasswordRepairReasonInconsistent, cond.Reason)
}

func newPostgreSQLStandbyPasswordRepairTestContext(
	t *testing.T,
	leaderPodName string,
	replicaPodNames ...string,
) *componentTransformContext {
	t.Helper()

	const (
		namespace     = "default"
		clusterName   = "test"
		componentName = "postgresql"
		compName      = "test-postgresql"
	)

	cluster := &appsv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName, Namespace: namespace},
	}
	cluster.Generation = 1
	comp := &appsv1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: compName, Namespace: namespace},
		Status: appsv1alpha1.ComponentStatus{
			ObservedGeneration: 1,
			Phase:              appsv1alpha1.RunningClusterCompPhase,
		},
	}
	comp.Generation = 1
	compDef := &appsv1alpha1.ComponentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "postgresql"},
		Spec: appsv1alpha1.ComponentDefinitionSpec{
			ServiceKind: "PostgreSQL",
		},
	}
	labels := constant.GetComponentWellKnownLabels(clusterName, componentName)
	objects := []client.Object{
		cluster,
		comp,
		compDef,
		postgresqlTestPod(namespace, leaderPodName, labels),
	}
	for _, podName := range replicaPodNames {
		objects = append(objects, postgresqlTestPod(namespace, podName, labels))
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(rscheme).
		WithObjects(objects...).
		Build()
	replicas := int32(1 + len(replicaPodNames))
	membersStatus := []workloads.MemberStatus{{
		PodName: leaderPodName,
		ReplicaRole: &workloads.ReplicaRole{
			Name:     "leader",
			IsLeader: true,
		},
	}}
	for _, podName := range replicaPodNames {
		membersStatus = append(membersStatus, workloads.MemberStatus{
			PodName:     podName,
			ReplicaRole: &workloads.ReplicaRole{Name: "replica"},
		})
	}
	runningITS := &workloads.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: compName,
			Annotations: map[string]string{
				constant.KubeBlocksGenerationKey: "1",
			},
		},
		Spec: workloads.InstanceSetSpec{
			Replicas: &replicas,
			Roles: []workloads.ReplicaRole{{
				Name:     "leader",
				IsLeader: true,
			}},
		},
		Status: workloads.InstanceSetStatus{
			ObservedGeneration: 1,
			Replicas:           replicas,
			ReadyReplicas:      replicas,
			AvailableReplicas:  replicas,
			UpdatedReplicas:    replicas,
			InitReplicas:       replicas,
			ReadyInitReplicas:  replicas,
			CurrentRevision:    "revision-1",
			UpdateRevision:     "revision-1",
			MembersStatus:      membersStatus,
		},
	}
	runningITS.Generation = 1
	return &componentTransformContext{
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
			Replicas:    replicas,
		},
		RunningWorkload: runningITS,
		ProtoWorkload:   runningITS.DeepCopy(),
	}
}

func postgresqlTestPod(namespace, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "postgresql",
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.10",
		},
	}
}

type fakePodExecRunner struct {
	pgpass        map[string]string
	pgpassError   map[string]error
	pgpassStderr  map[string]string
	pgMode        map[string]string
	missingPgpass map[string]bool
	ensureResult  string
	ensurePod     string
	stdin         string
	lastCommand   []string
	commands      [][]string
}

func (r *fakePodExecRunner) Exec(
	_ context.Context,
	pod *corev1.Pod,
	command []string,
	stdin string,
) (string, string, error) {
	r.commands = append(r.commands, append([]string{}, command...))
	r.lastCommand = append([]string{}, command...)
	if len(command) == 2 && command[0] == "cat" && command[1] == standbyPgpassPath {
		if err := r.pgpassError[pod.Name]; err != nil {
			return "", r.pgpassStderr[pod.Name], err
		}
		if r.missingPgpass[pod.Name] {
			return "", "cat: /run/postgresql/pgpass: No such file or directory", fmt.Errorf("exit status 1")
		}
		return r.pgpass[pod.Name], "", nil
	}
	if len(command) == 3 && command[0] == "sh" && command[1] == "-c" &&
		command[2] == readPostgreSQLModeEnvCommand {
		return r.pgMode[pod.Name], "", nil
	}
	r.ensurePod = pod.Name
	r.stdin = stdin
	return r.ensureResult, "", nil
}

var _ podExecRunner = &fakePodExecRunner{}
