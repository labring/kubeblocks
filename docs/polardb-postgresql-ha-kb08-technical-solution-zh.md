# KubeBlocks 0.8 Patroni PostgreSQL HA 技术方案

## 定位

`deploy/addons/polardb-postgresql` 迁移的是 KB 0.9 中的 Patroni/Spilo PostgreSQL 路线，而不是 PolarDB-PG 数据库内核。名称中的 `polardb` 仅保留历史兼容性；对外必须表述为“Patroni PostgreSQL HA addon”。

## 架构

```text
Client
  |
  +--> primary Service --> Patroni primary / PostgreSQL
                              |
                              +--> Patroni Kubernetes DCS
                              +--> streaming replication --> Patroni standby

KubeBlocks 0.8
  |- Cluster / Component / ReplicatedStateMachine
  |- native Switchover OpsRequest
  |- Custom OpsDefinition: rejoin (/restart), rebuild (/reinitialize)
  `- BackupPolicy -> ActionSet pg-basebackup -> BackupRepo -> Restore OpsRequest
```

Patroni 持有 PostgreSQL 的 leader election 与复制状态。ComponentDefinition 使用 `roleArbitrator: External`，Lorry 继续执行 role probe、账号和生命周期辅助操作。KB 0.8 addon 复用现有的 `postgresql` probe handler，并以 `ComponentDefinition.spec.labels.apps.kubeblocks.io/patroni-managed=true` 标记组件；移植后的 manager 据此向 Lorry 注入 `KB_ENABLE_HA=false`，防止启动第二个选主循环。

## KB 0.9 到 KB 0.8 的映射

| 0.9 能力 | 0.8 实现 | 边界 |
| --- | --- | --- |
| `polardb-postgresql` built-in handler | 复用 0.8 已有 `postgresql` handler，在 `ComponentDefinition.spec.labels` 加 `patroni-managed` 标记并由 manager 注入 | 不依赖新 tools handler；同版本 manager 必须识别该标签 |
| Patroni 主备和角色探测 | `ComponentDefinition` + external role arbitrator | 角色真相来自 Patroni |
| `Switchover` | 0.8 原生 `OpsRequest type=Switchover` | 候选者的可用性由 Patroni API 判断 |
| offline/rejoin | Custom Ops 调 `/restart` | 没有 0.9 offline-instance API |
| `RebuildInstance` | Custom Ops 调 `/reinitialize` | 仅重建可访问 standby；不是 InstanceSet 临时扩容语义 |
| `pg-basebackup` / restore drill | 0.8 ActionSet、BackupPolicyTemplate、Backup、Restore OpsRequest | 备份目标需要健康 secondary |
| 自动复制健康重建 | 未原样移植 | 0.9 使用 InstanceSet；0.8 为 RSM，不能无验证复制控制器 |

## 资源与运维流程

1. Chart 创建 versioned ComponentDefinition、ConfigConstraint、ActionSet、BackupPolicyTemplate、ClusterDefinition compatibility anchor，以及 rejoin/rebuild OpsDefinition。
2. Cluster 使用直接 `componentDef` 引用，并在创建 Component 时立即补齐 ComponentDefinition labels，避免 0.8 的首次调谐标签延迟影响服务、备份和 Ops 解析。
3. 计划切换通过 ComponentDefinition 的 Patroni HTTP switchover action 完成。自动候选者要求 Patroni reported lag 不超过 `ha.switchover.maxLagBytes`，默认 `0`。action 在 `POST /switchover` 后最多等待 120 秒，确认原主降为 standby 且目标成为 leader 才成功；若 Job 重试时已观察到原主不再是 leader，自动模式直接视为已完成，避免反向切换。
4. Rejoin Job 拒绝 primary，调用 target standby 的 `/restart`，轮询 `/patroni` 至 `role=replica` 且 `state=running`。Patroni 重启时可能主动断开 HTTP 请求，因此请求连接中断不直接判失败，最终状态轮询才是完成依据。
5. Rebuild Job 除相同 primary 防护外要求 `CONFIRM_REBUILD=true`，调用 `/reinitialize`，从当前 primary 重新构造 target standby 数据。
6. OpsRequest 进入 `Succeed`、`Failed` 或 `Cancelled` 时，controller 会幂等清理 Cluster 的 operation queue。该兜底覆盖 Restore 已持久化终态但未及时释放队列的场景，避免后续备份、切换或维护操作永久 Pending。
6. BackupPolicyTemplate 选择 secondary 运行物理 `pg_basebackup`，输出到 DataProtection BackupRepo；Restore OpsRequest 先恢复数据卷再创建目标 Cluster。
7. manager 在创建 `kb*` 系统账户前查询已有系统账户，覆盖“Lorry 已创建但 controller 状态未写回”的重试窗口；新版 PostgreSQL Lorry 在 `42710 duplicate_object` 时更新该系统账户密码。二者保证重试、manager 重启与恢复后重新调谐不会卡在已存在角色上，普通用户仍保持“已存在即失败”的 API 语义。

## 不可省略的生产控制

- 至少三个独立故障域，并将 PostgreSQL Pod、Patroni DCS、BackupRepo 与监控系统分散部署。
- 远端或复制 PVC，不能使用节点本地存储作为生产数据卷。
- 对 node-loss、network-partition、storage-split，先完成 BMC/云 API fencing 与存储写入撤销，再允许 survivor 接受写流量。
- 备份和恢复应在远端 BackupRepo 上定期 drill，记录 RPO/RTO、切换前后 leader、Patroni `/cluster`、OpsRequest 和告警证据。
- 所有 KubeBlocks、Spilo、PgBouncer、exporter、datasafed 镜像按 digest 固定；ComponentDefinition 不可变，发生不兼容变更时使用新的 `ha.componentDefinition.name`。

没有上述控制时，本 addon 可被称为“多副本 Patroni PostgreSQL 功能 HA”，不能被称为完整的生产故障域 HA，更不能称为 PolarDB-PG 生产 HA。
