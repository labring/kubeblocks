# KubeBlocks 0.9 对接官方 PolarDB Stack 控制面

## 架构边界

生产级共享存储 PolarDB-PG 的数据库资源必须由官方 PolarDB Stack Operator 和
Cluster Manager 管理。KubeBlocks 0.9 在此方案中提供一个零副本的控制投影
`Cluster`，它只作为 `OpsRequest`、审计和 RBAC 锚点，不会创建或重启任何
PolarDB 数据库 Pod、PVC、Service 或 Cluster Manager。

该边界避免了两个控制器同时控制数据库角色。实际数据平面为：

```text
KubeBlocks Custom Ops Job
  -> MPDCluster annotations (switchRw / restartIns / forceRebuild)
  -> PolarDB Stack Operator workflow
  -> PolarDB Cluster Manager + shared-storage locking + engine actions
```

官方共享存储的 `MPDCluster.spec.dbClusterType` 必须为 `share`。KubeBlocks Job
在写入任何动作前会检查该条件、Cluster 状态和目标实例是否来自
`status.dbInstanceStatus`。

## 原生 Ops 映射

| KubeBlocks `OpsDefinition` | 官方 PolarDB Stack 动作 | 成功条件 |
| --- | --- | --- |
| `polardb-pg-stack-switchover` | `metadata.annotations.switchRw=<RO ID>` | `Running` 且 `leaderInstanceId=<RO ID>` |
| `polardb-pg-stack-rejoin` | `metadata.annotations.restartIns=<instance ID>` | `Running` 且工作流临时注解已清除 |
| `polardb-pg-stack-rebuild` | `metadata.annotations.forceRebuild=true` | `Running` 且 `forceRebuild` 已清除 |
| `polardb-pg-stack-fence` | 物理 STONITH webhook，再等待 Cluster Manager 收敛 | provider 已确认 fence；`Running`；若原 RW 被 fence 则领导者已改变 |

`switchRw` 的候选必须是官方 Stack 状态中当前 RO 节点。候选的新鲜度、WAL/LogIndex
状态和共享存储写锁由官方 Cluster Manager 判断和执行，KubeBlocks 不自行选主。

`restartIns` 是可恢复成员的 rejoin 路径；不恢复时只能通过官方的 cluster-wide
`forceRebuild`。它有显式 `CONFIRM_FORCE_REBUILD=true` 二次确认，防止把单点修复
误变成整个共享集群重建。

## STONITH 要求

fencing Job 需要同命名空间 `polardb-pg-stonith` Secret 的 `endpoint` 和 `token`。
endpoint 必须在确认节点已断电、网络已隔离或存储写权限已撤销后才返回 HTTP 2xx。
没有 Secret、请求超时、返回非 2xx 或 Cluster Manager 未收敛，OpsRequest 都会失败。
删除 Pod、网络策略或终止进程不属于生产 fencing。

Webhook 使用 `POST`，body 为 `cluster`、`namespace`、`targetInstance`、`reason`
和 `opsRequest`。生产 provider 必须对请求做身份认证和审计，保证相同请求幂等，
并仅在目标节点断电、数据网隔离或共享存储写权限撤销已经由基础设施确认后返回
2xx。Webhook 返回一个异步任务 ID、接受请求或仅终止 Kubernetes Pod 都不能作为
成功条件。

安装、RBAC、绑定和操作示例见
[`examples/polardb-pg-stack-ops/README.md`](../examples/polardb-pg-stack-ops/README.md)。

## 远程备份

`deploy/addons/polardb-pg` 增加 `polar-pg-basebackup`。它运行在已按 digest 固定
的官方 PolarDB-PG 运行时中，使用 `/u01/polardb_pg/bin/pg_basebackup` 从真实引擎
流式导出物理备份，由 KubeBlocks `datasafed` 写入 `BackupRepo(accessMethod: Tool)`。
MinIO/S3 参考对象见
[`examples/polardb-pg/backuprepo-minio.example.yaml`](../examples/polardb-pg/backuprepo-minio.example.yaml)，
执行备份的对象见
[`examples/polardb-pg/backup.yaml`](../examples/polardb-pg/backup.yaml)。
在没有外部对象存储的回归环境中，可使用按 digest 固定镜像的
[`remote-backup-test-minio.yaml`](../examples/polardb-pg/remote-backup-test-minio.yaml)
验证 `BackupRepo(accessMethod: Tool)`；其中的 `emptyDir` MinIO 只用于测试，
绝不能作为生产备份库。

该备份 ActionSet 当前只匹配 `polardb-pg-local-v3`，并在初次启动时为独立备份 Job
配置 `pg_hba.conf` 的 replication 规则。生产环境必须把
`backup.replicationHbaCIDR` 收窄到实际 Kubernetes Pod CIDR，不能保留测试默认的
`0.0.0.0/0`。它已拒绝普通 PostgreSQL
endpoint，并不宣称可恢复官方共享存储 `MPDCluster`。共享存储生产恢复需要把
PolarDB Stack 的存储卷、Cluster Manager 元数据与 WAL 归档一起纳入一致性恢复；
在对应官方备份代理和真实恢复演练完成前，不应使用 local-instance 的
`pg_basebackup` 作为共享集群的灾备恢复路径。

## 生产验收边界

公开的 Stack Operator 源码公开了 `MPDCluster` 及上述三种运维注解，但备份控制面
依赖独立的 `apsaradb.aliyun.com` `Backup`、`BackupExecute`、`BackupLogPlan` CRD
和官方备份代理；这些 CRD schema 不在该源码仓库中。本适配不会猜测该私有或版本
相关 API，也不会将 KubeBlocks `pg_basebackup` 直接作用于共享盘。

要完成共享存储生产远程备份和恢复闭环，目标环境必须先提供与已安装 Stack release
完全匹配的官方备份 CRD、备份代理、WAL 归档和恢复 runbook。届时应把 KubeBlocks
Backup/Ops 对象映射到该 release 的正式 `BackupExecute` API，并以一次隔离恢复
验证数据、PFS/共享卷、Cluster Manager 元数据和归档 WAL 的一致性。当前已完成的
远程备份实现及测试范围是官方 `polardb_pg_local_instance` 真实内核，而不是
共享存储 `MPDCluster` 的灾备恢复认证。
