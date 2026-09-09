# Legacy PostgreSQL Patroni HA on KubeBlocks 0.9

> **Warning:** This addon runs `apecloud/spilo`, which is ordinary PostgreSQL
> with Patroni. Despite its historical `polardb-postgresql` name, it is not the
> PolarDB-PG database engine and must not be presented as PolarDB-PG. For the
> real-engine local-instance integration, see
> [PolarDB-PG for KubeBlocks 0.9](polardb-pg-real-engine-zh.md).

This integration manages a Patroni-based PostgreSQL deployment through
KubeBlocks 0.9 native Ops. Patroni remains the database HA authority; KubeBlocks
drives lifecycle operations and disables lorry's second HA loop for the
`polardb-postgresql` builtin handler.

For the implementation architecture, production boundary, verification record,
and merged PR traceability, see the [Chinese technical solution](polardb-postgresql-ha-technical-solution-zh.md).

## Supported Operations

- Planned switchover. Automatic candidate selection accepts only a healthy
  replica with Patroni lag at or below `ha.switchover.maxLagBytes`; the default
  is `0` bytes.
- Logical fencing after a planned switchover, rejoin, and rebuild through
  KubeBlocks `HorizontalScaling` and `RebuildInstance` OpsRequests.
- `pg-basebackup` backup and restore drills through the KubeBlocks data
  protection controller.

The logical fence removes the previous primary Pod only after a successful
Patroni switchover. It is not a node power fence, network fence, storage detach,
or STONITH implementation. Automatic failover across a node or control-plane
partition requires an environment-specific fencing provider and a tested
runbook. Do not claim split-brain protection for that failure mode until those
controls are in place.

## Release Images

Use one release tag for all KubeBlocks control-plane artifacts:

```text
ghcr.io/<owner>/kubeblocks:<release>
ghcr.io/<owner>/kubeblocks-tools:<release>
ghcr.io/<owner>/kubeblocks-datascript:<release>
ghcr.io/<owner>/kubeblocks-dataprotection:<release>
ghcr.io/<owner>/kubeblocks-addon-charts:<release>
```

The `kubeblocks-addon-charts` image contains the `polardb-postgresql` addon
Chart. The older `kubeblocks-charts` package remains for pre-release test
installations and is not the formal release source.
`.github/workflows/release-image.yml` builds and pushes all five first-party images for a
GitHub release; `.github/workflows/publish-ghcr-images.yml` publishes matching
commit images after a default-branch merge. A release tag is an operational
immutability contract: publish it once, never republish it, and record its
registry digest in the release and deployment change record. GHCR does not make
tags immutable by default. Production deployments must select a released tag,
then deploy each image as `repository@sha256:<digest>`; `latest` and per-commit
images are for development or CI only.

If GHCR packages are private, grant the cluster pull access before installation:

```bash
kubectl -n kb-system create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io \
  --docker-username='<github-user>' \
  --docker-password='<read-packages-token>'
kubectl -n kb-system patch serviceaccount kubeblocks-addon-installer \
  --type merge -p '{"imagePullSecrets":[{"name":"ghcr-pull"}]}'
```

## Install

Create a release values file from
`examples/polardb-postgresql/production/values-release.example.yaml`. Replace
every placeholder with the digest recorded by the approved release workflow.
The file pins the manager, tools, datascript, data protection controller, addon
charts, and the independently approved `datasafed` backup dependency.
The production addon values must also pin the `spilo`, `pgbouncer`, and
`postgres-exporter` runtime images. One `spilo` digest applies only to the
matching `serviceVersion`; use a separate approved values record for each
PostgreSQL version.

```bash
RELEASE_VALUES=examples/polardb-postgresql/production/values-release.yaml

helm upgrade --install kubeblocks deploy/helm -n kb-system --create-namespace \
  --values "${RELEASE_VALUES}" \
  --set image.imagePullSecrets[0].name=ghcr-pull

kubectl get addon polardb-postgresql
kbcli addon enable polardb-postgresql
kubectl wait --for=jsonpath='{.status.phase}'=Enabled addon/polardb-postgresql --timeout=10m
kubectl get componentdefinition polardb-postgresql-ha-v1
```

Use `examples/polardb-postgresql/cluster.yaml` to create a two-replica Cluster.
The Cluster must reach `Running` before accepting traffic.

For production, install the addon with
`examples/polardb-postgresql/production/addon-values-production.example.yaml`
only after the BackupRepo is `Ready` and alert routing has been tested. This
enables the daily base-backup schedule, 30-day retention, and PolarDB PostgreSQL
PrometheusRule. Install the weekly restore drill with
`examples/polardb-postgresql/production/scripts/install-restore-drill.sh` in
each source Cluster namespace.

Use [the production HA runbook](../examples/polardb-postgresql/production/RUNBOOK.md)
for physical fencing, fault injection, and acceptance evidence. Those procedures
require a multi-node environment with an infrastructure-specific fence provider;
they cannot be validated on a single-node local-storage Kubernetes cluster.

## Verification

Run the complete disposable drill only after a BackupRepo is `Ready` and the
data protection controller has a pullable `DATASAFED_IMAGE`:

```bash
NAMESPACE=kb-polardb-pg-e2e \
CLUSTER=polardb-pg-e2e \
WITH_REBUILD=true \
WITH_BACKUP=true \
make test-polardb-postgresql-ha
```

The drill verifies a planned switchover, demotion of the former primary before
logical fencing, rejoin, rebuild, backup, restore into a temporary Cluster, and
cleanup of the temporary restore Cluster and backup.

## Upgrade And Rollback

`ComponentDefinition` is immutable in KubeBlocks 0.9. The addon therefore uses
`polardb-postgresql-ha-v1` and definition-scoped resources with the same
versioned prefix. Helm retains them so existing Clusters are not changed by an
addon release.

For an incompatible HA implementation, publish a new release with a new
`ha.componentDefinition.name` such as `polardb-postgresql-ha-v2`. New Clusters
may use v2 only after its addon checks pass. Existing v1 Clusters remain on v1;
migrate them through a planned data migration or backup/restore procedure, not
by changing `componentDef` in place. Remove retained v1 resources only after no
Cluster, backup policy, or restore workflow references them.

For a manager rollback, restore the exact preceding manager, tools, datascript,
data protection, and charts image digests together. Confirm the Addon is
`Enabled`, its ComponentDefinition is `Available`, and a disposable drill passes
before resuming production changes.

Run the release-lock and fencing safeguard checks before opening the release
change:

```bash
make test-polardb-postgresql-production-manifests
```
