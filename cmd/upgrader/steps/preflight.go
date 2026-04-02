package steps

import (
	"fmt"
	"strings"
)

// PreflightCore 在 core 步骤之前检查集群与 0.8→0.9 升级前置条件。
type PreflightCore struct {
	Modules map[string]bool
}

func (s *PreflightCore) Name() string                  { return "preflight_core" }
func (s *PreflightCore) Description() string           { return "Core 升级前置检查" }
func (s *PreflightCore) Check(RunOptions) (bool, error) { return false, nil }

func (s *PreflightCore) Run(opts RunOptions) error {
	var errs []string

	check := func(msg string, name string, args ...string) {
		if _, err := runCmd(opts, name, args...); err != nil {
			errs = append(errs, msg)
		}
	}

	checkKubectl := func(msg string, args ...string) {
		if _, err := kubectl(opts, args...); err != nil {
			errs = append(errs, msg)
		}
	}

	check("kubectl 无法连接集群", "kubectl", "cluster-info")
	check("helm 未安装", "helm", "version")
	checkKubectl("命名空间 kb-system 不存在", "get", "ns", "kb-system")
	checkKubectl("Deployment kubeblocks 不存在", "get", "deploy", "kubeblocks", "-n", "kb-system")
	checkKubectl("Deployment kubeblocks-dataprotection 不存在", "get", "deploy", "kubeblocks-dataprotection", "-n", "kb-system")

	for _, crd := range []string{
		"clusters.apps.kubeblocks.io",
		"addons.extensions.kubeblocks.io",
		"backuppolicies.dataprotection.kubeblocks.io",
	} {
		checkKubectl(
			fmt.Sprintf("CRD %s 不存在，KubeBlocks 未安装？", crd),
			"get", "crd", crd,
		)
	}

	s.checkVersion(opts, &errs)

	if len(errs) > 0 {
		for _, e := range errs {
			logWarn(e)
		}
		return fmt.Errorf("preflight_core 检查失败，共 %d 项不满足", len(errs))
	}
	return nil
}

// checkVersion 通过 helm list 检查当前 KubeBlocks 版本是否在支持范围内。
// 已是 0.9.3 时不失败：后续 helm_upgrade 等步骤的 Check 会按实际状态跳过。
func (s *PreflightCore) checkVersion(opts RunOptions, errs *[]string) {
	chart := getHelmChartVersion(opts, "kb-system", "kubeblocks")
	if chart == "" {
		*errs = append(*errs, "未找到 helm release kubeblocks，KubeBlocks 未安装？")
		return
	}
	version := strings.TrimPrefix(chart, "kubeblocks-")

	if strings.HasPrefix(version, "0.8.") || version == "0.9.3" {
		return
	}
	*errs = append(*errs, fmt.Sprintf("当前版本 %s，仅支持从 0.8.x 升级至 0.9.3，或集群已为 kubeblocks-0.9.3", version))
}

// PreflightDBFix 在 dbfix 步骤之前检查工具与 0.9 CRD（须在 core 完成之后执行）。
type PreflightDBFix struct{}

func (s *PreflightDBFix) Name() string                  { return "preflight_dbfix" }
func (s *PreflightDBFix) Description() string           { return "DBFix 前置检查" }
func (s *PreflightDBFix) Check(RunOptions) (bool, error) { return false, nil }

func (s *PreflightDBFix) Run(opts RunOptions) error {
	var errs []string

	check := func(msg string, name string, args ...string) {
		if _, err := runCmd(opts, name, args...); err != nil {
			errs = append(errs, msg)
		}
	}

	checkKubectl := func(msg string, args ...string) {
		if _, err := kubectl(opts, args...); err != nil {
			errs = append(errs, msg)
		}
	}

	check("jq 未安装（dbfix 模块的验证脚本需要）", "jq", "--version")
	check("python3 或 PyYAML 未安装（修复 Redis 需要）", "python3", "-c", "import yaml")
	check("mysql 客户端未安装（验证 MySQL 需要）", "mysql", "--version")
	check("psql 客户端未安装（验证 PostgreSQL 需要）", "psql", "--version")
	check("redis-cli 未安装（验证 Redis 需要）", "redis-cli", "--version")

	for _, crd := range []string{
		"instancesets.workloads.kubeblocks.io",
		"configurations.apps.kubeblocks.io",
	} {
		checkKubectl(
			fmt.Sprintf("CRD %s 不存在", crd),
			"get", "crd", crd,
		)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			logWarn(e)
		}
		return fmt.Errorf("preflight_dbfix 检查失败，共 %d 项不满足", len(errs))
	}
	return nil
}
