package steps

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed scripts/*
var scripts embed.FS

type RunOptions struct {
	Ctx     context.Context
	WorkDir string
}

type Step interface {
	Name() string
	Description() string
	Check(opts RunOptions) (skip bool, err error)
	Run(opts RunOptions) error
}

// ResourceSnapshot stores resourceVersion and tracked resource names from a List,
// enabling a Watch to start without missing events.
type ResourceSnapshot struct {
	ResourceVersion string
	Names           map[string]struct{}
}

var (
	clusterTerminalPhases = map[string]bool{"Running": true, "Stopped": true}
	addonTerminalPhases   = map[string]bool{"Enabled": true, "Disabled": true, "Failed": true}
)

const helmAddonReleasePrefix = "kb-addon-"

const addonJobNamespace = "kb-system"

// helmTrackedAddonCRNames returns Addon CR names derived from `helm list -n kb-system`
// releases whose name has prefix kb-addon- (strip prefix = CR name).
func helmTrackedAddonCRNames(opts RunOptions) (map[string]struct{}, error) {
	out, err := runCmd(opts, "helm", "list", "-n", "kb-system", "-o", "json")
	if err != nil {
		return nil, err
	}
	var releases []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &releases); err != nil {
		return nil, fmt.Errorf("解析 helm list JSON 失败: %w", err)
	}
	names := make(map[string]struct{})
	for _, r := range releases {
		if strings.HasPrefix(r.Name, helmAddonReleasePrefix) {
			names[strings.TrimPrefix(r.Name, helmAddonReleasePrefix)] = struct{}{}
		}
	}
	return names, nil
}

type helmTrackedAddonRuntime struct {
	Name               string
	Phase              string
	Generation         int64
	ObservedGeneration int64
	JobState           string
	JobCreatedAt       time.Time
	ActivePodPhases    []string
	NewPodSeen         bool
}

func trackedAddonNames(opts RunOptions, snap *ResourceSnapshot) (map[string]struct{}, error) {
	if snap != nil && len(snap.Names) > 0 {
		names := make(map[string]struct{}, len(snap.Names))
		for name := range snap.Names {
			names[name] = struct{}{}
		}
		return names, nil
	}
	return helmTrackedAddonCRNames(opts)
}

func trackedAddonList(tracked map[string]struct{}) []string {
	names := make([]string, 0, len(tracked))
	for name := range tracked {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func loadHelmTrackedAddonRuntime(opts RunOptions, tracked map[string]struct{}, startedAt time.Time) ([]helmTrackedAddonRuntime, error) {
	states := make(map[string]*helmTrackedAddonRuntime, len(tracked))
	for _, name := range trackedAddonList(tracked) {
		states[name] = &helmTrackedAddonRuntime{
			Name:     name,
			JobState: "Absent",
		}
	}

	addonOut, err := kubectl(opts, "get", "addons.extensions.kubeblocks.io",
		"-l", "app.kubernetes.io/name=kubeblocks", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("获取 Addon 列表失败: %w", err)
	}
	var addonList struct {
		Items []struct {
			Metadata struct {
				Name       string `json:"name"`
				Generation int64  `json:"generation"`
			} `json:"metadata"`
			Status struct {
				Phase              string `json:"phase"`
				ObservedGeneration int64  `json:"observedGeneration"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(addonOut), &addonList); err != nil {
		return nil, fmt.Errorf("解析 Addon 列表失败: %w", err)
	}
	for _, item := range addonList.Items {
		if state, ok := states[item.Metadata.Name]; ok {
			state.Phase = item.Status.Phase
			state.Generation = item.Metadata.Generation
			state.ObservedGeneration = item.Status.ObservedGeneration
		}
	}

	jobOut, err := kubectl(opts, "get", "jobs", "-n", addonJobNamespace,
		"-l", "app.kubernetes.io/managed-by=kubeblocks", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("获取 Addon Job 列表失败: %w", err)
	}
	var jobList struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				Labels            map[string]string `json:"labels"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Active    int `json:"active"`
				Succeeded int `json:"succeeded"`
				Failed    int `json:"failed"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(jobOut), &jobList); err != nil {
		return nil, fmt.Errorf("解析 Addon Job 列表失败: %w", err)
	}
	for _, item := range jobList.Items {
		addonName := item.Metadata.Labels["extensions.kubeblocks.io/addon-name"]
		state, ok := states[addonName]
		if !ok {
			continue
		}
		expectedJobName := fmt.Sprintf("install-%s-addon", addonName)
		if item.Metadata.Name != expectedJobName {
			continue
		}
		state.JobCreatedAt = item.Metadata.CreationTimestamp
		switch {
		case item.Metadata.DeletionTimestamp != nil:
			state.JobState = "Deleting"
		case item.Status.Succeeded > 0:
			state.JobState = "Succeeded"
		case item.Status.Failed > 0:
			state.JobState = "Failed"
		case item.Status.Active > 0:
			state.JobState = "Active"
		default:
			state.JobState = "Pending"
		}
	}

	podOut, err := kubectl(opts, "get", "pods", "-n", addonJobNamespace,
		"-l", "app.kubernetes.io/managed-by=kubeblocks", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("获取 Addon Pod 列表失败: %w", err)
	}
	var podList struct {
		Items []struct {
			Metadata struct {
				Labels            map[string]string `json:"labels"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(podOut), &podList); err != nil {
		return nil, fmt.Errorf("解析 Addon Pod 列表失败: %w", err)
	}
	for _, item := range podList.Items {
		addonName := item.Metadata.Labels["extensions.kubeblocks.io/addon-name"]
		state, ok := states[addonName]
		if !ok {
			continue
		}
		expectedJobName := fmt.Sprintf("install-%s-addon", addonName)
		if item.Metadata.Labels["job-name"] != expectedJobName {
			continue
		}
		if item.Metadata.CreationTimestamp.After(startedAt) {
			state.NewPodSeen = true
		}
		if item.Status.Phase != "Succeeded" && item.Status.Phase != "Failed" {
			state.ActivePodPhases = append(state.ActivePodPhases, item.Status.Phase)
		}
	}

	result := make([]helmTrackedAddonRuntime, 0, len(states))
	for _, name := range trackedAddonList(tracked) {
		result = append(result, *states[name])
	}
	return result, nil
}

func checkHelmTrackedAddonsSettled(opts RunOptions) bool {
	tracked, err := trackedAddonNames(opts, nil)
	if err != nil || len(tracked) == 0 {
		return err == nil
	}
	states, err := loadHelmTrackedAddonRuntime(opts, tracked, time.Time{})
	if err != nil {
		return false
	}
	for _, state := range states {
		if state.Generation != 0 && state.Generation != state.ObservedGeneration {
			return false
		}
		if !addonTerminalPhases[state.Phase] {
			return false
		}
		if state.JobState == "Active" || state.JobState == "Pending" || state.JobState == "Deleting" {
			return false
		}
		if len(state.ActivePodPhases) > 0 {
			return false
		}
	}
	return true
}

func waitHelmTrackedAddonsSettled(ctx context.Context, opts RunOptions, snap *ResourceSnapshot, startedAt time.Time) error {
	tracked, err := trackedAddonNames(opts, snap)
	if err != nil {
		return fmt.Errorf("获取待跟踪 Addon 失败: %w", err)
	}
	if len(tracked) == 0 {
		logInfo("无 kb-addon-* Helm release，跳过 Addon 等待")
		return nil
	}

	names := trackedAddonList(tracked)
	logInfo("跟踪 Helm Addon: %s", strings.Join(names, ", "))

	announcedWaiting := false
	lastSummary := ""
	for {
		states, err := loadHelmTrackedAddonRuntime(opts, tracked, startedAt)
		if err != nil {
			return err
		}

		summaryParts := make([]string, 0, len(states))
		allSettled := true
		for _, state := range states {
			podSummary := "-"
			if len(state.ActivePodPhases) > 0 {
				podSummary = strings.Join(state.ActivePodPhases, ",")
			}
			phase := state.Phase
			if phase == "" {
				phase = "<empty>"
			}
			summaryParts = append(summaryParts,
				fmt.Sprintf("%s[phase=%s gen=%d/%d job=%s pods=%s]",
					state.Name, phase, state.ObservedGeneration, state.Generation, state.JobState, podSummary))

			if state.Generation != 0 && state.Generation != state.ObservedGeneration {
				allSettled = false
			}
			if !addonTerminalPhases[state.Phase] {
				allSettled = false
			}
			switch state.JobState {
			case "Active", "Pending", "Deleting":
				allSettled = false
			}
			if len(state.ActivePodPhases) > 0 {
				allSettled = false
			}
		}

		summary := strings.Join(summaryParts, " ")
		if summary != lastSummary {
			logInfo("Addon 收敛状态: %s", summary)
			lastSummary = summary
		}

		if allSettled {
			logInfo("所有 Helm Addon install job 已完成，Addon 已收敛到目标 generation 且 phase 回到终态")
			return nil
		}
		if !announcedWaiting {
			logInfo("Addon 当前尚未进入 install/upgrade 过程，继续等待 install job/pod 出现...")
			announcedWaiting = true
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// snapshotHelmTrackedAddons records resourceVersion from a full Addon list and Names
// for every Addon that appears in kb-addon-* Helm releases (so wait matches helm).
func snapshotHelmTrackedAddons(opts RunOptions) (*ResourceSnapshot, error) {
	tracked, err := helmTrackedAddonCRNames(opts)
	if err != nil {
		return nil, err
	}
	snap := &ResourceSnapshot{Names: make(map[string]struct{})}
	if len(tracked) == 0 {
		return snap, nil
	}
	out, err := kubectl(opts, "get", "addons.extensions.kubeblocks.io",
		"-l", "app.kubernetes.io/name=kubeblocks", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("解析 Addon 列表失败: %w", err)
	}
	snap.ResourceVersion = list.Metadata.ResourceVersion
	for _, it := range list.Items {
		if _, ok := tracked[it.Metadata.Name]; ok {
			snap.Names[it.Metadata.Name] = struct{}{}
		}
	}
	return snap, nil
}

// ===== Command execution =====

func runScript(opts RunOptions, name string) error {
	content, err := scripts.ReadFile(name)
	if err != nil {
		return fmt.Errorf("读取脚本 %s 失败: %w", name, err)
	}
	path := filepath.Join(opts.WorkDir, filepath.Base(name))
	if err := os.WriteFile(path, content, 0755); err != nil {
		return fmt.Errorf("写入脚本失败: %w", err)
	}
	cmd := exec.CommandContext(opts.Ctx, "bash", path)
	cmd.Dir = opts.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("         $ bash %s\n", filepath.Base(name))
	return cmd.Run()
}

func runCmd(opts RunOptions, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(opts.Ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	fmt.Printf("         $ %s %s\n", name, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func kubectl(opts RunOptions, args ...string) (string, error) {
	return runCmd(opts, "kubectl", args...)
}

func kubectlIgnoreError(opts RunOptions, args ...string) string {
	out, _ := kubectl(opts, args...)
	return out
}

// ===== Generic step types =====

type scriptStep struct {
	name, desc, script string
}

func (s scriptStep) Name() string                   { return s.name }
func (s scriptStep) Description() string            { return s.desc }
func (s scriptStep) Check(RunOptions) (bool, error) { return false, nil }
func (s scriptStep) Run(opts RunOptions) error      { return runScript(opts, s.script) }

// ===== Pre-condition check helpers =====

// checkDeploymentImage returns true if the container uses the target image.
func checkDeploymentImage(opts RunOptions, ns, deploy, container, targetImage string) bool {
	out := kubectlIgnoreError(opts, "get", "deploy", deploy, "-n", ns, "-o", "json")
	if out == "" {
		return false
	}
	type cSpec struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	var dep struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers     []cSpec `json:"containers"`
					InitContainers []cSpec `json:"initContainers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &dep); err != nil {
		return false
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == container && c.Image == targetImage {
			return true
		}
	}
	for _, c := range dep.Spec.Template.Spec.InitContainers {
		if c.Name == container && c.Image == targetImage {
			return true
		}
	}
	return false
}

// checkDeploymentReady checks updatedReplicas==replicas, availableReplicas==replicas, unavailableReplicas==0.
func checkDeploymentReady(opts RunOptions, ns, name string) bool {
	out := kubectlIgnoreError(opts, "get", "deploy", name, "-n", ns, "-o", "json")
	if out == "" {
		return false
	}
	var dep struct {
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			UpdatedReplicas     int `json:"updatedReplicas"`
			AvailableReplicas   int `json:"availableReplicas"`
			UnavailableReplicas int `json:"unavailableReplicas"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &dep); err != nil {
		return false
	}
	desired := 1
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	return dep.Status.UpdatedReplicas == desired &&
		dep.Status.AvailableReplicas == desired &&
		dep.Status.UnavailableReplicas == 0
}

// checkAllPhases lists resources via `kubectl get <getArgs> -o json`,
// applies optional name filter, and returns true when every item's phase is in expected.
func checkAllPhases(opts RunOptions, getArgs []string, filter func(string) bool, expected map[string]bool) bool {
	args := make([]string, 0, len(getArgs)+3)
	args = append(args, "get")
	args = append(args, getArgs...)
	args = append(args, "-o", "json")
	out := kubectlIgnoreError(opts, args...)
	if out == "" {
		return true
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string }  `json:"metadata"`
			Status   struct{ Phase string } `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false
	}
	for _, item := range list.Items {
		if filter != nil && !filter(item.Metadata.Name) {
			continue
		}
		if !expected[item.Status.Phase] {
			return false
		}
	}
	return true
}

const clusterDefLabelKey = "clusterdefinition.kubeblocks.io/name"

// clusterMatchesType checks spec.clusterDefinitionRef or the well-known label
// instead of relying on metadata.name substring, matching the verify_*.sh approach.
func clusterMatchesType(clusterDefRef string, labels map[string]string, dbType string) bool {
	dbType = strings.ToLower(dbType)
	if strings.Contains(strings.ToLower(clusterDefRef), dbType) {
		return true
	}
	if v, ok := labels[clusterDefLabelKey]; ok && strings.Contains(strings.ToLower(v), dbType) {
		return true
	}
	return false
}

func checkAllClustersPhase(opts RunOptions, dbType string) bool {
	out := kubectlIgnoreError(opts, "get", "cluster", "-A", "-o", "json")
	if out == "" {
		return true
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ClusterDefinitionRef string `json:"clusterDefinitionRef"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false
	}
	for _, item := range list.Items {
		if !clusterMatchesType(item.Spec.ClusterDefinitionRef, item.Metadata.Labels, dbType) {
			continue
		}
		if !clusterTerminalPhases[item.Status.Phase] {
			return false
		}
	}
	return true
}

// getHelmChartVersion returns the chart field (e.g. "kubeblocks-0.9.3") for a helm release, or "".
func getHelmChartVersion(opts RunOptions, ns, releaseName string) string {
	out, err := runCmd(opts, "helm", "list", "-n", ns, "-o", "json")
	if err != nil {
		return ""
	}
	var releases []struct {
		Name  string `json:"name"`
		Chart string `json:"chart"`
	}
	if err := json.Unmarshal([]byte(out), &releases); err != nil {
		return ""
	}
	for _, r := range releases {
		if r.Name == releaseName {
			return r.Chart
		}
	}
	return ""
}

func checkHelmChartVersion(opts RunOptions, ns, releaseName, versionSuffix string) bool {
	chart := getHelmChartVersion(opts, ns, releaseName)
	return chart != "" && strings.HasSuffix(chart, versionSuffix)
}

// mysqlConfHasLowerCase checks if my.cnf has lower_case_table_names=1 in [mysqld].
func mysqlConfHasLowerCase(data string) bool {
	inMysqld := false
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[mysqld]" {
			inMysqld = true
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inMysqld = false
			continue
		}
		if inMysqld {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 &&
				strings.TrimSpace(parts[0]) == "lower_case_table_names" &&
				strings.TrimSpace(strings.Trim(parts[1], `"`)) == "1" {
				return true
			}
		}
	}
	return false
}

// ===== Snapshot helpers =====

func snapshotClustersByType(opts RunOptions, dbType string) (*ResourceSnapshot, error) {
	out, err := kubectl(opts, "get", "cluster", "-A", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("获取 %s 集群列表失败: %w", dbType, err)
	}
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ClusterDefinitionRef string `json:"clusterDefinitionRef"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("解析资源列表失败: %w", err)
	}
	snap := &ResourceSnapshot{
		ResourceVersion: list.Metadata.ResourceVersion,
		Names:           make(map[string]struct{}),
	}
	for _, item := range list.Items {
		if !clusterMatchesType(item.Spec.ClusterDefinitionRef, item.Metadata.Labels, dbType) {
			continue
		}
		key := item.Metadata.Name
		if item.Metadata.Namespace != "" {
			key = item.Metadata.Namespace + "/" + item.Metadata.Name
		}
		snap.Names[key] = struct{}{}
	}
	for k := range snap.Names {
		logInfo("快照 %s", k)
	}
	logInfo("快照完成: resourceVersion=%s, %d 个 %s 集群", snap.ResourceVersion, len(snap.Names), dbType)
	return snap, nil
}

// saveSnapshot takes a cluster snapshot and writes it into target. Logs warning on failure.
func saveSnapshot(opts RunOptions, dbType string, target *ResourceSnapshot) {
	snap, err := snapshotClustersByType(opts, dbType)
	if err != nil {
		logWarn("快照 %s 集群失败: %v", dbType, err)
		return
	}
	if target != nil {
		*target = *snap
	}
}

// ===== Watch helpers =====

// runWatch is the core watch loop. Caller manages context deadline.
// It watches resources, updates status map, and returns nil when all reach terminalPhases.
func runWatch(ctx context.Context, status map[string]string, rv string,
	getArgs []string, terminalPhases map[string]bool) error {

	allReady := func() bool {
		for _, phase := range status {
			if !terminalPhases[phase] {
				return false
			}
		}
		return true
	}
	if allReady() {
		return nil
	}

	watchArgs := make([]string, 0, len(getArgs)+5)
	watchArgs = append(watchArgs, "get")
	watchArgs = append(watchArgs, getArgs...)
	watchArgs = append(watchArgs, "--watch-only", "-o", "json")
	if rv != "" {
		watchArgs = append(watchArgs, "--resource-version="+rv)
	}
	watchCmd := exec.CommandContext(ctx, "kubectl", watchArgs...)
	fmt.Printf("         $ kubectl %s\n", strings.Join(watchArgs, " "))
	stdout, err := watchCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 watch pipe 失败: %w", err)
	}
	if err := watchCmd.Start(); err != nil {
		return fmt.Errorf("启动 watch 失败: %w", err)
	}

	dec := json.NewDecoder(stdout)
	for {
		var obj struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		}
		if err := dec.Decode(&obj); err != nil {
			watchCmd.Process.Kill()
			watchCmd.Wait()
			if ctx.Err() != nil {
				break
			}
			return fmt.Errorf("watch 异常退出: %w", err)
		}
		key := obj.Metadata.Name
		if obj.Metadata.Namespace != "" {
			key = obj.Metadata.Namespace + "/" + obj.Metadata.Name
		}
		if _, tracked := status[key]; tracked {
			status[key] = obj.Status.Phase
			logInfo("%s → %s", key, obj.Status.Phase)
			if allReady() {
				watchCmd.Process.Kill()
				watchCmd.Wait()
				return nil
			}
		}
	}

	var pending []string
	for k, phase := range status {
		if !terminalPhases[phase] {
			pending = append(pending, fmt.Sprintf("%s(%s)", k, phase))
		}
	}
	return fmt.Errorf("等待超时，未就绪: %s", strings.Join(pending, ", "))
}

// watchFromSnapshot watches from a snapshot's resourceVersion.
// On 410 Gone, does a fresh List (filtered to snapshot names) and retries.
func watchFromSnapshot(ctx context.Context,
	snap *ResourceSnapshot, getArgs []string, terminalPhases map[string]bool) error {

	if snap == nil || len(snap.Names) == 0 {
		return nil
	}

	status := make(map[string]string, len(snap.Names))
	for k := range snap.Names {
		status[k] = ""
	}

	if snap.ResourceVersion == "" {
		logInfo("resourceVersion 为空，改用 List+Watch 获取当前状态，跟踪 %d 个资源", len(status))
		return refreshAndWatch(ctx, snap.Names, getArgs, terminalPhases)
	}

	logInfo("从 resourceVersion=%s 启动 watch，跟踪 %d 个资源", snap.ResourceVersion, len(status))
	err := runWatch(ctx, status, snap.ResourceVersion, getArgs, terminalPhases)
	if err != nil && ctx.Err() == nil {
		logWarn("watch 异常（可能 410 Gone），重新 List+Watch: %v", err)
		return refreshAndWatch(ctx, snap.Names, getArgs, terminalPhases)
	}
	return err
}

// refreshAndWatch does a fresh List, filters to only the given names, and watches from the new resourceVersion.
func refreshAndWatch(ctx context.Context, trackedNames map[string]struct{},
	getArgs []string, terminalPhases map[string]bool) error {

	listOpts := RunOptions{Ctx: ctx}
	args := make([]string, 0, len(getArgs)+3)
	args = append(args, "get")
	args = append(args, getArgs...)
	args = append(args, "-o", "json")
	out, err := kubectl(listOpts, args...)
	if err != nil {
		return fmt.Errorf("kubectl get 失败: %w", err)
	}

	type resource struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []resource `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return fmt.Errorf("解析资源列表失败: %w", err)
	}

	status := make(map[string]string)
	for _, item := range list.Items {
		key := item.Metadata.Name
		if item.Metadata.Namespace != "" {
			key = item.Metadata.Namespace + "/" + item.Metadata.Name
		}
		if _, tracked := trackedNames[key]; tracked {
			status[key] = item.Status.Phase
		}
	}
	if len(status) == 0 {
		return nil
	}

	logInfo("fallback: 重新跟踪 %d 个资源，resourceVersion=%s", len(status), list.Metadata.ResourceVersion)
	return runWatch(ctx, status, list.Metadata.ResourceVersion, getArgs, terminalPhases)
}

// ===== Logging =====

func logInfo(format string, args ...interface{}) {
	fmt.Printf("         [INFO] "+format+"\n", args...)
}

func logWarn(format string, args ...interface{}) {
	fmt.Printf("         [WARN] "+format+"\n", args...)
}

func logOK(format string, args ...interface{}) {
	fmt.Printf("         [OK]   "+format+"\n", args...)
}
