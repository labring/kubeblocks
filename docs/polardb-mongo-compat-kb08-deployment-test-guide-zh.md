# KubeBlocks 0.8 MongoDB 兼容版部署与测试指南

本指南部署的是 `polardb-mongo-compat`：FerretDB + PostgreSQL DocumentDB。它只承诺文档数据库的 MongoDB 协议/驱动兼容边界，不是原生 MongoDB 副本集或 Alibaba Cloud PolarDB for MySQL document database。

## 前置条件

- 已安装 KubeBlocks `0.8.2` 和一个 `Ready` 的 BackupRepo。
- 集群可拉取 `docker.io/postgres:17`、`ghcr.io/ferretdb/ferretdb`、`docker.io/mongo:7.0` 和 mongo tools 镜像。
- 以下命令在仓库根目录执行；示例命名空间是 `polardb-kb08-e2e`。生产请替换为独立业务命名空间和资源规格。

DocumentDB 首次启动会安装扩展并创建 `documentdb_api` schema，耗时明显长于 PostgreSQL 开放 TCP 端口的时间。addon 的 startup/readiness probe 会等待该 schema 存在，因此在这段初始化期间 Pod 可能是 `Running` 但尚未 `Ready`；不要据此缩短或移除探针。

生产环境必须将 `images.mongoTools` 覆盖为受控仓库中的 Linux/AMD64 或多架构 digest，例如 `registry.example.com/mongo@sha256:...`。`mongodump` 与 `mongorestore` 的 Backup/Restore Job 会在工作节点拉取该镜像，不能依赖 Docker Hub 的临时可达性或开发机导入的单一架构镜像。

## 安装 addon 与创建实例

```bash
helm template kb-addon-polardb-mongo-compat deploy/addons/polardb-mongo-compat | kubectl apply -f -
kubectl create namespace polardb-kb08-e2e
kubectl apply -f examples/polardb-mongo-compat-kb08/cluster.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/polardb-mongo-kb08 -n polardb-kb08-e2e --timeout=10m
kubectl apply -f examples/polardb-mongo-compat-kb08/pdb.yaml
```

KubeBlocks 0.8 的 direct ComponentDefinition Cluster 不适合用 `kbcli cluster create` 从任意原始 YAML 创建；实例创建使用 `kubectl apply`，生命周期和恢复使用 KBCLI。

连接服务为 `polardb-mongo-kb08-ferretdb:27017`，账号 Secret 为 `polardb-mongo-kb08-documentdb-account-ferretdb`。生产连接必须通过受控网络路径并启用适用的 TLS；示例未提供公网暴露配置。

## 写入、备份和恢复

```bash
kubectl apply -f examples/polardb-mongo-compat-kb08/smoke-source.yaml
kubectl wait --for=condition=complete job/polardb-mongo-kb08-smoke-source -n polardb-kb08-e2e --timeout=5m
kubectl logs job/polardb-mongo-kb08-smoke-source -n polardb-kb08-e2e

kubectl apply -f examples/polardb-mongo-compat-kb08/backup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed backup/polardb-mongo-kb08-backup-v2 -n polardb-kb08-e2e --timeout=10m
kubectl get backup polardb-mongo-kb08-backup-v2 -n polardb-kb08-e2e

kubectl apply -f examples/polardb-mongo-compat-kb08/restore-cluster.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/polardb-mongo-kb08-restore-v2 -n polardb-kb08-e2e --timeout=10m
kubectl apply -f examples/polardb-mongo-compat-kb08/restore.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed restore/polardb-mongo-kb08-restore-v2-from-backup -n polardb-kb08-e2e --timeout=10m
kubectl apply -f examples/polardb-mongo-compat-kb08/smoke-restore.yaml
kubectl wait --for=condition=complete job/polardb-mongo-kb08-smoke-restore-v2 -n polardb-kb08-e2e --timeout=5m
kubectl logs job/polardb-mongo-kb08-smoke-restore-v2 -n polardb-kb08-e2e
```

每次重新备份或恢复时都要改 `Backup`、`Cluster`、`Restore` 和 Job 名称，避免覆盖历史演练记录。

恢复名称可以超过 Kubernetes label value 的 63 字符限制。控制器会把稳定哈希写入 Job label，并把完整 Restore 名称放在 annotation 中用于回调。可使用以下长名称样例做回归验证：

```bash
kubectl apply -f examples/polardb-mongo-compat-kb08/restore-long-name-cluster.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/polardb-mongo-kb08-restore-long-v1 -n polardb-kb08-e2e --timeout=10m
kubectl apply -f examples/polardb-mongo-compat-kb08/restore-long-name.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed restore/polardb-mongo-kb08-restore-name-that-exceeds-the-kubernetes-label-value-limit-v1 -n polardb-kb08-e2e --timeout=10m
kubectl create -o name -f examples/polardb-mongo-compat-kb08/smoke-long-restore.yaml
```

## 生命周期操作

KBCLI 路径：

```bash
kbcli cluster hscale polardb-mongo-kb08 --components=ferretdb --replicas=2 --auto-approve --name=polardb-mongo-kb08-hscale-v1 -n polardb-kb08-e2e
kbcli cluster restart polardb-mongo-kb08 --components=ferretdb --auto-approve --name=polardb-mongo-kb08-restart-v1 -n polardb-kb08-e2e
kbcli cluster stop polardb-mongo-kb08 --auto-approve --name=polardb-mongo-kb08-stop-v1 -n polardb-kb08-e2e
kbcli cluster start polardb-mongo-kb08 --name=polardb-mongo-kb08-start-v1 -n polardb-kb08-e2e
```

原始 YAML 路径：

```bash
kubectl create -f examples/polardb-mongo-compat-kb08/hscale-ferretdb.yaml
kubectl create -f examples/polardb-mongo-compat-kb08/restart-ferretdb.yaml
kubectl create -f examples/polardb-mongo-compat-kb08/stop.yaml
kubectl create -f examples/polardb-mongo-compat-kb08/start.yaml
```

上述操作 YAML 使用 `generateName`，因此必须用 `kubectl create`，不能使用 `kubectl apply`。每次操作后使用 `kbcli cluster describe-ops <name> -n polardb-kb08-e2e` 或 `kubectl get opsrequest -n polardb-kb08-e2e` 确认 `Succeed`。

停止/启动后，执行可重复的只读验证：

```bash
kubectl create -o name -f examples/polardb-mongo-compat-kb08/smoke-read.yaml
kubectl logs job/<returned-job-name> -n polardb-kb08-e2e
```

预期结果包含 `count=2`、按 `qty` 降序的 `7,6`、聚合 `total=13` 和 `maxScore=15`，同时保留 `type_qty` 索引。

## 已在 192.168.10.85 验证的结果

测试环境为 KB `0.8.2`、Kubernetes `1.28.15`、单节点 `openebs-hostpath`。2026-09-07 已完成：实例创建、CRUD、普通索引、排序、简单聚合、`mongodump` 备份、目标实例恢复、恢复后读取、FerretDB 1 到 2 副本扩容、滚动重启、停止、启动及停止后数据读取。KBCLI 与原始 YAML 两条操作入口均验证为 `Succeed`。恢复验证结果为两条文档，`alpha` 聚合总量 `13`、最大分数 `15`；长 Restore 名称也完成恢复并保留 `type_qty` 索引。

未验收或不承诺：原生副本集/分片/oplog/change stream、MongoDB 角色管理与 Admin 命令全集、完整事务和聚合兼容性、后端 DocumentDB 多副本 HA、卷扩容，以及跨节点故障演练。
