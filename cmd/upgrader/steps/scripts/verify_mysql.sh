#!/bin/bash

# 检查所有正在运行的 MySQL 集群连接的脚本
# 用法: ./check_mysql_clusters.sh [namespace]
# 需要在能访问集群 Service 的环境中运行

set -o pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

TIMEOUT=5
TARGET_NS="${1:-}"

echo "=========================================="
echo "MySQL Cluster Connection Checker"
echo "=========================================="
echo ""

# 获取所有正在运行的 MySQL 类型的 Cluster (包括 apecloud-mysql, mysql 等)
if [ -n "$TARGET_NS" ]; then
    CLUSTERS=$(kubectl get cluster -n "$TARGET_NS" -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") | test("mysql"; "i"))
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("mysql"; "i"))
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
else
    CLUSTERS=$(kubectl get cluster -A -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") | test("mysql"; "i"))
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("mysql"; "i"))
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
fi

if [ -z "$CLUSTERS" ]; then
    echo "No running MySQL clusters found."
    exit 0
fi

TOTAL=0
SUCCESS=0
FAILED=0

CLUSTER_COUNT=$(echo "$CLUSTERS" | wc -l | tr -d ' ')
echo "Found $CLUSTER_COUNT running MySQL clusters"
echo ""
echo "=========================================="
echo ""

# 遍历每个集群
while IFS= read -r cluster_info; do
    [ -z "$cluster_info" ] && continue

    TOTAL=$((TOTAL + 1))

    NS=$(echo "$cluster_info" | cut -d'/' -f1)
    CLUSTER_NAME=$(echo "$cluster_info" | cut -d'/' -f2)

    echo -n "[$TOTAL/$CLUSTER_COUNT] $NS/$CLUSTER_NAME "

    # 获取连接凭证 Secret
    SECRET_NAME=""
    for suffix in "-conn-credential" "-mysql-conn-credential"; do
        if kubectl get secret "${CLUSTER_NAME}${suffix}" -n "$NS" &>/dev/null; then
            SECRET_NAME="${CLUSTER_NAME}${suffix}"
            break
        fi
    done

    if [ -z "$SECRET_NAME" ]; then
        echo -e "... ${YELLOW}SKIP${NC} (no secret)"
        continue
    fi

    # 获取用户名、密码
    USERNAME=$(kubectl get secret "$SECRET_NAME" -n "$NS" -o jsonpath='{.data.username}' 2>/dev/null | base64 -d 2>/dev/null)
    PASSWORD=$(kubectl get secret "$SECRET_NAME" -n "$NS" -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null)

    # 获取 Service 的 ClusterIP 和 Port
    SVC_NAME=""
    for suffix in "-mysql" ""; do
        if kubectl get svc "${CLUSTER_NAME}${suffix}" -n "$NS" &>/dev/null; then
            SVC_NAME="${CLUSTER_NAME}${suffix}"
            break
        fi
    done

    if [ -z "$SVC_NAME" ]; then
        echo -e "... ${YELLOW}SKIP${NC} (no service)"
        continue
    fi

    # 获取 ClusterIP
    HOST=$(kubectl get svc "$SVC_NAME" -n "$NS" -o jsonpath='{.spec.clusterIP}' 2>/dev/null)

    # 获取端口
    PORT=$(kubectl get svc "$SVC_NAME" -n "$NS" -o jsonpath='{.spec.ports[?(@.name=="tcp-mysql")].port}' 2>/dev/null)
    if [ -z "$PORT" ]; then
        PORT=$(kubectl get svc "$SVC_NAME" -n "$NS" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)
    fi

    # 默认端口
    PORT=${PORT:-3306}

    if [ -z "$USERNAME" ] || [ -z "$PASSWORD" ] || [ -z "$HOST" ]; then
        echo -e "... ${YELLOW}SKIP${NC} (missing credentials)"
        continue
    fi

    # 打印连接串
    CONN_STR="mysql://${USERNAME}:****@${HOST}:${PORT}"
    echo ""
    echo "    Connection: $CONN_STR"

    # 使用 mysql 连接并执行查询，检查 read_only 状态
    # read_only=0 表示是主节点，read_only=1 表示是从节点
    # 使用 MYSQL_PWD 环境变量避免密码警告
    RESULT=$(MYSQL_PWD="$PASSWORD" mysql -h "$HOST" -P "$PORT" -u "$USERNAME" \
        --connect-timeout=$TIMEOUT \
        -N -e "SELECT @@read_only;" 2>&1 | tr -d ' \n\r')

    if [ "$RESULT" == "0" ]; then
        echo -e "    Status: ${GREEN}OK${NC} (Primary, read_only=0)"
        SUCCESS=$((SUCCESS + 1))
    elif [ "$RESULT" == "1" ]; then
        echo -e "    Status: ${RED}FAILED${NC} (Standby, read_only=1)"
        FAILED=$((FAILED + 1))
    else
        echo -e "    Status: ${RED}FAILED${NC}"
        echo "    Error: ${RESULT:0:80}"
        FAILED=$((FAILED + 1))
    fi
    echo ""

done <<< "$CLUSTERS"

echo "=========================================="
echo "Summary: Total=$TOTAL, Success=$SUCCESS, Failed=$FAILED"
echo "=========================================="
