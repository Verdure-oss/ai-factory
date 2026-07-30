#!/bin/bash
# 升级脚本：导入镜像、升级 Helm chart、重建 sandbox
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
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

import_image "${ROOT_DIR}/dist/ai-factory-server.tar" "ai-factory-server"
import_image "${ROOT_DIR}/dist/coding-agent-sandbox.tar" "coding-agent-sandbox"
import_image "${ROOT_DIR}/dist/agent-sandbox-controller.tar" "agent-sandbox-controller"

# 2. 升级 Helm chart（应用新的 values.yaml 配置）
echo ""
echo "2. 升级 Helm chart..."

# 从现有 secret 读取凭证
GITHUB_TOKEN=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.GITHUB_TOKEN}' 2>/dev/null | base64 -d || echo "")
WEBHOOK_SECRET=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.WEBHOOK_SECRET}' 2>/dev/null | base64 -d || echo "")
OPENAI_API_KEY=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.OPENAI_API_KEY}' 2>/dev/null | base64 -d || echo "")
CODEX_API_KEY=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.CODEX_API_KEY}' 2>/dev/null | base64 -d || echo "")

CHART_PATH="${ROOT_DIR}/charts/ai-factory"
if [ ! -d "${CHART_PATH}" ]; then
    echo "   错误: chart 目录未找到: ${CHART_PATH}"
    exit 1
fi

helm upgrade --install ai-factory "${CHART_PATH}" \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}" \
    --set openai.codexApiKey="${CODEX_API_KEY}"

echo "   ✓ Helm chart 已升级"

# 3. 等待 server 就绪
echo ""
echo "3. 等待 ai-factory-server 就绪..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s

# 4. 重建 sandbox warm pool pods（使用新镜像）
echo ""
echo "4. 重建 sandbox pods..."
OLD_PODS=$(kubectl get pods -n "${NAMESPACE}" -o name 2>/dev/null | grep go-dev || true)
if [ -n "${OLD_PODS}" ]; then
    echo "${OLD_PODS}" | xargs kubectl delete -n "${NAMESPACE}" 2>/dev/null || true
    sleep 15
fi
echo "   ✓ 等待 warm pool 重建..."

# 5. 显示状态
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
