set -euo pipefail

export PATH="${PATH}:${DP_DATASAFED_BIN_PATH}:${PG_BIN}"
export DATASAFED_BACKEND_BASE_PATH="${DP_BACKUP_BASE_PATH}"
export PGPASSWORD="${DP_DB_PASSWORD}"

if [[ ! -x "${PG_BIN}/pg_basebackup" || ! -x "${PG_BIN}/psql" ]]; then
  echo "the pinned runtime does not expose the expected PolarDB-PG client binaries at ${PG_BIN}" >&2
  exit 1
fi

if [[ "$("${PG_BIN}/psql" -U "${DP_DB_USER}" -h "${DP_DB_HOST}" -p "${DP_DB_PORT:-5432}" -d postgres -Atqc 'SHOW polar_deploy_mode;')" != "OPEN_SOURCE" ]]; then
  echo "refusing to use this action set against a non-PolarDB-PG endpoint" >&2
  exit 1
fi

backup_dir="$(mktemp -d)"
cleanup_backup_dir() {
  local exit_code=$?
  rm -rf "${backup_dir}"
  return "${exit_code}"
}
trap 'cleanup_backup_dir; handle_exit' EXIT

start_time="$(get_current_time)"
# A PolarDB-PG local instance stores user data in shared_datadir, not only in
# PostgreSQL's primary data directory. --polardata is mandatory; a plain
# pg_basebackup can produce a bootable but empty database after recovery.
"${PG_BIN}/pg_basebackup" \
  -D "${backup_dir}/primary_datadir" \
  --polardata="${backup_dir}/shared_datadir" \
  -Pv -c fast -X stream \
  -h "${DP_DB_HOST}" -p "${DP_DB_PORT:-5432}" -U "${DP_DB_USER}" \
  && tar -C "${backup_dir}" -cpf - primary_datadir shared_datadir \
  | datasafed push -z zstd-fastest - "/${DP_BACKUP_NAME}.tar.zst"
stat_and_save_backup_info "${start_time}"
