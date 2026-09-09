resolve_mpd_cluster
assert_shared_storage_cluster
assert_member "${TARGET_INSTANCE:?TARGET_INSTANCE is required}"
wait_until "MPDCluster to be Running before switchover" is_running

current_leader="$(leader_id)"
[ -n "${current_leader}" ] || fail "MPDCluster does not report a leaderInstanceId"
if [ "${current_leader}" = "${TARGET_INSTANCE}" ]; then
  echo "${TARGET_INSTANCE} is already the MPDCluster leader; switchover is idempotently complete"
  exit 0
fi

candidate_role="$(member_role "${TARGET_INSTANCE}")"
[ "$(printf '%s' "${candidate_role}" | tr '[:upper:]' '[:lower:]')" = "ro" ] || \
  fail "target ${TARGET_INSTANCE} must be a current RO instance, got role=${candidate_role:-empty}"

# switchRw is the official Stack Operator contract. The operator disables HA,
# asks Cluster Manager to switch, applies its storage lock workflow, then
# re-enables HA before returning the MPDCluster to Running.
mpd_annotate "switchRw=${TARGET_INSTANCE}"

is_switchover_complete() {
  [ "$(cluster_state)" = "Running" ] && [ "$(leader_id)" = "${TARGET_INSTANCE}" ]
}
wait_until "Cluster Manager switchover to ${TARGET_INSTANCE}" is_switchover_complete
echo "Cluster Manager promoted ${TARGET_INSTANCE} for ${MPD_NAMESPACE}/${MPD_CLUSTER}"
