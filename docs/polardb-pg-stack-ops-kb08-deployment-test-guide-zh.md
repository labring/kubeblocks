# KubeBlocks 0.8 PolarDB-PG Stack HA 部署与测试指南

本指南部署的是 `polardb-pg-stack-ops`，即 KubeBlocks 0.8 到官方 PolarDB Stack
控制面的运维桥接。它要求一个由官方 Stack Operator 管理的健康共享存储
`MPDCluster`，不是用本指南创建数据库。

## 生产部署

### 前置条件

- KubeBlocks `0.8.2` 已运行。
- 已从目标 Stack release 安装 `mpdclusters.mpd.polardb.aliyun.com` CRD、官方
  Stack Operator 和 Cluster Manager。
- 已创建 `spec.dbClusterType: share` 的健康 `MPDCluster`，包含一个 RW 和至少一个
  RO，且共享存储、跨故障域和 WAL/备份策略已经过验收。
- 已准备可确认物理隔离的 STONITH provider。它不能只删除 Pod 或接受异步请求。

安装 addon：

```bash
helm upgrade --install kb-addon-polardb-pg-stack-ops \
  deploy/addons/polardb-pg-stack-ops -n kb-system --create-namespace

kubectl get componentdefinition polardb-pg-stack-v1
kubectl get opsdefinition | grep '^polardb-pg-stack-'
```

编辑 `examples/polardb-pg-stack-ops-kb08/binding-cluster.yaml` 中的两个
`REPLACE_MPDCLUSTER_*` 值，使其引用外部 Stack 创建的对象。再应用最小 RBAC 和
绑定 Cluster：

```bash
kubectl apply -f examples/polardb-pg-stack-ops-kb08/binding-rbac.yaml
kubectl apply -f examples/polardb-pg-stack-ops-kb08/binding-cluster.yaml

kubectl get cluster polardb-stack-demo -n polardb-stack-demo
kubectl get pod -n polardb-stack-demo
```

`control` 组件固定为零副本，因此不会存在由 KubeBlocks 创建的数据库 Pod；其状态
可能显示为 Stopped。这是正常现象，Custom Ops 的 Job 不依赖该副本。

默认示例将 binding 与 `MPDCluster` 放在同一命名空间。若两个对象必须跨命名空间，
在替换 `REPLACE_MPDCLUSTER_NAMESPACE` 后额外应用以下对象；它只把目标命名空间中
`MPDCluster` 的 `get`/`patch` 权限授予 binding 的 ServiceAccount：

```bash
kubectl apply -f examples/polardb-pg-stack-ops-kb08/binding-rbac-mpdcluster-namespace.yaml
```

### 操作前检查

```bash
kubectl get mpdcluster REPLACE_MPDCLUSTER_NAME \
  -n REPLACE_MPDCLUSTER_NAMESPACE \
  -o go-template='{{range $id, $instance := .status.dbInstanceStatus}}{{$id}}{{" role="}}{{$instance.role}}{{" state="}}{{$instance.currentState.state}}{{"\\n"}}{{end}}'
```

只对 `Running` 的共享存储集群执行操作，并根据输出替换 YAML 中的实例 ID。

### Switchover 与 Rejoin

```bash
kubectl apply -f examples/polardb-pg-stack-ops-kb08/ops-switchover.yaml
kubectl get opsrequest polardb-stack-switchover -n polardb-stack-demo -w

kubectl apply -f examples/polardb-pg-stack-ops-kb08/ops-rejoin.yaml
kubectl get opsrequest polardb-stack-rejoin -n polardb-stack-demo -w
```

Switchover 完成后，验证 `leaderInstanceId` 等于指定 RO ID；Rejoin 完成后，验证
`restartIns` 注解已清除且 Stack 报告健康。

### Rebuild 与 Fencing

`forceRebuild` 是整个共享存储集群的破坏性操作，只有在官方 Runbook 判断成员不能
rejoin 时才使用。YAML 中的 `CONFIRM_FORCE_REBUILD: "true"` 是强制的显式确认。

```bash
kubectl apply -f examples/polardb-pg-stack-ops-kb08/ops-rebuild.yaml
kubectl get opsrequest polardb-stack-rebuild -n polardb-stack-demo -w

kubectl apply -f examples/polardb-pg-stack-ops-kb08/stonith-secret.example.yaml
kubectl apply -f examples/polardb-pg-stack-ops-kb08/ops-fence.yaml
kubectl get opsrequest polardb-stack-fence -n polardb-stack-demo -w
```

Fencing 只能在基础设施确认 power/network/storage write fence 后才可成功。若目标是
RW，确认返回 `Running` 的新 leader 不是被 fence 的成员。没有 Secret、endpoint
超时或非 2xx 都必须使 OpsRequest 失败。

## API 合同回归

`examples/polardb-pg-stack-ops-kb08/contract-test-*` 不是数据库 HA 测试。它要求
已安装官方 `MPDCluster` CRD，但不启动 Stack Operator、Cluster Manager、数据库 Pod
或共享存储；脚本只在 `control-plane=contract-test-only` 绑定上模拟状态变化。

```bash
helm template kb-addon-polardb-pg-stack-ops deploy/addons/polardb-pg-stack-ops | kubectl apply -f -
bash examples/polardb-pg-stack-ops-kb08/scripts/run-contract-test.sh
```

预期：对非 RO 成员的 switchover 请求为 `Failed` 且不会写入 `switchRw`；合法
switchover、rejoin 和 rebuild 的 Custom Ops 为 `Succeed`；指向 `127.0.0.1:1` 的
fencing 拒绝用例为 `Failed`，且不会写入 `last-stonith` 审计注解。该结果只证明
KubeBlocks 0.8 的 API、RBAC、参数校验和 annotation contract 正确，不能替代真实
生产 HA、物理 fencing 或恢复演练。
