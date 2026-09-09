package steps

import (
	"fmt"
	"strings"
)

// PreflightCore verifies the cluster and upgrade prerequisites before core steps.
type PreflightCore struct {
	Modules map[string]bool
}

func (s *PreflightCore) Name() string                   { return "preflight_core" }
func (s *PreflightCore) Description() string            { return "core upgrade prerequisite checks" }
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

	check("kubectl cannot connect to the cluster", "kubectl", "cluster-info")
	check("helm is not installed", "helm", "version")
	checkKubectl("namespace kb-system does not exist", "get", "ns", "kb-system")
	checkKubectl("Deployment kubeblocks does not exist", "get", "deploy", "kubeblocks", "-n", "kb-system")
	checkKubectl("Deployment kubeblocks-dataprotection does not exist", "get", "deploy", "kubeblocks-dataprotection", "-n", "kb-system")

	for _, crd := range []string{
		"clusters.apps.kubeblocks.io",
		"addons.extensions.kubeblocks.io",
		"backuppolicies.dataprotection.kubeblocks.io",
	} {
		checkKubectl(
			fmt.Sprintf("CRD %s does not exist; is KubeBlocks installed?", crd),
			"get", "crd", crd,
		)
	}

	s.checkVersion(opts, &errs)

	if len(errs) > 0 {
		for _, e := range errs {
			logWarn(e)
		}
		return fmt.Errorf("preflight_core failed with %d unmet requirements", len(errs))
	}
	return nil
}

// checkVersion validates that the current KubeBlocks version is in the supported range.
// It accepts v0.9.3 because subsequent steps skip when they are already complete.
func (s *PreflightCore) checkVersion(opts RunOptions, errs *[]string) {
	chart := getHelmChartVersion(opts, "kb-system", "kubeblocks")
	if chart == "" {
		*errs = append(*errs, "Helm release kubeblocks was not found; is KubeBlocks installed?")
		return
	}
	version := strings.TrimPrefix(chart, "kubeblocks-")

	if strings.HasPrefix(version, "0.8.") || version == "0.9.3" {
		return
	}
	*errs = append(*errs, fmt.Sprintf("current version %s is unsupported; only 0.8.x to 0.9.3 upgrades or kubeblocks-0.9.3 are supported", version))
}

// PreflightDBFix validates required tools and v0.9 CRDs before DBFix steps.
type PreflightDBFix struct{}

func (s *PreflightDBFix) Name() string                   { return "preflight_dbfix" }
func (s *PreflightDBFix) Description() string            { return "DBFix prerequisite checks" }
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

	check("jq is not installed (required by DBFix verification scripts)", "jq", "--version")
	check("python3 or PyYAML is not installed (required to repair Redis)", "python3", "-c", "import yaml")
	check("mysql client is not installed (required to verify MySQL)", "mysql", "--version")
	check("psql client is not installed (required to verify PostgreSQL)", "psql", "--version")
	check("redis-cli is not installed (required to verify Redis)", "redis-cli", "--version")
	check("mongosh is not installed (required to verify MongoDB)", "mongosh", "--version")

	for _, crd := range []string{
		"instancesets.workloads.kubeblocks.io",
		"configurations.apps.kubeblocks.io",
	} {
		checkKubectl(
			fmt.Sprintf("CRD %s does not exist", crd),
			"get", "crd", crd,
		)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			logWarn(e)
		}
		return fmt.Errorf("preflight_dbfix failed with %d unmet requirements", len(errs))
	}
	return nil
}
