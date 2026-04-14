#!/bin/bash

# 检查所有正在运行的 Redis 集群连接的脚本
# 用法: ./redis.sh [namespace]
# 需要在能访问集群 Service 的环境中运行
#
# 检测项目:
#   1. 新旧密码相同性验证 (0.8 -> 0.9 升级兼容性)
#   2. 新旧 Service 连通性测试
#   3. 主从身份验证

set -o pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

TIMEOUT=5
TARGET_NS="${1:-}"

echo "=========================================="
echo "Redis Cluster Connection Checker"
echo "  - Password Consistency Verification"
echo "  - Legacy/New Service Connectivity"
echo "  - Primary Role Verification"
echo "=========================================="
echo ""

# 获取所有正在运行的 Redis 类型的 Cluster
if [ -n "$TARGET_NS" ]; then
    CLUSTERS=$(kubectl get cluster -n "$TARGET_NS" -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") | test("redis"; "i"))
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("redis"; "i"))
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
else
    CLUSTERS=$(kubectl get cluster -A -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") | test("redis"; "i"))
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") | test("redis"; "i"))
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
fi

if [ -z "$CLUSTERS" ]; then
    echo "No running Redis clusters found."
    exit 0
fi

TOTAL=0
SUCCESS=0
FAILED=0
PASSWORD_MISMATCH=0
SERVICE_MISMATCH=0

CLUSTER_COUNT=$(echo "$CLUSTERS" | wc -l | tr -d ' ')
echo "Found $CLUSTER_COUNT running Redis clusters"
echo ""
echo "=========================================="
echo ""

# 测试 Redis 连接并获取角色
test_redis_connection() {
    local host="$1"
    local port="$2"
    local password="$3"

    if [ -n "$password" ]; then
        redis-cli -h "$host" -p "$port" -a "$password" --no-auth-warning \
            INFO replication 2>&1 | grep "^role:" | cut -d':' -f2 | tr -d ' \n\r'
    else
        redis-cli -h "$host" -p "$port" \
            INFO replication 2>&1 | grep "^role:" | cut -d':' -f2 | tr -d ' \n\r'
    fi
}

# 获取 Service 的连接信息
get_service_info() {
    local ns="$1"
    local svc_name="$2"

    local host=$(kubectl get svc "$svc_name" -n "$ns" -o jsonpath='{.spec.clusterIP}' 2>/dev/null)
    local port=$(kubectl get svc "$svc_name" -n "$ns" -o jsonpath='{.spec.ports[?(@.name=="tcp-redis")].port}' 2>/dev/null)
    if [ -z "$port" ]; then
        port=$(kubectl get svc "$svc_name" -n "$ns" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)
    fi
    port=${port:-6379}

    echo "$host:$port"
}

# 遍历每个集群
while IFS= read -r cluster_info; do
    [ -z "$cluster_info" ] && continue

    TOTAL=$((TOTAL + 1))
    CLUSTER_FAILED=0

    NS=$(echo "$cluster_info" | cut -d'/' -f1)
    CLUSTER_NAME=$(echo "$cluster_info" | cut -d'/' -f2)

    echo "[$TOTAL/$CLUSTER_COUNT] ${BLUE}$NS/$CLUSTER_NAME${NC}"
    echo "-------------------------------------------"

    # ============================================
    # 1. 新旧密码相同性验证
    # ============================================
    # 新格式 secret: {cluster}-redis-account-default
    # 旧格式 secret: {cluster}-conn-credential
    NEW_SECRET="${CLUSTER_NAME}-redis-account-default"
    OLD_SECRET="${CLUSTER_NAME}-conn-credential"

    NEW_PASSWORD=$(kubectl get secret "$NEW_SECRET" -n "$NS" -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null)
    OLD_PASSWORD=$(kubectl get secret "$OLD_SECRET" -n "$NS" -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null)

    if [ -n "$NEW_PASSWORD" ] && [ -n "$OLD_PASSWORD" ]; then
        if [ "$NEW_PASSWORD" == "$OLD_PASSWORD" ]; then
            echo -e "  [Password] ${GREEN}PASS${NC} New/Old password match"
        else
            echo -e "  [Password] ${RED}FAIL${NC} New/Old password MISMATCH!"
            echo -e "             New secret: $NEW_SECRET"
            echo -e "             Old secret: $OLD_SECRET"
            CLUSTER_FAILED=1
            PASSWORD_MISMATCH=$((PASSWORD_MISMATCH + 1))
        fi
    elif [ -n "$NEW_PASSWORD" ]; then
        echo -e "  [Password] ${YELLOW}SKIP${NC} Only new secret exists ($NEW_SECRET)"
    elif [ -n "$OLD_PASSWORD" ]; then
        echo -e "  [Password] ${YELLOW}SKIP${NC} Only old secret exists ($OLD_SECRET)"
    else
        echo -e "  [Password] ${YELLOW}SKIP${NC} No password secret found"
    fi

    # 使用可用密码进行后续测试
    PASSWORD="${NEW_PASSWORD:-$OLD_PASSWORD}"

    # ============================================
    # 2. 新旧 Service 连通性测试
    # ============================================
    # 新格式 service: {cluster}-redis-redis
    # 旧格式 service: {cluster}-redis
    NEW_SVC="${CLUSTER_NAME}-redis-redis"
    OLD_SVC="${CLUSTER_NAME}-redis"

    NEW_SVC_EXISTS=$(kubectl get svc "$NEW_SVC" -n "$NS" -o jsonpath='{.metadata.name}' 2>/dev/null)
    OLD_SVC_EXISTS=$(kubectl get svc "$OLD_SVC" -n "$NS" -o jsonpath='{.metadata.name}' 2>/dev/null)

    NEW_SVC_OK=false
    OLD_SVC_OK=false
    NEW_ROLE=""
    OLD_ROLE=""

    # 测试新 Service
    if [ -n "$NEW_SVC_EXISTS" ]; then
        NEW_INFO=$(get_service_info "$NS" "$NEW_SVC")
        NEW_HOST=$(echo "$NEW_INFO" | cut -d':' -f1)
        NEW_PORT=$(echo "$NEW_INFO" | cut -d':' -f2)
        NEW_ROLE=$(test_redis_connection "$NEW_HOST" "$NEW_PORT" "$PASSWORD")

        if [ -n "$NEW_ROLE" ]; then
            NEW_SVC_OK=true
            echo -e "  [New Svc]  ${GREEN}PASS${NC} $NEW_SVC -> $NEW_HOST:$NEW_PORT (role=$NEW_ROLE)"
        else
            echo -e "  [New Svc]  ${RED}FAIL${NC} $NEW_SVC -> $NEW_HOST:$NEW_PORT (connection failed)"
            CLUSTER_FAILED=1
        fi
    fi

    # 测试旧 Service
    if [ -n "$OLD_SVC_EXISTS" ]; then
        OLD_INFO=$(get_service_info "$NS" "$OLD_SVC")
        OLD_HOST=$(echo "$OLD_INFO" | cut -d':' -f1)
        OLD_PORT=$(echo "$OLD_INFO" | cut -d':' -f2)
        OLD_ROLE=$(test_redis_connection "$OLD_HOST" "$OLD_PORT" "$PASSWORD")

        if [ -n "$OLD_ROLE" ]; then
            OLD_SVC_OK=true
            echo -e "  [Old Svc]  ${GREEN}PASS${NC} $OLD_SVC -> $OLD_HOST:$OLD_PORT (role=$OLD_ROLE)"
        else
            echo -e "  [Old Svc]  ${RED}FAIL${NC} $OLD_SVC -> $OLD_HOST:$OLD_PORT (connection failed)"
            CLUSTER_FAILED=1
        fi
    fi

    # 验证新旧 Service 都存在时的一致性
    if [ -n "$NEW_SVC_EXISTS" ] && [ -n "$OLD_SVC_EXISTS" ]; then
        if [ "$NEW_SVC_OK" = true ] && [ "$OLD_SVC_OK" = true ]; then
            if [ "$NEW_ROLE" == "$OLD_ROLE" ]; then
                echo -e "  [Svc Sync] ${GREEN}PASS${NC} Both services report same role"
            else
                echo -e "  [Svc Sync] ${RED}FAIL${NC} Role mismatch: new=$NEW_ROLE, old=$OLD_ROLE"
                CLUSTER_FAILED=1
                SERVICE_MISMATCH=$((SERVICE_MISMATCH + 1))
            fi
        else
            echo -e "  [Svc Sync] ${RED}FAIL${NC} One or both services unreachable"
            SERVICE_MISMATCH=$((SERVICE_MISMATCH + 1))
        fi
    elif [ -z "$NEW_SVC_EXISTS" ] && [ -z "$OLD_SVC_EXISTS" ]; then
        echo -e "  [Service]  ${YELLOW}SKIP${NC} No services found"
        CLUSTER_FAILED=1
    fi

    # ============================================
    # 3. 主从身份验证总结
    # ============================================
    FINAL_ROLE="${NEW_ROLE:-$OLD_ROLE}"
    if [ "$FINAL_ROLE" == "master" ]; then
        echo -e "  [Role]     ${GREEN}Primary (master)${NC}"
    elif [ "$FINAL_ROLE" == "slave" ]; then
        echo -e "  [Role]     ${RED}Replica (slave)${NC}"
        CLUSTER_FAILED=1
    else
        echo -e "  [Role]     ${RED}Unknown${NC}"
        CLUSTER_FAILED=1
    fi

    # 集群结果统计
    if [ $CLUSTER_FAILED -eq 0 ]; then
        SUCCESS=$((SUCCESS + 1))
    else
        FAILED=$((FAILED + 1))
    fi

    echo ""

done <<< "$CLUSTERS"

echo "=========================================="
echo "Summary:"
echo "  Total Clusters:    $TOTAL"
echo "  All Tests Passed:  ${GREEN}$SUCCESS${NC}"
echo "  Some Tests Failed: ${RED}$FAILED${NC}"
if [ $PASSWORD_MISMATCH -gt 0 ]; then
    echo -e "  Password Mismatch: ${RED}$PASSWORD_MISMATCH${NC}"
fi
if [ $SERVICE_MISMATCH -gt 0 ]; then
    echo -e "  Service Mismatch:  ${RED}$SERVICE_MISMATCH${NC}"
fi
echo "=========================================="
