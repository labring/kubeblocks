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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	ctrlcomp "github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	standbyPasswordRepairConditionType = "StandbyPasswordRepair"

	standbyPasswordRepairReasonSucceeded    = "Succeeded"
	standbyPasswordRepairReasonFailed       = "Failed"
	standbyPasswordRepairReasonInconsistent = "StandbyPasswordInconsistent"
	standbyPasswordRepairReasonSkipped      = "Skipped"

	standbyPasswordRepairRequeueInterval = time.Minute
	standbyPgpassPath                    = "/run/postgresql/pgpass"
	standbyUserName                      = "standby"
	readStandbyPasswordEnvCommand        = `printf "%s" "${PGPASSWORD_STANDBY:-}"`
	readPostgreSQLModeEnvCommand         = `printf "%s" "${PG_MODE:-}"`
)

const ensureStandbyPasswordScript = `
set -eu
password="$(cat)"
if [ -z "$password" ]; then
  echo "empty standby password" >&2
  exit 1
fi
escaped_password="$(printf "%s" "$password" | sed "s/'/''/g")"
matches="$(psql -U postgres -v ON_ERROR_STOP=1 -Atq <<SQL
SET standard_conforming_strings = on;
SELECT COALESCE((
  SELECT rolpassword = 'md5' || md5('$escaped_password' || 'standby')
  FROM pg_authid
  WHERE rolname = 'standby'
), false);
SQL
)"
if [ "$matches" != "t" ]; then
  psql -U postgres -v ON_ERROR_STOP=1 -Atq <<SQL >/dev/null
SET standard_conforming_strings = on;
SET password_encryption = 'md5';
ALTER USER standby PASSWORD '$escaped_password';
SQL
fi
verified="$(psql -U postgres -v ON_ERROR_STOP=1 -Atq <<SQL
SET standard_conforming_strings = on;
SELECT COALESCE((
  SELECT rolpassword = 'md5' || md5('$escaped_password' || 'standby')
  FROM pg_authid
  WHERE rolname = 'standby'
), false);
SQL
)"
if [ "$verified" != "t" ]; then
  echo "standby password verification failed" >&2
  exit 1
fi
printf "%s\n" "$matches"
`

var errStandbyEntryNotFound = errors.New("standby entry not found")

// componentPostgreSQLStandbyPasswordRepairTransformer repairs drift between
// the standby password used by pods and the password stored in PostgreSQL.
type componentPostgreSQLStandbyPasswordRepairTransformer struct {
	restConfig *rest.Config
	execRunner podExecRunner
}

var _ graph.Transformer = &componentPostgreSQLStandbyPasswordRepairTransformer{}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) Transform(
	ctx graph.TransformContext,
	dag *graph.DAG,
) error {
	transCtx, _ := ctx.(*componentTransformContext)
	if model.IsObjectDeleting(transCtx.ComponentOrig) {
		return nil
	}
	if !isPostgreSQLComponent(transCtx) {
		return nil
	}
	if transCtx.Component.Status.Phase != appsv1alpha1.RunningClusterCompPhase {
		return nil
	}

	runningITS, ok := transCtx.RunningWorkload.(*workloads.InstanceSet)
	if !ok || runningITS == nil || runningITS.Status.AvailableReplicas == 0 {
		return nil
	}

	pods, err := t.runningPods(transCtx)
	if err != nil {
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}
	if len(pods) == 0 {
		err := fmt.Errorf("postgresql standby password repair: no running pods with pod ip found")
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}

	runner := t.execRunner
	if runner == nil {
		runner = newKubePodExecRunner(t.restConfig)
	}
	skip, err := shouldSkipStandbyPasswordRepair(transCtx.Context, runner, pods)
	if err != nil {
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}
	if skip {
		t.markRepairSkipped(transCtx)
		return nil
	}

	leaderPod, err := t.leaderPod(transCtx, runningITS)
	if err != nil {
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}
	if leaderPod == nil || leaderPod.Status.PodIP == "" {
		err := fmt.Errorf(
			"postgresql standby password repair: leader pod %q has no pod ip",
			leaderMemberPodName(runningITS),
		)
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}

	expectedPassword, err := consistentStandbyPassword(transCtx.Context, runner, pods)
	if err != nil {
		if isInconsistentStandbyPasswordError(err) {
			t.markRepairFailed(transCtx, standbyPasswordRepairReasonInconsistent, err)
			return nil
		}
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}

	repaired, err := ensureLeaderStandbyPassword(transCtx.Context, runner, leaderPod, expectedPassword)
	if err != nil {
		t.markRepairFailed(transCtx, standbyPasswordRepairReasonFailed, err)
		return intctrlutil.NewDelayedRequeueError(standbyPasswordRepairRequeueInterval, err.Error())
	}

	t.markRepairSucceeded(transCtx, repaired)
	if repaired && transCtx.EventRecorder != nil {
		transCtx.EventRecorder.Event(transCtx.Component, corev1.EventTypeNormal,
			standbyPasswordRepairConditionType, "repaired PostgreSQL standby password drift")
	}
	return nil
}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) runningPods(
	transCtx *componentTransformContext,
) ([]*corev1.Pod, error) {
	labels := constant.GetComponentWellKnownLabels(transCtx.Cluster.Name, transCtx.SynthesizeComponent.Name)
	pods, err := ctrlcomp.ListPodOwnedByComponent(
		transCtx.Context,
		transCtx.Client,
		transCtx.SynthesizeComponent.Namespace,
		labels,
		inDataContext4C(),
	)
	if err != nil {
		return nil, fmt.Errorf("postgresql standby password repair: list component pods: %w", err)
	}

	runningPods := make([]*corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		runningPods = append(runningPods, pod)
	}
	return runningPods, nil
}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) leaderPod(
	transCtx *componentTransformContext,
	runningITS *workloads.InstanceSet,
) (*corev1.Pod, error) {
	podName := leaderMemberPodName(runningITS)
	if podName == "" {
		return nil, fmt.Errorf("postgresql standby password repair: leader pod not found")
	}

	pod := &corev1.Pod{}
	podKey := types.NamespacedName{Namespace: transCtx.Component.Namespace, Name: podName}
	if err := transCtx.Client.Get(transCtx.Context, podKey, pod, inDataContext4C()); err != nil {
		return nil, fmt.Errorf("postgresql standby password repair: get leader pod %q: %w", podName, err)
	}
	return pod, nil
}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) markRepairSucceeded(
	transCtx *componentTransformContext,
	repaired bool,
) {
	message := "PostgreSQL standby password is up to date"
	if repaired {
		message = "PostgreSQL standby password was repaired"
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               standbyPasswordRepairConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             standbyPasswordRepairReasonSucceeded,
		Message:            message,
	})
}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) markRepairSkipped(
	transCtx *componentTransformContext,
) {
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               standbyPasswordRepairConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             standbyPasswordRepairReasonSkipped,
		Message:            "PostgreSQL standby password repair is skipped for standby cluster mode",
	})
}

func (t *componentPostgreSQLStandbyPasswordRepairTransformer) markRepairFailed(
	transCtx *componentTransformContext,
	reason string,
	err error,
) {
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               standbyPasswordRepairConditionType,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            err.Error(),
	})
}

func shouldSkipStandbyPasswordRepair(ctx context.Context, runner podExecRunner, pods []*corev1.Pod) (bool, error) {
	for _, pod := range pods {
		mode, err := postgreSQLModeFromPod(ctx, runner, pod)
		if err != nil {
			return false, err
		}
		// In standby-cluster mode the addon may point standby credentials at a
		// remote primary, so they are not safe for local role repair.
		if strings.Contains(strings.ToLower(mode), "standby") {
			return true, nil
		}
	}
	return false, nil
}

func postgreSQLModeFromPod(ctx context.Context, runner podExecRunner, pod *corev1.Pod) (string, error) {
	stdout, stderr, err := runner.Exec(ctx, pod, []string{"sh", "-c", readPostgreSQLModeEnvCommand}, "")
	if err != nil {
		return "", fmt.Errorf(
			"postgresql standby password repair: read pg mode from pod %q: %w: %s",
			pod.Name,
			err,
			strings.TrimSpace(stderr),
		)
	}
	return strings.TrimSpace(stdout), nil
}

func consistentStandbyPassword(ctx context.Context, runner podExecRunner, pods []*corev1.Pod) (string, error) {
	expectedPassword := ""
	for _, pod := range pods {
		password, err := standbyPasswordFromPod(ctx, runner, pod)
		if err != nil {
			return "", err
		}
		if expectedPassword == "" {
			expectedPassword = password
			continue
		}
		if password != expectedPassword {
			return "", inconsistentStandbyPasswordError{}
		}
	}
	return expectedPassword, nil
}

func standbyPasswordFromPod(ctx context.Context, runner podExecRunner, pod *corev1.Pod) (string, error) {
	stdout, stderr, err := runner.Exec(ctx, pod, []string{"cat", standbyPgpassPath}, "")
	if err != nil {
		return "", fmt.Errorf(
			"postgresql standby password repair: read pgpass from pod %q: %w: %s",
			pod.Name,
			err,
			strings.TrimSpace(stderr),
		)
	}
	password, err := parseStandbyPasswordFromPgpass(stdout)
	if err == nil {
		return password, nil
	}
	if !errors.Is(err, errStandbyEntryNotFound) {
		return "", fmt.Errorf("postgresql standby password repair: parse pgpass from pod %q: %w", pod.Name, err)
	}

	// Some PostgreSQL leaders keep only the superuser entry in pgpass, while the
	// replication password is still exposed through the pod environment.
	stdout, stderr, err = runner.Exec(ctx, pod, []string{"sh", "-c", readStandbyPasswordEnvCommand}, "")
	if err != nil {
		return "", fmt.Errorf(
			"postgresql standby password repair: read standby env from pod %q: %w: %s",
			pod.Name,
			err,
			strings.TrimSpace(stderr),
		)
	}
	password = strings.TrimRight(stdout, "\r\n")
	if password == "" {
		return "", fmt.Errorf(
			"postgresql standby password repair: standby password not found in pod %q pgpass or env",
			pod.Name,
		)
	}
	return password, nil
}

func ensureLeaderStandbyPassword(
	ctx context.Context,
	runner podExecRunner,
	leaderPod *corev1.Pod,
	expectedPassword string,
) (bool, error) {
	if strings.ContainsAny(expectedPassword, "\r\n") {
		return false, fmt.Errorf("postgresql standby password repair: invalid standby password")
	}
	stdout, _, err := runner.Exec(
		ctx,
		leaderPod,
		[]string{"sh", "-c", ensureStandbyPasswordScript},
		expectedPassword,
	)
	if err != nil {
		return false, fmt.Errorf(
			"postgresql standby password repair: ensure standby password on leader pod %q: %w",
			leaderPod.Name,
			err,
		)
	}
	switch strings.TrimSpace(stdout) {
	case "t":
		return false, nil
	case "f":
		return true, nil
	default:
		return false, fmt.Errorf(
			"postgresql standby password repair: unexpected verification result from leader pod %q",
			leaderPod.Name,
		)
	}
}

func parseStandbyPasswordFromPgpass(content string) (string, error) {
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := splitPgpassLine(line)
		if len(fields) < 5 {
			continue
		}
		if fields[3] == standbyUserName {
			if fields[4] == "" {
				return "", fmt.Errorf("standby password is empty")
			}
			return fields[4], nil
		}
	}
	return "", errStandbyEntryNotFound
}

func splitPgpassLine(line string) []string {
	fields := make([]string, 0, 5)
	var field strings.Builder
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			field.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ':':
			fields = append(fields, field.String())
			field.Reset()
		default:
			field.WriteRune(r)
		}
	}
	if escaped {
		field.WriteRune('\\')
	}
	fields = append(fields, field.String())
	return fields
}

type inconsistentStandbyPasswordError struct{}

func (inconsistentStandbyPasswordError) Error() string {
	return "postgresql standby password repair: standby passwords differ across running pods"
}

func isInconsistentStandbyPasswordError(err error) bool {
	var target inconsistentStandbyPasswordError
	return errors.As(err, &target)
}

type podExecRunner interface {
	Exec(ctx context.Context, pod *corev1.Pod, command []string, stdin string) (stdout string, stderr string, err error)
}

type kubePodExecRunner struct {
	restConfig *rest.Config
}

func newKubePodExecRunner(restConfig *rest.Config) *kubePodExecRunner {
	return &kubePodExecRunner{restConfig: restConfig}
}

func (r *kubePodExecRunner) Exec(
	ctx context.Context,
	pod *corev1.Pod,
	command []string,
	stdin string,
) (string, string, error) {
	if pod == nil {
		return "", "", fmt.Errorf("pod is nil")
	}
	containerName := postgreSQLContainerName(pod)
	if containerName == "" {
		return "", "", fmt.Errorf("pod %q has no container", pod.Name)
	}

	restConfig, err := r.config()
	if err != nil {
		return "", "", err
	}
	restClient, err := rest.RESTClientFor(restConfig)
	if err != nil {
		return "", "", fmt.Errorf("create kubernetes rest client: %w", err)
	}
	req := restClient.Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     stdin != "",
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("create kubernetes exec executor: %w", err)
	}
	var stdout, stderr bytes.Buffer
	var stdinReader io.Reader
	if stdin != "" {
		stdinReader = strings.NewReader(stdin)
	}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdinReader,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	return stdout.String(), stderr.String(), err
}

func (r *kubePodExecRunner) config() (*rest.Config, error) {
	var (
		config *rest.Config
		err    error
	)
	if r.restConfig != nil {
		config = rest.CopyConfig(r.restConfig)
	} else {
		config, err = ctrl.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("get kubernetes rest config: %w", err)
		}
	}
	config.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	config.APIPath = "/api"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return config, nil
}

func postgreSQLContainerName(pod *corev1.Pod) string {
	preferredNames := map[string]struct{}{
		"postgresql": {},
		"postgres":   {},
		"spilo":      {},
	}
	for _, container := range pod.Spec.Containers {
		if _, ok := preferredNames[strings.ToLower(container.Name)]; ok {
			return container.Name
		}
	}
	for _, container := range pod.Spec.Containers {
		name := strings.ToLower(container.Name)
		if strings.Contains(name, "lorry") || strings.Contains(name, "config") {
			continue
		}
		return container.Name
	}
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	return pod.Spec.Containers[0].Name
}

var _ podExecRunner = &kubePodExecRunner{}
