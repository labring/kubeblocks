# PolarDB Mongo Compatibility (Version B)

This example implements Version B: `FerretDB` exposes a MongoDB-compatible
protocol while PostgreSQL with the DocumentDB extension stores the data.

For production, replace the backend with an HA PostgreSQL-compatible platform
that supports the DocumentDB extension. When that platform is PolarDB-PG, first
validate extension compatibility against the target kernel version.

## Create

```bash
kubectl apply -f examples/polardb-mongo-compat/install.yaml

kubectl wait --for=condition=Ready cluster/polardb-mongo-compat-sample --timeout=30m
```

To use another Cluster name, update the Cluster, PDB, account Secret, and
`kubectl wait` object names in `install.yaml`. Frontend-to-backend addressing is
injected through `serviceVarRef`; no backend Service name needs manual changes.

## Connect

```bash
USER=$(kubectl get secret polardb-mongo-compat-sample-documentdb-account-ferretdb -o jsonpath='{.data.username}' | base64 -d)
PASS=$(kubectl get secret polardb-mongo-compat-sample-documentdb-account-ferretdb -o jsonpath='{.data.password}' | base64 -d)
HOST=polardb-mongo-compat-sample-ferretdb
PORT=27017
DB=kbcompat

mongosh "mongodb://${USER}:${PASS}@${HOST}:${PORT}/${DB}?authMechanism=SCRAM-SHA-256&authSource=postgres"
```

## Verify

Run the bundled smoke Job:

```bash
kubectl apply -f examples/polardb-mongo-compat/smoke-mongosh-job.yaml
kubectl wait --for=condition=complete job/polardb-mongo-compat-smoke --timeout=5m
kubectl logs job/polardb-mongo-compat-smoke
```

## Native KubeBlocks Backup and Restore

`install.yaml` registers the `mongodump` ActionSet and BackupPolicyTemplate.
Once the Cluster is created, KubeBlocks creates
`polardb-mongo-compat-sample-documentdb-backup-policy`. Backups therefore
produce standard `Backup` resources and write to the default `BackupRepo`.

### kbcli

```bash
kbcli cluster list-backup-policy polardb-mongo-compat-sample -n default
kbcli cluster backup polardb-mongo-compat-sample \
  --name polardb-mongo-compat-kbcli-backup \
  --method mongodump \
  --policy polardb-mongo-compat-sample-documentdb-backup-policy \
  --deletion-policy Retain \
  -n default

kbcli cluster list-backups --name=polardb-mongo-compat-kbcli-backup -n default
kbcli cluster restore polardb-mongo-compat-cli \
  --backup polardb-mongo-compat-kbcli-backup \
  -n default

kubectl wait --for=condition=Ready cluster/polardb-mongo-compat-cli --timeout=30m
kubectl get restore -n default
```

### YAML

```bash
kubectl get backuppolicy polardb-mongo-compat-sample-documentdb-backup-policy
kubectl apply -f examples/polardb-mongo-compat/backup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed backup/polardb-mongo-compat-sample-backup --timeout=30m
kubectl get backup polardb-mongo-compat-sample-backup
```

Restore targets a new Cluster rather than importing over the source Cluster. The
workflow creates a standard `Restore` resource and a post-ready Job after both
DocumentDB and FerretDB are ready:

```bash
kubectl apply -f examples/polardb-mongo-compat/restore.yaml
kubectl wait --for=condition=Ready cluster/polardb-mongo-compat-restored --timeout=30m
kubectl get restore -n default
```

The logical backup uses `mongodump --archive --gzip`; restore uses
`mongorestore --drop`. The restore Job uses the target Cluster's own `ferretdb`
system account and does not depend on the source Cluster Secret. It overwrites
namespaces contained in the dump, so apply `restore.yaml` only to a new, empty
target Cluster.

This is the native KubeBlocks `Backup`/`Restore` lifecycle, but logical MongoDB
exports do not provide transaction-consistent snapshots, incremental backups,
PITR, oplog semantics, or `rs.*` semantics.

You can also verify the API manually:

```javascript
db.version()
db.demo.insertOne({ name: "polardb-mongo-compat", mode: "ferretdb" })
db.demo.find().pretty()
db.demo.updateOne({ name: "polardb-mongo-compat" }, { $set: { checked: true } })
db.demo.countDocuments()
```

Anonymous connections can perform handshake commands such as `ping` and `hello`.
Business reads and writes require credentials with `SCRAM-SHA-256`.

## Scope

- The frontend listens on `27017` and is exposed through a MongoDB URI.
- The PostgreSQL backend listens on `5432` and is used only by FerretDB inside
  the Cluster.
- The example fixes Version B orchestration and connectivity. It does not claim
  full native MongoDB semantics.
- The example backend is a single DocumentDB-compatible PostgreSQL replica for
  validating orchestration, connectivity, and compatibility boundaries. A
  production deployment requires an HA PostgreSQL-compatible backend.
- Verified compatibility includes CRUD, indexes, simple aggregation, `distinct`,
  `mongosh`, PyMongo, the Go driver, and `mongodump`/`mongorestore`.
- `createUser` with the built-in `readWrite` role was validated. KubeBlocks
  account lifecycle commands are not an account-management interface here.
- Unsupported or non-native guarantees include `rs.*`, transaction commit,
  `createRole`, `grantRolesToUser`, `getRoles`, and full sharding semantics.
- See `docs/polardb-mongo-ferretdb-technical-solution-zh.md` for the Chinese
  architecture document and
  `docs/polardb-mongo-ferretdb-deployment-test-guide-zh.md` for the Chinese
  deployment and test guide.
