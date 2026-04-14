# KubeBlocks Upgrader 工作流说明

工具定位
- 0.8.x -> 0.9.3 专用 upgrader
- 已经沉淀出一套比较清楚的“脚本式升级”骨架，后续版本可以沿着这套骨架继续写

---

## 一、启动阶段

程序入口在 [main.go](../main.go)。

启动阶段主要做 4 件事：

1. 解析命令行参数，确定要执行哪些模块
   - 参数：`-modules`
   - 当前支持：
     - `core`
     - `clickhouse`
     - `dbfix`
   - 无论是否显式指定，`core` 都会强制执行

2. 创建临时目录
   - 路径：`~/.kb-upgrader/workdir`
   - 作用：
     - 落地 `go:embed` 进去的 shell 脚本
     - 给脚本执行提供工作目录

3. 根据模块，生成步骤列表
   - 入口函数：`steps.RegisterAll(modules)`
   - 当前步骤类型大致有三类：
     - 单条 `kubectl` / `helm` 命令
     - shell 脚本执行
     - Go 代码里实现的独立逻辑

4. 打印升级 banner，进入步骤循环

当前实现**没有 progress 文件**。也就是说，它不是靠“记录上次跑到哪一步”来断点重跑，而是靠每一步自己的 `Check()` 去判断：

- 这一步是否已经达成目标状态
- 如果已达成，就跳过
- 如果未达成，就执行

这是这版 upgrader 的一个核心设计选择。

---

## 二、步骤循环

步骤循环也在 [main.go](../main.go)。

对每个步骤，固定执行下面这套逻辑：

1. 调 `step.Check()`
   - 如果返回 `skip=true`
     - 说明当前集群状态已经满足这一步的目标
     - 直接跳过
   - 如果返回错误
     - 说明前置条件检查失败
     - 直接退出

2. 如果没有跳过，则执行 `step.Run()`
   - 执行成功，继续下一步
   - 执行失败，打印错误后直接退出

3. 全部步骤执行完成后
   - 删除临时目录
   - 程序结束

### 当前步骤模型

每个步骤都实现统一接口：

```go
type Step interface {
    Name() string
    Description() string
    Check(opts RunOptions) (skip bool, err error)
    Run(opts RunOptions) error
}
```

这意味着后续如果写 `0.9 -> 1.0`，大体框架仍然建议保留：

- `Check()` 负责判断“这一步还需不需要做”
- `Run()` 负责真正执行

---

## 三、等待就绪部分

当前 upgrader 里，有两类“需要等待资源收敛”的环节。

### 1. 替换 KB 镜像后，等待 kbcontroller + install addon

这一段由：

- `patch_kb_images`
- `wait_kb_ready`

两步组成。

#### 第一步：执行镜像替换

步骤：

- `patch_kb_images`

关键函数：

- `PatchKBImages.Run()`
- `snapshotHelmTrackedAddons()`
- `patch_kb_images.sh`

工作方式：

1. 先 `List` 当前由 `helm list -n kb-system` 找到的 `kb-addon-*` 对应 Addon
2. 记录这些 Addon 名称，作为本轮待跟踪对象
3. 执行镜像替换脚本

这里记录的不是“addon phase 的快照”，而是“本轮需要跟踪哪些 addon”。

#### 第二步：等待收敛

步骤：

- `wait_kb_ready`

关键函数：

- `WaitKBReady.Check()`
- `WaitKBReady.Run()`
- `waitHelmTrackedAddonsSettled()`
- `loadHelmTrackedAddonRuntime()`

当前等待逻辑是两段：

**第一段：等 controller rollout**

使用：

- `kubectl rollout status deploy/kubeblocks`
- `kubectl rollout status deploy/kubeblocks-dataprotection`

目标是确保 controller 本身已经起来。

**第二段：等 Addon install/reconcile 收敛**

当前并不是简单看 Addon `phase`，而是同时检查：

1. Addon `status.phase` 是否回到终态
   - `Enabled`
   - `Disabled`
   - `Failed`

2. Addon `generation == observedGeneration`
   - 说明 controller 已经处理到当前版本

3. `install-<addon>-addon` 这个 Job 是否还在运行

4. 对应 Job Pod 是否还在运行

也就是说，当前 `wait_kb_ready` 的目标是：

- **controller 起来了**
- **Addon 的 install job 真正跑完了**
- **Addon 状态已经收敛**

这部分逻辑主要沉淀在 [util.go](../steps/util.go) 里，后续版本建议继续复用。

---

### 2. 修复 Redis / MySQL 后，等待集群重启收敛

这一类等待由：

- `waitDBReady()`
- `snapshotClustersByType()`
- `watchFromSnapshot()`

这套组合完成。

它的思路和 Addon 等待不一样：

#### 第一步：先快照

在执行修复脚本前，先：

- `List` 当前目标集群
- 记录资源名
- 记录 `resourceVersion`

关键函数：

- `saveSnapshot()`
- `snapshotClustersByType()`

#### 第二步：执行修复与重启

执行 shell 脚本或 Go 封装逻辑，比如：

- `runFixRedis`
- `runFixMySQLCV`
- `runFixMySQLLowercase`

#### 第三步：用 snapshot 对应的 `resourceVersion` 开始 watch

关键函数：

- `waitDBReady()`
- `watchFromSnapshot()`
- `runWatch()`

当前 watch 的终态定义是：

- `Running`
- `Stopped`

也就是说，这一套等待本质上是在确认：

- 修复脚本执行后
- Redis/MySQL Cluster 的 `status.phase`
- 是否重新回到了允许的终态

如果 `resourceVersion` 不可用，就 fallback 到重新 `List+Watch`。

---

## 四、详细步骤列表

这里按**当前代码中的真实顺序**列出。

### Core 模块

1. `preflight_core`
   - 检查：
     - `kubectl`
     - `helm`
     - `kb-system`
     - `kubeblocks` / `kubeblocks-dataprotection`
     - 核心 CRD
     - 当前 KB chart 版本

2. `annotate_addons`
   - 检查所有 Addon 是否已带 `helm.sh/resource-policy=keep`
   - 如果未带则补注解

3. `install_crds`
   - 检查新增 CRD 是否存在
   - 不存在则安装 v0.9.3 新增 CRD

4. `delete_incompatible_ops`
   - 检查目标 `OpsDefinition` 是否还存在
   - 若存在则删除

5. `helm_upgrade`
   - 检查 `kubeblocks` 的 chart 版本是否已是 `0.9.3`
   - 若不是则执行 Helm upgrade

6. `upgrade_kbcli`
   - 检查 `kbcli` 客户端版本是否已是 `0.9.3`
   - 若不是则升级

7. `patch_kb_images`
   - 检查 `kubeblocks` Deployment 里的 manager/tools 镜像是否已是目标镜像
   - 若不是则执行镜像替换

8. `wait_kb_ready`
   - 等待 controller rollout
   - 等待 install addon / reconcile 完成

### ClickHouse 模块

9. `upgrade_clickhouse`
   - 检查 clickhouse addon chart 版本是否已是 `0.9.1`
   - 若不是则升级

### DBFix 模块

10. `preflight_dbfix`
   - 检查：
     - `jq`
     - `python3 + PyYAML`
     - `mysql`
     - `psql`
     - `redis-cli`
     - `mongosh`
     - `instancesets` / `configurations` CRD

11. `fix_redis_and_wait`
   - `Check()`：检查 Redis CR 是否已经完成转换
   - `Run()`：
     - `fix_redis_check_sentinel.sh`
     - `fix_redis_cluster.sh`
     - `restart_redis.sh`
     - `waitDBReady(redis)`

12. `fix_mysql_cv_and_wait`
   - `Check()`：检查 MySQL 版本映射是否已经修好
   - 支持的版本映射（`mysqlVersionMap`）：
     - `ac-mysql-8.0.31` → `ac-mysql-8.0.30`
     - `ac-mysql-8.0.30-1` → `ac-mysql-8.0.30`
     - `mysql-5.7.42` → `mysql-5.7.44`
   - `Run()`：
     - `fix_mysql_cv.sh`
     - `restart_mysql.sh`
     - 如有需要，顺手执行一次 `fix_mysql_lowercase.sh`
     - `waitDBReady(mysql)`

13. `fix_mysql_lowercase_and_wait`
   - `Check()`：检查所有需要 `lower_case_table_names=1` 的 MySQL ConfigMap 是否都已补齐
   - `Run()`：
     - `fix_mysql_lowercase.sh`
     - `waitDBReady(mysql)`

14. `verify_pg`

15. `verify_mysql`

16. `verify_redis`

17. `verify_mongo`


---

## 五、如果后续写 `0.9 -> 1.0` 的 upgrader，该怎么接

如果后续换另外一个人写 `0.9 -> 1.0`，建议的思路不是在当前这版上继续塞判断，而是：

- **复制当前 `cmd/upgrader/` 目录**
- 继续保留这套框架
- 替换版本专属 helper 和脚本

### 建议保留的框架

这些是后续版本最值得复用的：

1. `main.go`
   - 参数解析
   - 步骤循环
   - 失败即退出

2. `preflight.go`
   - 分模块前置检查的组织方式

3. `util.go`
   - 命令执行：
     - `runCmd`
     - `kubectl`：执行并返回 `(string, error)`，失败时向上传播错误
     - `kubectlIgnoreError`：仅用于"资源不存在时返回空"这类 Run 阶段的探测，**不应在 Check 函数中使用**，否则 apiserver 短暂不可用时会被误判为"已完成跳过"
     - `runScript`
   - Addon 等待：
     - `helmTrackedAddonCRNames`
     - `snapshotHelmTrackedAddons`
     - `waitHelmTrackedAddonsSettled`
   - Cluster 等待：
     - `snapshotClustersByType`
     - `saveSnapshot`
     - `watchFromSnapshot`
     - `runWatch`

4. `steps.go` 里的组织方式
   - `patch -> wait`
   - `fix_xxx_and_wait`
   - `checkNeedsFix + runFix + wait`

### 建议后续版本重点替换的部分

这些通常都是版本专属逻辑：

1. `RegisterAll()` 中的步骤顺序
   - 下一版升级顺序大概率不同

2. 版本专属检查函数
   - 如：
     - `checkRedisNeedsFix`
     - `checkMySQLVersionNeedsFix`
     - `checkMySQLLowercaseNeedsFix`

3. 版本专属修复函数
   - 如：
     - `runFixRedis`
     - `runFixMySQLCV`
     - `runFixMySQLLowercase`

4. 顶部写死的目标值
   - 如：
     - `targetManagerImage`
     - `targetToolsImage`
     - `serviceImageUpdates`
     - `mysqlVersionMap`

5. `scripts/` 目录
   - 默认都应重审，必要时重写


