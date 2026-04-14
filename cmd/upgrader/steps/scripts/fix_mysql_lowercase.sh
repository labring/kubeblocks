#!/usr/bin/env bash
  set -euo pipefail

  if ! command -v jq >/dev/null 2>&1; then
    echo "jq is required"
    exit 1
  fi

  kubectl get cluster -A | grep mysql | awk '{print $1,$2}' | while read -r ns cluster; do
    cfg="${cluster}-mysql"

    lctn="$(kubectl -n "$ns" get configuration "$cfg" -o json 2>/dev/null | jq -r '
      .spec.configItemDetails[]
      | select(.name=="mysql-consensusset-config")
      | .configFileParams["my.cnf"].parameters.lower_case_table_names
      ' 2>/dev/null || true)"

    if [[ "$lctn" != "1" ]]; then
      continue
    fi

    cm="${cluster}-mysql-mysql-consensusset-config"
    data="$(kubectl -n "$ns" get cm "$cm" -o jsonpath='{.data.my\.cnf}' 2>/dev/null || true)"

    if [[ -z "$data" ]]; then
      echo "[MISSING CM OR my.cnf] $ns/$cm"
      continue
    fi

    found="$(printf '%s\n' "$data" | awk '
      BEGIN {in_section=0; found=0}
      /^[[:space:]]*\[mysqld\][[:space:]]*$/ {in_section=1; next}
      /^[[:space:]]*\[/ {in_section=0}
      in_section && $0 ~ /^[[:space:]]*lower_case_table_names[[:space:]]*=[[:space:]]*(1|"1")[[:space:]]*$/ {found=1}
      END {print found}
    ')"

    if [[ "$found" == "1" ]]; then
      echo "[OK] $ns/$cm has lower_case_table_names=1 in [mysqld]"
      continue
    fi

    echo "[MISSING] $ns/$cm missing lower_case_table_names=1 in [mysqld] -> patching"

    new_data="$(printf '%s\n' "$data" | awk '
      BEGIN {in_section=0; inserted=0}
      /^[[:space:]]*\[mysqld\][[:space:]]*$/ {in_section=1; print; next}
      /^[[:space:]]*\[/ {
        if (in_section && inserted==0) { print "lower_case_table_names=1"; inserted=1 }
        in_section=0
        print
        next
      }
      { print }
      END {
        if (in_section && inserted==0) { print "lower_case_table_names=1" }
      }
    ')"

    kubectl -n "$ns" patch cm "$cm" --type=json -p="[{
      \"op\":\"replace\",
      \"path\":\"/data/my.cnf\",
      \"value\":$(printf '%s' "$new_data" | jq -Rs .)
    }]"

    # 根据 InstanceSet 删除 MySQL Pods
    instancesets="$(kubectl -n "$ns" get instanceset \
      -l app.kubernetes.io/instance="$cluster",apps.kubeblocks.io/component-name=mysql \
      -o json | jq -r '.items[] | .metadata.name + "\t" + ( .spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",") )')"

    if [[ -z "$instancesets" ]]; then
      echo "[SKIP] no InstanceSet found for $ns/$cluster mysql"
      continue
    fi

    while IFS=$'\t' read -r isname selector; do
      if [[ -z "$selector" ]]; then
        echo "[SKIP] InstanceSet $ns/$isname has empty selector"
        continue
      fi
      pods="$(kubectl -n "$ns" get pod -l "$selector" -o name)"
      if [[ -z "$pods" ]]; then
        echo "[SKIP] no pods for $ns/$isname"
        continue
      fi
      echo "[RESTART] deleting pods for $ns/$isname"
      kubectl -n "$ns" delete $pods --force
    done <<< "$instancesets"
  done