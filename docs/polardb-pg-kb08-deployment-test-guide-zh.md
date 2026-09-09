# KubeBlocks 0.8 PolarDB-PG 部署与测试指南

本 addon 使用 `polardb/polardb_pg_local_instance`。它在 KubeBlocks 0.8 上提供 PolarDB-PG local instance、物理备份恢复和基础生命周期操作，但当前固定单副本 localfs，不能被描述为 PolarDB-PG 生产高可用部署。

需要真实共享存储 PolarDB-PG HA 时，使用独立的 `polardb-pg-stack-ops` addon 对接官方 Stack Operator 和 Cluster Manager。它不会复用或扩容本 addon 的 localfs PVC；部署边界见 [Stack HA 技术方案](polardb-pg-stack-ops-kb08-technical-solution-zh.md)。

## 安装与创建

```bash
helm template kb-addon-polardb-pg deploy/addons/polardb-pg | kubectl apply -f -
kubectl create namespace polardb-kb08-e2e
kubectl apply -f examples/polardb-pg-kb08/cluster.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Running cluster/polardb-pg-kb08 -n polardb-kb08-e2e --timeout=15m
```

`deploy/addons/polardb-pg/values.yaml` 中的 `backup.replicationHbaCIDR` 测试默认值为 `0.0.0.0/0`，生产必须改为实际 Pod CIDR。镜像需要 `privileged` 和 `SYS_PTRACE`，部署前应按所在集群的 PodSecurity 标准完成审计。

## 写入和物理备份恢复

```bash
kubectl apply -f examples/polardb-pg-kb08/smoke-source.yaml
kubectl wait --for=condition=complete job/polardb-pg-kb08-smoke-source-v2 -n polardb-kb08-e2e --timeout=10m
kubectl logs job/polardb-pg-kb08-smoke-source-v2 -n polardb-kb08-e2e

kubectl apply -f examples/polardb-pg-kb08/backup.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Completed backup/polardb-pg-kb08-backup-v2 -n polardb-kb08-e2e --timeout=20m
kubectl get backup polardb-pg-kb08-backup-v2 -n polardb-kb08-e2e
```

KBCLI 原生恢复路径会创建目标 Cluster 和 Restore OpsRequest：

```bash
kbcli cluster restore polardb-pg-kb08-restore-v3 --backup polardb-pg-kb08-backup-v2 --volume-restore-policy Serial -n polardb-kb08-e2e
kubectl wait --for=jsonpath='{.status.phase}'=Succeed opsrequest/polardb-pg-kb08-restore-v3 -n polardb-kb08-e2e --timeout=20m
kubectl apply -f examples/polardb-pg-kb08/smoke-restore.yaml
kubectl logs job/polardb-pg-kb08-smoke-restore-v4 -n polardb-kb08-e2e
```

`examples/polardb-pg-kb08/restore.yaml` 是等价的 `OpsRequest` 模板，适用于已经按相同 ComponentDefinition 创建的目标 Cluster。不要使用旧式“直接创建 Cluster + PVC Restore”路径；KB 0.8 的正确路径是 Restore OpsRequest，它会在组件启动前创建数据恢复任务。

## 重启与读取验证

```bash
kbcli cluster restart polardb-pg-kb08 --components=polardb --auto-approve --name=polardb-pg-kb08-restart-v1 -n polardb-kb08-e2e
kbcli cluster describe-ops polardb-pg-kb08-restart-v1 -n polardb-kb08-e2e
kubectl create -o name -f examples/polardb-pg-kb08/smoke-read.yaml
kubectl logs job/<returned-job-name> -n polardb-kb08-e2e
```

读取 Job 断言 `polar_deploy_mode=OPEN_SOURCE` 且 `kb08_restore_check` 仍为 `2:alpha,beta-updated`。

## 已在 192.168.10.85 验证的结果

测试环境为 KB `0.8.2`、Kubernetes `1.28.15`、单节点 `openebs-hostpath`。2026-09-07 已完成实例创建、`OPEN_SOURCE` 引擎确认、表/索引/增删改、`pg_basebackup --polardata` 备份、Restore OpsRequest 恢复、恢复后数据验证和单副本滚动重启。备份完成后系统账号密文随 Backup 保存，恢复后的目标账号可直接用于连接验证。

本测试不覆盖共享存储多副本、高可用主备切换、跨节点故障恢复、在线卷扩容、滚动版本升级和生产级性能压测。要达到这些目标，必须接入实际的 PolarDB-PG 分布式存储/控制面，并在多节点集群上单独验收；不能仅凭本 addon 的单副本恢复能力宣称生产高可用。
