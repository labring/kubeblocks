package steps

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	targetManagerImage = "ghcr.io/labring/kubeblocks:v0.9.3"
	targetToolsImage   = "ghcr.io/labring/kubeblocks-tools:v0.9.3"
)

var mysqlVersionMap = map[string]string{
	"ac-mysql-8.0.31":   "ac-mysql-8.0.30",
	"ac-mysql-8.0.30-1": "ac-mysql-8.0.30",
	"mysql-5.7.42":      "mysql-5.7.44",
}

// RegisterAll returns upgrade steps in execution order based on enabled modules.
func RegisterAll(modules map[string]bool) []Step {
	addonSnap := &ResourceSnapshot{}

	all := []Step{
		&PreflightCore{Modules: modules},
		&AnnotateAddons{},
		&InstallCRDs{},
		&DeleteIncompatibleOps{},
		&HelmUpgradeKubeBlocks{},
		&UpgradeKbcli{},
		&PatchKBImages{AddonSnapshot: addonSnap},
		&WaitKBReady{AddonSnapshot: addonSnap},
	}

	if modules["clickhouse"] {
		all = append(all, &UpgradeClickHouseAddon{})
	}

	if modules["dbfix"] {
		redisSnap := &ResourceSnapshot{}
		mysqlSnap := &ResourceSnapshot{}
		mysqlLcSnap := &ResourceSnapshot{}
		all = append(all,
			&PreflightDBFix{},
			&FixRedisAndWait{Snapshot: redisSnap},
			&FixMySQLCVAndWait{Snapshot: mysqlSnap},
			&FixMySQLLowercaseAndWait{Snapshot: mysqlLcSnap},
			scriptStep{"verify_pg", "验证 PostgreSQL 连通性 + 主从", "scripts/verify_pg.sh"},
			scriptStep{"verify_mysql", "验证 MySQL 连通性 + 主从", "scripts/verify_mysql.sh"},
			scriptStep{"verify_redis", "验证 Redis 连通性 + 密码 + 主从", "scripts/verify_redis.sh"},
			scriptStep{"verify_mongo", "验证 MongoDB 连通性 + 主从", "scripts/verify_mongo.sh"},
		)
	}

	return all
}

// ===================================================================
//  Core 模块
// ===================================================================

// --- AnnotateAddons ---

type AnnotateAddons struct{}

func (s *AnnotateAddons) Name() string        { return "annotate_addons" }
func (s *AnnotateAddons) Description() string { return "给 Addon 添加 resource-policy=keep 注解" }

func (s *AnnotateAddons) Check(opts RunOptions) (bool, error) {
	out, err := kubectl(opts, "get", "addons.extensions.kubeblocks.io",
		"-l", "app.kubernetes.io/name=kubeblocks", "-o", "json")
	if err != nil {
		return false, err
	}
	if out == "" {
		return true, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false, err
	}
	for _, item := range list.Items {
		if item.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
			return false, nil
		}
	}
	return true, nil
}

func (s *AnnotateAddons) Run(opts RunOptions) error {
	_, err := kubectl(opts, "annotate", "addons.extensions.kubeblocks.io",
		"-l", "app.kubernetes.io/name=kubeblocks",
		"helm.sh/resource-policy=keep", "--overwrite")
	return err
}

// --- InstallCRDs ---

type InstallCRDs struct{}

func (s *InstallCRDs) Name() string        { return "install_crds" }
func (s *InstallCRDs) Description() string { return "安装 v0.9.3 新增 CRD" }

func (s *InstallCRDs) Check(opts RunOptions) (bool, error) {
	_, err := kubectl(opts, "get", "crd", "storageproviders.dataprotection.kubeblocks.io")
	return err == nil, nil
}

func (s *InstallCRDs) Run(opts RunOptions) error {
	_, err := kubectl(opts, "apply", "-f",
		"https://github.com/apecloud/kubeblocks/releases/download/v0.9.3/dataprotection.kubeblocks.io_storageproviders.yaml")
	return err
}

// --- DeleteIncompatibleOps ---

type DeleteIncompatibleOps struct{}

func (s *DeleteIncompatibleOps) Name() string        { return "delete_incompatible_ops" }
func (s *DeleteIncompatibleOps) Description() string { return "删除不兼容的 OpsDefinition" }

var incompatibleOps = []string{"kafka-quota", "kafka-topic", "kafka-user-acl", "switchover"}

func (s *DeleteIncompatibleOps) Check(opts RunOptions) (bool, error) {
	for _, name := range incompatibleOps {
		out, err := kubectl(opts, "get", "opsdefinitions.apps.kubeblocks.io", name,
			"--ignore-not-found", "-o", "name")
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(out) != "" {
			return false, nil
		}
	}
	return true, nil
}

func (s *DeleteIncompatibleOps) Run(opts RunOptions) error {
	for _, name := range incompatibleOps {
		out := kubectlIgnoreError(opts, "get", "opsdefinitions.apps.kubeblocks.io", name, "-o", "json")
		if out == "" || strings.Contains(out, "not found") {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(out), &obj); err == nil {
			if spec, _ := obj["spec"].(map[string]interface{}); spec != nil {
				if _, ok := spec["actions"]; !ok {
					logInfo("%s 缺少 spec.actions，先 patch", name)
					kubectl(opts, "patch", "opsdefinitions.apps.kubeblocks.io", name,
						"--type=merge", "-p", `{"spec":{"actions":[{"failurePolicy":"Fail","name":"placeholder","workload":{"backoffLimit":2,"podSpec":{"containers":[{"command":["echo"],"image":"busybox","imagePullPolicy":"IfNotPresent","name":"placeholder"}]},"type":"Job"}}]}}`)
				}
			}
		}
		if _, err := kubectl(opts, "delete", "opsdefinitions.apps.kubeblocks.io", name, "--ignore-not-found"); err != nil {
			return err
		}
		logOK("已删除 %s", name)
	}
	return nil
}

// --- HelmUpgradeKubeBlocks ---

type HelmUpgradeKubeBlocks struct{}

func (s *HelmUpgradeKubeBlocks) Name() string        { return "helm_upgrade" }
func (s *HelmUpgradeKubeBlocks) Description() string { return "Helm upgrade KubeBlocks 到 v0.9.3" }

func (s *HelmUpgradeKubeBlocks) Check(opts RunOptions) (bool, error) {
	return checkHelmChartVersion(opts, "kb-system", "kubeblocks", "-0.9.3"), nil
}

func (s *HelmUpgradeKubeBlocks) Run(opts RunOptions) error {
	if _, err := runCmd(opts, "helm", "repo", "add", "kubeblocks", "https://apecloud.github.io/helm-charts"); err != nil {
		logWarn("helm repo add 失败（repo 可能已存在）: %v", err)
	}
	if _, err := runCmd(opts, "helm", "repo", "update", "kubeblocks"); err != nil {
		return err
	}
	_, err := runCmd(opts, "helm",
		"-n", "kb-system", "upgrade", "kubeblocks", "kubeblocks/kubeblocks",
		"--version", "0.9.3",
		"--set", "upgradeAddons=true",
		"--set", "admissionWebhooks.enabled=true",
		"--set", "admissionWebhooks.ignoreReplicasCheck=true",
		"--set", "replicaCount=0",
		"--set", "loggerSettings.level=debug",
		"--set", "reconcileWorkers=30",
		"--set", "client.qps=120",
		"--set", "client.burst=180")
	return err
}

// --- UpgradeKbcli ---

type UpgradeKbcli struct{}

func (s *UpgradeKbcli) Name() string        { return "upgrade_kbcli" }
func (s *UpgradeKbcli) Description() string { return "升级 kbcli 到 v0.9.3" }

// kbcliClientVersion 从 `kbcli version` 文本中解析客户端版本（匹配 "kbcli: x.y.z" 行）。
func kbcliClientVersion(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "kbcli:") {
			i := strings.Index(line, ":")
			if i >= 0 && i+1 < len(line) {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[i+1:]), "v"))
			}
		}
	}
	return ""
}

func (s *UpgradeKbcli) Check(opts RunOptions) (bool, error) {
	out, err := runCmd(opts, "kbcli", "version")
	if err != nil {
		return false, nil
	}
	ver := kbcliClientVersion(out)
	return strings.HasPrefix(ver, "0.9.3"), nil
}

func (s *UpgradeKbcli) Run(opts RunOptions) error {
	os.Remove("/usr/bin/kbcli")
	os.Remove("/usr/local/bin/kbcli")
	if _, err := runCmd(opts, "bash", "-c",
		"curl -fsSL https://kubeblocks.io/installer/install_cli.sh | bash -s 0.9.3"); err != nil {
		return err
	}
	_, err := runCmd(opts, "cp", "/usr/local/bin/kbcli", "/usr/bin/kbcli")
	return err
}

// --- PatchKBImages ---

type PatchKBImages struct {
	AddonSnapshot *ResourceSnapshot
}

func (s *PatchKBImages) Name() string        { return "patch_kb_images" }
func (s *PatchKBImages) Description() string { return "替换 KubeBlocks 自定义镜像" }

func (s *PatchKBImages) Check(opts RunOptions) (bool, error) {
	if !checkDeploymentImage(opts, "kb-system", "kubeblocks", "manager", targetManagerImage) {
		return false, nil
	}
	return checkDeploymentImage(opts, "kb-system", "kubeblocks", "tools", targetToolsImage), nil
}

func (s *PatchKBImages) Run(opts RunOptions) error {
	snap, err := snapshotHelmTrackedAddons(opts)
	if err != nil {
		logWarn("快照 Helm 侧 Addon 失败: %v", err)
	} else if s.AddonSnapshot != nil {
		*s.AddonSnapshot = *snap
	}
	return runScript(opts, "scripts/patch_kb_images.sh")
}

// --- WaitKBReady ---

type WaitKBReady struct {
	AddonSnapshot *ResourceSnapshot
}

func (s *WaitKBReady) Name() string        { return "wait_kb_ready" }
func (s *WaitKBReady) Description() string { return "等待 KubeBlocks 控制器 + Addon 就绪" }

func (s *WaitKBReady) Check(opts RunOptions) (bool, error) {
	if !checkDeploymentReady(opts, "kb-system", "kubeblocks") ||
		!checkDeploymentReady(opts, "kb-system", "kubeblocks-dataprotection") {
		return false, nil
	}
	return checkHelmTrackedAddonsSettled(opts), nil
}

func (s *WaitKBReady) Run(opts RunOptions) error {
	startedAt := time.Now()
	for _, deploy := range []string{"deploy/kubeblocks", "deploy/kubeblocks-dataprotection"} {
		if _, err := runCmd(opts, "kubectl", "rollout", "status", deploy, "-n", "kb-system"); err != nil {
			return err
		}
	}
	return waitHelmTrackedAddonsSettled(opts.Ctx, opts, s.AddonSnapshot, startedAt)
}

// ===================================================================
//  ClickHouse 模块
// ===================================================================

type UpgradeClickHouseAddon struct{}

func (s *UpgradeClickHouseAddon) Name() string        { return "upgrade_clickhouse" }
func (s *UpgradeClickHouseAddon) Description() string { return "升级 ClickHouse addon 到 v0.9.1" }

func (s *UpgradeClickHouseAddon) Check(opts RunOptions) (bool, error) {
	return checkHelmChartVersion(opts, "kb-system", "kb-addon-clickhouse", "-0.9.1"), nil
}

func (s *UpgradeClickHouseAddon) Run(opts RunOptions) error {
	if _, err := runCmd(opts, "kbcli", "addon", "upgrade", "clickhouse", "--version", "0.9.1"); err == nil {
		return nil
	}
	logWarn("kbcli 升级失败，回退到 helm")
	runCmd(opts, "helm", "uninstall", "kb-addon-clickhouse", "--namespace", "kb-system")
	_, err := runCmd(opts, "helm", "install",
		"kb-addon-clickhouse", "kubeblocks/clickhouse",
		"--namespace", "kb-system", "--create-namespace", "--version", "0.9.1")
	return err
}

// ===================================================================
//  DBFix 模块
// ===================================================================

func checkRedisNeedsFix(opts RunOptions) (bool, error) {
	out, err := kubectl(opts, "get", "cluster", "-A", "-o", "json")
	if err != nil {
		return false, err
	}
	if out == "" {
		return false, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Labels    map[string]string `json:"labels"`
				Namespace string            `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				ClusterDefinitionRef string                 `json:"clusterDefinitionRef"`
				ClusterVersionRef    string                 `json:"clusterVersionRef"`
				Topology             string                 `json:"topology"`
				Affinity             map[string]interface{} `json:"affinity"`
				Backup               map[string]interface{} `json:"backup"`
				Monitor              interface{}            `json:"monitor"`
				Tolerations          interface{}            `json:"tolerations"`
				ComponentSpecs       []struct {
					Name            string   `json:"name"`
					ComponentDef    string   `json:"componentDef"`
					ComponentDefRef string   `json:"componentDefRef"`
					EnabledLogs     []string `json:"enabledLogs"`
					Env             []struct {
						Name string `json:"name"`
					} `json:"env"`
					Monitor            interface{} `json:"monitor"`
					NoCreatePDB        interface{} `json:"noCreatePDB"`
					RsmTransformPolicy interface{} `json:"rsmTransformPolicy"`
				} `json:"componentSpecs"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false, err
	}

	for _, item := range list.Items {
		if !clusterMatchesType(item.Spec.ClusterDefinitionRef, item.Metadata.Labels, "redis") {
			continue
		}
		labels := item.Metadata.Labels
		if labels["app.kubernetes.io/version"] != "7.0.6" ||
			labels["helm.sh/chart"] != "redis-cluster-0.9.0" ||
			labels["kb.io/database"] != "redis-7.0.6" {
			return true, nil
		}
		if item.Spec.Topology != "replication" {
			return true, nil
		}
		if item.Spec.Monitor != nil || item.Spec.Tolerations != nil {
			return true, nil
		}
		if item.Spec.Affinity != nil {
			if _, ok := item.Spec.Affinity["topologyKeys"]; ok {
				return true, nil
			}
		}
		if item.Spec.Backup != nil {
			if repoName, ok := item.Spec.Backup["repoName"]; ok && fmt.Sprint(repoName) != "" {
				return true, nil
			}
			if incr, ok := item.Spec.Backup["incrementalBackupEnabled"]; !ok || fmt.Sprint(incr) != "false" {
				return true, nil
			}
		}
		if item.Spec.ClusterVersionRef != "" {
			return true, nil
		}
		for _, comp := range item.Spec.ComponentSpecs {
			if comp.ComponentDefRef != "" || comp.ComponentDef == "" {
				return true, nil
			}
			if comp.Monitor != nil || comp.NoCreatePDB != nil || comp.RsmTransformPolicy != nil {
				return true, nil
			}
			if comp.Name == "redis" {
				hasRunningLog := false
				for _, log := range comp.EnabledLogs {
					if log == "running" {
						hasRunningLog = true
						break
					}
				}
				hasSentinelEnv := false
				for _, env := range comp.Env {
					if env.Name == "CUSTOM_SENTINEL_MASTER_NAME" {
						hasSentinelEnv = true
						break
					}
				}
				if !hasRunningLog || !hasSentinelEnv {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func runFixRedis(opts RunOptions, snapshot *ResourceSnapshot) error {
	saveSnapshot(opts, "redis", snapshot)
	if err := runScript(opts, "scripts/fix_redis_check_sentinel.sh"); err != nil {
		return err
	}
	if err := runScript(opts, "scripts/fix_redis_cluster.sh"); err != nil {
		return fmt.Errorf("修复 Redis cluster 失败: %w", err)
	}
	if err := runScript(opts, "scripts/restart_redis.sh"); err != nil {
		return err
	}
	return nil
}

type FixRedisAndWait struct {
	Snapshot *ResourceSnapshot
}

func (s *FixRedisAndWait) Name() string { return "fix_redis_and_wait" }
func (s *FixRedisAndWait) Description() string {
	return "修复 Redis 集群 CR + 重启 + 等待就绪"
}
func (s *FixRedisAndWait) Check(opts RunOptions) (bool, error) {
	needsFix, err := checkRedisNeedsFix(opts)
	if err != nil {
		return false, err
	}
	return !needsFix, nil
}
func (s *FixRedisAndWait) Run(opts RunOptions) error {
	if err := runFixRedis(opts, s.Snapshot); err != nil {
		return err
	}
	return waitDBReady(opts, "redis", s.Snapshot)
}

func checkMySQLVersionNeedsFix(opts RunOptions) (bool, error) {
	out, err := kubectl(opts, "get", "cluster", "-A", "-o", "json")
	if err != nil {
		return false, err
	}
	if out == "" {
		return false, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ClusterDefinitionRef string `json:"clusterDefinitionRef"`
				ClusterVersionRef    string `json:"clusterVersionRef"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false, err
	}

	for _, item := range list.Items {
		if !clusterMatchesType(item.Spec.ClusterDefinitionRef, item.Metadata.Labels, "mysql") {
			continue
		}
		for oldVersion := range mysqlVersionMap {
			if item.Spec.ClusterVersionRef == oldVersion ||
				item.Metadata.Labels["clusterversion.kubeblocks.io/name"] == oldVersion {
				return true, nil
			}
			isOut, err := kubectl(opts, "get", "instanceset", item.Metadata.Name+"-mysql",
				"-n", item.Metadata.Namespace, "-o", "json")
			if err != nil {
				return false, err
			}
			if isOut == "" {
				return true, nil
			}
			var is struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Spec struct {
					Template struct {
						Metadata struct {
							Labels map[string]string `json:"labels"`
						} `json:"metadata"`
					} `json:"template"`
				} `json:"spec"`
			}
			if err := json.Unmarshal([]byte(isOut), &is); err != nil {
				return false, err
			}
			if is.Metadata.Labels["clusterversion.kubeblocks.io/name"] == oldVersion ||
				is.Spec.Template.Metadata.Labels["clusterversion.kubeblocks.io/name"] == oldVersion {
				return true, nil
			}
		}
	}
	return false, nil
}

func runFixMySQLCV(opts RunOptions, snapshot *ResourceSnapshot) error {
	saveSnapshot(opts, "mysql", snapshot)
	if err := runScript(opts, "scripts/fix_mysql_cv.sh"); err != nil {
		return fmt.Errorf("修复 MySQL 版本映射失败: %w", err)
	}
	if err := runScript(opts, "scripts/restart_mysql.sh"); err != nil {
		return fmt.Errorf("重启 MySQL 失败: %w", err)
	}
	return nil
}

type FixMySQLCVAndWait struct {
	Snapshot *ResourceSnapshot
}

func (s *FixMySQLCVAndWait) Name() string { return "fix_mysql_cv_and_wait" }
func (s *FixMySQLCVAndWait) Description() string {
	return "修复 MySQL 版本映射 + 必要时补 lowercase + 等待就绪"
}
func (s *FixMySQLCVAndWait) Check(opts RunOptions) (bool, error) {
	needsFix, err := checkMySQLVersionNeedsFix(opts)
	if err != nil {
		return false, err
	}
	return !needsFix, nil
}
func (s *FixMySQLCVAndWait) Run(opts RunOptions) error {
	if err := runFixMySQLCV(opts, s.Snapshot); err != nil {
		return err
	}
	needsLowercase, err := checkMySQLLowercaseNeedsFix(opts)
	if err != nil {
		return err
	}
	if needsLowercase {
		if err := runFixMySQLLowercase(opts, s.Snapshot); err != nil {
			return err
		}
	}
	return waitDBReady(opts, "mysql", s.Snapshot)
}

func checkMySQLLowercaseNeedsFix(opts RunOptions) (bool, error) {
	out, err := kubectl(opts, "get", "cluster", "-A", "-o", "json")
	if err != nil {
		return false, err
	}
	if out == "" {
		return false, nil
	}
	var list struct {
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
		return false, err
	}

	for _, item := range list.Items {
		if !clusterMatchesType(item.Spec.ClusterDefinitionRef, item.Metadata.Labels, "mysql") {
			continue
		}
		ns, cluster := item.Metadata.Namespace, item.Metadata.Name

		needsLCTN, err := mysqlConfigNeedsLCTN(opts, ns, cluster)
		if err != nil {
			return false, err
		}
		if !needsLCTN {
			continue
		}

		cm := cluster + "-mysql-mysql-consensusset-config"
		cmOut := kubectlIgnoreError(opts, "-n", ns, "get", "cm", cm, "-o", "json")
		if cmOut == "" {
			return true, nil
		}
		var cmData struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal([]byte(cmOut), &cmData); err != nil {
			return false, err
		}
		if !mysqlConfHasLowerCase(cmData.Data["my.cnf"]) {
			return true, nil
		}
	}
	return false, nil
}

// mysqlConfigNeedsLCTN checks whether the configuration resource declares lower_case_table_names=1.
func mysqlConfigNeedsLCTN(opts RunOptions, ns, cluster string) (bool, error) {
	cfgOut, err := kubectl(opts, "-n", ns, "get", "configuration", cluster+"-mysql", "-o", "json")
	if err != nil {
		return false, err
	}
	if cfgOut == "" {
		return false, nil
	}
	var cfg struct {
		Spec struct {
			ConfigItemDetails []struct {
				Name             string `json:"name"`
				ConfigFileParams map[string]struct {
					Parameters map[string]string `json:"parameters"`
				} `json:"configFileParams"`
			} `json:"configItemDetails"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(cfgOut), &cfg); err != nil {
		return false, err
	}
	for _, detail := range cfg.Spec.ConfigItemDetails {
		if detail.Name == "mysql-consensusset-config" {
			if fp, ok := detail.ConfigFileParams["my.cnf"]; ok {
				return fp.Parameters["lower_case_table_names"] == "1", nil
			}
		}
	}
	return false, nil
}

func runFixMySQLLowercase(opts RunOptions, snapshot *ResourceSnapshot) error {
	saveSnapshot(opts, "mysql", snapshot)
	return runScript(opts, "scripts/fix_mysql_lowercase.sh")
}

type FixMySQLLowercaseAndWait struct {
	Snapshot *ResourceSnapshot
}

func (s *FixMySQLLowercaseAndWait) Name() string { return "fix_mysql_lowercase_and_wait" }
func (s *FixMySQLLowercaseAndWait) Description() string {
	return "修复 MySQL lower_case_table_names + 等待就绪"
}
func (s *FixMySQLLowercaseAndWait) Check(opts RunOptions) (bool, error) {
	needsFix, err := checkMySQLLowercaseNeedsFix(opts)
	if err != nil {
		return false, err
	}
	return !needsFix, nil
}
func (s *FixMySQLLowercaseAndWait) Run(opts RunOptions) error {
	if err := runFixMySQLLowercase(opts, s.Snapshot); err != nil {
		return err
	}
	return waitDBReady(opts, "mysql", s.Snapshot)
}

func waitDBReady(opts RunOptions, dbType string, snap *ResourceSnapshot) error {
	if snap == nil || len(snap.Names) == 0 {
		var err error
		snap, err = snapshotClustersByType(opts, dbType)
		if err != nil {
			return err
		}
	}
	if len(snap.Names) == 0 {
		logInfo("无 %s 集群需要等待", dbType)
		return nil
	}
	return watchFromSnapshot(opts.Ctx, snap, []string{"cluster", "-A"}, clusterTerminalPhases)
}

