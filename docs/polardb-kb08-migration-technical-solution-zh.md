# KubeBlocks 0.8 PolarDB-PG 与 Mongo 兼容版迁移方案

## 范围与边界

本迁移面向 KubeBlocks `0.8.2`。它提供两个独立的 addon，不能把它们都称为原生 PolarDB 高可用产品：

| addon | 数据面 | 当前承诺 |
| --- | --- | --- |
| `polardb-pg` | `polardb/polardb_pg_local_instance` | PolarDB-PG 本地实例、物理备份/恢复和 KubeBlocks 生命周期操作；单副本 localfs，不是共享存储 HA。 |
| `polardb-pg-stack-ops` | 官方 PolarDB Stack `MPDCluster` + Cluster Manager | 零副本 KubeBlocks 控制投影，提供 switchover、rejoin、rebuild 和物理 fencing 的 Custom Ops 桥接；数据库、共享存储和角色仍由官方 Stack 控制。 |
| `polardb-mongo-compat` | FerretDB + PostgreSQL DocumentDB | MongoDB wire protocol/驱动兼容入口、DocumentDB 持久化、逻辑备份/恢复和前端扩缩容。 |

`polardb-mongo-compat` 是版本 B，不是 Alibaba Cloud PolarDB for MySQL document database 控制面，也不复用 KubeBlocks 原生 `mongodb` addon。名称特意保留 `compat`，避免将协议兼容误解成原生 MongoDB 或 PolarDB for MySQL 的完整产品语义。

## 架构

```text
MongoDB driver / mongosh
          |
          v
   FerretDB Service (1..16 replicas)
          |
          v
PostgreSQL DocumentDB (1 replica + PVC)
          |
          v
KubeBlocks BackupRepo

PolarDB-PG local instance (1 replica + PVC)
          |
          v
KubeBlocks BackupRepo
```

Mongo 兼容版的 FerretDB 前端可横向扩容；后端当前固定为单副本。PolarDB-PG addon 使用 PolarDB-PG local instance 镜像，并将物理数据目录交给 `pg_basebackup --polardata` 备份。真正的 PolarDB-PG 共享存储主备、故障仲裁和跨节点高可用，需要外部 PolarDB/Stack Operator 数据面与相应 CRD。`polardb-pg-stack-ops` 将 KubeBlocks 0.8 的 Custom Ops 映射到官方 `MPDCluster` 注解，但不创建或接管数据平面。单节点测试环境只能验证该 API contract，不能构成生产 HA 验收。

## KubeBlocks 0.8 适配

- 使用 direct `ComponentDefinition` 的 Cluster，通过兼容性 `ClusterDefinition` 锚点选择 `BackupPolicyTemplate`；不依赖 0.9 的 `ClusterVersion` 和 `ComponentVersion`。
- 运行时操作按逻辑组件名回退解析 ComponentDefinition，使 `Restart`、`HorizontalScaling`、`Stop` 和 `Start` 可用于 direct ComponentDefinition Cluster。
- 无角色 FerretDB RSM 的串行重启改为使用 Kubernetes readiness，而不是角色标签；仅在 `updatedReplicas != replicas` 时重排队，避免 OnDelete StatefulSet 已完成后持续循环。
- `Stop` 对带最小副本限制的组件保留常规水平缩容校验，但在 KubeBlocks Stop 快照存在时允许临时缩至 `0`；因此普通用户缩容仍不能绕过 `minReplicas`。
- 备份将 ComponentDefinition 系统账号密文写入 Backup 注解；恢复时只解密目标组件对应账号，避免 restore 创建随机密码导致 Mongo 或 PolarDB-PG 恢复后的连接凭据漂移。
- direct ComponentDefinition Cluster 跳过仅适用于旧 ClusterVersion 路径的 system-account reconciler，避免无意义的 ClusterVersion 查询错误。

## 生产前置条件

- 使用独立命名空间、独立 BackupRepo、固定镜像 digest、TLS、NetworkPolicy、资源配额、PDB 和监控告警。
- 将 `polardb-pg` 的 `backup.replicationHbaCIDR` 从测试默认 `0.0.0.0/0` 收敛到实际 Pod CIDR；不要把测试默认值直接带入生产。
- 默认存储类必须满足数据持久化要求。测试集群的 `openebs-hostpath` 不支持扩容，因此卷扩容不在本版本验收范围内。
- 在生产变更前执行恢复演练、滚动重启、节点故障演练、容量压测和 addon 镜像/Chart 签名验证。
- 使用 `polardb-pg-stack-ops` 时，还必须验收官方 Stack 的共享存储、Cluster Manager、真实 STONITH provider、WAL 归档和隔离恢复；不要把 `polardb-pg` local-instance 的 `pg_basebackup` 用于 `MPDCluster`。

## Mongo 兼容性承诺

已验证范围是 SCRAM 认证、`mongosh`/MongoDB driver 连接、CRUD、条件查询、排序、普通索引、简单聚合、备份恢复和 FerretDB 前端扩缩容。以下不作为承诺：原生副本集和 `rs.*`、分片、oplog/change stream、完整 Admin 命令、`db.createRole`/角色继承语义、所有聚合/事务/特殊 BSON 边界，以及 MongoDB 服务器级别运维命令。账号由 KubeBlocks system account Secret 管理，而不是 MongoDB 原生角色管理。
