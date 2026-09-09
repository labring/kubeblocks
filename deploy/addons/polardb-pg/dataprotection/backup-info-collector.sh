get_current_time() {
  "${PG_BIN}/psql" -U "${DP_DB_USER}" -h "${DP_DB_HOST}" -p "${DP_DB_PORT:-5432}" -d postgres -Atqc \
    "SELECT now() AT TIME ZONE 'UTC'"
}

stat_and_save_backup_info() {
  export PATH="${PATH}:${DP_DATASAFED_BIN_PATH}"
  export DATASAFED_BACKEND_BASE_PATH="${DP_BACKUP_BASE_PATH}"

  local start_time="$1"
  local stop_time="${2:-}"

  if [[ -z "${stop_time}" ]]; then
    stop_time="$(get_current_time)"
  fi

  start_time="$(date -d "${start_time}" -u '+%Y-%m-%dT%H:%M:%SZ')"
  stop_time="$(date -d "${stop_time}" -u '+%Y-%m-%dT%H:%M:%SZ')"
  local total_size
  total_size="$(datasafed stat / | awk '/TotalSize/ { print $2 }')"
  printf '{"totalSize":"%s","timeRange":{"start":"%s","end":"%s"}}\n' \
    "${total_size}" "${start_time}" "${stop_time}" >"${DP_BACKUP_INFO_FILE}"
}

handle_exit() {
  local exit_code=$?
  if [[ ${exit_code} -ne 0 ]]; then
    echo "PolarDB-PG base backup failed with exit code ${exit_code}" >&2
    touch "${DP_BACKUP_INFO_FILE}.exit"
    exit "${exit_code}"
  fi
}
