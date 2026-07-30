#!/bin/bash
# 远程部署脚本：在虚拟机上执行，导入镜像并安装 chart
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-ai-factory}"

echo "=== ai-factory 远程部署脚本 ==="
echo ""

# 检查 kubectl
if ! command -v kubectl &> /dev/null; then
    echo "错误: kubectl 未安装"
    exit 1
fi

if ! kubectl cluster-info &> /dev/null; then
    echo "错误: 无法连接到 K8s 集群"
    exit 1
fi

echo "✓ K8s 集群连接正常"
echo ""

# 1. 导入镜像
echo "1. 导入镜像..."

# 检测容器运行时
CONTAINER_RUNTIME=""
if command -v docker &> /dev/null; then
    CONTAINER_RUNTIME="docker"
elif command -v ctr &> /dev/null; then
    CONTAINER_RUNTIME="containerd"
elif command -v crictl &> /dev/null; then
    CONTAINER_RUNTIME="cri"
else
    echo "错误: 未检测到容器运行时 (docker/containerd/crictl)"
    exit 1
fi

echo "   检测到容器运行时: ${CONTAINER_RUNTIME}"

import_image() {
    local tar_file=$1
    local image_name=$2

    if [ ! -f "${tar_file}" ]; then
        echo "   ⚠ ${tar_file} 未找到，跳过"
        return
    fi

    case "${CONTAINER_RUNTIME}" in
        docker)
            docker load < "${tar_file}"
            ;;
        containerd)
            # 使用 nerdctl 或 ctr 导入
            if command -v nerdctl &> /dev/null; then
                nerdctl load < "${tar_file}"
            else
                ctr -n k8s.io images import "${tar_file}"
            fi
            ;;
        cri)
            # 使用 crictl 加载
            # 先用 ctr 导入到 containerd
            ctr -n k8s.io images import "${tar_file}"
            ;;
    esac
    echo "   ✓ ${image_name}"
}

import_image "${SCRIPT_DIR}/ai-factory-server.tar" "ai-factory-server"
import_image "${SCRIPT_DIR}/coding-agent-sandbox.tar" "coding-agent-sandbox"
import_image "${SCRIPT_DIR}/agent-sandbox-controller.tar" "agent-sandbox-controller"

# 如果是 kind 集群，需要加载到 kind
if command -v kind &> /dev/null; then
    CLUSTER_NAME=$(kind get clusters 2>/dev/null | head -1)
    if [ -n "${CLUSTER_NAME}" ]; then
        echo ""
        echo "检测到 kind 集群: ${CLUSTER_NAME}"
        echo "加载镜像到 kind..."
        kind load docker-image ai-factory-server:latest --name "${CLUSTER_NAME}" 2>/dev/null || true
        kind load docker-image coding-agent-sandbox:latest --name "${CLUSTER_NAME}" 2>/dev/null || true
        kind load docker-image ai-factory/agent-sandbox-controller:latest --name "${CLUSTER_NAME}" 2>/dev/null || true
        echo "   ✓ 镜像已加载到 kind"
    fi
fi

# 2. 安装 CRD
echo ""
echo "2. 安装 CRD..."

if [ -d "${SCRIPT_DIR}/components/factory-task" ] && [ -f "${SCRIPT_DIR}/components/factory-task/install" ]; then
    "${SCRIPT_DIR}/components/factory-task/install"
    echo "   ✓ FactoryTask CRD"
else
    echo "   ⚠ FactoryTask CRD 安装脚本未找到"
fi

if [ -d "${SCRIPT_DIR}/components/agent-sandbox" ] && [ -f "${SCRIPT_DIR}/components/agent-sandbox/install" ]; then
    # 镜像已预构建并导入，跳过构建只部署 CRD manifests
    export AGENT_SANDBOX_BUILD_IMAGES=false
    export IMAGE_PREFIX="ai-factory/"
    export IMAGE_TAG="latest"
    "${SCRIPT_DIR}/components/agent-sandbox/install"
    echo "   ✓ agent-sandbox CRD"
else
    echo "   ⚠ agent-sandbox CRD 安装脚本未找到"
fi

# 3. 收集凭证
echo ""
echo "3. 配置凭证..."
echo ""

if [ -z "${GITHUB_TOKEN:-}" ]; then
    read -p "GitHub Token: " GITHUB_TOKEN
fi

if [ -z "${WEBHOOK_SECRET:-}" ]; then
    read -p "Webhook Secret: " WEBHOOK_SECRET
fi

if [ -z "${OPENAI_API_KEY:-}" ]; then
    read -p "OpenAI API Key: " OPENAI_API_KEY
fi

# 4. 安装 Helm chart
echo ""
echo "4. 安装 Helm chart..."

CHART_PATH=""
if ls "${SCRIPT_DIR}"/ai-factory-*.tgz 1>/dev/null 2>&1; then
    CHART_PATH=$(ls "${SCRIPT_DIR}"/ai-factory-*.tgz | head -1)
elif [ -d "${SCRIPT_DIR}/ai-factory-chart" ]; then
    CHART_PATH="${SCRIPT_DIR}/ai-factory-chart"
else
    echo "错误: Helm chart 未找到"
    exit 1
fi

if ! command -v helm &> /dev/null; then
    echo "   helm 未安装，正在安装..."
    curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

echo "   使用 chart: ${CHART_PATH}"
helm upgrade --install ai-factory "${CHART_PATH}" \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}"

# 5. 等待部署完成
echo ""
echo "5. 等待部署完成..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=60s

# 6. 显示状态
echo ""
echo "=== 部署完成 ==="
echo ""
echo "Pod 状态:"
kubectl get pods -n "${NAMESPACE}"
echo ""
echo "Warm Pool 状态:"
kubectl get sandboxwarmpool -n "${NAMESPACE}" 2>/dev/null || echo "  (等待初始化)"
echo ""
echo "下一步:"
echo "  1. 暴露服务:"
echo "     kubectl port-forward --address=0.0.0.0 svc/ai-factory-server 8080:80 -n ${NAMESPACE}"
echo "     # 或配置 Ingress/NodePort 对外暴露"
echo ""
echo "  2. 检查日志:"
echo "     kubectl logs -f deployment/ai-factory-server -n ${NAMESPACE}"
echo ""
echo "  3. 配置 GitHub webhook:"
echo "     http://your-vm-ip:8080/webhook/github"
echo "     Content-Type: application/json"
echo "     Secret: 你设置的 WEBHOOK_SECRET"
echo ""
echo "  4. 给 issue 打标签触发:"
echo "     ai-factory-run   → 完整 agent 流程"
echo "     ai-factory-smoke → sandbox 环境检查"
echo ""
echo "  5. 常用排查命令:"
echo "     kubectl get pods -n ${NAMESPACE}"
echo "     kubectl get factorytasks -n ${NAMESPACE}"
echo "     kubectl get sandboxwarmpool -n ${NAMESPACE}"
echo "     kubectl logs deployment/ai-factory-server -n ${NAMESPACE} --tail=50"
