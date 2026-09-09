# PolarDB PostgreSQL Production HA Runbook

This runbook governs the controls outside KubeBlocks that are required before
claiming production HA. It applies to the `polardb-postgresql-ha-v1` addon and
does not turn a planned KubeBlocks switchover into automatic cross-site
failover.

## Entry Gates

Do not start an incident recovery or fault-injection drill until all gates pass.

1. The database has at least three schedulable Kubernetes nodes in independent
   failure domains. The database Pods, Patroni/DCS dependencies, monitoring,
   and backup path cannot share one failure domain.
2. Persistent volumes are remote or replicated. `openebs-hostpath`, local LVM,
   and any node-local BackupRepo fail this gate.
3. BackupRepo is `Ready`, its data is outside the database node failure domain,
   and the scheduled backup has produced a recent `Completed` backup.
4. The metrics backend and alert delivery path are both healthy. A
   `PrometheusRule` object alone is not evidence of delivery. The source
   Cluster must set `disableExporter: false`. For VictoriaMetrics, merge the
   provided kube-state-metrics custom-resource configuration before enabling
   the backup-alert rule.
5. Every running image is recorded as `repository@sha256:<digest>`, including
   KubeBlocks, dataprotection, addon charts, Spilo, PgBouncer, exporter, and
   datasafed.
6. The fencing runner is outside the Kubernetes failure domain and has a
   tested BMC/provider identity for every database node. Its credential is a
   protected secret, never a ConfigMap or a Pod environment variable.

Record the preflight output in the incident or drill ticket:

```bash
kubectl get nodes -L topology.kubernetes.io/zone
kubectl get backuprepo -A
kubectl get backuppolicy,backupschedule,backup -n <namespace>
kubectl get pod -n <namespace> -l app.kubernetes.io/instance=<cluster> -o wide
kubectl get prometheusrule -A
kubectl get cluster -n <namespace> <cluster> -o jsonpath='{.status.phase}{"\n"}'
```

## Monitoring And Backup Operations

Install the production addon values only after the remote BackupRepo and alert
routing are ready. Verify the alert engine evaluates the rule and send a
controlled test notification to the database on-call channel. The following
alerts must be delivered and acknowledged: no primary, replication lag, failed
backup, and failed restore drill.

The scheduled policy performs a daily base backup and retains 30 days in the
production example. Run the weekly restore CronJob after the backup window. It
restores the newest completed backup into `restore-drill-<timestamp>`, waits for
`Running`, and deletes only that temporary Cluster. A failed run is an incident:
preserve the Job and OpsRequest logs, then remove a temporary restore Cluster
only after the evidence has been collected.

```bash
kubectl get cronjob,job -n <namespace> -l app.kubernetes.io/component=restore-drill
kubectl logs -n <namespace> job/<restore-drill-job>
kubectl get opsrequest -n <namespace> -o wide
```

## Fencing Decision

For `node-lost`, `network-partition`, and `storage-split`, do not promote or
accept writes on a replacement primary until the old primary has been fenced.
The successful fence proof is both of the following:

1. The BMC/cloud provider confirms that the old node is powered off or isolated.
2. For `storage-split`, the storage platform also confirms that the old node has
   lost write access to every database volume.

`scripts/fence-redfish.sh` implements the BMC portion for Redfish systems. It
is intentionally an external-runner command. First validate target mapping
without a BMC call:

```bash
FENCE_TARGET=<redfish-system-id> \
REDFISH_ENDPOINT='https://<bmc>/redfish/v1/Systems' \
FENCE_REASON=node-lost \
CONFIRM_FENCE=<redfish-system-id> \
FENCE_DRY_RUN=true \
bash scripts/fence-redfish.sh
```

For a live drill, remove `FENCE_DRY_RUN`, provide `REDFISH_USERNAME` and a
readable `REDFISH_PASSWORD_FILE`, and keep TLS verification enabled with
`REDFISH_CA_BUNDLE`. `ALLOW_INSECURE_TLS=true` is an emergency-only exception
that requires an incident approval record.

## Fault Injection Procedures

Use a disposable multi-node environment. Never use `kubectl delete pod` as a
substitute for a physical node-loss, network-partition, or storage-split test.

### Node Lost

1. Capture the preflight evidence, Patroni leader identity, write timestamp,
   and current replication lag.
2. Power off the host of the active primary through the external fencing
   runner. Record the BMC task ID and `PowerState=Off` evidence.
3. Verify the old primary cannot serve its write endpoint. Observe replacement
   primary selection and measure RTO/RPO.
4. Repair the host, then use the KubeBlocks rejoin or rebuild operation. Verify
   it returns as a replica and that no divergent timeline remains.

### Network Partition

1. Isolate the active primary from Kubernetes API, Patroni peers, clients, and
   storage as prescribed by the network platform, while retaining an out-of-band
   BMC path.
2. Fence the isolated primary externally before allowing the survivor side to
   accept writes. Preserve the network policy/firewall change and BMC evidence.
3. Restore connectivity only after the old primary has been rejoined or rebuilt
   as a replica. Validate a single writable primary before ending the drill.

### Storage Split

1. Simulate loss of the primary's storage path using the storage provider's
   supported test mechanism. Do not corrupt data or detach a volume from a live
   production primary.
2. Revoke or detach the old primary's volume write access, then power-fence it.
   A BMC-only action is not sufficient for this scenario.
3. Verify the replacement primary writes only after both fence proofs are
   available. Rebuild the old node from a known-good replica or backup; do not
   force it to rejoin with uncertain storage history.

## Acceptance Record

Each scenario passes only when the record contains all of the following:

- UTC start/end, incident commander, target node, and approved change ID.
- Before/after Patroni member and leader history, KubeBlocks Cluster/OpsRequest
  status, and write endpoint checks proving one writable primary.
- Fence provider response, BMC power state, and, for storage split, storage
  access revocation evidence.
- Measured RPO/RTO against the approved objective, alert receipt/acknowledgment,
  and successful rejoin or rebuild evidence.
- A successful restore drill from the retained BackupRepo after the final
  scenario.

Any missing proof is a failed drill. Keep automatic failover disabled for that
failure mode until the gap is corrected and the scenario is rerun.
