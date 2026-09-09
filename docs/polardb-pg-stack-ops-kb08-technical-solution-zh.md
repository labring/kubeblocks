# KubeBlocks 0.8 PolarDB-PG Stack 生产 HA 控制面方案

## 定位

`polardb-pg-stack-ops` 是 KubeBlocks 0.8 对官方 PolarDB Stack Operator 和
Cluster Manager 的生产 HA 运维桥接。它不把本地 `polardb-pg` addon 扩容成多个
PVC，也不使用 Patroni 替代 PolarDB 的共享存储和选主控制面。

实际数据库 Pod、共享存储、Cluster Manager、RW/RO 角色和故障恢复均由官方
`MPDCluster` 管理。KubeBlocks 只创建一个固定为零副本的 `control` 组件，作为
`OpsRequest`、RBAC 和审计锚点。因此 KubeBlocks 不会与 Stack Operator 竞争
数据库资源的 owner 或领导者决策。

```text
KubeBlocks 0.8 Custom Ops Job
        |
        | get binding Cluster; patch official MPDCluster annotations
        v
MPDCluster (dbClusterType: share)
        |
        v
PolarDB Stack Operator + Cluster Manager + shared storage
        |
        v
RW and RO PolarDB-PG instances
```

## 操作映射

| KubeBlocks 0.8 OpsDefinition | 官方 Stack 动作 | 成功条件 |
| --- | --- | --- |
| `polardb-pg-stack-switchover` | `switchRw=<RO ID>` | `clusterStatus=Running` 且 `leaderInstanceId` 等于目标 ID。 |
| `polardb-pg-stack-rejoin` | `restartIns=<instance ID>` | `clusterStatus=Running` 且临时注解已由 Stack 清除。 |
| `polardb-pg-stack-rebuild` | `forceRebuild=true` | `clusterStatus=Running` 且临时注解已清除。必须显式传入 `CONFIRM_FORCE_REBUILD=true`。 |
| `polardb-pg-stack-fence` | 调用物理 STONITH webhook，再等待 Cluster Manager | fencing provider 已确认隔离；集群恢复 `Running`；若 fence 原 RW，必须观察到新 leader。 |

`switchRw` 的候选必须是官方状态中一个正在运行的 RO 成员。候选新鲜度、WAL/LogIndex
和共享存储写锁均由 Cluster Manager 判断，KubeBlocks 不自行选主。

## KubeBlocks 0.8 适配

KubeBlocks 0.9 的 `OpsDefinition.actions/workload/componentInfos` 在 0.8 中
不存在。本 addon 使用 0.8 的 `componentDefinitionRefs`、`parametersSchema` 和
`jobSpec` 表示同一组操作。每个 Custom Ops Job 继承绑定组件指定的
`ServiceAccount`，只拥有读取绑定 Cluster 和读取/patch 目标 `MPDCluster` 的权限。

0.8 的 Custom Job 自动提供 `KB_CLUSTER_NAME`，但不提供操作名称和 namespace。
addon 使用 Downward API 注入 Pod namespace 与 Kubernetes Job 名称，作为 fencing
请求和 `last-stonith` 注解中的可审计操作标识；不依赖一个不存在的数据库 Pod。

`polardb-pg-stack-v1` 的副本范围是 `0..0`。因此它不能被水平扩容，也不会
启动控制容器。所有四个操作 Job 独立于组件副本运行。

## 生产前置条件

满足以下条件后，才能把该绑定用于生产 HA：

1. 使用与目标 PolarDB Stack release 完全匹配的官方 Stack Operator、Cluster
   Manager 和 `MPDCluster` CRD。
2. 目标 `MPDCluster.spec.dbClusterType` 为 `share`，有受支持的多路径/共享存储、
   至少一个 RW 和一个健康 RO，并跨实际故障域部署。
3. KubeBlocks binding 与 `MPDCluster` 使用同一命名空间，或为跨命名空间访问配置
   等效的最小 RBAC；不能将通配 `cluster-admin` 绑定给操作 Job。
4. `polardb-pg-stonith` Secret 的 endpoint 只能在已确认断电、网络隔离或共享存储
   写权限撤销后返回 2xx。删除 Pod 或网络策略不构成 fencing。
5. 使用该 Stack release 的正式 Backup/Restore CRD、备份代理、WAL 归档和隔离恢复
   Runbook。local-instance addon 的 `pg_basebackup` 只适用于 localfs，不能用于
   共享存储 `MPDCluster`。
6. 生产变更前完成 RW/RO switchover、成员 rejoin、真实物理 fencing、隔离恢复、滚动
   升级和跨故障域故障注入演练，并记录 RPO/RTO 证据。

## 不在本 addon 中的内容

- 不创建 `MPDCluster`、数据库 Pod、PVC、服务、Cluster Manager 或共享盘。
- 不模拟 PolarDB 选主、LogIndex、WAL 一致性或存储锁。
- 不将 `polardb-pg` local-instance 的物理备份用于共享存储恢复。
- 不把单节点或仅有 fake `MPDCluster` 状态的合同测试描述为生产 HA 验收。

部署、RBAC、YAML 操作对象和合同测试见
[`docs/polardb-pg-stack-ops-kb08-deployment-test-guide-zh.md`](polardb-pg-stack-ops-kb08-deployment-test-guide-zh.md)。
