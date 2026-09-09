# PolarDB PostgreSQL HA Ops for KubeBlocks 0.9

This directory contains the KB-native operations used to run the production HA
workflow for PolarDB PostgreSQL on KubeBlocks 0.9.

The operational model is:

- Switchover: `OpsRequest.spec.type=Switchover`.
- Fencing: `OpsRequest.spec.type=HorizontalScaling` with `scaleIn.onlineInstancesToOffline`.
- Rejoin: `OpsRequest.spec.type=HorizontalScaling` with `scaleOut.offlineInstancesToOnline`.
- Rebuild: `OpsRequest.spec.type=RebuildInstance`.
- Backup: `OpsRequest.spec.type=Backup`.
- Restore drill: `OpsRequest.spec.type=Restore` to create a drill cluster from a completed backup.

For the Patroni-based `polardb-postgresql-ha-v1` component definition, lorry must not
run a second HA controller. The manager image in this branch injects
`KB_ENABLE_HA=false` for the `polardb-postgresql` builtin handler by default. The
component definition should also set:

```yaml
env:
- name: PGDATA
  value: /home/postgres/pgdata/pgroot/data
- name: KB_ENABLE_HA
  value: "false"
```

Use `scripts/patch-polardb-pg-componentdefinition.sh` to patch an existing KB 0.9
test ComponentDefinition with those defaults and with a switchover action that
selects the healthy replica with the smallest Patroni lag when `instanceName: "*"`
is used.

The installable addon chart includes `polardb-postgresql-ha-v1-basebackup` and a
default `BackupPolicyTemplate` for `polardb-postgresql-ha-v1`. The standalone
`backuppolicytemplate.yaml` in this directory is kept for patched KB 0.9 test
environments that already have the PostgreSQL addon `postgres-basebackup`
ActionSet installed.

The example OpsRequests and drill script use the default test names:

- namespace: `kb-polardb-pg`
- cluster: `polardb-pg`
- component: `postgresql`

Adjust names before applying them in another environment.

Run the drill against a disposable test cluster:

```bash
NAMESPACE=kb-polardb-pg CLUSTER=polardb-pg COMPONENT=postgresql \
  bash examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh
```

Optional stages are disabled by default:

```bash
WITH_REBUILD=true WITH_BACKUP=true \
  bash examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh
```

To let the drill apply and wait for the BackupPolicyTemplate before the
backup/restore phase:

```bash
WITH_BACKUP=true APPLY_BACKUP_POLICY_TEMPLATE=true \
  bash examples/polardb-postgresql/ha/scripts/kb09-polardb-pg-ha-drill.sh
```

For backup/restore drills, ensure the KB dataprotection controller uses a
pullable `DATASAFED_IMAGE`. In the KB 0.9 test environment this was set to
`apecloud-registry.cn-zhangjiakou.cr.aliyuncs.com/apecloud/datasafed:0.2.0`.
The drill creates temporary backups with `deletionPolicy: Delete` and cleans up
the restore cluster by default. The restore cluster name defaults to
`restore-<timestamp>` to stay below Kubernetes label length limits; set
`RESTORE_CLUSTER` only when you need a fixed drill name.
