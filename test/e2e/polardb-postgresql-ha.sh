#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

NAMESPACE="${NAMESPACE:-kb-polardb-pg-e2e}"
CLUSTER="${CLUSTER:-polardb-pg-e2e}"
COMPONENT="${COMPONENT:-postgresql}"
COMPONENT_DEFINITION="${COMPONENT_DEFINITION:-polardb-postgresql-ha-v1}"
HELM_NAMESPACE="${HELM_NAMESPACE:-kb-system}"
HELM_RELEASE="${HELM_RELEASE:-kb-addon-polardb-postgresql}"
CHART_DIR="${CHART_DIR:-${ROOT_DIR}/deploy/addons/polardb-postgresql}"
REPLICAS="${REPLICAS:-2}"
STORAGE_CLASS="${STORAGE_CLASS:-}"
STORAGE_SIZE="${STORAGE_SIZE:-1Gi}"
CPU_REQUEST="${CPU_REQUEST:-100m}"
CPU_LIMIT="${CPU_LIMIT:-1}"
MEMORY_REQUEST="${MEMORY_REQUEST:-512Mi}"
MEMORY_LIMIT="${MEMORY_LIMIT:-2Gi}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-900}"
RECREATE="${RECREATE:-true}"
CLEANUP="${CLEANUP:-false}"
WITH_REBUILD="${WITH_REBUILD:-true}"
WITH_BACKUP="${WITH_BACKUP:-false}"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

wait_for() {
  local desc="$1"
  local cmd="$2"
  local start
  start="$(date +%s)"
  while true; do
    if eval "${cmd}"; then
      return 0
    fi
    if [ "$(( $(date +%s) - start ))" -ge "${TIMEOUT_SECONDS}" ]; then
      die "timeout waiting for ${desc}"
    fi
    sleep 5
  done
}

delete_cluster_if_needed() {
  if [ "${RECREATE}" != "true" ]; then
    return
  fi
  kubectl delete cluster -n "${NAMESPACE}" "${CLUSTER}" --ignore-not-found --wait=false
  wait_for "cluster ${CLUSTER} deleted" \
    "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' --ignore-not-found --no-headers 2>/dev/null | wc -l | tr -d ' ')\" = 0"
  wait_for "cluster ${CLUSTER} pods deleted" \
    "test \"\$(kubectl get pod -n '${NAMESPACE}' -l app.kubernetes.io/instance='${CLUSTER}' --no-headers 2>/dev/null | wc -l | tr -d ' ')\" = 0"
}

install_addon_chart() {
  log "installing ${HELM_RELEASE} from ${CHART_DIR}"
  helm upgrade --install "${HELM_RELEASE}" "${CHART_DIR}" -n "${HELM_NAMESPACE}" --create-namespace
  kubectl get componentdefinition "${COMPONENT_DEFINITION}" >/dev/null
}

create_cluster() {
  local storage_class_yaml=""
  if [ -n "${STORAGE_CLASS}" ]; then
    storage_class_yaml="        storageClassName: ${STORAGE_CLASS}"
  fi

  kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
  delete_cluster_if_needed

  log "creating ${NAMESPACE}/${CLUSTER}"
  kubectl apply -f - <<YAML
apiVersion: apps.kubeblocks.io/v1alpha1
kind: Cluster
metadata:
  name: ${CLUSTER}
  namespace: ${NAMESPACE}
spec:
  terminationPolicy: Delete
  componentSpecs:
  - name: ${COMPONENT}
    componentDef: ${COMPONENT_DEFINITION}
    replicas: ${REPLICAS}
    disableExporter: true
    affinity:
      podAntiAffinity: Preferred
      topologyKeys:
      - kubernetes.io/hostname
      tenancy: SharedNode
    resources:
      limits:
        cpu: "${CPU_LIMIT}"
        memory: ${MEMORY_LIMIT}
      requests:
        cpu: "${CPU_REQUEST}"
        memory: ${MEMORY_REQUEST}
    volumeClaimTemplates:
    - name: data
      spec:
        accessModes:
        - ReadWriteOnce
${storage_class_yaml}
        resources:
          requests:
            storage: ${STORAGE_SIZE}
YAML
}

wait_cluster_ready() {
  wait_for "cluster ${CLUSTER} Running" \
    "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running"
  wait_for "component ${COMPONENT} pods Ready" \
    "kubectl wait --for=condition=Ready pod -n '${NAMESPACE}' -l app.kubernetes.io/instance='${CLUSTER}',apps.kubeblocks.io/component-name='${COMPONENT}' --timeout=30s >/dev/null 2>&1"
}

run_drill() {
  log "running PolarDB PostgreSQL HA drill"
  NAMESPACE="${NAMESPACE}" \
  CLUSTER="${CLUSTER}" \
  COMPONENT="${COMPONENT}" \
  COMPONENT_DEFINITION="${COMPONENT_DEFINITION}" \
  TIMEOUT_SECONDS="${TIMEOUT_SECONDS}" \
  WITH_REBUILD="${WITH_REBUILD}" \
  WITH_BACKUP="${WITH_BACKUP}" \
  APPLY_BACKUP_POLICY_TEMPLATE=false \
  BACKUP_POLICY_TEMPLATE_FILE="${ROOT_DIR}/examples/polardb-postgresql/ha/backuppolicytemplate.yaml" \
    bash "${ROOT_DIR}/examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh"
}

cleanup() {
  if [ "${CLEANUP}" != "true" ]; then
    return
  fi
  log "cleaning up ${NAMESPACE}/${CLUSTER}"
  kubectl delete cluster -n "${NAMESPACE}" "${CLUSTER}" --ignore-not-found --wait=false
}

install_addon_chart
create_cluster
wait_cluster_ready
run_drill
cleanup

kubectl get cluster,pod,opsrequest,backuppolicy,backupschedule -n "${NAMESPACE}" -o wide || true
log "PolarDB PostgreSQL HA e2e completed"
