#!/bin/bash
set -e

echo "开始处理所有命名空间中的 Redis Cluster ..."

kubectl get cluster -A -o json | jq -r '
  .items[] | select(
    ((.spec.clusterDefinitionRef // "") | test("redis"; "i"))
    or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("redis"; "i"))
  ) | "\(.metadata.namespace) \(.metadata.name)"' | while read ns name; do
  echo ""
  echo "🧩 处理 Redis Cluster: $name (命名空间: $ns)"

  INPUT_FILE="${name}-${ns}.yaml"
  OUTPUT_FILE="${name}-${ns}-new.yaml"

  echo "📥 导出 YAML 到 $INPUT_FILE"
  kubectl get cluster "$name" -n "$ns" -o yaml > "$INPUT_FILE"

  if [ ! -s "$INPUT_FILE" ]; then
    echo "❌ 错误: 无法导出 $name 的 YAML"
    continue
  fi

  # --------------------------
  # 转换内容（使用 convert_before_to_after.sh 逻辑）
  # --------------------------
  echo "⚙️  执行 YAML 转换..."
  python3 - "$INPUT_FILE" "$OUTPUT_FILE" <<'PY'
import re
import sys
import yaml
from copy import deepcopy

input_file = sys.argv[1]
output_file = sys.argv[2]

with open(input_file, "r", encoding="utf-8") as f:
    doc = yaml.safe_load(f)

if not isinstance(doc, dict):
    raise SystemExit("Input YAML root must be a mapping")

metadata = doc.setdefault("metadata", {})
spec = doc.setdefault("spec", {})
labels = metadata.setdefault("labels", {})

name = metadata.get("name", "")

def extract_version(value: str) -> str:
    if not value:
        return ""
    m = re.match(r"^[^-]+-(.+)$", value)
    return m.group(1) if m else value

version = ""
for candidate in [
    spec.get("clusterVersionRef", ""),
    labels.get("clusterversion.kubeblocks.io/name", ""),
]:
    version = extract_version(candidate)
    if version:
        break

if not version:
    for comp in spec.get("componentSpecs", []) or []:
        if isinstance(comp, dict) and comp.get("serviceVersion"):
            version = str(comp["serviceVersion"])
            break

major = version.split(".")[0] if version else "7"

# Only add the required labels during conversion.
labels["app.kubernetes.io/version"] = "7.0.6"
labels["helm.sh/chart"] = "redis-cluster-0.9.0"
labels["kb.io/database"] = "redis-7.0.6"
labels["clusterversion.kubeblocks.io/name"] = ""
labels["app.kubernetes.io/instance"] = name

affinity = spec.get("affinity")
if isinstance(affinity, dict):
    affinity.pop("topologyKeys", None)

backup = spec.get("backup")
if isinstance(backup, dict):
    backup.setdefault("incrementalBackupEnabled", False)
    backup.pop("repoName", None)

spec.pop("clusterVersionRef", None)
spec.pop("monitor", None)
spec.pop("tolerations", None)
spec.setdefault("topology", "replication")

def reorder_component_keys(component: dict) -> dict:
    preferred_order = [
        "componentDef",
        "enabledLogs",
        "env",
        "name",
        "replicas",
        "resources",
        "serviceAccountName",
        "serviceVersion",
        "switchPolicy",
        "volumeClaimTemplates",
    ]
    ordered = {}
    for key in preferred_order:
        if key in component:
            ordered[key] = component[key]
    for key, value in component.items():
        if key not in ordered:
            ordered[key] = value
    return ordered

new_components = []
for comp in spec.get("componentSpecs", []) or []:
    if not isinstance(comp, dict):
        new_components.append(comp)
        continue

    c = deepcopy(comp)
    comp_def_ref = c.pop("componentDefRef", "")

    if "componentDef" not in c and comp_def_ref:
        if comp_def_ref == "redis":
            c["componentDef"] = f"redis-{major}"
        elif comp_def_ref.startswith("redis-sentinel"):
            c["componentDef"] = f"redis-sentinel-{major}"
        else:
            c["componentDef"] = comp_def_ref

    c.pop("monitor", None)
    c.pop("noCreatePDB", None)
    c.pop("rsmTransformPolicy", None)

    if version and not c.get("serviceVersion"):
        c["serviceVersion"] = version

    if c.get("name") == "redis":
        c.setdefault("enabledLogs", ["running"])
        env = c.setdefault("env", [])
        if isinstance(env, list):
            exists = any(isinstance(e, dict) and e.get("name") == "CUSTOM_SENTINEL_MASTER_NAME" for e in env)
            if not exists:
                env.append({"name": "CUSTOM_SENTINEL_MASTER_NAME"})

    new_components.append(reorder_component_keys(c))

if "componentSpecs" in spec:
    spec["componentSpecs"] = new_components

with open(output_file, "w", encoding="utf-8") as f:
    yaml.safe_dump(doc, f, allow_unicode=True, sort_keys=False)
PY

  # --------------------------
  # 应用修改
  # --------------------------
  echo "✨ YAML 已修改完毕，保存为: $OUTPUT_FILE"
  echo "🚀 正在 apply 到命名空间 $ns ..."
  kubectl apply -f "$OUTPUT_FILE" -n "$ns"
  echo "🎉 $name-$ns 已成功更新！"

  # 重启 Sentinel Pod
  kubectl get pods -n "$ns" -l apps.kubeblocks.io/component-name=redis-sentinel --no-headers 2>/dev/null | grep "$name" | awk '{print $1}' | while read pod_name; do
    echo "🧩 正在处理 Sentinel Pod: $pod_name (命名空间: $ns)"
    kubectl delete pod "$pod_name" -n "$ns" --force >/dev/null 2>&1
    echo "✅ 已强制删除 Sentinel Pod: $pod_name"
  done

  # 重启 Redis Pod
  kubectl get pods -n "$ns" -l apps.kubeblocks.io/component-name=redis --no-headers 2>/dev/null | grep "$name" | awk '{print $1}' | while read pod_name; do
    echo "🧩 正在处理 Redis Pod: $pod_name (命名空间: $ns)"
    kubectl delete pod "$pod_name" -n "$ns" --force >/dev/null 2>&1
    echo "✅ 已强制删除 Redis Pod: $pod_name"
  done
done

echo ""
echo "✅ 所有 Redis Cluster 已处理完成！"
