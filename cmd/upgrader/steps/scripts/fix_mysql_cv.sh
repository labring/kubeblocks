#!/bin/bash
set -e

# 版本映射：OLD -> NEW
declare -A VERSION_MAP=(
  ["ac-mysql-8.0.31"]="ac-mysql-8.0.30"
  ["mysql-5.7.42"]="mysql-5.7.44"
  ["ac-mysql-8.0.30-1"]="ac-mysql-8.0.30"
)

for OLD in "${!VERSION_MAP[@]}"; do
  NEW="${VERSION_MAP[$OLD]}"
  echo "🚀 开始处理版本迁移: $OLD -> $NEW"
  echo "==========================================="

  kubectl get cluster -A | grep "$OLD" | awk '{print $1" "$2}' | while read namespace name; do
    echo "🔍 处理 Cluster: $name (ns: $namespace)"

    echo "📌 修改 Cluster.metadata.labels.clusterversion.kubeblocks.io/name"
    kubectl patch cluster "$name" -n "$namespace" --type=merge -p \
      "{\"metadata\":{\"labels\":{\"clusterversion.kubeblocks.io/name\":\"$NEW\"}}}"

    echo "📌 修改 Cluster.spec.clusterVersionRef"
    kubectl patch cluster "$name" -n "$namespace" --type=json -p \
      "[{\"op\":\"replace\",\"path\":\"/spec/clusterVersionRef\",\"value\":\"$NEW\"}]"

    # 检查 InstanceSet 是否存在
    if kubectl get instanceset "${name}-mysql" -n "$namespace" &>/dev/null; then
      echo "📌 修改 InstanceSet.metadata.labels.clusterversion.kubeblocks.io/name"
      kubectl patch instanceset "${name}-mysql" -n "$namespace" --type=merge -p \
        "{\"metadata\":{\"labels\":{\"clusterversion.kubeblocks.io/name\":\"$NEW\"}}}"

      echo "📌 修改 InstanceSet.spec.template.metadata.labels 中的版本号"
      kubectl patch instanceset "${name}-mysql" -n "$namespace" --type=json -p \
        "[{\"op\":\"replace\",\"path\":\"/spec/template/metadata/labels/clusterversion.kubeblocks.io~1name\",\"value\":\"$NEW\"}]"
    else
      echo "⚠️ InstanceSet ${name}-mysql 不存在，跳过"
    fi

    echo "✅ $name 完成修改"
    echo "-------------------------------------------"
  done

  echo ""
done

echo "🎉 所有版本迁移完成"