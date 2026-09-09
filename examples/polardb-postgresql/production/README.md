# PolarDB PostgreSQL Production Operations

This directory contains controls that are intentionally not enabled by the
development addon defaults.

## Release Lock

1. Copy `values-release.example.yaml` into the release change record.
2. Replace every placeholder with a digest from the approved release workflow.
3. Render the Helm chart and assert every first-party image uses `@sha256:`.
4. Apply the same values file to the manager and data protection controller.

`kubeblocks-dataprotection` is part of the release set because backup and
restore are not safe when it can change independently. `datasafed` is an
external dependency; approve and pin its digest separately.

## Backup, Alerting, And Restore Drill

Install the addon with `addon-values-production.example.yaml` after the target
BackupRepo reports `Ready`. This enables daily base backups with 30-day
retention and the PolarDB PostgreSQL PrometheusRule.

For VictoriaMetrics, merge
`monitoring/victoria-metrics-kube-state-metrics-values.example.yaml` into the
target monitoring release before enabling the addon rule. It exposes the Backup
and BackupPolicy state metrics required by the backup alerts.

Install the periodic restore drill in the source Cluster namespace:

```bash
NAMESPACE=<namespace> \
CLUSTER=<cluster> \
TOOLS_IMAGE='ghcr.io/<owner>/kubeblocks-tools@sha256:<digest>' \
bash scripts/install-restore-drill.sh
```

The job restores the latest completed backup into a timestamped temporary
Cluster, requires it to become `Running`, then deletes only that temporary
Cluster. It never deletes the source Cluster or its backup. Set the schedule
after the daily backup window and route `PolarDBPostgreSQLRestoreDrillFailed`
to the database on-call channel.

## Physical Fencing

Run fencing from an automation runner outside the Kubernetes failure domain.
`scripts/fence-redfish.sh` implements BMC power-off through Redfish and refuses
to run unless `CONFIRM_FENCE` exactly matches the immutable target ID. It is
appropriate only when the BMC, storage fencing, and network isolation design
are independently available.

For `storage-split`, the platform integration must both power-fence the old
primary and detach or revoke its storage access before a replacement primary
accepts writes. A BMC-only Redfish call is insufficient for shared storage.
Follow [RUNBOOK.md](RUNBOOK.md) for the entry gates, exact fault-injection
order, evidence requirements, and recovery acceptance criteria.

## Acceptance Criteria

- At least three schedulable nodes across failure domains and a quorum-safe
  Patroni topology.
- Storage is remote or replicated; local hostpath/LVM does not satisfy node
  failure recovery requirements.
- BackupRepo uses storage outside the database node failure domain.
- Alert delivery is tested for no primary, replication lag, backup failure, and
  restore drill failure.
- For each `node-lost`, `network-partition`, and `storage-split` scenario,
  collect the BMC or provider fence evidence, Patroni leader history, RPO/RTO,
  and a successful rejoin/rebuild record.
