# KubeBlocks Upgrader `0.8 -> 0.9` 使用说明

本文档说明 `0.8.x -> 0.9.3` upgrader 的实际使用方式。

## 构建二进制

在仓库根目录执行下述命令进行交叉编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o upgrader ./cmd/upgrader/
```

## 拷贝到目标机器的目标目录

例如：

```bash
scp upgrader djy-kbtest:/root/djy/
```

## 添加执行权限

登录目标机器后执行：

```bash
chmod +x upgrader
```

## 执行 upgrader

例如执行 `core` 和 `dbfix`：

```bash
./upgrader -modules core,dbfix
```

## 说明

- `-modules` 使用英文逗号分隔
- 默认会强制包含 `core`
- 该使用方式适用于 `0.8.x -> 0.9.3` 升级
