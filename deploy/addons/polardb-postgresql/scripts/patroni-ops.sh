#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "ERROR: $*" >&2
  exit 1
}

require_target_instance() {
  : "${TARGET_INSTANCE:?TARGET_INSTANCE is required}"
  : "${KB_CLUSTER_NAME:?KB_CLUSTER_NAME is required}"
  : "${KB_COMP_NAME:?KB_COMP_NAME is required}"
  : "${KB_COMP_HEADLESS_SVC_NAME:?KB_COMP_HEADLESS_SVC_NAME is required}"

  case "${TARGET_INSTANCE}" in
    "${KB_CLUSTER_NAME}-${KB_COMP_NAME}-"*) ;;
    *) die "TARGET_INSTANCE ${TARGET_INSTANCE} does not belong to ${KB_CLUSTER_NAME}/${KB_COMP_NAME}" ;;
  esac
}

patroni_field() {
  local field="$1"
  curl --fail --silent --show-error --max-time 5 "${PATRONI_ENDPOINT}/patroni" 2>/dev/null |
    python3 -c "import json, sys; print(json.load(sys.stdin).get('${field}', ''))" 2>/dev/null || true
}

assert_not_primary() {
  local role
  role="$(patroni_field role)"
  case "${role}" in
    replica|standby_leader) ;;
    master|primary|leader) die "refusing to operate on primary instance ${TARGET_INSTANCE}" ;;
    *) die "cannot verify a safe Patroni role for ${TARGET_INSTANCE}: ${role:-empty}" ;;
  esac
}

wait_for_replica() {
  local deadline now role state
  deadline=$(( $(date +%s) + ${WAIT_SECONDS:-900} ))
  while true; do
    role="$(patroni_field role)"
    state="$(patroni_field state)"
    if [ "${role}" = "replica" ] && [ "${state}" = "running" ]; then
      echo "${TARGET_INSTANCE} is a running Patroni replica"
      return 0
    fi
    now=$(date +%s)
    if [ "${now}" -ge "${deadline}" ]; then
      die "timed out waiting for ${TARGET_INSTANCE} to rejoin as a replica (role=${role:-empty}, state=${state:-empty})"
    fi
    sleep 5
  done
}

require_target_instance
PATRONI_ENDPOINT="http://${TARGET_INSTANCE}.${KB_COMP_HEADLESS_SVC_NAME}:8008"
assert_not_primary

case "${PATRONI_OPERATION:?PATRONI_OPERATION is required}" in
  restart)
    # Patroni can close the HTTP connection while it restarts its own process.
    # The replica/running poll below is the authoritative operation result.
    curl --fail --silent --show-error --max-time 10 -X POST "${PATRONI_ENDPOINT}/restart" >/dev/null || true
    ;;
  reinitialize)
    [ "${CONFIRM_REBUILD:-}" = "true" ] || die "CONFIRM_REBUILD=true is required"
    # Reinitialize can also briefly take Patroni's HTTP endpoint down.
    curl --fail --silent --show-error --max-time 10 -X POST "${PATRONI_ENDPOINT}/reinitialize" >/dev/null || true
    ;;
  *)
    die "unsupported Patroni operation: ${PATRONI_OPERATION}"
    ;;
esac

wait_for_replica
