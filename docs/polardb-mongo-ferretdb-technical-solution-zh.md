---
title: KubeBlocks 0.9 PolarDB Mongo 兼容版技术方案
description: 基于 FerretDB、DocumentDB-compatible PostgreSQL 和 KubeBlocks 原生 Backup/Restore 的 MongoDB 协议兼容方案。
keywords: kubeblocks polardb mongo ferretdb documentdb backup restore
---

# KubeBlocks 0.9 PolarDB Mongo 兼容版技术方案

## 定位

本方案是版本 B：FerretDB + PostgreSQL DocumentDB extension + KubeBlocks。

它提供 MongoDB wire protocol 和常用驱动兼容入口，数据由 PostgreSQL-compatible 后端保存。它不是原生 MongoDB 副本集，也不应复用仓库已有的 mongodb addon 名称。产品名固定为 polardb-mongo-compat。

当前示例使用 ghcr.io/ferretdb/postgres-documentdb:17-0.107.0-ferretdb-2.7.0 验证编排和协议兼容。生产部署必须把 documentdb 替换为可运行 DocumentDB extension 的 HA PostgreSQL-compatible 后端，例如经过兼容性验证的 PolarDB-PG 底座；单副本示例本身不构成后端生产 HA 承诺。

## 架构

~~~text
MongoDB client / mongosh / driver
              |
              | MongoDB URI :27017
              v
FerretDB Service and stateless replicas
              |
              | PostgreSQL connection
              v
DocumentDB-compatible PostgreSQL backend
              |
              v
Persistent storage / HA PostgreSQL-compatible platform
~~~

组件职责：

| 对象 | 名称 | 职责 |
| --- | --- | --- |
| ComponentDefinition | polardb-mongo-documentdb | PostgreSQL + DocumentDB 数据后端，持久化卷名为 data |
| ComponentDefinition | polardb-mongo-ferretdb | 无状态 MongoDB 协议前端，服务端口 27017 |
| ClusterDefinition | polardb-mongo-compat | 规定后端先于前端启动 |
| Cluster | polardb-mongo-compat-sample | 默认 1 个后端和 2 个 FerretDB 前端 |
| ActionSet | polardb-mongo-compat-mongodump | 原生逻辑备份和 post-ready 恢复动作 |
| BackupPolicyTemplate | polardb-mongo-compat-backup-policy-template | 自动生成 Cluster 的默认 BackupPolicy |

FerretDB 后端地址通过 serviceVarRef 注入，账号通过 credentialVarRef 引用 DocumentDB 的 ferretdb 系统账号。客户端只连接 ferretdb Service，不直接暴露后端 5432。

## 认证边界

客户端 URI 形式如下：

~~~text
mongodb://<user>:<password>@<cluster>-ferretdb:27017/<database>?authMechanism=SCRAM-SHA-256&authSource=postgres
~~~

ferretdb 系统账号实际由 PostgreSQL 保存，因此认证库是 postgres。业务请求必须使用 SCRAM-SHA-256 认证。

账户和角色能力必须区分处理：实测 `db.createUser()` 可以创建用户，使用内置 `readWrite`
角色的用户能够完成认证和写入；但 `createRole` 和 `grantRolesToUser` 返回 `no such command`。
因此不承诺自定义角色、动态授予角色、角色枚举或完整 MongoDB RBAC 语义。当前
ComponentDefinition 也没有定义业务账户生命周期动作，不能通过 `kbcli cluster create-account`
或 `list-accounts` 管理业务用户。用户创建应通过受控的 MongoDB API 自动化完成，并把
自定义角色需求放在应用或 PostgreSQL-compatible 后端的访问控制设计中处理。

## 原生备份与恢复

### 对象生命周期

~~~text
Cluster
  -> BackupPolicyTemplate 匹配 documentdb ComponentDefinition
  -> 自动创建 <cluster>-documentdb-backup-policy
  -> Backup CR
  -> ActionSet Backup Job
  -> mongodump --archive --gzip | datasafed push
  -> BackupRepo

新 Cluster + kubeblocks.io/restore-from-backup annotation
  -> Restore CR
  -> post-ready Restore Job
  -> datasafed pull | mongorestore --archive --gzip --drop
  -> 新 Cluster 的 FerretDB Service
~~~

BackupPolicyTemplate 选择 documentdb 的 ferretdb 系统账号。备份 Job 使用 KubeBlocks 注入的 DP_DB_* 凭据；恢复 Job 优先使用同一组变量，缺失时使用新目标 DocumentDB Pod 已注入的 POSTGRES_USER 和 POSTGRES_PASSWORD。恢复不依赖源 Cluster 的 Secret，也不会在 ActionSet 中保存明文密码。

ActionSet 通过 datasafed 而不是直接访问本地 PVC：

- 支持默认本地 PVC BackupRepo。
- 支持 KubeBlocks 通过工具访问的对象存储型 BackupRepo。
- 由 KubeBlocks 创建并跟踪标准 Backup 和 Restore CR。
- 可以使用相同的 BackupPolicy 从 YAML 或 kbcli 发起。

恢复只允许新建、可清空的目标 Cluster。mongorestore --drop 会覆盖 dump 中出现的 namespace，不能用于原地回滚源 Cluster。

### 一致性与能力边界

这是逻辑备份，不是存储快照或 PostgreSQL 物理备份：

- 已覆盖：完整 archive 导出、完整 archive 导入、集合数据、普通索引和常用聚合结果校验。
- 未承诺：事务一致快照、增量备份、PITR、oplog、Change Streams、rs.*、复制集选主或分片恢复。
- 后端的物理备份、RPO/RTO 和跨可用区恢复由最终 HA PostgreSQL-compatible 平台单独提供和演练。

## Restore 名称兼容

Kubernetes label value 最大为 63 字符。KubeBlocks 的 Restore Job 以前直接把完整 Restore 名放在 label 中，较长的 Cluster 名会使 Job 被 API 拒绝。

本实现将不合法或过长的 Restore 名转换为稳定的 SHA-256 短 hash label，并在 Job annotation 保留完整 Restore 名。事件回查和清理使用同一 label 计算，因此短名现有行为不变，长名也能创建和完成 Restore Job。

## 生产高可用要求

生产发布至少需要满足：

1. documentdb 后端采用已验证的 HA PostgreSQL-compatible 平台，包含同步复制、自动 failover、稳定 primary/proxy endpoint 和明确的 RPO/RTO。
2. FerretDB 至少 2 副本，配置 PDB、反亲和、资源请求/限制、TLS、NetworkPolicy 和监控告警。
3. BackupRepo 使用受控对象存储或可靠卷，配置保留策略、访问凭据、加密和定期恢复演练。
4. 验收包含 Backup Completed、Restore Completed、数据/索引校验、前端故障恢复、后端 failover、滚动升级和回滚。
5. 对外只承诺经过实际验证的 MongoDB 协议与驱动能力，不宣称完整 MongoDB 语义或原生副本集兼容。

## 已验证兼容项

| 能力 | 状态 |
| --- | --- |
| MongoDB URI、mongosh、SCRAM-SHA-256 | 已验证 |
| CRUD、索引、简单聚合 | 已验证 |
| FerretDB 前端多副本 | 已验证 |
| KubeBlocks Backup CR | 已验证 |
| KubeBlocks Restore CR | 已验证 |
| kbcli cluster backup / kbcli cluster restore | 已验证 |
| mongodump / mongorestore | 已验证 |
| `db.createUser` + 内置 `readWrite` 角色 | 已验证 |
| `createRole`、`grantRolesToUser`、自定义角色 | 不支持 |
| KubeBlocks HorizontalScaling、VerticalScaling、Restart、Stop/Start、Expose、Delete | 已验证 |
| MongoDB 副本集切换、MongoDB 参数重配、升级/回滚 | 不支持或未提供版本发布物 |
| MongoDB 事务、复制集、分片 | 不承诺 |
