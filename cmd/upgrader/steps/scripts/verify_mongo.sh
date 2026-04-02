#!/bin/bash

# 检查所有正在运行的 MongoDB 集群连接的脚本
# 用法: ./check_mongo_clusters.sh [namespace]
# 需要在能访问集群 Service 的环境中运行（集群内或通过 VPN）

set -o pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

TIMEOUT=5
TARGET_NS="${1:-}"

echo "=========================================="
echo "MongoDB Cluster Connection Checker"
echo "=========================================="
echo ""

# 获取所有正在运行的 MongoDB 类型的 Cluster
if [ -n "$TARGET_NS" ]; then
    CLUSTERS=$(kubectl get cluster -n "$TARGET_NS" -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") == "mongodb")
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") == "mongodb")
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
else
    CLUSTERS=$(kubectl get cluster -A -o json 2>/dev/null | \
        jq -r '.items[] | select(
            ((.spec.clusterDefinitionRef // "") == "mongodb")
            or ((.metadata.labels["clusterdefinition.kubeblocks.io/name"] // "") == "mongodb")
        ) | select(.status.phase == "Running") | "\(.metadata.namespace)/\(.metadata.name)"')
fi

if [ -z "$CLUSTERS" ]; then
    echo "No running MongoDB clusters found."
    exit 0
fi

TOTAL=0
SUCCESS=0
FAILED=0

CLUSTER_COUNT=$(echo "$CLUSTERS" | wc -l | tr -d ' ')
echo "Found $CLUSTER_COUNT running MongoDB clusters"
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
    for suffix in "-conn-credential" "-mongodb-conn-credential"; do
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
    for suffix in "-mongodb" ""; do
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
    PORT=$(kubectl get svc "$SVC_NAME" -n "$NS" -o jsonpath='{.spec.ports[?(@.name=="tcp-mongodb")].port}' 2>/dev/null)
    if [ -z "$PORT" ]; then
        PORT=$(kubectl get svc "$SVC_NAME" -n "$NS" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null)
    fi

    # 默认端口
    PORT=${PORT:-27017}

    if [ -z "$USERNAME" ] || [ -z "$PASSWORD" ] || [ -z "$HOST" ]; then
        echo -e "... ${YELLOW}SKIP${NC} (missing credentials)"
        continue
    fi

    # 打印连接串
    CONN_STR="mongodb://${USERNAME}:****@${HOST}:${PORT}/admin"
    echo ""
    echo "    Connection: $CONN_STR"

    # 使用 mongosh 连接并检查状态
    RESULT=$(mongosh "mongodb://${USERNAME}:${PASSWORD}@${HOST}:${PORT}/admin?authSource=admin&directConnection=true" \
        --quiet \
        --eval "
            try {
                const status = rs.status();
                const self = status.members.find(m => m.self === true);
                if (self) {
                    print('STATE:' + self.stateStr);
                } else {
                    print('STATE:CONNECTED');
                }
            } catch(e) {
                // 可能是单节点模式
                try {
                    db.runCommand({ping: 1});
                    print('STATE:STANDALONE');
                } catch(e2) {
                    print('ERROR:' + e2.message);
                }
            }
        " 2>&1 | grep -E "^(STATE:|ERROR:)" | head -1)

    STATE=$(echo "$RESULT" | sed 's/^STATE://' | sed 's/^ERROR:/ERROR: /')

    if [ -z "$STATE" ]; then
        # 如果 mongosh 不可用，尝试用旧版 mongo 客户端
        RESULT=$(mongo "mongodb://${USERNAME}:${PASSWORD}@${HOST}:${PORT}/admin?authSource=admin&directConnection=true" \
            --quiet \
            --eval "
                try {
                    var status = rs.status();
                    var self = status.members.filter(function(m){return m.self})[0];
                    if (self) { print('STATE:' + self.stateStr); }
                    else { print('STATE:CONNECTED'); }
                } catch(e) {
                    try {
                        db.runCommand({ping: 1});
                        print('STATE:STANDALONE');
                    } catch(e2) {
                        print('ERROR:' + e2.message);
                    }
                }
            " 2>&1 | grep -E "^(STATE:|ERROR:)" | head -1)
        STATE=$(echo "$RESULT" | sed 's/^STATE://' | sed 's/^ERROR:/ERROR: /')
    fi

    case "$STATE" in
        PRIMARY)
            echo -e "    Status: ${GREEN}OK${NC} (Primary)"
            SUCCESS=$((SUCCESS + 1))
            ;;
        SECONDARY)
            echo -e "    Status: ${GREEN}OK${NC} (Secondary)"
            SUCCESS=$((SUCCESS + 1))
            ;;
        STANDALONE)
            echo -e "    Status: ${GREEN}OK${NC} (Standalone)"
            SUCCESS=$((SUCCESS + 1))
            ;;
        CONNECTED)
            echo -e "    Status: ${GREEN}OK${NC} (Connected)"
            SUCCESS=$((SUCCESS + 1))
            ;;
        RECOVERING|STARTUP|STARTUP2)
            echo -e "    Status: ${YELLOW}WARN${NC} ($STATE)"
            FAILED=$((FAILED + 1))
            ;;
        ERROR:*)
            echo -e "    Status: ${RED}FAILED${NC}"
            echo "    Error: ${STATE:0:100}"
            FAILED=$((FAILED + 1))
            ;;
        *)
            if [ -z "$STATE" ]; then
                echo -e "    Status: ${RED}FAILED${NC} (connection timeout or client not found)"
            else
                echo -e "    Status: ${YELLOW}WARN${NC} ($STATE)"
            fi
            FAILED=$((FAILED + 1))
            ;;
    esac
    echo ""

done <<< "$CLUSTERS"

echo "=========================================="
echo "Summary: Total=$TOTAL, Success=$SUCCESS, Failed=$FAILED"
echo "=========================================="