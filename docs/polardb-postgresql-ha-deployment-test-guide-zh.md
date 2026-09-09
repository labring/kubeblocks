---
title: KubeBlocks 0.9 真实 PolarDB-PG 部署与测试指南
description: 在 KubeBlocks 0.9 中部署、验证并测试真实 PolarDB-PG 17.11.1.0 内核。
keywords: kubeblocks polardb pg postgresql deployment test engine
---

# KubeBlocks 0.9 真实 PolarDB-PG 部署与测试指南

## 1. 目的、结论与边界

本指南部署的是 PolarDB 开源项目的官方运行时：

```text
docker.io/polardb/polardb_pg_local_instance:17
```

本仓库将 Linux/amd64 manifest 固定为：

```text
docker.io/polardb/polardb_pg_local_instance@sha256:5e455bbb9a13e05d4c62ac1def1db776758b0180c4c2191c8a7f79c4f27884a4
```

测试集群已实测返回以下标识，并检测到 162 个 `polar_*` 专有内核参数：

```text
PostgreSQL 17.11 (PolarDB 17.11.1.0 build unknown) on x86_64-linux-gnu
polardb_version = PolarDB V17.11.1.0 (open)
polar_deploy_mode = OPEN_SOURCE
```

这证明运行的是**真实 PolarDB-PG 内核**，不是普通 PostgreSQL、Spilo 或 Patroni。

:::caution

当前 Addon 使用官方 `local_instance` 运行时，一个 KubeBlocks Pod 配一个本地
`ReadWriteOnce` 数据卷。它适用于真实内核验证、基本数据库功能和 KubeBlocks
生命周期测试，**不是生产 HA 部署**。

旧的 `polardb-postgresql` Addon 使用 `apecloud/spilo` 和 Patroni，实质为普通
PostgreSQL 流复制实现。旧的 switchover、fencing、rejoin、backup/restore 文档和
OpsRequest 只适用于该遗留方案，不能证明或实现 PolarDB-PG 的共享存储 HA。

:::

真实 PolarDB-PG 的生产 HA 需要独立的共享存储拓扑和官方 PolarDB 控制面适配，
详见第 9 节。

## 2. 前提条件

- Kubernetes 节点为 Linux/amd64。
- 已部署 KubeBlocks 0.9.x，且 `ComponentDefinition` 和 `ComponentVersion` CRD 可用。
- 集群策略允许官方镜像的 `privileged: true` 和 `SYS_PTRACE` 能力。
- 存在默认或显式指定的可用 `ReadWriteOnce` StorageClass。
- 节点可拉取 Docker Hub 官方 PolarDB-PG 镜像。

部署前检查：

```bash
kubectl get crd componentdefinitions.apps.kubeblocks.io
kubectl get nodes -o wide
kubectl get storageclass
kubectl auth can-i create componentdefinitions.apps.kubeblocks.io
```

对于单节点测试集群，使用默认 StorageClass 即可。`openebs-hostpath` 等节点本地
卷仅适合本指南的功能测试，不能用于生产 HA。

## 3. 获取包含真实引擎 Addon 的源码

本文档所在分支已包含真实引擎 Addon、示例清单和验证脚本：

```bash
git clone --branch docs/polardb-postgresql-ha-solution \
  https://github.com/wallyxjh/kubeblocks.git
cd kubeblocks

test -f deploy/addons/polardb-pg/Chart.yaml
test -f examples/polardb-pg/cluster-local-test.yaml
test -x examples/polardb-pg/scripts/verify-local-engine.sh
```

| 资源 | 值 |
| --- | --- |
| Helm release | `kb-addon-polardb-pg` |
| ComponentDefinition | `polardb-pg-local-v3` |
| ComponentVersion | `polardb-pg-local-v3-version` |
| 测试 Namespace | `polardb-pg-real` |
| 测试 Cluster | `polardb-pg-real` |
| 数据库容器 | `polardb` |

KubeBlocks 0.9 的 ComponentDefinition 不可变。若变更真实引擎版本、镜像 digest、
挂载方式或运行时安全上下文，必须创建新的定义名，例如
`polardb-pg-local-v4`，不能原地修改 `polardb-pg-local-v3`。

## 4. 使用 Helm 安装真实 PolarDB-PG Addon

```bash
helm lint deploy/addons/polardb-pg

helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \
  --namespace kb-system \
  --create-namespace \
  --wait --timeout 10m

kubectl get componentdefinition polardb-pg-local-v3
kubectl get componentversion polardb-pg-local-v3-version
kubectl get componentdefinition polardb-pg-local-v3 \
  -o jsonpath='{.spec.runtime.containers[0].image}{"\n"}'
```

最后一条命令必须输出固定 digest，而不是可变 tag：

```text
docker.io/polardb/polardb_pg_local_instance@sha256:5e455bbb9a13e05d4c62ac1def1db776758b0180c4c2191c8a7f79c4f27884a4
```

本地 Chart 直接以 Helm 安装，因此不使用 `kbcli addon enable`。`kbcli` 可以用于
查看已安装的 KubeBlocks 和业务 Cluster；Addon 安装和镜像 digest 固定以本节 Helm
渲染结果为准。

## 5. 使用 YAML 创建真实 PolarDB-PG

仓库提供可直接使用的单实例测试清单：

```bash
kubectl apply --dry-run=server \
  -f examples/polardb-pg/cluster-local-test.yaml
kubectl apply -f examples/polardb-pg/cluster-local-test.yaml

kubectl wait --for=condition=Ready cluster/polardb-pg-real \
  -n polardb-pg-real --timeout=15m
kubectl get cluster,pod,pvc,svc -n polardb-pg-real -o wide
```

若需要指定 StorageClass，请在创建前复制清单并在
`volumeClaimTemplates[0].spec` 中增加 `storageClassName`：

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: polardb-pg-real
---
apiVersion: apps.kubeblocks.io/v1alpha1
kind: Cluster
metadata:
  name: polardb-pg-real
  namespace: polardb-pg-real
spec:
  terminationPolicy: Delete
  componentSpecs:
    - name: polardb
      componentDef: polardb-pg-local-v3
      replicas: 1
      disableExporter: true
      resources:
        requests:
          cpu: 500m
          memory: 1Gi
        limits:
          cpu: "2"
          memory: 4Gi
      volumeClaimTemplates:
        - name: data
          spec:
            accessModes:
              - ReadWriteOnce
            storageClassName: <已存在的测试StorageClass>
            resources:
              requests:
                storage: 10Gi
```

`metadata.name` 必须缩进在 `metadata:` 下，两个 Kubernetes 资源仅使用一行
`---` 分隔。`storageClassName` 只能填写 `kubectl get storageclass` 中已有的名称；
不要保留尖括号或其他占位符。

## 6. 验证真实 PolarDB-PG 内核

先设置变量并运行仓库验证脚本：

```bash
export NS=polardb-pg-real
export CLUSTER=polardb-pg-real

NAMESPACE="$NS" CLUSTER="$CLUSTER" \
  bash examples/polardb-pg/scripts/verify-local-engine.sh
```

预期输出包含：

```text
PolarDB-PG engine verified: pod=polardb-pg-real-polardb-0 \
version=PostgreSQL 17.11 (PolarDB 17.11.1.0 build unknown) ... \
polar_settings=162
```

也可手工检查：

```bash
POD=$(kubectl get pod -n "$NS" \
  -l app.kubernetes.io/instance="$CLUSTER",apps.kubeblocks.io/component-name=polardb \
  -o jsonpath='{.items[0].metadata.name}')

kubectl exec -n "$NS" "$POD" -c polardb -- \
  psql -U postgres -d postgres -c 'SELECT version();'

kubectl exec -n "$NS" "$POD" -c polardb -- \
  psql -U postgres -d postgres -c \
  "SELECT count(*) FROM pg_settings WHERE name LIKE 'polar_%';"

kubectl exec -n "$NS" "$POD" -c polardb -- \
  psql -U postgres -d postgres -c \
  "SELECT name, setting FROM pg_settings WHERE name IN ('polardb_version', 'polar_deploy_mode', 'polar_datadir') ORDER BY name;"

kubectl get pod -n "$NS" "$POD" \
  -o jsonpath='{.spec.containers[0].image}{"\n"}'
```

验收要求：

- `SELECT version()` 必须含有 `(PolarDB `。
- `polar_*` 参数数量必须大于零；测试镜像当前实测为 162。
- `polardb_version` 应为 `PolarDB V17.11.1.0 (open)`，`polar_deploy_mode` 为
  `OPEN_SOURCE`。
- Pod 镜像必须为固定的官方 digest。

仅显示 `PostgreSQL 12.18`、`apecloud/spilo` 或没有 `polar_*` 参数时，说明部署的
是旧的普通 PostgreSQL 方案，不能作为 PolarDB-PG 验收结果。

## 7. CRUD、服务访问与重建持久化测试

### 7.1 CRUD

```bash
kubectl exec -i -n "$NS" "$POD" -c polardb -- \
  psql -U postgres -d postgres -v ON_ERROR_STOP=1 <<'SQL'
DROP TABLE IF EXISTS kb_polardb_engine_validation;
CREATE TABLE kb_polardb_engine_validation (
  id integer PRIMARY KEY,
  payload text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO kb_polardb_engine_validation (id, payload)
VALUES (1, 'created'), (2, 'to-delete');
UPDATE kb_polardb_engine_validation SET payload = 'updated' WHERE id = 1;
DELETE FROM kb_polardb_engine_validation WHERE id = 2;
SELECT id, payload FROM kb_polardb_engine_validation ORDER BY id;
SQL
```

预期仅返回一行 `1 | updated`。

### 7.2 经 ClusterIP Service 连接

```bash
SERVICE=polardb-pg-real-polardb-postgresql

kubectl exec -n "$NS" "$POD" -c polardb -- sh -ec \
  "PGPASSWORD=\"\$POSTGRES_PASSWORD\" psql -h \"$SERVICE\" -p 5432 \
  -U postgres -d postgres -Atqc \"SELECT current_database() || ':' || current_user;\""
```

预期返回 `postgres:postgres`。此命令不输出密码。

### 7.3 删除 Pod 后验证数据持久化

```bash
kubectl delete pod -n "$NS" "$POD" --wait=false

kubectl wait --for=condition=Ready cluster/"$CLUSTER" \
  -n "$NS" --timeout=15m

POD=$(kubectl get pod -n "$NS" \
  -l app.kubernetes.io/instance="$CLUSTER",apps.kubeblocks.io/component-name=polardb \
  -o jsonpath='{.items[0].metadata.name}')

kubectl exec -n "$NS" "$POD" -c polardb -- \
  psql -U postgres -d postgres -c \
  'SELECT id, payload FROM kb_polardb_engine_validation ORDER BY id;'
```

Cluster 应重新变为 `Running`，Pod 为 `1/1 Running`，查询仍应返回 `1 | updated`。
这验证 PVC 持久化和 KubeBlocks Pod 重建生命周期，不表示节点级或存储级 HA。

### 7.4 真实内核远程物理备份

先创建独立运维的 S3/MinIO `BackupRepo`。可参考
`examples/polardb-pg/backuprepo-minio.example.yaml`，其中的密钥、endpoint、bucket
和 TLS 校验值必须替换为生产值。安装 Addon 时须把 replication HBA 收窄到真实 Pod
CIDR，例如：

```bash
helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \
  --namespace kb-system --create-namespace \
  --set backup.replicationHbaCIDR=10.0.0.0/16

kubectl apply -f examples/polardb-pg/backuprepo-minio.example.yaml
kubectl patch cluster "$CLUSTER" -n "$NS" --type=merge -p \
  '{"spec":{"backup":{"enabled":false,"retentionPeriod":"7d","method":"polar-pg-basebackup","repoName":"polardb-pg-minio"}}}'
kubectl annotate cluster "$CLUSTER" -n "$NS" \
  kubeblocks.io/reconcile=polardb-pg-remote-backup --overwrite

kubectl apply -f examples/polardb-pg/backup.yaml
NAMESPACE="$NS" BACKUP=polardb-pg-local-basebackup \
  bash examples/polardb-pg/scripts/verify-remote-backup.sh
```

成功结果为 `Backup.status.phase=Completed`，且脚本输出对象存储路径和字节数。该
ActionSet 调用的是官方镜像中的 `/u01/polardb_pg/bin/pg_basebackup`，并使用
PostgreSQL 17 可用于 stdout tar 输出的 `-X fetch`。它仅适用于 local-instance；
共享存储 MPDCluster 的备份恢复边界见 `docs/polardb-pg-stack-ops-zh.md`。

## 8. 常见问题

### 8.1 Cluster 状态为空或为 `Creating`

先检查 ComponentDefinition 是否存在、Pod 事件和 PVC：

```bash
kubectl get componentdefinition polardb-pg-local-v3
kubectl get cluster,pod,pvc -n "$NS" -o wide
kubectl describe pod -n "$NS" "$POD"
kubectl get events -n "$NS" --sort-by=.metadata.creationTimestamp
```

`PVC Pending` 通常是 StorageClass 无法供给、`WaitForFirstConsumer` 尚未调度，或
资源请求超过节点容量。修复 StorageClass 后，不要原地修改已创建 Cluster 的
`volumeClaimTemplates`；在没有业务数据时删除测试 Cluster 后重建。已有数据时
先完成备份和恢复验证。

### 8.2 Pod 被安全策略拒绝

官方 local-instance 镜像需要 `privileged` 和 `SYS_PTRACE`。若事件显示 Pod 安全
准入拒绝，需由集群管理员为测试 Namespace 建立最小授权策略；不要静默删除该
运行时要求。

### 8.3 为什么 Cluster Definition 和 Version 列可能为空

本方案在 KB 0.9 直接引用 `ComponentDefinition` 创建 Cluster，不使用旧
ClusterDefinition 模型。因此 `kubectl get cluster` 的 `CLUSTER-DEFINITION`、
`VERSION` 列可能为空；只要 `STATUS=Running`、关联 ComponentDefinition 与 Pod
镜像正确，这不是内核启动失败。

## 9. 生产 HA 的未完成条件

本指南不提供生产 HA 认证。官方 local-instance 在一个容器内使用 localfs 运行；
把它扩成多个 KubeBlocks Pod 且每个 Pod 挂独立 PVC，会破坏 PolarDB 共享存储模型，
不能获得 PolarDB 读写分离、LogIndex 和故障切换语义。

当前源码已经提供到官方 Stack 的 KubeBlocks Ops 桥接：`switchRw`、`restartIns`、
`forceRebuild` 和 fail-closed STONITH webhook 均只操作官方 `MPDCluster` 控制面。
同时，真实 local-instance 内核的远程 `pg_basebackup` 已通过 S3 `BackupRepo`
集成测试。它们不等同于生产 HA 验收，生产环境仍必须完成：

1. 部署官方 Stack Operator、Cluster Manager 和至少三个跨故障域的共享存储计算节点；
   所有计算节点须经 PolarFS/PFS 和多路径访问同一数据目录。
2. 对真实 `MPDCluster` 验证桥接的 switchover、rejoin/rebuild 与 Cluster Manager
   状态收敛，不能以本地模拟 CRD 状态替代。
3. 接入能确认断电、网络隔离或共享存储写权限撤销的物理 STONITH provider，并在节点
   失联、网络分区、存储异常下完成故障注入演练。
4. 使用与 Stack release 匹配的官方备份代理、WAL 归档与恢复 API，完成隔离恢复，
   验证共享卷、Cluster Manager 元数据和数据一致性。
5. 配置备份保留、监控告警、故障注入演练和已验收的运维 runbook。

在这些目标基础设施验收完成前，`polardb-pg-local-v3` 仍只能标记为“真实内核
单实例集成”；Stack Ops bridge 则标记为“控制面适配已实现、真实共享存储 HA 待验收”，
不能标记为“生产可用 HA”。

## 10. 清理测试环境

确认测试数据无需保留后：

```bash
kubectl delete cluster polardb-pg-real -n polardb-pg-real --wait=true
kubectl delete namespace polardb-pg-real --wait=true

# 仅当没有其他真实 PolarDB-PG Cluster 使用该定义时执行。
helm uninstall kb-addon-polardb-pg -n kb-system
kubectl delete componentversion polardb-pg-local-v3-version --ignore-not-found
kubectl delete componentdefinition polardb-pg-local-v3 --ignore-not-found
```

执行前务必确认 Namespace、Cluster 和 PVC 名称。`terminationPolicy: Delete` 会删除
该测试 Cluster 关联的数据卷。
