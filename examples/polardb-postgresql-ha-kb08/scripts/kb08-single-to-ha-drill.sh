#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-polardb-pg-ha-kb08}"
CLUSTER="${CLUSTER:-pg-single}"
COMPONENT="${COMPONENT:-postgresql}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-1200}"
WITH_BACKUP="${WITH_BACKUP:-false}"
BACKUP_POLICY="${BACKUP_POLICY:-${CLUSTER}-polardb-pg-ha-backup-policy}"

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

pod_count() {
  kubectl get pod -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
    -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' '
}

role_pod() {
  local role="$1"
  kubectl get pod -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT},kubeblocks.io/role=${role}" \
    -o jsonpath='{.items[0].metadata.name}'
}

backup_role_selector() {
  kubectl get backuppolicy -n "${NAMESPACE}" "${BACKUP_POLICY}" \
    -o go-template='{{with .spec.target.podSelector.matchLabels}}{{if index . "kubeblocks.io/role"}}{{index . "kubeblocks.io/role"}}{{end}}{{end}}'
}

wait_for "single-replica Cluster Running" \
  "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running"
wait_for "one PostgreSQL Pod" "test \"\$(pod_count)\" = 1"
wait_for "BackupPolicy ${BACKUP_POLICY}" \
  "kubectl get backuppolicy -n '${NAMESPACE}' '${BACKUP_POLICY}' >/dev/null 2>&1"
kubectl wait -n "${NAMESPACE}" --for=condition=Ready pod \
  -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
  --timeout="${TIMEOUT_SECONDS}s"

[ -n "$(role_pod primary)" ] || die "single replica is not labeled primary"
[ -z "$(backup_role_selector)" ] || die "single-replica backup policy still selects a secondary"

if [ "${WITH_BACKUP}" = true ]; then
  backup="${CLUSTER}-single-$(date +%s)"
  kubectl apply -f - <<YAML
apiVersion: dataprotection.kubeblocks.io/v1alpha1
kind: Backup
metadata:
  name: ${backup}
  namespace: ${NAMESPACE}
spec:
  backupMethod: pg-basebackup
  backupPolicyName: ${BACKUP_POLICY}
  deletionPolicy: Delete
YAML
  wait_for "Backup ${backup} Completed" \
    "test \"\$(kubectl get backup -n '${NAMESPACE}' '${backup}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Completed"
fi

kubectl patch cluster -n "${NAMESPACE}" "${CLUSTER}" --type=json \
  -p='[{"op":"replace","path":"/spec/componentSpecs/0/replicas","value":2}]'
wait_for "two PostgreSQL Pods" "test \"\$(pod_count)\" = 2"
kubectl wait -n "${NAMESPACE}" --for=condition=Ready pod \
  -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
  --timeout="${TIMEOUT_SECONDS}s"
wait_for "cluster ${CLUSTER} Running after scale-out" \
  "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running"
wait_for "secondary backup selector" "test \"\$(backup_role_selector)\" = secondary"

old_primary="$(role_pod primary)"
[ -n "${old_primary}" ] || die "could not find primary after scale-out"
[ -n "$(role_pod secondary)" ] || die "could not find secondary after scale-out"

ops="${CLUSTER}-switchover-$(date +%s)"
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
wait_for "OpsRequest ${ops} Succeed" \
  "test \"\$(kubectl get opsrequest -n '${NAMESPACE}' '${ops}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Succeed"
wait_for "cluster ${CLUSTER} Running after switchover" \
  "test \"\$(kubectl get cluster -n '${NAMESPACE}' '${CLUSTER}' -o jsonpath='{.status.phase}' 2>/dev/null)\" = Running"

new_primary="$(role_pod primary)"
[ -n "${new_primary}" ] || die "could not find primary after switchover"
[ "${new_primary}" != "${old_primary}" ] || die "switchover did not change the primary"

kubectl get cluster,backuppolicy,backup,opsrequest,pod -n "${NAMESPACE}" -o wide
