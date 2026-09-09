set -euo pipefail

export PATH="${PATH}:${DP_DATASAFED_BIN_PATH}:${PG_BIN}"
export DATASAFED_BACKEND_BASE_PATH="${DP_BACKUP_BASE_PATH}"

data_root="${DATA_DIR:?DATA_DIR must be set}"
data_dir="${data_root}/primary_datadir"
shared_dir="${data_root}/shared_datadir"
archive="/${DP_BACKUP_NAME:?DP_BACKUP_NAME must be set}.tar.zst"

if [[ ! -x "${PG_BIN}/pg_ctl" ]]; then
  echo "the pinned runtime does not expose pg_ctl at ${PG_BIN}" >&2
  exit 1
fi

# The preparation Job runs before the restored Component starts. Do not retain
# data from a previous failed attempt, and do not restore into the local demo
# replica directory: that replica is rebuilt only when an operator requests it.
rm -rf "${data_dir}" "${shared_dir}" "${data_root}/replica_datadir"*
mkdir -p "${data_root}"

datasafed pull -d zstd-fastest "${archive}" - | tar -xpf - -C "${data_root}"
chown -R postgres:postgres "${data_dir}" "${shared_dir}"

test -s "${data_dir}/PG_VERSION"
test -s "${data_dir}/postgresql.conf"
test -s "${shared_dir}/global/pg_control"

# Fail before the Component is created if the archive is not a PolarDB-PG
# physical base backup. This guards against wiring a generic PostgreSQL backup
# to this ActionSet by mistake.
grep -Eq '^[[:space:]]*polar_datadir[[:space:]]*=' "${data_dir}/postgresql.conf"
