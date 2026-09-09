set -eu

KUBECTL=kubectl
MPD_RESOURCE=mpdcluster.mpd.polardb.aliyun.com
WAIT_SECONDS="${WAIT_SECONDS:-900}"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

validate_dns_name() {
  local value="$1"
  local label="$2"
  printf '%s\n' "${value}" | grep -Eq '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$' || \
    fail "${label} must be a DNS-compatible name"
}

resolve_mpd_cluster() {
  command -v "${KUBECTL}" >/dev/null 2>&1 || fail "kubectl is not available in the KubeBlocks Ops image"

  MPD_CLUSTER="$("${KUBECTL}" -n "${KB_OPS_NAMESPACE}" get cluster "${KB_CLUSTER_NAME}" \
    -o jsonpath='{.metadata.annotations.polardb-pg\.kubeblocks\.io/mpdcluster}')"
  MPD_NAMESPACE="$("${KUBECTL}" -n "${KB_OPS_NAMESPACE}" get cluster "${KB_CLUSTER_NAME}" \
    -o jsonpath='{.metadata.annotations.polardb-pg\.kubeblocks\.io/mpdcluster-namespace}')"
  MPD_NAMESPACE="${MPD_NAMESPACE:-${KB_OPS_NAMESPACE}}"

  [ -n "${MPD_CLUSTER}" ] || fail "binding Cluster is missing polardb-pg.kubeblocks.io/mpdcluster"
  validate_dns_name "${MPD_CLUSTER}" "MPDCluster name"
  validate_dns_name "${MPD_NAMESPACE}" "MPDCluster namespace"
  "${KUBECTL}" -n "${MPD_NAMESPACE}" get "${MPD_RESOURCE}" "${MPD_CLUSTER}" >/dev/null
}

mpd_jsonpath() {
  "${KUBECTL}" -n "${MPD_NAMESPACE}" get "${MPD_RESOURCE}" "${MPD_CLUSTER}" -o "jsonpath=$1"
}

mpd_annotate() {
  "${KUBECTL}" -n "${MPD_NAMESPACE}" annotate "${MPD_RESOURCE}" "${MPD_CLUSTER}" "$@" --overwrite
}

assert_shared_storage_cluster() {
  [ "$(mpd_jsonpath '{.spec.dbClusterType}')" = "share" ] || \
    fail "${MPD_NAMESPACE}/${MPD_CLUSTER} is not an official shared-storage MPDCluster"
}

cluster_state() {
  mpd_jsonpath '{.status.clusterStatus}'
}

leader_id() {
  mpd_jsonpath '{.status.leaderInstanceId}'
}

assert_member() {
  local target="$1"
  validate_dns_name "${target}" "target instance ID"
  [ "$(mpd_jsonpath "{.status.dbInstanceStatus['${target}'].insId}")" = "${target}" ] || \
    fail "instance ${target} is not reported by MPDCluster.status.dbInstanceStatus"
}

member_role() {
  local target="$1"
  mpd_jsonpath "{.status.dbInstanceStatus['${target}'].role}"
}

annotation_value() {
  local key="$1"
  mpd_jsonpath "{.metadata.annotations['${key}']}"
}

wait_until() {
  local description="$1"
  shift
  local deadline=$(( $(date +%s) + WAIT_SECONDS ))
  until "$@"; do
    if [ "$(date +%s)" -ge "${deadline}" ]; then
      fail "timed out waiting for ${description}; clusterState=$(cluster_state), leader=$(leader_id)"
    fi
    sleep 5
  done
}

is_running() {
  [ "$(cluster_state)" = "Running" ]
}
