# PolarDB-PG 生产运维：恢复演练、监控告警与发布工件

本文适用于 KubeBlocks 0.9 中真实 `polardb-pg-local-v4` 引擎的本地实例集成。
它验证真实 PolarDB-PG 内核的物理备份和隔离恢复，不将本地实例伪装成
PolarDB 共享存储高可用集群。共享存储 HA 必须由官方 PolarDB Stack Operator 和
Cluster Manager 管理，并通过 `polardb-pg-stack-ops` 对接 KubeBlocks Ops。

## 1. 恢复演练

`polardb-pg-local-v4-basebackup` ActionSet 同时包含物理备份和恢复步骤：

- 备份使用官方 `/u01/polardb_pg/bin/pg_basebackup`，流式写入外部 `BackupRepo`。
- 恢复准备 Job 从对象存储拉取归档，同时恢复新 PVC 的
  `/var/polardb/primary_datadir` 与 `/var/polardb/shared_datadir`，并校验
  `PG_VERSION`、共享存储的 `global/pg_control` 和 `polar_datadir` 配置。
- v4 运行时在恢复卷上只启动恢复后的主库；空卷仍走官方镜像入口初始化主库和
  本地演示副本，因此不会因恢复归档不包含该演示副本而误失败。

手工演练命令：

```bash
NAMESPACE=<源集群命名空间> \\
CLUSTER=<源集群名称> \\
CLEANUP=false \\
bash examples/polardb-pg/scripts/run-restore-drill.sh
```

脚本会写入唯一标记、创建新的物理备份、用 KubeBlocks `Restore` OpsRequest 创建
隔离恢复集群、核验 `SELECT version()` 包含 `PolarDB`，再核验标记行。`CLEANUP=true`
只删除临时恢复 Cluster，保留演练 Backup 作为证据。

周期演练在备份窗口之后执行：

```bash
NAMESPACE=<源集群命名空间> \\
CLUSTER=<源集群名称> \\
SCHEDULE='30 3 * * 0' \\
bash examples/polardb-pg/scripts/install-restore-drill-cronjob.sh
```

## 2. 监控告警

安装 Chart 前，将
`examples/polardb-pg/production/monitoring/victoria-metrics-kube-state-metrics-values.example.yaml`
合并到目标 VictoriaMetrics 的 kube-state-metrics 配置。这会暴露 BackupPolicy、
Backup 与 OpsRequest 的状态指标，并授予 Kube State Metrics 对这三个 CR 的最小
集群级只读权限。

然后使用生产 values 启用规则。VictoriaMetrics 环境会生成 `VMRule`，由
`VMAlert` 原生评估；Prometheus Operator 环境改为启用 `prometheusRule.enabled`：

```bash
helm upgrade --install kb-addon-polardb-pg deploy/addons/polardb-pg \\
  -n kb-system --create-namespace \\
  -f examples/polardb-pg/production/addon-values-production.example.yaml
```

内置规则覆盖：备份策略不可用、物理备份失败、恢复演练 Job 失败，以及
PolarDB Stack 的 switchover/rejoin/rebuild/fence OpsRequest 失败。对 Custom
OpsRequest，规则按 `spec.custom.opsDefinitionName` 匹配，而不是笼统的 `type: Custom`。
`VMRule` 或
`PrometheusRule` 存在不等于告警已送达；上线门禁必须包含一次受控失败告警的接收和确认记录。
建议使用隔离的测试 Stack 集群和故意拒绝请求的 STONITH endpoint，短暂把
`alertRule.opsFailureMinutes` 设为 `0` 验证 `PolarDBPGStackOperationFailed`，随后
立即恢复生产阈值 `5`。

## 3. 正式 Release 工件

当前 release bundle 的 Charts、引擎镜像与 Stack Ops 工具镜像都以 digest 固定。
构建时会渲染 Chart 并拒绝任何非 digest 镜像引用，生成两个 Chart 包、包含恢复演练
脚本/监控 values/Stack Ops 示例的 `polardb-pg-production-assets-*.tgz`、`SHA256SUMS`
及 `release-manifest.yaml`：

```bash
RELEASE_VERSION=0.3.3 scripts/release/package-polardb-pg.sh
scripts/release/verify-polardb-pg-release.sh dist/polardb-pg-v0.3.3
```

`.github/workflows/release-polardb-pg-artifacts.yml` 使用 GitHub OIDC 对
`SHA256SUMS` 生成 Sigstore bundle；该校验清单覆盖 Chart、production assets、镜像
清单和 release manifest。在 GitHub Release 发布事件中会把完整 bundle 作为 release
asset 上传。部署前应校验 SHA256 与 Sigstore 身份，并在变更记录中
同时固定 KubeBlocks manager、data-protection、datasafed、addon charts 与数据库
镜像的 digest。
