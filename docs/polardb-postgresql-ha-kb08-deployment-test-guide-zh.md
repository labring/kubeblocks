# KubeBlocks 0.8 Patroni PostgreSQL HA 部署与验收指南

> `polardb-postgresql` 是历史名称。本 addon 运行的是 `apecloud/spilo`（普通 PostgreSQL + Patroni），不是 PolarDB-PG 的共享存储数据库引擎。不能把本方案作为真实 PolarDB-PG 的实现或验收结果。

本指南覆盖 KubeBlocks 0.8 中一至三副本的 Patroni PostgreSQL 部署、两副本以上的主备与计划内切换、standby rejoin、standby rebuild，以及 `pg-basebackup` 备份和恢复。单副本可直接启动，并可原地扩容为主备；在 standby Ready 前它不具备故障切换能力。Patroni 是唯一的数据库选主者；KubeBlocks 负责生命周期编排和状态观测。由于 KB 0.8 的 tools 镜像已有 `postgresql` probe，本 addon 使用该 handler，并通过 `ComponentDefinition.spec.labels.apps.kubeblocks.io/patroni-managed=true` 标记让移植后的 manager 向 Lorry 注入 `KB_ENABLE_HA=false`。

## 前置条件

- KubeBlocks manager、tools、dataprotection 和 addon 应来自包含本提交的同一版本镜像。运行时 probe 使用 KB 0.8 已有的 `postgresql` handler，不要求额外的 tools handler；移植后的 manager 负责根据 Patroni 标记关闭 Lorry HA loop。
- 单副本功能验证只需一个可调度节点；两副本以上的主备需要至少两个节点；生产 HA 验收至少三个独立故障域节点。
- 数据卷和 BackupRepo 必须是远端或复制存储。`openebs-hostpath`、local LVM 和节点本地 BackupRepo 只能用于功能测试。
- 在创建备份前，`kubectl get backuprepo` 必须显示一个 `Ready` 的远端仓库。

## 安装 Addon

以下命令将 addon 的全局定义安装到 `kb-system`。先部署升级后的 KubeBlocks manager，再执行该命令。

```bash
helm upgrade --install kb-addon-polardb-postgresql deploy/addons/polardb-postgresql \
  -n kb-system --create-namespace

kubectl get componentdefinition polardb-pg-ha-v2
kubectl get opsdefinition polardb-pg-ha-v2-rejoin \
  polardb-pg-ha-v2-rebuild
```

生产环境需固定 Spilo、PgBouncer 与 exporter 的 digest。可从 [values-production.example.yaml](../examples/polardb-postgresql-ha-kb08/values-production.example.yaml) 创建经审批的 values 文件：

```bash
helm upgrade --install kb-addon-polardb-postgresql deploy/addons/polardb-postgresql \
  -n kb-system --create-namespace \
  -f examples/polardb-postgresql-ha-kb08/values-production.example.yaml
```

## 创建单副本并扩容为主备

单副本与两副本使用相同的 `polardb-postgresql` addon、
`polardb-postgresql-ha-kb08` ClusterDefinition 和 `polardb-pg-ha-v2`
ComponentDefinition。创建后不能替换 ComponentDefinition，但可以直接调整副本数。

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/namespace.yaml
kubectl apply -f examples/polardb-postgresql-ha-kb08/cluster-single.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/pg-single \
  -n polardb-pg-ha-kb08 --timeout=20m

# 单副本时 BackupPolicy 不选择 secondary，而是选择唯一可用实例。
kubectl get backuppolicy pg-single-polardb-pg-ha-v2-backup-policy \
  -n polardb-pg-ha-kb08 -o yaml
kubectl apply -f examples/polardb-postgresql-ha-kb08/backup-single.yaml

# 原地扩容到主备。待 standby Ready 后才具备切换能力。
kubectl patch cluster pg-single -n polardb-pg-ha-kb08 --type=json \
  -p='[{"op":"replace","path":"/spec/componentSpecs/0/replicas","value":2}]'
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/pg-single \
  -n polardb-pg-ha-kb08 --timeout=20m

# 执行完整单副本到主备的验收；WITH_BACKUP=true 会在扩容前验证备份。
WITH_BACKUP=true \
  bash examples/polardb-postgresql-ha-kb08/scripts/kb08-single-to-ha-drill.sh
```

单副本运行时不要提交 Switchover、rejoin 或 rebuild 请求，因为没有 standby 候选者。

## 直接创建两副本集群

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/namespace.yaml
kubectl apply -f examples/polardb-postgresql-ha-kb08/cluster.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/pg-ha \
  -n polardb-pg-ha-kb08 --timeout=20m

kbcli cluster describe pg-ha -n polardb-pg-ha-kb08
kbcli cluster list-instances pg-ha -n polardb-pg-ha-kb08
kubectl get pod -n polardb-pg-ha-kb08 -l app.kubernetes.io/instance=pg-ha -L kubeblocks.io/role -o wide
```

确认每个 Pod 中的 Lorry 使用 PostgreSQL probe，且没有启动第二个 HA loop：

```bash
for pod in $(kubectl get pod -n polardb-pg-ha-kb08 \
  -l app.kubernetes.io/instance=pg-ha -o jsonpath='{range .items[*]}{.metadata.name}{"\\n"}{end}'); do
  kubectl exec -n polardb-pg-ha-kb08 "$pod" -c lorry -- printenv KB_BUILTIN_HANDLER
  kubectl exec -n polardb-pg-ha-kb08 "$pod" -c lorry -- printenv KB_ENABLE_HA
done
```

每个 Pod 的两行输出必须依次为 `postgresql`、`false`。检查 Patroni 角色时，可使用：

```bash
kubectl exec -n polardb-pg-ha-kb08 pg-ha-postgresql-0 -c postgresql -- \
  curl -fsS http://127.0.0.1:8008/patroni
```

## 写入与计划切换

先写入验收数据：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/smoke-write.yaml
kubectl wait --for=condition=complete job/pg-ha-smoke-write \
  -n polardb-pg-ha-kb08 --timeout=10m
```

自动选择 lag 不超过 `ha.switchover.maxLagBytes` 的候选者。Job 会等待 Patroni 角色收敛；因网络或控制面重试再次执行时，已完成的自动切换会被识别为成功，而不会反向切换：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/ops-switchover-auto.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeed opsrequest/pg-ha-switchover-auto \
  -n polardb-pg-ha-kb08 --timeout=15m
kbcli cluster describe-ops pg-ha-switchover-auto -n polardb-pg-ha-kb08
```

`kbcli` 的等价入口是 `promote`：

```bash
kbcli cluster promote pg-ha --component=postgresql --auto-approve \
  --name=pg-ha-switchover-kbcli -n polardb-pg-ha-kb08
```

指定候选者时先确认该实例当前不是 primary，再应用 [ops-switchover-candidate.yaml](../examples/polardb-postgresql-ha-kb08/ops-switchover-candidate.yaml)。切换完成后验证写服务和数据：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/smoke-read.yaml
kubectl wait --for=condition=complete job/pg-ha-smoke-read \
  -n polardb-pg-ha-kb08 --timeout=10m
```

## Rejoin 与 Rebuild

KB 0.8 没有 0.9 的 `RebuildInstance` 和 offline-instance API。因此 addon 使用 `Custom` OpsDefinition，并且只操作明确指定的 standby：

- `rejoin` 调 Patroni `POST /restart`，适用于仍可访问且数据未分叉的 standby。
- `rebuild` 调 Patroni `POST /reinitialize`，从当前 primary 重新拉取 standby 数据。它会丢弃目标 standby 的本地数据，必须显式设置 `CONFIRM_REBUILD: "true"`。

编辑 YAML 中的 `TARGET_INSTANCE` 为当前 `secondary`，不要对 primary 应用：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/ops-rejoin.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeed opsrequest/pg-ha-rejoin \
  -n polardb-pg-ha-kb08 --timeout=20m

# 仅在可丢弃目标 standby 本地数据时执行
kubectl apply -f examples/polardb-postgresql-ha-kb08/ops-rebuild.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeed opsrequest/pg-ha-rebuild \
  -n polardb-pg-ha-kb08 --timeout=25m
```

Ops Job 会拒绝 primary，并在结束前等待目标回到 `replica/running`。Pod 完全不可达、PVC 无法挂载或需要从历史备份恢复时，不能依赖这条 HTTP reinitialize 流程，应走备份恢复或人工故障处置。

## 备份与恢复

先确认 BPT 已为集群产生 BackupPolicy，且健康 secondary 存在：

```bash
kbcli cluster list-backup-policy pg-ha -n polardb-pg-ha-kb08
kubectl get backuppolicy pg-ha-polardb-pg-ha-v2-backup-policy \
  -n polardb-pg-ha-kb08
```

YAML 与 KBCLI 两种入口等价：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/backup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed backup/pg-ha-backup \
  -n polardb-pg-ha-kb08 --timeout=30m

# 或者
kbcli cluster backup pg-ha --method=pg-basebackup --name=pg-ha-backup-kbcli \
  -n polardb-pg-ha-kb08
```

恢复会创建新的 Cluster，不能覆盖源 Cluster：

```bash
kubectl apply -f examples/polardb-postgresql-ha-kb08/restore.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeed opsrequest/pg-ha-restore \
  -n polardb-pg-ha-kb08 --timeout=30m
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/pg-ha-restore \
  -n polardb-pg-ha-kb08 --timeout=30m

# 或者
kbcli cluster restore pg-ha-restore-kbcli --backup pg-ha-backup \
  --volume-restore-policy=Serial -n polardb-pg-ha-kb08

kubectl apply -f examples/polardb-postgresql-ha-kb08/smoke-restore.yaml
kubectl wait --for=condition=complete job/pg-ha-smoke-restore \
  -n polardb-pg-ha-kb08 --timeout=10m
```

## 一键功能 Drill

默认脚本只做计划切换；`rejoin` 和 `rebuild` 需要显式开启。它不会替代物理节点隔离或生产故障演练。

```bash
bash examples/polardb-postgresql-ha-kb08/scripts/kb08-patroni-ha-drill.sh

WITH_REJOIN=true \
bash examples/polardb-postgresql-ha-kb08/scripts/kb08-patroni-ha-drill.sh

# 会清空一个指定 standby 的本地数据
WITH_REBUILD=true \
bash examples/polardb-postgresql-ha-kb08/scripts/kb08-patroni-ha-drill.sh
```

## 生产边界

单副本仅适用于开发、测试或接受单点风险的场景；它不构成 HA。该 addon 在两副本以上可以提供计划内切换和 Patroni standby 恢复流程，但不单独满足“节点失联/网络分区/存储分裂下的生产自动 HA”声明。生产验收还必须具备外部 STONITH 或等价的节点与存储写入隔离、远端 BackupRepo、跨故障域调度、告警送达以及真实的 RPO/RTO 演练。详见 [技术方案](polardb-postgresql-ha-kb08-technical-solution-zh.md)。
