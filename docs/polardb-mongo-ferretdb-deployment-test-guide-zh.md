---
title: KubeBlocks 0.9 PolarDB Mongo 兼容版部署与测试指南
description: 使用 YAML 和 kbcli 部署、备份、恢复和验证 PolarDB Mongo 兼容版。
keywords: kubeblocks kbcli yaml polardb mongo ferretdb backup restore
---

# KubeBlocks 0.9 PolarDB Mongo 兼容版部署与测试指南

## 前置条件

- KubeBlocks 和 kbcli 均为 0.9.x。
- 默认 StorageClass 可提供 ReadWriteOnce PVC。
- 至少有一个状态为 Ready 的 BackupRepo。
- 节点可拉取 FerretDB、postgres-documentdb 和 docker.io/mongo:7.0 镜像。

~~~bash
kubectl get backuprepo
kbcli version
~~~

## YAML 部署

install.yaml 包含两个 ComponentDefinition、ClusterDefinition、ActionSet、BackupPolicyTemplate、样例 Cluster 和 PDB。

~~~bash
kubectl apply -f examples/polardb-mongo-compat/install.yaml
kubectl wait --for=condition=Ready cluster/polardb-mongo-compat-sample --timeout=30m
kubectl get component,pod,pvc,pdb -l app.kubernetes.io/instance=polardb-mongo-compat-sample
~~~

期望状态：

~~~text
Cluster: Running
documentdb: 1/1 Running
ferretdb: 2/2 Running
PVC: Bound
~~~

## MongoDB 协议 smoke

~~~bash
kubectl apply -f examples/polardb-mongo-compat/smoke-mongosh-job.yaml
kubectl wait --for=condition=complete job/polardb-mongo-compat-smoke --timeout=5m
kubectl logs job/polardb-mongo-compat-smoke
~~~

该 Job 验证 ping、insertMany、find、updateOne、deleteOne、索引和简单聚合。

## YAML 原生备份

安装完成后，KubeBlocks 应自动创建默认 BackupPolicy：

~~~bash
kubectl get backuppolicy polardb-mongo-compat-sample-documentdb-backup-policy
~~~

创建标准 Backup CR：

~~~bash
kubectl apply -f examples/polardb-mongo-compat/backup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed backup/polardb-mongo-compat-sample-backup --timeout=30m
kubectl get backup polardb-mongo-compat-sample-backup -o wide
~~~

期望 Backup 显示：

~~~text
METHOD: mongodump
REPO:   <ready BackupRepo>
STATUS: Completed
~~~

## YAML 原生恢复

恢复必须创建新 Cluster：

~~~bash
kubectl apply -f examples/polardb-mongo-compat/restore.yaml
kubectl wait --for=condition=Ready cluster/polardb-mongo-compat-restored --timeout=30m
kubectl get restore -n default
~~~

目标 Restore 的状态必须为 Completed。Cluster=Running 只代表运行组件已就绪；必须另外检查 Restore CR。

验证恢复后的数据时，使用新 Cluster 的 Secret 和 FerretDB Service：

~~~bash
CLUSTER=polardb-mongo-compat-restored
USER=$(kubectl get secret "$CLUSTER-documentdb-account-ferretdb" -o jsonpath='{.data.username}' | base64 -d)
PASS=$(kubectl get secret "$CLUSTER-documentdb-account-ferretdb" -o jsonpath='{.data.password}' | base64 -d)

kubectl run mongo-verify --rm -it --restart=Never --image=docker.io/mongo:7.0 -- \
  mongosh "mongodb://$USER:$PASS@$CLUSTER-ferretdb:27017/kbcompat?authMechanism=SCRAM-SHA-256&authSource=postgres"
~~~

在 mongosh 中至少校验集合文档数、getIndexes() 和关键聚合结果。

## kbcli 原生备份与恢复

kbcli 使用相同的自动生成 BackupPolicy 和标准 CR：

~~~bash
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
~~~

## KubeBlocks 生命周期操作回归

以下结果来自 2026-09-04 的 KubeBlocks 0.9.3 测试集群。源 Cluster
`pbm-ops-e2e` 写入 50 条带复合唯一索引的数据；每个会影响工作负载的成功操作后，
都重新校验文档数、`_id_` 和 `bucket_1_sequence_1` 索引、五组 `$group/$sum`
聚合结果，以及一次写入和删除探针。

| 原生 MongoDB addon 操作 | 兼容版实测 | 结果或边界 |
| --- | --- | --- |
| Create / Connect / CRUD | 通过 | `mongosh` 使用 SCRAM-SHA-256 和 `authSource=postgres` 连接；CRUD、索引和简单聚合通过。 |
| HorizontalScaling | 通过 | FerretDB 从 1 扩到 2 副本，再缩回 1 副本；后端 DocumentDB 固定为单副本。 |
| VerticalScaling | 通过 | DocumentDB 与 FerretDB 的 CPU、内存请求更新完成，数据校验通过。 |
| Restart | 通过 | 两个组件重启完成，数据校验通过。 |
| Stop / Start | 通过 | Stop 后 Pod 被释放而数据 PVC 保留；Start 后恢复 Running，数据校验通过。 |
| Expose enable / disable | 通过 | FerretDB NodePort Service 创建和删除均由 Expose OpsRequest 完成。 |
| BackupRepo / Backup / Restore | 通过 | 使用状态为 Ready 的 `local-default-pvc` BackupRepo；`mongodump` Backup Completed，恢复到新 Cluster 后 50 条数据、索引和聚合一致。 |
| Delete | 通过 | 删除专用恢复目标后，Cluster 和其 DocumentDB PVC 均被清理；`Retain` Backup 仍保持 Completed。 |
| VolumeExpansion | 受环境限制 | 当前 `openebs-hostpath` StorageClass 未声明 `allowVolumeExpansion`，OpsRequest 被 admission webhook 拒绝。该操作需要使用可扩容 StorageClass，并预留实际后端容量。 |
| Switchover / 指定实例 Switchover | 不支持 | admission webhook 返回 `this cluster component ferretdb does not support switchover`。FerretDB 是无状态协议前端，当前示例后端只有一个 DocumentDB 实例，不存在 MongoDB primary 角色可切换。 |
| Configure | 不支持 | 当前 ComponentDefinition 没有托管 MongoDB 配置模板；Reconfiguring 请求找不到配置 ConfigMap。不能把 PostgreSQL 参数重配误当成 MongoDB 参数管理。 |
| `kbcli cluster create-account` / `list-accounts` | 不支持 | FerretDB ComponentDefinition 未声明业务账户生命周期动作，实测命令返回 `error: name is required`，不可作为账户管理入口。 |
| MongoDB 用户和角色 API | 部分通过 | `db.createUser()` 和内置 `readWrite` 角色认证写入已通过；`createRole` 和 `grantRolesToUser` 返回 `no such command`。不支持自定义角色或完整角色变更语义。 |
| Upgrade / Rollback | 未提供 | 测试集群没有任何 `polardb-mongo-*` ComponentVersion 或第二个已验证 release。`kbcli cluster upgrade --dry-run=server` 只能生成请求体，不能证明存在可执行升级或回滚路径。 |
| Volume snapshot / 数据文件备份 | 不支持 | 自动 BackupPolicy 只声明 `mongodump`，且 `snapshotVolumes: false`；本版本不提供物理卷快照、增量、PITR 或 oplog 恢复。 |

运行已支持的生命周期操作时，优先使用 `kbcli` 生成标准 OpsRequest。例如：

~~~bash
kbcli cluster hscale polardb-mongo-compat-sample \
  --components=ferretdb --replicas=2 --auto-approve -n default

kbcli cluster restart polardb-mongo-compat-sample --auto-approve -n default
kbcli cluster stop polardb-mongo-compat-sample --auto-approve -n default
kbcli cluster start polardb-mongo-compat-sample --force -n default

kbcli cluster expose polardb-mongo-compat-sample \
  --type=vpc --sub-type=NodePort --enable=true \
  --components=ferretdb --auto-approve -n default
~~~

`start` 在 kbcli 0.9.3 中不接受 `--auto-approve`；使用 `--force` 可跳过交互式预检。

## 端到端验收

建议在备份前写入带索引的独立测试集合，然后在恢复 Cluster 中比对：

| 校验项 | 期望 |
| --- | --- |
| 文档数 | 与源集合一致 |
| _id_ 和业务索引 | 均存在 |
| $group / $sum 聚合 | 与源集合一致 |
| Backup CR | Completed |
| Restore CR | Completed |
| 新 Cluster | Running |

本方案在 KubeBlocks 0.9.3 测试集群已完成两条备份恢复路径和生命周期回归验证：

1. YAML 创建 Backup 和 annotation 驱动的新 Cluster Restore。
2. kbcli cluster backup 和 kbcli cluster restore。

两条备份恢复路径此前均验证了 25 条文档、复合唯一索引 `bucket_1_sequence_1` 和五组聚合结果恢复一致。最新生命周期回归使用 50 条文档，确认缩扩容、资源变更、重启、停启和端点变更后源数据仍一致，并确认恢复目标的数据、索引和聚合一致。

## 常见问题

### 没有生成 BackupPolicy

检查 ActionSet 和 BackupPolicyTemplate 是否已安装，并确认 Cluster 已完成一次 reconcile：

~~~bash
kubectl get actionset polardb-mongo-compat-mongodump
kubectl get backuppolicytemplate polardb-mongo-compat-backup-policy-template
kubectl get backuppolicy -n default
~~~

没有 BackupPolicy 时，手工运行 mongodump 只会产生普通 Job，不会产生 KubeBlocks Backup CR。

### Restore 已创建但尚未完成

~~~bash
kubectl get restore -n default -o wide
kubectl describe restore <restore-name>
kubectl get job -n default
~~~

确认目标 Cluster 的 DocumentDB 和 FerretDB 都已 Ready；Restore Job 通过目标 Cluster 的系统账号连接 FerretDB，而不是使用源 Cluster 的 Secret。

### 需要更高数据保护等级

本 ActionSet 是全量逻辑备份。事务一致性、增量、PITR、oplog、卷快照和后端物理恢复应由最终 HA PostgreSQL-compatible 平台及其备份策略承担。
