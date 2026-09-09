resolve_mpd_cluster
assert_shared_storage_cluster
assert_member "${TARGET_INSTANCE:?TARGET_INSTANCE is required}"
wait_until "MPDCluster to be Running before rejoin" is_running

# restartIns is the official Stack Operator path. Cluster Manager owns the
# engine restart and member rejoin; the KubeBlocks Job never changes PG data or
# starts an alternative HA controller.
mpd_annotate "restartIns=${TARGET_INSTANCE}"

is_rejoin_complete() {
  [ "$(cluster_state)" = "Running" ] && [ -z "$(annotation_value restartIns)" ]
}
wait_until "Cluster Manager rejoin of ${TARGET_INSTANCE}" is_rejoin_complete
echo "Cluster Manager completed restart/rejoin for ${TARGET_INSTANCE}"
