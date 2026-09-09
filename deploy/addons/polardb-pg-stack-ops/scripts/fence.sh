resolve_mpd_cluster
assert_shared_storage_cluster
assert_member "${TARGET_INSTANCE:?TARGET_INSTANCE is required}"
wait_until "MPDCluster to be Running before fencing" is_running

reason="${FENCE_REASON:?FENCE_REASON is required}"
printf '%s\n' "${reason}" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9 .,:/_-]{0,119}$' || \
  fail "FENCE_REASON contains unsupported characters"
[ -n "${STONITH_ENDPOINT:-}" ] && [ -n "${STONITH_TOKEN:-}" ] || \
  fail "STONITH endpoint/token secret is missing or empty"

leader_before="$(leader_id)"
payload="{\"cluster\":\"${MPD_CLUSTER}\",\"namespace\":\"${MPD_NAMESPACE}\",\"targetInstance\":\"${TARGET_INSTANCE}\",\"reason\":\"${reason}\",\"opsRequest\":\"${KB_OPS_NAMESPACE}/${KB_OPS_NAME}\"}"

# The endpoint must return success only after the infrastructure provider has
# confirmed power-off, network isolation, or storage write fencing. Curl does
# not accept a best-effort acknowledgement as a successful KubeBlocks Op.
curl --fail --silent --show-error --connect-timeout 10 --max-time 300 \
  --request POST "${STONITH_ENDPOINT}" \
  --header "Authorization: Bearer ${STONITH_TOKEN}" \
  --header 'Content-Type: application/json' \
  --data "${payload}" >/dev/null

mpd_annotate "polardb-pg.kubeblocks.io/last-stonith=${KB_OPS_NAMESPACE}.${KB_OPS_NAME}"

is_fence_recovery_complete() {
  [ "$(cluster_state)" = "Running" ] || return 1
  if [ "${leader_before}" = "${TARGET_INSTANCE}" ]; then
    [ "$(leader_id)" != "${TARGET_INSTANCE}" ]
  fi
}
wait_until "Cluster Manager recovery after fencing ${TARGET_INSTANCE}" is_fence_recovery_complete
echo "physical fencing and Cluster Manager recovery completed for ${TARGET_INSTANCE}"
