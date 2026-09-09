#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-polardb-pg-ha-kb08}"
CLUSTER="${CLUSTER:-pg-ha}"
COMPONENT="${COMPONENT:-postgresql}"
COMPONENT_DEFINITION="${COMPONENT_DEFINITION:-polardb-pg-ha-v1}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-900}"
WITH_REJOIN="${WITH_REJOIN:-false}"
WITH_REBUILD="${WITH_REBUILD:-false}"
WITH_BACKUP="${WITH_BACKUP:-false}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

wait_for() {
  local description="$1"
  local command="$2"
  local start
  start="$(date +%s)"
  while true; do
    if eval "${command}"; then
      return 0
    fi
    if [ "$(( $(date +%s) - start ))" -ge "${TIMEOUT_SECONDS}" ]; then
      die "timeout waiting for ${description}"
    fi
    sleep 5
  done
}

wait_cluster_running() {
  wait_for "cluster ${CLUSTER} Running" \
    "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running"
}

wait_ops() {
  local ops="$1"
  local start phase
  start="$(date +%s)"
  while true; do
    phase="$(kubectl get opsrequest -n "${NAMESPACE}" "${ops}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "${phase}" in
      Succeed) return 0 ;;
      Failed) die "OpsRequest ${ops} failed" ;;
    esac
    if [ "$(( $(date +%s) - start ))" -ge "${TIMEOUT_SECONDS}" ]; then
      die "timeout waiting for OpsRequest ${ops}"
    fi
    sleep 5
  done
}

role_pod() {
  local role="$1"
  kubectl get pod -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT},kubeblocks.io/role=${role}" \
    -o jsonpath='{.items[0].metadata.name}'
}

verify_lorry_ha_disabled() {
  local pod handler value
  for pod in $(kubectl get pod -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\\n"}{end}'); do
    handler="$(kubectl exec -n "${NAMESPACE}" "${pod}" -c lorry -- printenv KB_BUILTIN_HANDLER 2>/dev/null || true)"
    value="$(kubectl exec -n "${NAMESPACE}" "${pod}" -c lorry -- printenv KB_ENABLE_HA 2>/dev/null || true)"
    [ "${handler}" = postgresql ] || die "${pod} has KB_BUILTIN_HANDLER=${handler:-unset}, expected postgresql"
    [ "${value}" = false ] || die "${pod} has KB_ENABLE_HA=${value:-unset}, expected false"
  done
}

apply_switchover() {
  local ops="${CLUSTER}-switchover-$(date +%s)"
  kubectl apply -f - <<YAML
apiVersion: apps.kubeblocks.io/v1alpha1
kind: OpsRequest
metadata:
  name: ${ops}
  namespace: ${NAMESPACE}
spec:
  clusterRef: ${CLUSTER}
  type: Switchover
  switchover:
    - componentName: ${COMPONENT}
      instanceName: "*"
YAML
  wait_ops "${ops}"
}

apply_custom_op() {
  local suffix="$1"
  local target="$2"
  local ops="${CLUSTER}-${suffix}-$(date +%s)"
  local confirmation=""
  if [ "${suffix}" = rebuild ]; then
    confirmation='        CONFIRM_REBUILD: "true"'
  fi
  kubectl apply -f - <<YAML
apiVersion: apps.kubeblocks.io/v1alpha1
kind: OpsRequest
metadata:
  name: ${ops}
  namespace: ${NAMESPACE}
spec:
  clusterRef: ${CLUSTER}
  type: Custom
  customSpec:
    componentName: ${COMPONENT}
    opsDefinitionRef: ${COMPONENT_DEFINITION}-${suffix}
    params:
      - TARGET_INSTANCE: ${target}
${confirmation}
YAML
  wait_ops "${ops}"
}

wait_cluster_running
verify_lorry_ha_disabled

old_primary="$(role_pod primary)"
[ -n "${old_primary}" ] || die "could not find the current primary"
apply_switchover
wait_cluster_running

new_primary="$(role_pod primary)"
[ -n "${new_primary}" ] || die "could not find the new primary"
[ "${new_primary}" != "${old_primary}" ] || die "switchover did not move the primary"

if [ "${WITH_REJOIN}" = true ]; then
  apply_custom_op rejoin "${old_primary}"
fi

if [ "${WITH_REBUILD}" = true ]; then
  replica="$(role_pod secondary)"
  [ -n "${replica}" ] || die "could not find a secondary to rebuild"
  apply_custom_op rebuild "${replica}"
fi

if [ "${WITH_BACKUP}" = true ]; then
  die "run the backup and restore manifests separately after verifying BackupRepo readiness"
fi

kubectl get cluster,pod,opsrequest -n "${NAMESPACE}" -o wide
