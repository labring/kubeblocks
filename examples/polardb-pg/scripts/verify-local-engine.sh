#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-polardb-pg-real}"
CLUSTER="${CLUSTER:-polardb-pg-real}"
COMPONENT="${COMPONENT:-polardb}"

kubectl wait --for=condition=Ready "cluster/${CLUSTER}" -n "${NAMESPACE}" --timeout="${TIMEOUT:-15m}"

pod="$(kubectl get pod -n "${NAMESPACE}" \
  -l "app.kubernetes.io/instance=${CLUSTER},apps.kubeblocks.io/component-name=${COMPONENT}" \
  -o jsonpath='{.items[0].metadata.name}')"

if [[ -z "${pod}" ]]; then
  echo "no PolarDB-PG Pod found for ${NAMESPACE}/${CLUSTER}/${COMPONENT}" >&2
  exit 1
fi

version="$(kubectl exec -n "${NAMESPACE}" "${pod}" -c polardb -- \
  psql -U postgres -d postgres -Atqc 'SELECT version();')"

if [[ "${version}" != *"(PolarDB "* ]]; then
  echo "expected a PolarDB-PG banner, got: ${version}" >&2
  exit 1
fi

polar_settings="$(kubectl exec -n "${NAMESPACE}" "${pod}" -c polardb -- \
  psql -U postgres -d postgres -Atqc "SELECT count(*) FROM pg_settings WHERE name LIKE 'polar_%';")"

if [[ ! "${polar_settings}" =~ ^[1-9][0-9]*$ ]]; then
  echo "expected PolarDB-specific settings, got count: ${polar_settings}" >&2
  exit 1
fi

printf 'PolarDB-PG engine verified: pod=%s version=%s polar_settings=%s\n' \
  "${pod}" "${version}" "${polar_settings}"
