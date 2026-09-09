#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:?set NAMESPACE to the source Cluster namespace}"
CLUSTER="${CLUSTER:?set CLUSTER to the source Cluster name}"
COMPONENT="${COMPONENT:-polardb}"
BACKUP_METHOD="${BACKUP_METHOD:-polar-pg-basebackup}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-1800}"
CLEANUP="${CLEANUP:-false}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

trim_name() {
  printf '%s' "$1" | cut -c1-63 | sed 's/-$//'
}

wait_for_phase() {
  local resource="$1"
  local name="$2"
  local wanted="$3"
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  local phase

  while ((SECONDS < deadline)); do
    phase="$(kubectl get "$resource" "$name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [[ "$phase" == "$wanted" ]]; then
      return 0
    fi
    if [[ "$phase" == "Failed" ]]; then
      kubectl get "$resource" "$name" -n "$NAMESPACE" -o yaml >&2 || true
      die "$resource/$name failed"
    fi
    sleep 5
  done

  kubectl get "$resource" "$name" -n "$NAMESPACE" -o yaml >&2 || true
  die "timed out waiting for $resource/$name to become $wanted"
}

source_pod="$(kubectl get pod -n "$NAMESPACE" \
  -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$source_pod" ]] || die "no source PolarDB-PG Pod found"

run_id="$(date -u +%Y%m%d%H%M%S)"
backup="$(trim_name "${CLUSTER}-restore-drill-${run_id}")"
# KubeBlocks derives Service, BackupPolicy and BackupSchedule names by adding
# component suffixes to the restored Cluster name. Keep the restore Cluster
# independent of a potentially long source name so every derived object stays
# within the Kubernetes 63-character name and label limits.
restore_cluster="pgrd-${run_id}"
restore_ops="$(trim_name "${restore_cluster}-restore")"
backup_policy="${BACKUP_POLICY:-${CLUSTER}-${COMPONENT}-backup-policy}"

kubectl exec -n "$NAMESPACE" "$source_pod" -c polardb -- \
  psql -v ON_ERROR_STOP=1 -U postgres -d postgres \
  -c 'CREATE TABLE IF NOT EXISTS kubeblocks_restore_drill (run_id text PRIMARY KEY, written_at timestamptz NOT NULL DEFAULT now());' \
  -c "INSERT INTO kubeblocks_restore_drill(run_id) VALUES ('${run_id}');"

cat <<YAML | kubectl apply -f -
apiVersion: dataprotection.kubeblocks.io/v1alpha1
kind: Backup
metadata:
  name: ${backup}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/component: restore-drill
    app.kubernetes.io/instance: ${CLUSTER}
spec:
  backupPolicyName: ${backup_policy}
  backupMethod: ${BACKUP_METHOD}
  deletionPolicy: Retain
  retentionPeriod: 7d
YAML
wait_for_phase backup "$backup" Completed

cat <<YAML | kubectl apply -f -
apiVersion: apps.kubeblocks.io/v1alpha1
kind: OpsRequest
metadata:
  name: ${restore_ops}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/component: restore-drill
    app.kubernetes.io/instance: ${CLUSTER}
spec:
  clusterName: ${restore_cluster}
  type: Restore
  restore:
    backupName: ${backup}
    backupNamespace: ${NAMESPACE}
    volumeRestorePolicy: Parallel
    deferPostReadyUntilClusterRunning: true
YAML
wait_for_phase opsrequest "$restore_ops" Succeed
wait_for_phase cluster "$restore_cluster" Running

restore_pod="$(kubectl get pod -n "$NAMESPACE" \
  -l "app.kubernetes.io/instance=${restore_cluster},apps.kubeblocks.io/component-name=${COMPONENT}" \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$restore_pod" ]] || die "no restored PolarDB-PG Pod found"

version="$(kubectl exec -n "$NAMESPACE" "$restore_pod" -c polardb -- \
  psql -U postgres -d postgres -Atqc 'SELECT version();')"
[[ "$version" == *"(PolarDB "* ]] || die "restored Pod is not the PolarDB-PG engine: $version"

marker_count="$(kubectl exec -n "$NAMESPACE" "$restore_pod" -c polardb -- \
  psql -U postgres -d postgres -Atqc \
  "SELECT count(*) FROM kubeblocks_restore_drill WHERE run_id = '${run_id}';")"
[[ "$marker_count" == "1" ]] || die "restore marker is missing; expected 1, got ${marker_count}"

printf 'PolarDB-PG restore drill succeeded: backup=%s restoreCluster=%s pod=%s\n' \
  "$backup" "$restore_cluster" "$restore_pod"

if [[ "$CLEANUP" == "true" ]]; then
  kubectl delete cluster "$restore_cluster" -n "$NAMESPACE" --wait=true
  printf 'Deleted isolated restore Cluster %s; retained Backup %s for evidence.\n' \
    "$restore_cluster" "$backup"
fi
