# PolarDB Stack Ops Bridge

This directory binds a KubeBlocks `Cluster` to an existing official PolarDB
Stack `MPDCluster`. It is an operations and audit projection only:

- The official Stack Operator owns `MPDCluster`, its database Pods, shared
  storage, Cluster Manager and database services.
- KubeBlocks owns a zero-replica `control` component and creates `Custom`
  `OpsRequest` Jobs. It never creates a second database controller.
- `switchRw`, `restartIns`, and `forceRebuild` are written only to the official
  MPDCluster annotation interface. The official Stack Operator and Cluster
  Manager perform the engine and storage actions.

Do not point this bridge at `polardb-pg-local-v3`: the local-instance addon is
an engine validation runtime and is not a shared-storage MPDCluster.

## Prerequisites

1. Install the official PolarDB Stack Operator and create a healthy
   `MPDCluster` with `spec.dbClusterType: share`, a supported multipath/shared
   storage backend, Cluster Manager, one RW instance, and at least one RO
   instance.
2. The target namespace must contain the `MPDCluster`; use the same namespace
   for the binding Cluster unless a deliberate cross-namespace RBAC policy is
   installed.
3. Install the addon:

```bash
helm upgrade --install kb-addon-polardb-pg-stack-ops \
  deploy/addons/polardb-pg-stack-ops -n kb-system --create-namespace
```

4. Replace both `REPLACE_MPDCLUSTER_*` values in `binding-cluster.yaml`, then
   apply the binding and its least-privilege service account:

```bash
kubectl apply -f examples/polardb-pg-stack-ops/binding-rbac.yaml
kubectl apply -f examples/polardb-pg-stack-ops/binding-cluster.yaml
```

On KubeBlocks 0.9 a zero-replica binding is shown as `Stopped`, with no
database Pods. This is expected and Custom Ops remain schedulable because their
Jobs are independent of component replicas.

## Operations

List official instance IDs before applying an operation:

```bash
kubectl get mpdcluster REPLACE_MPDCLUSTER_NAME -n REPLACE_MPDCLUSTER_NAMESPACE \
  -o go-template='{{range $id, $instance := .status.dbInstanceStatus}}{{$id}}{{" role="}}{{$instance.role}}{{" state="}}{{$instance.currentState.state}}{{"\n"}}{{end}}'
```

- **Switchover:** set `TARGET_INSTANCE` to a currently healthy RO ID and apply
  `ops-switchover.yaml`. The Op succeeds only after the Stack reports
  `clusterStatus=Running` and `leaderInstanceId` equals that ID.
- **Rejoin:** set `TARGET_INSTANCE` in `ops-rejoin.yaml`. It maps to the
  official `restartIns` workflow. For an unrecoverable instance use rebuild,
  not repeated restart attempts.
- **Rebuild:** `ops-rebuild.yaml` maps to `forceRebuild=true`. It is cluster
  wide and destructive, so the explicit confirmation parameter is mandatory.
- **Fencing:** create the real physical provider Secret from
  `stonith-secret.example.yaml`, set the target in `ops-fence.yaml`, then
  apply it. The provider is required to return success only after a confirmed
  power/network/storage write fence. The Op then waits for Cluster Manager to
  return the MPDCluster to `Running`; if the fenced instance was RW, it also
  waits for a new leader.

Watch both operation planes:

```bash
kubectl get opsrequest -n polardb-stack-demo -w
kubectl get mpdcluster REPLACE_MPDCLUSTER_NAME -n REPLACE_MPDCLUSTER_NAMESPACE -w
```

The bridge has no Kubernetes-only fallback for fencing. A request fails closed
when the STONITH Secret is absent, the endpoint is unavailable, or it does not
confirm the fence.

## API contract regression

`contract-test-*.yaml` is a non-production regression fixture. It uses the
official `MPDCluster` CRD schema but no Stack Operator, Cluster Manager,
database Pod, shared disk or fencing provider. A test harness supplies the
status transitions after it observes the official annotations. It verifies only
that the KubeBlocks Custom Ops Job uses the least-privilege RBAC and the
`switchRw`/`restartIns`/`forceRebuild` API contract. It cannot establish engine
HA correctness, data consistency, or physical fencing.

After installing the bridge and the official `MPDCluster` CRD, run the fixture
locally with:

```bash
bash examples/polardb-pg-stack-ops/scripts/run-contract-test.sh
```

The script expects the fencing rejection case to fail. It never calls a real
STONITH provider and refuses to emulate status unless the binding has the
`control-plane=contract-test-only` annotation.
