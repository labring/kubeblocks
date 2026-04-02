#!/bin/bash
kubectl get cluster -A -o json | jq -r '
  .items[] | select(
    ((.spec.clusterDefinitionRef // "") | test("redis"; "i"))
    or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("redis"; "i"))
  ) | select(.status.phase != "Stopped")
  | "\(.metadata.namespace) \(.metadata.name)"' | while read ns name; do
  yaml=$(kubectl get cluster "$name" -n "$ns" -o yaml)
  echo "$yaml" | grep -q "redis-sentinel"
  if [ $? -ne 0 ]; then
    echo "❌ No redis-sentinel found → Cluster: $name, Namespace: $ns"
  fi
done
