# PolarDB-X

PolarDB-X is a cloud-native distributed SQL database. This example adds a full logical backup method for PolarDB-X through the CN component by using the MySQL protocol.

## Backup Design

The backup method is `mysqldump`.

- It selects one CN pod from the PolarDB-X cluster.
- It connects through the generated KubeBlocks connection credential.
- It dumps only user databases and skips PolarDB-X/MySQL system schemas.
- It streams the SQL dump into the configured KubeBlocks `BackupRepo` through `datasafed`.

This is a full logical backup optimized for PolarDB-X compatibility. It does not provide a transaction-consistent snapshot, physical backup, incremental backup, continuous log backup, or PITR.

Restore is implemented as a `postReady` job that mounts the `BackupRepo`, decompresses the generated `${backupName}.sql.zst`, and imports it through the target CN endpoint.

This example has been verified with KubeBlocks 0.8.2 and 0.9.3 by creating a `Backup`, decompressing the generated `.sql.zst` file from the `BackupRepo`, restoring the SQL dump, and querying the restored table data.

## Prerequisites

Enable the PolarDB-X addon before creating a cluster.

For KubeBlocks 0.8:

```bash
kbcli addon enable polardbx --force
```

For KubeBlocks 0.9, use the 0.9 addon package. The bundled 0.8.1 addon may fail 0.9 admission validation.

```bash
kbcli addon upgrade polardbx --version 0.9.1 --force
```

Create a default backup repository if the environment does not already have one:

```bash
kubectl apply -f examples/polardbx/backuprepo.yaml
```

Install the PolarDB-X backup method:

```bash
kubectl apply -f examples/polardbx/backup-policy-template.yaml
```

## Create A Test Cluster

```bash
kubectl apply -f examples/polardbx/cluster.yaml
kubectl wait --for=condition=Ready cluster/polardbx-cluster --timeout=30m
```

For a memory-constrained KubeBlocks 0.9 environment, use `examples/polardbx/cluster-small.yaml` instead of `cluster.yaml`.

Create test data through the CN endpoint:

```bash
kubectl get secret polardbx-cluster-conn-credential \
  -o jsonpath='{.data.password}' | base64 -d
kubectl run polardbx-client --rm -it --restart=Never --image=docker.io/apecloud/mysql:8.0.30 -- \
  mysql -h polardbx-cluster-cn.default.svc -P3306 -upolardbx_root -p
```

Example SQL:

```sql
CREATE DATABASE IF NOT EXISTS kb_backup_test;
USE kb_backup_test;
CREATE TABLE IF NOT EXISTS t_backup(id INT PRIMARY KEY, v VARCHAR(64));
REPLACE INTO t_backup VALUES (1, 'kb-polardbx-backup');
SELECT * FROM t_backup;
```

## Backup

```bash
kubectl apply -f examples/polardbx/backup.yaml
kubectl wait --for=condition=Completed backup/polardbx-cluster-backup --timeout=30m
kubectl get backup polardbx-cluster-backup
```

## Restore

Default restore imports the SQL dump back through a CN pod selected from the target cluster.

```bash
kubectl apply -f examples/polardbx/restore.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed restore/polardbx-cluster-restore --timeout=30m
kubectl get restore polardbx-cluster-restore
```

To restore one dumped database into a different existing database name, set the restore env values on the `Restore` object:

```yaml
spec:
  env:
  - name: RESTORE_SOURCE_DATABASE
    value: kb_backup_test
  - name: RESTORE_DATABASE
    value: kb_restore_test
  - name: RESTORE_SKIP_CREATE_DATABASE
    value: "true"
```

`RESTORE_SKIP_CREATE_DATABASE=true` is useful for KubeBlocks 0.8 PolarDB-X clusters where `CREATE DATABASE` can return `ERR_CDC_GENERIC` while CDC is unstable. In that case, create or reuse the target database first, then let restore import the table DDL and data.

Validate restored data through CN:

```bash
kubectl run polardbx-client --rm -it --restart=Never --image=docker.io/apecloud/mysql:8.0.30 -- \
  mysql -h polardbx-cluster-cn.default.svc -P3306 -upolardbx_root -p \
  -e "SELECT COUNT(*), MIN(id), MAX(id), GROUP_CONCAT(v ORDER BY id) FROM kb_backup_test.t_backup;"
```

Automated volume-based restore is not applicable to this logical backup method.

## Verification Notes

- KubeBlocks 0.8.2: restored `polardbx-cluster-backup-08-showdb` into `kb_restore_08.t_backup`; validation returned `1, 1, 1, kb-polardbx-backup`.
- KubeBlocks 0.9.3: restored `polardbx-cluster-backup-09-showdb` into `kb_restore_verify_09.t_backup`; validation returned `1, 1, 1, kb09-polardbx-backup`.
