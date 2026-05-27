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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	workloads "github.com/apecloud/kubeblocks/apis/workloads/v1alpha1"
	cfgcore "github.com/apecloud/kubeblocks/pkg/configuration/core"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	intctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

const (
	patroniDCSRepairConditionType = "PatroniDCSRepair"

	patroniDCSRepairReasonSucceeded = "Succeeded"
	patroniDCSRepairReasonFailed    = "Failed"

	patroniDCSRepairRequeueInterval = time.Minute
	patroniHTTPTimeout              = 10 * time.Second
	patroniMaxResponseBytes         = 1 << 20
	patroniDefaultRESTPort          = int32(8008)

	pgHBAConfigFile = "pg_hba.conf"
)

var (
	requiredPatroniPgHBARules = []string{
		"host all all 0.0.0.0/0 md5",
		"host replication standby 0.0.0.0/0 md5",
		"host replication all 127.0.0.1/32 md5",
	}
	fallbackPatroniPgHBARules = append([]string{"local all all trust"}, requiredPatroniPgHBARules...)
)

// componentPatroniDCSRepairTransformer repairs PostgreSQL Patroni dynamic
// config that may miss pg_hba rules after failover.
type componentPatroniDCSRepairTransformer struct {
	patroniClient patroniConfigClient
}

var _ graph.Transformer = &componentPatroniDCSRepairTransformer{}

func (t *componentPatroniDCSRepairTransformer) Transform(ctx graph.TransformContext, dag *graph.DAG) error {
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

	leaderPod, err := t.getLeaderPod(transCtx, runningITS)
	if err != nil {
		t.markRepairFailed(transCtx, err)
		return intctrlutil.NewDelayedRequeueError(patroniDCSRepairRequeueInterval, err.Error())
	}
	if leaderPod == nil || leaderPod.Status.PodIP == "" {
		err := fmt.Errorf("postgresql patroni dcs repair: leader pod %q has no pod ip", leaderMemberPodName(runningITS))
		t.markRepairFailed(transCtx, err)
		return intctrlutil.NewDelayedRequeueError(patroniDCSRepairRequeueInterval, err.Error())
	}

	patroniURL, err := patroniRESTURL(leaderPod.Status.PodIP, patroniRESTPort(leaderPod))
	if err != nil {
		t.markRepairFailed(transCtx, err)
		return intctrlutil.NewDelayedRequeueError(patroniDCSRepairRequeueInterval, err.Error())
	}
	expectedPgHBA, err := t.expectedPgHBARules(transCtx)
	if err != nil {
		t.markRepairFailed(transCtx, err)
		return intctrlutil.NewDelayedRequeueError(patroniDCSRepairRequeueInterval, err.Error())
	}

	patroniClient := t.patroniClient
	if patroniClient == nil {
		patroniClient = newHTTPPatroniConfigClient()
	}
	repaired, err := repairPatroniPgHBA(
		transCtx.Context,
		patroniClient,
		patroniURL,
		expectedPgHBA,
		previousPatroniDCSRepairFailed(transCtx.Component.Status.Conditions),
	)
	if err != nil {
		t.markRepairFailed(transCtx, err)
		return intctrlutil.NewDelayedRequeueError(patroniDCSRepairRequeueInterval, err.Error())
	}

	t.markRepairSucceeded(transCtx, repaired)
	if repaired && transCtx.EventRecorder != nil {
		transCtx.EventRecorder.Event(transCtx.Component, corev1.EventTypeNormal,
			patroniDCSRepairConditionType, "repaired PostgreSQL Patroni DCS pg_hba rules")
	}
	return nil
}

func isPostgreSQLComponent(transCtx *componentTransformContext) bool {
	if transCtx == nil || transCtx.CompDef == nil {
		return false
	}
	serviceKind := strings.ToLower(strings.TrimSpace(transCtx.CompDef.Spec.ServiceKind))
	for _, alias := range constant.GetPostgreSQLAlias() {
		if serviceKind == alias {
			return true
		}
	}
	compDefName := strings.ToLower(transCtx.CompDef.Name)
	return compDefName == constant.ServiceKindPostgreSQL ||
		strings.HasPrefix(compDefName, constant.ServiceKindPostgreSQL+"-")
}

func (t *componentPatroniDCSRepairTransformer) getLeaderPod(
	transCtx *componentTransformContext,
	runningITS *workloads.InstanceSet,
) (*corev1.Pod, error) {
	podName := leaderMemberPodName(runningITS)
	if podName == "" {
		return nil, fmt.Errorf("postgresql patroni dcs repair: leader pod not found")
	}

	pod := &corev1.Pod{}
	podKey := types.NamespacedName{Namespace: transCtx.Component.Namespace, Name: podName}
	if err := transCtx.Client.Get(transCtx.Context, podKey, pod, inDataContext4C()); err != nil {
		return nil, fmt.Errorf("postgresql patroni dcs repair: get leader pod %q: %w", podName, err)
	}
	return pod, nil
}

func leaderMemberPodName(runningITS *workloads.InstanceSet) string {
	if runningITS == nil {
		return ""
	}
	for _, member := range runningITS.Status.MembersStatus {
		if member.ReplicaRole != nil && member.ReplicaRole.IsLeader {
			return member.PodName
		}
	}
	return ""
}

func (t *componentPatroniDCSRepairTransformer) expectedPgHBARules(
	transCtx *componentTransformContext,
) ([]string, error) {
	for _, configSpec := range transCtx.SynthesizeComponent.ConfigTemplates {
		cmKey := types.NamespacedName{
			Namespace: transCtx.Cluster.Namespace,
			Name: cfgcore.GetComponentCfgName(
				transCtx.Cluster.Name,
				transCtx.SynthesizeComponent.Name,
				configSpec.Name,
			),
		}
		cmObj := &corev1.ConfigMap{}
		if err := transCtx.Client.Get(transCtx.Context, cmKey, cmObj, inDataContext4C()); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("postgresql patroni dcs repair: get configmap %q: %w", cmKey.Name, err)
		}
		if content, ok := cmObj.Data[pgHBAConfigFile]; ok {
			rules := parsePgHBAContent(content)
			if len(rules) > 0 {
				return ensurePgHBARemoteRules(rules), nil
			}
		}
	}
	return append([]string{}, fallbackPatroniPgHBARules...), nil
}

func (t *componentPatroniDCSRepairTransformer) markRepairSucceeded(
	transCtx *componentTransformContext,
	repaired bool,
) {
	message := "PostgreSQL Patroni DCS pg_hba rules are up to date"
	if repaired {
		message = "PostgreSQL Patroni DCS pg_hba rules were repaired"
	}
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               patroniDCSRepairConditionType,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             patroniDCSRepairReasonSucceeded,
		Message:            message,
	})
}

func (t *componentPatroniDCSRepairTransformer) markRepairFailed(
	transCtx *componentTransformContext,
	err error,
) {
	meta.SetStatusCondition(&transCtx.Component.Status.Conditions, metav1.Condition{
		Type:               patroniDCSRepairConditionType,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: transCtx.Component.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             patroniDCSRepairReasonFailed,
		Message:            err.Error(),
	})
}

func previousPatroniDCSRepairFailed(conditions []metav1.Condition) bool {
	condition := meta.FindStatusCondition(conditions, patroniDCSRepairConditionType)
	return condition != nil && condition.Status == metav1.ConditionFalse
}

func parsePgHBAContent(content string) []string {
	rules := make([]string, 0)
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if commentIndex := strings.Index(line, "#"); commentIndex >= 0 {
			line = strings.TrimSpace(line[:commentIndex])
		}
		if line == "" {
			continue
		}
		rules = append(rules, strings.Join(strings.Fields(line), " "))
	}
	return rules
}

func ensurePgHBARemoteRules(rules []string) []string {
	rules, _ = mergePgHBARules(rules, requiredPatroniPgHBARules)
	return rules
}

func repairPatroniPgHBA(
	ctx context.Context,
	patroniClient patroniConfigClient,
	baseURL string,
	expectedRules []string,
	reloadIfUnchanged bool,
) (bool, error) {
	config, err := patroniClient.GetConfig(ctx, baseURL)
	if err != nil {
		return false, err
	}
	patchedRules, changed := mergePgHBARules(config.PostgreSQL.PgHBA, expectedRules)
	if !changed {
		if reloadIfUnchanged {
			if err := patroniClient.Reload(ctx, baseURL); err != nil {
				return false, err
			}
		}
		return false, nil
	}

	patch := patroniDynamicConfig{
		PostgreSQL: patroniPostgreSQLConfig{
			PgHBA: patchedRules,
		},
	}
	if err := patroniClient.PatchConfig(ctx, baseURL, patch); err != nil {
		return false, err
	}
	if err := patroniClient.Reload(ctx, baseURL); err != nil {
		return false, err
	}

	config, err = patroniClient.GetConfig(ctx, baseURL)
	if err != nil {
		return false, err
	}
	if missing := missingPgHBARules(config.PostgreSQL.PgHBA, expectedRules); len(missing) > 0 {
		return false, fmt.Errorf("postgresql patroni dcs repair: pg_hba rules still missing: %s", strings.Join(missing, "; "))
	}
	return true, nil
}

func mergePgHBARules(currentRules, expectedRules []string) ([]string, bool) {
	merged := normalizePgHBARules(currentRules)
	existing := make(map[string]struct{}, len(merged))
	for _, rule := range merged {
		existing[rule] = struct{}{}
	}

	changed := false
	for _, rule := range normalizePgHBARules(expectedRules) {
		if _, ok := existing[rule]; ok {
			continue
		}
		merged = append(merged, rule)
		existing[rule] = struct{}{}
		changed = true
	}
	return merged, changed
}

func missingPgHBARules(currentRules, expectedRules []string) []string {
	current := normalizePgHBARules(currentRules)
	currentSet := make(map[string]struct{}, len(current))
	for _, rule := range current {
		currentSet[rule] = struct{}{}
	}

	missing := make([]string, 0)
	for _, rule := range normalizePgHBARules(expectedRules) {
		if _, ok := currentSet[rule]; !ok {
			missing = append(missing, rule)
		}
	}
	return missing
}

func normalizePgHBARules(rules []string) []string {
	normalized := make([]string, 0, len(rules))
	seen := map[string]struct{}{}
	for _, rule := range rules {
		rule = strings.Join(strings.Fields(strings.TrimSpace(rule)), " ")
		if rule == "" {
			continue
		}
		if _, ok := seen[rule]; ok {
			continue
		}
		seen[rule] = struct{}{}
		normalized = append(normalized, rule)
	}
	return normalized
}

func patroniRESTURL(podIP string, port int32) (string, error) {
	if net.ParseIP(podIP) == nil {
		return "", fmt.Errorf("postgresql patroni dcs repair: invalid patroni pod ip %q", podIP)
	}
	return "http://" + net.JoinHostPort(podIP, strconv.Itoa(int(port))), nil
}

func patroniRESTPort(pod *corev1.Pod) int32 {
	if pod == nil {
		return patroniDefaultRESTPort
	}
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			name := strings.ToLower(port.Name)
			if name == "patroni" || name == "patroni-rest" ||
				name == "patroni-restapi" || name == "patroni-api" || name == "restapi" {
				return port.ContainerPort
			}
		}
	}
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.ContainerPort == patroniDefaultRESTPort {
				return port.ContainerPort
			}
		}
	}
	return patroniDefaultRESTPort
}

type patroniConfigClient interface {
	GetConfig(ctx context.Context, baseURL string) (*patroniDynamicConfig, error)
	PatchConfig(ctx context.Context, baseURL string, config patroniDynamicConfig) error
	Reload(ctx context.Context, baseURL string) error
}

type patroniDynamicConfig struct {
	PostgreSQL patroniPostgreSQLConfig `json:"postgresql,omitempty"`
}

type patroniPostgreSQLConfig struct {
	PgHBA []string `json:"pg_hba,omitempty"`
}

type httpPatroniConfigClient struct {
	client *http.Client
}

func newHTTPPatroniConfigClient() *httpPatroniConfigClient {
	return &httpPatroniConfigClient{
		client: &http.Client{Timeout: patroniHTTPTimeout},
	}
}

func (c *httpPatroniConfigClient) GetConfig(ctx context.Context, baseURL string) (*patroniDynamicConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, patroniURL(baseURL, "config"), nil)
	if err != nil {
		return nil, fmt.Errorf("postgresql patroni dcs repair: build get config request: %w", err)
	}

	var config patroniDynamicConfig
	if err := c.doJSON(req, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *httpPatroniConfigClient) PatchConfig(
	ctx context.Context,
	baseURL string,
	config patroniDynamicConfig,
) error {
	body, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("postgresql patroni dcs repair: marshal patch config request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, patroniURL(baseURL, "config"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("postgresql patroni dcs repair: build patch config request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

func (c *httpPatroniConfigClient) Reload(ctx context.Context, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, patroniURL(baseURL, "reload"), nil)
	if err != nil {
		return fmt.Errorf("postgresql patroni dcs repair: build reload request: %w", err)
	}
	return c.do(req, nil)
}

func (c *httpPatroniConfigClient) doJSON(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	return c.do(req, out)
}

func (c *httpPatroniConfigClient) do(req *http.Request, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("postgresql patroni dcs repair: send %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("postgresql patroni dcs repair: %s %s returned %s", req.Method, req.URL.Path, resp.Status)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, patroniMaxResponseBytes))
		if err != nil {
			return fmt.Errorf("postgresql patroni dcs repair: read %s %s response: %w", req.Method, req.URL.Path, err)
		}
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, patroniMaxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("postgresql patroni dcs repair: decode %s %s response: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

func patroniURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}
