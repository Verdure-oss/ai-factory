#!/bin/bash
# 升级脚本：导入新镜像并滚动重启服务
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-ai-factory}"

echo "=== ai-factory 升级脚本 ==="
echo ""

# 检查 kubectl
if ! command -v kubectl &> /dev/null; then
    echo "错误: kubectl 未安装"
    exit 1
fi

# 1. 导入镜像
echo "1. 导入镜像..."

import_image() {
    local tar_file=$1
    local image_name=$2

    if [ ! -f "${tar_file}" ]; then
        echo "   ⚠ ${tar_file} 未找到，跳过"
        return
    fi

    # 检测容器运行时
    if command -v ctr &> /dev/null; then
        ctr -n k8s.io images import "${tar_file}"
    elif command -v nerdctl &> /dev/null; then
        nerdctl load < "${tar_file}"
    elif command -v docker &> /dev/null; then
        docker load < "${tar_file}"
    else
        echo "   错误: 未检测到容器运行时"
        exit 1
    fi
    echo "   ✓ ${image_name}"
}

import_image "${SCRIPT_DIR}/../dist/ai-factory-server.tar" "ai-factory-server"
import_image "${SCRIPT_DIR}/../dist/coding-agent-sandbox.tar" "coding-agent-sandbox"
import_image "${SCRIPT_DIR}/../dist/agent-sandbox-controller.tar" "agent-sandbox-controller"

# 2. 滚动重启 server
echo ""
echo "2. 滚动重启 ai-factory-server..."
kubectl rollout restart deployment/ai-factory-server -n "${NAMESPACE}"
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s

# 3. 重建 sandbox warm pool pods（使用新镜像）
echo ""
echo "3. 重建 sandbox pods..."
OLD_PODS=$(kubectl get pods -n "${NAMESPACE}" -o name | grep go-dev || true)
if [ -n "${OLD_PODS}" ]; then
    echo "${OLD_PODS}" | xargs kubectl delete -n "${NAMESPACE}" 2>/dev/null || true
    sleep 10
fi
echo "   ✓ 等待 warm pool 重建..."

# 4. 显示状态
echo ""
echo "=== 升级完成 ==="
echo ""
echo "Pod 状态:"
kubectl get pods -n "${NAMESPACE}" | grep -E "go-dev|ai-factory-server"
echo ""
echo "Warm Pool 状态:"
kubectl get sandboxwarmpool -n "${NAMESPACE}" 2>/dev/null || echo "  (等待初始化)"
echo ""
echo "Webhook 地址:"
NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || echo "NODE_IP")
NODE_PORT=$(kubectl get svc ai-factory-server -n "${NAMESPACE}" -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "NODE_PORT")
echo "  http://${NODE_IP}:${NODE_PORT}/webhook/github"
echo ""
echo "检查日志:"
echo "  kubectl logs -f deployment/ai-factory-server -n ${NAMESPACE}"
