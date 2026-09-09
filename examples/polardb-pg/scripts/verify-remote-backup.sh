#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-polardb-pg-real}"
BACKUP="${BACKUP:-polardb-pg-local-basebackup}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-1800}"

deadline=$((SECONDS + TIMEOUT_SECONDS))
while true; do
  phase="$(kubectl get backup "${BACKUP}" -n "${NAMESPACE}" -o jsonpath='{.status.phase}')"
  case "${phase}" in
    Completed)
      printf 'PolarDB-PG remote backup completed: name=%s path=%s totalSize=%s\n' \
        "${BACKUP}" \
        "$(kubectl get backup "${BACKUP}" -n "${NAMESPACE}" -o jsonpath='{.status.path}')" \
        "$(kubectl get backup "${BACKUP}" -n "${NAMESPACE}" -o jsonpath='{.status.totalSize}')"
      exit 0
      ;;
    Failed)
      kubectl get backup "${BACKUP}" -n "${NAMESPACE}" -o yaml >&2
      echo "PolarDB-PG remote backup failed: ${NAMESPACE}/${BACKUP}" >&2
      exit 1
      ;;
  esac

  if (( SECONDS >= deadline )); then
    kubectl get backup "${BACKUP}" -n "${NAMESPACE}" -o yaml >&2
    echo "timed out waiting for PolarDB-PG remote backup: ${NAMESPACE}/${BACKUP}" >&2
    exit 1
  fi
  sleep 5
done
