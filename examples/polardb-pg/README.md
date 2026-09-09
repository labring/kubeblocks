# Real PolarDB-PG for KubeBlocks 0.9

This example uses the official `polardb/polardb_pg_local_instance:17` runtime,
pinned to a Linux/amd64 digest in the addon chart. A successful deployment must
report `PolarDB` in `SELECT version()`.

This is a single-instance localfs integration. It is suitable for verifying the
real PolarDB-PG engine and basic KubeBlocks lifecycle, but it is not a
shared-storage or production HA deployment.

The `polardb-pg-local-v3` definition also prepares an explicit
`pg_hba.conf` replication rule for a KubeBlocks `pg_basebackup` Job. Before
using the remote backup method outside a test cluster, set
`backup.replicationHbaCIDR` to the Kubernetes Pod CIDR and configure an
independently operated S3-compatible `BackupRepo`; the chart default
`0.0.0.0/0` is test-only.

```bash
helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \
  -n kb-system --create-namespace

kubectl apply -f examples/polardb-pg/cluster-local-test.yaml
kubectl wait --for=condition=Ready cluster/polardb-pg-real \
  -n polardb-pg-real --timeout=15m
```

Verify the engine identity:

```bash
POD=$(kubectl get pod -n polardb-pg-real \
  -l app.kubernetes.io/instance=polardb-pg-real,apps.kubeblocks.io/component-name=polardb \
  -o jsonpath='{.items[0].metadata.name}')

kubectl exec -n polardb-pg-real "$POD" -c polardb -- \
  psql -U postgres -d postgres -Atqc 'SELECT version();'
```

The output must contain `(PolarDB `; a plain `PostgreSQL` banner means the
deployment is not a valid PolarDB-PG engine verification.
