# PolarDB-PG Local-Instance Operations

This directory covers backup recovery, monitoring, and release controls for
the real `polardb-pg-local-v4` engine integration. It does not convert the
official local-instance image into PolarDB shared-storage HA. Use the separate
`polardb-pg-stack-ops` addon with a real PolarDB Stack Operator and Cluster
Manager for shared-storage operations.

## Backup And Restore Drill

Before enabling scheduled backups, create a `BackupRepo` that is independent
of the database node failure domain and wait until it is `Ready`. Install the
addon with the reviewed values file, replacing `REPLACE_POD_CIDR` with the
actual Pod CIDR:

```bash
helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \\
  -n kb-system --create-namespace \\
  -f examples/polardb-pg/production/addon-values-production.example.yaml
```

Run one isolated recovery before enabling the CronJob. The script writes a
unique row, creates a physical base backup, restores a new Cluster from the
backup snapshot, verifies the PolarDB banner and the row, and never changes
the source Cluster.

```bash
NAMESPACE=<source-namespace> \\
CLUSTER=<source-cluster> \\
CLEANUP=false \\
bash examples/polardb-pg/scripts/run-restore-drill.sh
```

After recording the result, install the weekly drill. It deletes only the
temporary restore Cluster and retains the drill Backup for seven days.

```bash
NAMESPACE=<source-namespace> \\
CLUSTER=<source-cluster> \\
SCHEDULE='30 3 * * 0' \\
bash examples/polardb-pg/scripts/install-restore-drill-cronjob.sh
```

## Monitoring And Alert Delivery

Merge `monitoring/victoria-metrics-kube-state-metrics-values.example.yaml`
into the VictoriaMetrics Helm release before enabling the addon `VMRule`.
The template alerts on unavailable backup policy, failed
backup, failed restore drill Job, and failed Stack OpsRequest. Verify both
rule evaluation and notification delivery with a controlled failed Job before
considering the alert path accepted.

## Release Gate

Build the signed release bundle with
`scripts/release/package-polardb-pg.sh`. Deploy only charts whose SHA256SUMS
and Sigstore bundles are verified, and keep the manager, data-protection,
datasafed, addon chart, engine, and Stack operations images pinned by digest.
