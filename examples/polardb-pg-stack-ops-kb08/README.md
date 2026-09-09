# PolarDB-PG Stack Ops for KubeBlocks 0.8

This addon binds a zero-replica KubeBlocks control Cluster to an existing
official PolarDB Stack `MPDCluster`. Stack Operator and Cluster Manager remain
the only owners of database Pods, shared storage, services, and leader election.

Install the addon, replace the two MPDCluster placeholders in
`binding-cluster.yaml`, then apply the binding RBAC and Cluster:

```bash
helm upgrade --install kb-addon-polardb-pg-stack-ops \
  deploy/addons/polardb-pg-stack-ops -n kb-system --create-namespace
kubectl apply -f examples/polardb-pg-stack-ops-kb08/binding-rbac.yaml
kubectl apply -f examples/polardb-pg-stack-ops-kb08/binding-cluster.yaml
```

If the MPDCluster is in a different namespace, replace
`REPLACE_MPDCLUSTER_NAMESPACE` and additionally apply
`binding-rbac-mpdcluster-namespace.yaml`. The binding service account is kept
read/patch-only against that one resource type.

The four Custom Ops map to the official `switchRw`, `restartIns`,
`forceRebuild`, and physical STONITH contracts. They are not substitutes for an
official shared-storage deployment or a verified fencing provider.

See `docs/polardb-pg-stack-ops-kb08-technical-solution-zh.md` and
`docs/polardb-pg-stack-ops-kb08-deployment-test-guide-zh.md` for boundaries,
production prerequisites, YAML operations, and contract testing.
