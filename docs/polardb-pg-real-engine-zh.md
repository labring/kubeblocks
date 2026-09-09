# KubeBlocks 0.9 对接真实 PolarDB-PG

## 目标与边界

本 Addon 对接的是 PolarDB 开源项目的官方运行时
`polardb/polardb_pg_local_instance:17`，而不是 Spilo、Patroni 或普通
PostgreSQL 镜像。运行时镜像按 Linux/amd64 manifest digest 固定，实际数据库
版本应返回类似以下标识：

```text
PostgreSQL 17.11 (PolarDB 17.11.1.0 build ...)
```

当前阶段提供单实例 localfs PolarDB-PG 的 KubeBlocks 管理能力，包括持久卷、
Pod 重建后的数据保留、服务发现、版本身份验证和使用 KubeBlocks `BackupRepo`
的全量物理远程备份。它不是共享存储部署，也不是生产 HA 方案。

旧的 `polardb-postgresql` Addon 使用 `apecloud/spilo` 和 Patroni，属于普通
PostgreSQL 流复制实现，不能作为真实 PolarDB-PG 使用。

## 前提条件

- Kubernetes 节点为 Linux/amd64。
- 集群策略允许该官方镜像所需的 `privileged` 和 `SYS_PTRACE`。
- 存在可用的 `ReadWriteOnce` StorageClass；示例使用默认 StorageClass。
- KubeBlocks 为 0.9.x，且 ComponentDefinition CRD 已安装。

## 部署与验证

```bash
helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \
  -n kb-system --create-namespace

kubectl apply -f examples/polardb-pg/cluster-local-test.yaml

NAMESPACE=polardb-pg-real CLUSTER=polardb-pg-real \
  bash examples/polardb-pg/scripts/verify-local-engine.sh
```

预期结果包括：Cluster 为 `Running`、数据库 Pod 为 `1/1 Running`、PVC 为
`Bound`，并且验证脚本输出 `PolarDB-PG engine verified`。

## 为什么不能复用 Patroni HA

官方 local-instance 入口在一个容器内启动主库和一个内部只读副本，二者共享
`/var/polardb` 下的 `polar_datadir`。这仅用于 localfs 运行和引擎验证。将其
扩展为多个 KubeBlocks Pod，再为每个 Pod 分配独立 PVC，会破坏 PolarDB 的共享
存储模型，不能获得 PolarDB 的读写分离、LogIndex 或高可用语义。

真正的生产 HA 需要单独的共享存储适配：所有计算节点访问同一 PolarDB 数据
目录，存储层提供多路径或等效的并发访问与读写控制，并由 PolarDB Cluster
Manager/官方部署流程负责节点角色和故障切换。KubeBlocks 的后续适配应作为
该控制面的桥接，而非使用 Patroni 代替它。

官方 Stack 控制面桥接、KubeBlocks Custom Ops 映射、STONITH 约束与真实引擎的
远程备份边界见 [PolarDB Stack Ops 方案](polardb-pg-stack-ops-zh.md)。

`polardb-pg-local-v3` 为独立备份 Job 添加了显式的 replication HBA 规则，避免
把普通 SQL 访问规则误认为复制访问规则。Chart 默认 `0.0.0.0/0` 仅用于集成测试；
生产安装必须通过 `--set backup.replicationHbaCIDR=<Pod CIDR>` 收窄为实际 Pod
网段，并使用独立运行、带 TLS 与生命周期策略的 S3/MinIO `BackupRepo`。
