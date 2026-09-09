resolve_mpd_cluster
assert_shared_storage_cluster

[ "${CONFIRM_FORCE_REBUILD:?CONFIRM_FORCE_REBUILD is required}" = "true" ] || \
  fail "force rebuild is cluster-wide and destructive; set CONFIRM_FORCE_REBUILD=true"

# forceRebuild is the official Stack Operator operation. It is intentionally
# separate from restartIns because it recreates the shared-storage cluster,
# rather than only rejoining one member.
mpd_annotate "forceRebuild=true"

is_rebuild_complete() {
  [ "$(cluster_state)" = "Running" ] && [ -z "$(annotation_value forceRebuild)" ]
}
wait_until "Cluster Manager shared-storage rebuild" is_rebuild_complete
echo "Cluster Manager completed shared-storage rebuild for ${MPD_NAMESPACE}/${MPD_CLUSTER}"
