package steps

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	dbProviderClusterRoleName      = "cluster-version-reader"
	componentVersionsAPIGroup      = "apps.kubeblocks.io"
	componentVersionsResource      = "componentversions"
	componentVersionsRequiredVerbs = "get,list,watch"
)

type clusterRolePolicyRule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

type clusterRole struct {
	Rules []clusterRolePolicyRule `json:"rules"`
}

type PatchDBProviderComponentVersionsRBAC struct{}

func (s *PatchDBProviderComponentVersionsRBAC) Name() string {
	return "patch_dbprovider_componentversions_rbac"
}

func (s *PatchDBProviderComponentVersionsRBAC) Description() string {
	return "补 dbprovider 前端读取 ComponentVersion 的 RBAC 权限"
}

func (s *PatchDBProviderComponentVersionsRBAC) Check(opts RunOptions) (bool, error) {
	role, exists, err := loadDBProviderClusterRole(opts)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	return clusterRoleHasComponentVersionsPermission(role), nil
}

func (s *PatchDBProviderComponentVersionsRBAC) Run(opts RunOptions) error {
	role, exists, err := loadDBProviderClusterRole(opts)
	if err != nil {
		return err
	}
	if !exists {
		logInfo("未发现 ClusterRole %s，跳过 dbprovider 前端 RBAC 修复", dbProviderClusterRoleName)
		return nil
	}
	if clusterRoleHasComponentVersionsPermission(role) {
		logInfo("ClusterRole %s 已具备 componentversions %s 权限", dbProviderClusterRoleName, componentVersionsRequiredVerbs)
		return nil
	}

	patch, err := componentVersionsRBACPatch(role)
	if err != nil {
		return err
	}
	if _, err := kubectl(opts, "patch", "clusterrole", dbProviderClusterRoleName, "--type=json", "-p", patch); err != nil {
		return fmt.Errorf("补 ClusterRole %s 的 componentversions 权限失败: %w", dbProviderClusterRoleName, err)
	}
	logOK("已补 ClusterRole %s 的 componentversions %s 权限", dbProviderClusterRoleName, componentVersionsRequiredVerbs)
	return nil
}

func loadDBProviderClusterRole(opts RunOptions) (*clusterRole, bool, error) {
	out, err := kubectl(opts, "get", "clusterrole", dbProviderClusterRoleName, "--ignore-not-found", "-o", "json")
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, false, nil
	}

	role := &clusterRole{}
	if err := json.Unmarshal([]byte(out), role); err != nil {
		return nil, false, fmt.Errorf("解析 ClusterRole %s 失败: %w", dbProviderClusterRoleName, err)
	}
	return role, true, nil
}

func clusterRoleHasComponentVersionsPermission(role *clusterRole) bool {
	if role == nil {
		return false
	}
	for _, verb := range []string{"get", "list", "watch"} {
		allowed := false
		for _, rule := range role.Rules {
			if ruleAllowsComponentVersionsVerb(rule, verb) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func ruleAllowsComponentVersionsVerb(rule clusterRolePolicyRule, verb string) bool {
	return containsPolicyValue(rule.APIGroups, componentVersionsAPIGroup) &&
		containsPolicyValue(rule.Resources, componentVersionsResource) &&
		containsPolicyValue(rule.Verbs, verb)
}

func containsPolicyValue(values []string, target string) bool {
	for _, value := range values {
		if value == target || value == "*" {
			return true
		}
	}
	return false
}

func componentVersionsRBACPatch(role *clusterRole) (string, error) {
	rule := componentVersionsPolicyRule()
	path := "/rules/-"
	value := interface{}(rule)
	if role == nil || len(role.Rules) == 0 {
		path = "/rules"
		value = []clusterRolePolicyRule{rule}
	}

	patch := []map[string]interface{}{
		{
			"op":    "add",
			"path":  path,
			"value": value,
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func componentVersionsPolicyRule() clusterRolePolicyRule {
	return clusterRolePolicyRule{
		APIGroups: []string{componentVersionsAPIGroup},
		Resources: []string{componentVersionsResource},
		Verbs:     strings.Split(componentVersionsRequiredVerbs, ","),
	}
}
