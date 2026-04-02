#!/usr/bin/env bash
  set -euo pipefail

  ns="kb-system"
  deploy="kubeblocks"

  tools_img="ghcr.io/labring/kubeblocks-tools:v0.9.3"
  manager_img="ghcr.io/labring/kubeblocks:v0.9.3"

  out="kubeblocks-patched-$(date +%Y%m%d-%H%M%S).yaml"
  tmp="$(mktemp)"

  kubectl -n "$ns" get deploy "$deploy" -o yaml > "$tmp"

  kubectl patch --local -f "$tmp" -o yaml --type=strategic -p "
  spec:
    replicas: 1
    template:
      spec:
        containers:
        - name: manager
          image: $manager_img
          env:
          - name: KUBEBLOCKS_TOOLS_IMAGE
            value: $tools_img
        initContainers:
        - name: tools
          image: $tools_img
  " > "$out"

  kubectl apply -f "$out"

  echo "Applied and saved to: $out"

  kubectl -n kb-system patch deploy kubeblocks-dataprotection --type='json' -p='[
    {"op":"replace","path":"/spec/replicas","value":1}
  ]'
