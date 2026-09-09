---
title: KubeBlocks 0.9 PolarDB PostgreSQL Production HA Technical Solution
description: Architecture, operational boundary, validation, and release traceability for PolarDB PostgreSQL HA on KubeBlocks 0.9.
keywords: [kubeblocks, polardb postgresql, patroni, high availability, ha]
---

# KubeBlocks 0.9 PolarDB PostgreSQL 生产可用 HA 技术方案

部署、`kbcli` 运维、YAML 创建与验收命令见 [部署与测试指南](polardb-postgresql-ha-deployment-test-guide-zh.md)。

## 1. 目标与结论

本方案将 Patroni 管理的 PolarDB PostgreSQL 集成到 KubeBlocks 0.9，使用户能够以 KubeBlocks 的 Cluster、ComponentDefinition、OpsRequest、Backup 和 Restore 等原生资源管理数据库高可用生命周期。

已交付并在 KB 0.9 测试集群验证的范围如下：

- Patroni 主备选举、角色探测和 KubeBlocks Cluster 状态收敛。
- 指定候选与自动候选的计划内 switchover。
- logical fencing、rejoin、rebuild、备份和恢复演练的 KubeBlocks 原生 Ops 流程。
- 控制面、插件运行时和数据库运行时的 digest 固定发布策略。
- digest 固定镜像在部分运行时返回裸 `sha256` 状态时，InstanceSet 仍能正确判定就绪。

本方案不是通用的自动跨故障域 failover 承诺。节点失联、网络分区和存储分裂场景的生产 HA，必须接入目标基础设施的物理 STONITH/fencing，并通过多节点、远程或复制存储环境的故障注入验收后才可宣布达标。

## 2. 设计原则与边界

### 2.1 单一数据库 HA 权威

Patroni 是数据库选主、复制时间线和主备状态的唯一权威；KubeBlocks 负责声明式生命周期、操作编排、状态聚合和运维入口。为避免双控制器竞争，`polardb-postgresql` 内置 handler 显式设置 `KB_ENABLE_HA=false`，不启用 lorry 的第二套 HA 控制循环。

### 2.2 KubeBlocks 状态反映数据库事实

组件的主备角色来自 Patroni 状态及数据库探测。只有 InstanceSet 的副本就绪、可用，且 Patroni 复制健康检查通过时，KubeBlocks Cluster 才进入 `Running`。

### 2.3 fencing 分层

- **逻辑 fencing**：计划内切换已确认 Patroni 降级旧主后，将旧主实例离线，阻止其继续作为服务副本。这是已实现的 KubeBlocks Ops 能力。
- **物理 fencing / STONITH**：在节点失联、控制面分区、网络分区或共享存储异常时，从 Kubernetes 故障域外断电、隔离网络，并在需要时撤销旧主的存储写权限。这是生产部署必须补齐的基础设施能力。

逻辑 fencing 不能替代物理 fencing；没有旧主不可写的外部证据时，不得允许替代主库对外承接写流量。

## 3. 总体架构

```text
                     +---------------------------+
                     | KubeBlocks Manager        |
                     | Cluster / Component / Ops |
                     +-------------+-------------+
                                   |
                   reconcile, status, lifecycle Ops
                                   |
        +--------------------------+--------------------------+
        |                                                     |
        v                                                     v
+---------------------------+                    +---------------------------+
| ComponentDefinition       |                    | Data Protection           |
| polardb-postgresql-ha-vN  |                    | Backup / Restore          |
+-------------+-------------+                    +-------------+-------------+
              |                                                |
              v                                                v
+---------------------------+                    +---------------------------+
| InstanceSet / Pods        |                    | BackupRepo outside        |
| PostgreSQL + Patroni      |                    | database failure domain   |
| PgBouncer + exporter      |                    +---------------------------+
+-------------+-------------+
              |
              | Kubernetes ConfigMap DCS, Patroni REST API
              v
   +-------------------------------+
   | primary <---- replication ---> secondary |
   +-------------------------------+

External to the Kubernetes failure domain:
  monitoring and alert delivery, BMC/cloud fencing runner, storage fencing
```

关键实现位置：

- `deploy/addons/polardb-postgresql/templates/componentdefinition.yaml`：Patroni 容器、生命周期操作、服务、系统账号和版本化 ComponentDefinition。
- `config/lorry/components/binding_polardb_postgresql.yaml`：PolarDB PostgreSQL 内置 handler 绑定。
- `controllers/apps/transformer_component_postgresql_replication_health.go`：复制健康状态聚合。
- `pkg/controller/instanceset/instance_util.go`：容器镜像与运行时 ImageID 就绪判定。
- `examples/polardb-postgresql/ha/`：switchover、fence、rejoin、rebuild、backup 和 restore drill 示例。

## 4. KubeBlocks 原生 HA 操作

| 运维目标 | KubeBlocks API | 执行方式 | 关键安全约束 |
| --- | --- | --- | --- |
| 计划内切主 | `OpsRequest`，`type: Switchover` | 调用 Patroni REST `/switchover` | 指定候选必须是健康副本。自动候选只从 `running` 的副本中选择复制延迟最小者，并要求延迟不超过 `ha.switchover.maxLagBytes`，默认 `0`。 |
| 旧主逻辑隔离 | `OpsRequest`，`type: HorizontalScaling` | 将已确认降级的旧主离线 | 仅适用于计划内切换后的逻辑隔离，不能作为物理 STONITH。 |
| 副本重新加入 | `OpsRequest`，`type: HorizontalScaling` | 将离线实例重新加入拓扑 | 必须先确认旧主不再对外写入，并检查 Patroni timeline。 |
| 副本重建 | `OpsRequest`，`type: RebuildInstance` | 从健康副本或备份重建 | 出现时间线分叉、数据目录损坏或存储历史不可信时，禁止强制 rejoin，应执行 rebuild。 |
| 备份 | `OpsRequest`，`type: Backup` | `pg-basebackup` ActionSet 写入 BackupRepo | BackupRepo 必须位于数据库节点故障域之外。 |
| 恢复演练 | `OpsRequest`，`type: Restore` | 从已完成备份创建临时 Cluster | 临时恢复集群必须进入 `Running` 后才算通过；仅清理演练对象，不删除源库或源备份。 |

自动候选选择通过 Patroni `/cluster` 读取成员状态，排除当前主库、非运行成员和延迟超阈值成员，再按 `(lag, name)` 排序选择。该策略只用于**计划内** switchover；故障分区下的自动提升仍受物理 fencing 前置条件约束。

## 5. 镜像不可变性与 InstanceSet 状态修复

生产部署必须记录并按 `repository@sha256:<digest>` 固定以下镜像：

- KubeBlocks manager、tools、datascript、dataprotection 和 addon charts。
- PolarDB PostgreSQL 的 Spilo、PgBouncer 和 PostgreSQL exporter。
- 独立批准的 `datasafed` 备份依赖。

此前有容器运行时将 `status.image` 报告为本地裸 `sha256:<digest>`，但将完整镜像引用保留在 `status.imageID`。旧逻辑先比较镜像仓库名，导致已经运行且健康的 digest 固定 Pod 被误判为未就绪，Cluster 长期处于 `Creating` 或 `Updating`。

修复后的规则如下：

1. PodSpec 使用 digest 时，优先比较 PodSpec digest 与 `status.image` 或 `status.imageID` 的 digest。
2. 任一运行时 digest 不匹配即判定未就绪。
3. 非 digest 镜像继续执行严格的镜像名和 tag 比较，保持原有安全语义。

因此，ImageID 是 digest 固定镜像的权威运行时标识；仓库名前缀差异不会再阻断状态收敛。

## 6. 发布、升级与回滚

### 6.1 发布约束

`v0.9.3-polardb-ha.2` 是本次已验证发布。测试集群运行的 manager 为：

```text
ghcr.io/wallyxjh/kubeblocks@sha256:71afde7363bb9868f4ff22330198972789020d033ba34646a691389a558f00e9
```

发布标签仅是定位入口，不是不可变性本身。发布变更单必须记录所有实际部署 digest；不得使用 `latest`、可变 tag 或未批准的提交镜像。

### 6.2 ComponentDefinition 版本策略

KubeBlocks 0.9 的 `ComponentDefinition` 不可变。任何不兼容的 HA 定义调整必须创建新名称，例如从 `polardb-postgresql-ha-v1` 迁移到 `polardb-postgresql-ha-v2`。新集群仅在新定义验证通过后创建；已有集群不能原地替换 `componentDef`，必须经计划迁移或备份恢复迁移。

### 6.3 回滚原则

回滚时将 manager、tools、datascript、dataprotection 和 addon charts 一起恢复到上一套已批准 digest。回滚后需确认 Addon 为 `Enabled`、目标 ComponentDefinition 为 `Available`，并在隔离集群完成创建和 HA drill 后才恢复生产变更。

## 7. 生产准入条件

以下条件全部满足前，只能称为“具备生产 HA 实现能力”，不能称为“目标环境生产 HA 已验收”。

1. 至少三个可调度节点，分布在独立故障域；数据库、DCS 依赖、监控和备份路径不得共用单一故障域。
2. 使用远程或复制存储；node-local LVM、hostpath 和节点本地 BackupRepo 不满足节点故障恢复要求。
3. BackupRepo 为 `Ready`，备份数据位于数据库节点故障域外，并已完成一次可恢复备份。
4. 告警路由已实际送达并确认，包括无主库、复制延迟、备份失败和恢复演练失败。
5. fencing runner 位于 Kubernetes 故障域之外，已为每个数据库节点验证 BMC、云厂商或网络/存储隔离身份；凭据只保存在受保护 Secret 或外部密钥系统中。
6. 已在目标拓扑执行 `node-lost`、`network-partition` 和 `storage-split` 注入演练，并记录 fence 证据、Patroni leader history、RPO/RTO、rejoin/rebuild 和恢复演练结果。

生产故障处置、Redfish fencing 示例和验收证据清单见 [生产 HA Runbook](../examples/polardb-postgresql/production/RUNBOOK.md)。

## 8. 已完成验证

### 8.1 自动化验证

- `go test ./pkg/controller/instanceset`
- `go test ./controllers/apps -run TestShouldRequeuePendingPolarDBPostgreSQLComponent`
- GitHub Actions 全量 `make test`：[CICD-PUSH #33137376877](https://github.com/wallyxjh/kubeblocks/actions/runs/33137376877)，结果为成功。
- 已验证 release：[v0.9.3-polardb-ha.2](https://github.com/wallyxjh/kubeblocks/releases/tag/v0.9.3-polardb-ha.2)。

### 8.2 KB 0.9 测试集群验证

1. 已有 `backup-off-repro/repro-pg` 和 `kb-polardb-pg-digest-v2/polardb-pg-digest-v2` 在部署修复后的 manager 后均收敛到 `Running`，数据 Pod 未因状态修复而重建。
2. 使用全新命名空间创建 `kb-polardb-pg-v2-fresh/polardb-pg-v2-fresh`，不复用旧 PVC。结果为 Cluster `Running`、`Ready=True`、`ReplicasReady=True`，InstanceSet `2/2` Ready 和 Available，两个 Pod 均为 `5/5 Running`。
3. 在全新集群中验证 Patroni 角色：一个实例 `pg_is_in_recovery=false` 且 `transaction_read_only=off`，另一实例为 `true/on`；主库写入 `ha_smoke` 后，备库成功读到相同记录。
4. 两个新 Pod 的 PodSpec 镜像和运行时 ImageID 均为 digest，确认状态修复覆盖真实容器运行时。

该共享测试环境为单节点，且创建验证采用 `100m CPU / 512Mi` request、`1 CPU / 1Gi` limit 以适应共享容量。这是创建和状态收敛测试规格，不是生产容量建议，也不构成物理 fencing 验收。

## 9. PR、CI 与发布可追溯矩阵

仅以下已合并 PR 构成该方案的发布基线；已关闭或未合并的试验性分支不属于发布来源。

| 交付项 | 可追溯变更 | 结果 |
| --- | --- | --- |
| KubeBlocks 原生 HA 接入、Patroni 生命周期、switchover、逻辑 fencing、rejoin/rebuild、备份恢复和演练脚本 | [PR #13: feat: support PolarDB PostgreSQL native HA ops](https://github.com/wallyxjh/kubeblocks/pull/13) | 已合并到 `fix/v0.9.3`。 |
| addon chart 作为仓库关联 GHCR package 发布 | [PR #15: fix: publish addon charts to a repository-linked package](https://github.com/wallyxjh/kubeblocks/pull/15) | 已合并，支撑 addon 发布工件可追溯。 |
| digest 固定、备份保留、告警、恢复演练 CronJob、外部 Redfish fencing helper 和生产 Runbook | [PR #17: feat: add production PolarDB PostgreSQL operations](https://github.com/wallyxjh/kubeblocks/pull/17) | 已合并；物理 fencing 仍须由目标基础设施提供并验收。 |
| 托管 runner 上全量测试 | [PR #18: ci: run full test on hosted runner](https://github.com/wallyxjh/kubeblocks/pull/18) | 已合并；后续全量 CI 成功。 |
| digest 固定 Pod 的 InstanceSet 就绪状态修复 | [PR #19: fix: reconcile digest-pinned InstanceSet images](https://github.com/wallyxjh/kubeblocks/pull/19) | 已合并；已有卡住集群和全新集群均验证恢复/创建成功。 |
| 经过验证的源码和镜像发布 | [Release v0.9.3-polardb-ha.2](https://github.com/wallyxjh/kubeblocks/releases/tag/v0.9.3-polardb-ha.2) | 使用固定 manager digest 部署到 KB 0.9 测试环境。 |

## 10. 运维结论

当前代码、addon、Ops 流程、镜像发布和 KB 0.9 创建回归已形成闭环，能够作为目标环境生产 HA 改造的实现基线。最终生产验收的剩余工作不在 KubeBlocks 代码本身，而在目标基础设施的物理 fencing、跨故障域存储、监控告警投递、备份保留和定期恢复演练证据。未满足第 7 节任一条件时，应将该部署标记为“已实现 HA 能力，未完成生产环境 HA 验收”。
