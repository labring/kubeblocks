#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="${NAMESPACE:-polardb-stack-contract-test}"
MPD_CLUSTER="polardb-stack-contract-test"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-180}"

require_contract_crd() {
  kubectl get crd mpdclusters.mpd.polardb.aliyun.com >/dev/null || {
    echo "MPDCluster CRD is not installed; install it from the same official PolarDB Stack release first" >&2
    exit 1
  }
}

set_status() {
  local leader="$1"
  local rw_role="$2"
  local ro_role="$3"
  kubectl patch mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" --subresource=status \
    --type=merge -p "{\"status\":{\"clusterStatus\":\"Running\",\"leaderInstanceId\":\"${leader}\",\"dbInstanceStatus\":{\"rw-0\":{\"insId\":\"rw-0\",\"role\":\"${rw_role}\",\"currentState\":{\"state\":\"Running\"}},\"ro-1\":{\"insId\":\"ro-1\",\"role\":\"${ro_role}\",\"currentState\":{\"state\":\"Running\"}}}}}" >/dev/null
}

wait_for_annotation() {
  local key="$1"
  local expected="$2"
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while true; do
    local value
    value="$(kubectl get mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" \
      -o "jsonpath={.metadata.annotations['${key}']}")"
    if [[ "${value}" == "${expected}" ]]; then
      return 0
    fi
    (( SECONDS < deadline )) || {
      echo "timed out waiting for annotation ${key}=${expected}" >&2
      exit 1
    }
    sleep 1
  done
}

wait_for_op() {
  local name="$1"
  local expected="$2"
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while true; do
    local phase
    phase="$(kubectl get opsrequest "${name}" -n "${NAMESPACE}" -o jsonpath='{.status.phase}')"
    if [[ "${phase}" == "${expected}" ]]; then
      return 0
    fi
    if [[ "${phase}" == "Failed" || "${phase}" == "Succeed" ]]; then
      kubectl get opsrequest "${name}" -n "${NAMESPACE}" -o yaml >&2
      echo "unexpected phase for ${name}: ${phase}" >&2
      exit 1
    fi
    (( SECONDS < deadline )) || {
      kubectl get opsrequest "${name}" -n "${NAMESPACE}" -o yaml >&2
      echo "timed out waiting for ${name}" >&2
      exit 1
    }
    sleep 2
  done
}

apply_op() {
  local file="$1"
  local name="$2"
  kubectl delete opsrequest "${name}" -n "${NAMESPACE}" --ignore-not-found >/dev/null
  kubectl apply -f "${ROOT_DIR}/${file}" >/dev/null
}

require_contract_crd
kubectl apply -f "${ROOT_DIR}/contract-test-binding.yaml" >/dev/null
kubectl apply -f "${ROOT_DIR}/contract-test-mpdcluster.yaml" >/dev/null

binding_mode="$(kubectl get cluster "${MPD_CLUSTER}" -n "${NAMESPACE}" \
  -o jsonpath='{.metadata.annotations.polardb-pg\.kubeblocks\.io/control-plane}')"
[[ "${binding_mode}" == "contract-test-only" ]] || {
  echo "refusing to simulate status on a non-contract binding" >&2
  exit 1
}

set_status rw-0 RW RO
(
  wait_for_annotation switchRw ro-1
  set_status ro-1 RO RW
  kubectl annotate mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" switchRw- >/dev/null
) &
apply_op contract-test-switchover.yaml polardb-stack-contract-switchover
wait_for_op polardb-stack-contract-switchover Succeed

(
  wait_for_annotation restartIns rw-0
  kubectl annotate mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" restartIns- >/dev/null
) &
apply_op contract-test-rejoin.yaml polardb-stack-contract-rejoin
wait_for_op polardb-stack-contract-rejoin Succeed

(
  wait_for_annotation forceRebuild true
  kubectl annotate mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" forceRebuild- >/dev/null
) &
apply_op contract-test-rebuild.yaml polardb-stack-contract-rebuild
wait_for_op polardb-stack-contract-rebuild Succeed

apply_op contract-test-fence-reject.yaml polardb-stack-contract-fence-reject
wait_for_op polardb-stack-contract-fence-reject Failed

audit_annotation="$(kubectl get mpdcluster "${MPD_CLUSTER}" -n "${NAMESPACE}" \
  -o jsonpath='{.metadata.annotations.polardb-pg\.kubeblocks\.io/last-stonith}')"
[[ -z "${audit_annotation}" ]] || {
  echo "fencing reject test unexpectedly wrote a STONITH audit annotation" >&2
  exit 1
}

echo "PolarDB Stack Ops API-contract test passed (no Stack Operator or physical STONITH was exercised)"
