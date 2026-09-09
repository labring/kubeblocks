#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELM="${HELM:-helm}"
RELEASE_VERSION="${RELEASE_VERSION:-0.3.3}"

rendered="$(mktemp)"
release_root="$(mktemp -d)"
trap 'rm -f "$rendered"; rm -rf "$release_root"' EXIT

bash -n \
  "${ROOT_DIR}/deploy/addons/polardb-pg/dataprotection/pg-basebackup-backup.sh" \
  "${ROOT_DIR}/deploy/addons/polardb-pg/dataprotection/pg-basebackup-restore.sh" \
  "${ROOT_DIR}/examples/polardb-pg/scripts/run-restore-drill.sh" \
  "${ROOT_DIR}/examples/polardb-pg/scripts/install-restore-drill-cronjob.sh" \
  "${ROOT_DIR}/scripts/release/package-polardb-pg.sh" \
  "${ROOT_DIR}/scripts/release/verify-polardb-pg-release.sh"

"${HELM}" lint "${ROOT_DIR}/deploy/addons/polardb-pg"
"${HELM}" lint "${ROOT_DIR}/deploy/addons/polardb-pg-stack-ops"

"${HELM}" template polardb-pg "${ROOT_DIR}/deploy/addons/polardb-pg" \
  --set vmRule.enabled=true \
  --set prometheusRule.enabled=true >"${rendered}"

grep -F -- '--polardata="${backup_dir}/shared_datadir"' "${rendered}" >/dev/null
grep -F 'targetVolumes:' "${rendered}" >/dev/null
grep -F 'kind: PrometheusRule' "${rendered}" >/dev/null
grep -F 'kind: VMRule' "${rendered}" >/dev/null
grep -F 'ops_definition=~"polardb-pg-stack-(switchover|rejoin|rebuild|fence)"' "${rendered}" >/dev/null

monitoring_values="${ROOT_DIR}/examples/polardb-pg/production/monitoring/victoria-metrics-kube-state-metrics-values.example.yaml"
grep -F 'resources: [backups, backuppolicies]' "${monitoring_values}" >/dev/null
grep -F 'resources: [opsrequests]' "${monitoring_values}" >/dev/null
grep -F 'ops_definition: [spec, custom, opsDefinitionName]' "${monitoring_values}" >/dev/null
grep -F 'namespace: [metadata, namespace]' "${monitoring_values}" >/dev/null

OUTPUT_DIR="${release_root}/polardb-pg-v${RELEASE_VERSION}" \
  RELEASE_VERSION="${RELEASE_VERSION}" \
  "${ROOT_DIR}/scripts/release/package-polardb-pg.sh" >/dev/null
"${ROOT_DIR}/scripts/release/verify-polardb-pg-release.sh" \
  "${release_root}/polardb-pg-v${RELEASE_VERSION}" >/dev/null
test -f "${release_root}/polardb-pg-v${RELEASE_VERSION}/polardb-pg-production-assets-${RELEASE_VERSION}.tgz"

printf 'PolarDB-PG production manifest checks passed.\n'
