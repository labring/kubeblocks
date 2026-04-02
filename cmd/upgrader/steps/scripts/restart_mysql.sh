#!/bin/bash

kubectl get cluster -A -o json | jq -r '
  .items[] | select(
    ((.spec.clusterDefinitionRef // "") | test("mysql"; "i"))
    or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("mysql"; "i"))
  ) | "\(.metadata.namespace) \(.metadata.name)"' | while read cluster_ns redis_name; do

  echo "🔍 处理 Cluster: $redis_name (命名空间: $cluster_ns)"

  # 删除 mysql Pod
  kubectl get pods -n "$cluster_ns" -l apps.kubeblocks.io/component-name=mysql --no-headers 2>/dev/null | grep "$redis_name" | awk '{print $1}' | while read pod_name; do
    echo "🧩 正在处理 Pod: $pod_name (命名空间: $cluster_ns)"

    # 清空 finalizers
    kubectl patch pod "$pod_name" -n "$cluster_ns" -p '{"metadata":{"finalizers":[]}}' --type=merge >/dev/null 2>&1

    # 强制删除 Pod
    kubectl delete pod "$pod_name" -n "$cluster_ns" --force --grace-period=0 >/dev/null 2>&1

    echo "✅ 已强制删除 Pod: $pod_name"
  done

 done
