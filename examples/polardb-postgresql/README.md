# Legacy PostgreSQL Patroni HA

> This directory runs ordinary PostgreSQL through Spilo and Patroni. It is not
> the PolarDB-PG database engine. Use
> [the real PolarDB-PG integration](../polardb-pg/README.md) when the engine
> itself must be PolarDB-PG.

This directory contains KubeBlocks 0.9 examples for running legacy PostgreSQL
with the KB-native Patroni HA workflow.

For the production installation, upgrade policy, image policy, and HA boundary,
read [PolarDB PostgreSQL HA for KubeBlocks 0.9](../../docs/polardb-postgresql-ha.md).

This branch includes an installable addon chart at
`deploy/addons/polardb-postgresql`. The KubeBlocks charts image packaging script
also packages it as `polardb-postgresql-0.9.3.tgz`, so KB 0.9 environments can
install the engine either directly with Helm or through the configured addon
chart image.

The current KB 0.9 integration expects a Patroni-based ComponentDefinition named
`polardb-postgresql-ha-v1`. The controller-side adapter in this branch disables
lorry's built-in HA loop for the `polardb-postgresql` builtin handler, so Patroni
remains the only database HA controller while KubeBlocks drives lifecycle Ops.

## Create a Cluster

```bash
helm upgrade --install kb-addon-polardb-postgresql \
  deploy/addons/polardb-postgresql -n kb-system --create-namespace

kubectl create namespace kb-polardb-pg
kubectl apply -f examples/polardb-postgresql/cluster.yaml
kubectl get cluster,pod -n kb-polardb-pg
```

The example creates a two-replica component named `postgresql` from
`polardb-postgresql-ha-v1`.

For a self-contained test manifest that creates the namespace and uses the
cluster default StorageClass, run:

```bash
kubectl apply --dry-run=client \
  -f examples/polardb-postgresql/cluster-ha-test.yaml
kubectl apply -f examples/polardb-postgresql/cluster-ha-test.yaml
```

For the production manifest and validation sequence, read the [Chinese
deployment and test guide](../../docs/polardb-postgresql-ha-deployment-test-guide-zh.md).

## Enable HA Backup Policy

The addon chart installs `polardb-postgresql-ha-v1-backup-policy-template`, which
registers `pg-basebackup` for PolarDB PostgreSQL clusters. If you are patching an
existing test environment without installing the chart, apply the standalone
template:

```bash
kubectl apply -f examples/polardb-postgresql/ha/backuppolicytemplate.yaml
```

## Run HA Drill

```bash
NAMESPACE=kb-polardb-pg CLUSTER=polardb-pg COMPONENT=postgresql \
  bash examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh
```

Run the full production HA closure, including rebuild and backup/restore drill:

```bash
WITH_REBUILD=true WITH_BACKUP=true APPLY_BACKUP_POLICY_TEMPLATE=true \
  bash examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh
```

Or run the disposable end-to-end wrapper on the current kube context:

```bash
WITH_BACKUP=true make test-polardb-postgresql-ha
```

For KB 0.9 test environments, verify that dataprotection uses pullable tool
images. In the verified test cluster, `DATASAFED_IMAGE` was set to
`apecloud-registry.cn-zhangjiakou.cr.aliyuncs.com/apecloud/datasafed:0.2.0`.
