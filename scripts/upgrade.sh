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

# 2. 加载配置（优先从 ai-factory.env，否则从现有 K8s secret）
echo ""
echo "2. 加载配置..."

ENV_FILE="${ROOT_DIR}/scripts/ai-factory.env"

# 从现有 K8s secret 读取凭证（作为 fallback）
GITHUB_TOKEN=""
WEBHOOK_SECRET=""
OPENAI_API_KEY=""
CODEX_API_KEY=""

if [ -f "${ENV_FILE}" ]; then
    echo "   从 ${ENV_FILE} 加载配置..."
    # 解析 .env 文件，跳过注释和空行
    while IFS= read -r line; do
        [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
        if [[ "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*) ]]; then
            key="${BASH_REMATCH[1]}"
            value="${BASH_REMATCH[2]}"
            value="${value#\"}"
            value="${value%\"}"
            value="${value#\'}"
            value="${value%\'}"
            export "${key}=${value}"
        fi
    done < "${ENV_FILE}"
    echo "   ✓ 配置已加载"
fi

# 凭证：env 文件优先，否则从 K8s secret 读取
if [ -z "${GITHUB_TOKEN}" ]; then
    GITHUB_TOKEN=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.GITHUB_TOKEN}' 2>/dev/null | base64 -d || echo "")
fi
if [ -z "${WEBHOOK_SECRET}" ]; then
    WEBHOOK_SECRET=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.WEBHOOK_SECRET}' 2>/dev/null | base64 -d || echo "")
fi
if [ -z "${OPENAI_API_KEY}" ]; then
    OPENAI_API_KEY=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.OPENAI_API_KEY}' 2>/dev/null | base64 -d || echo "")
fi
if [ -z "${CODEX_API_KEY}" ]; then
    CODEX_API_KEY=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.CODEX_API_KEY}' 2>/dev/null | base64 -d || echo "")
fi

# 3. 升级 Helm chart
echo ""
echo "3. 升级 Helm chart..."

CHART_PATH="${ROOT_DIR}/charts/ai-factory"
if [ ! -d "${CHART_PATH}" ]; then
    echo "   错误: chart 目录未找到: ${CHART_PATH}"
    exit 1
fi

HELM_ARGS=(
    --set github.token="${GITHUB_TOKEN}"
    --set webhook.secret="${WEBHOOK_SECRET}"
    --set openai.apiKey="${OPENAI_API_KEY}"
    --set openai.codexApiKey="${CODEX_API_KEY}"
)

# 模型配置：env 文件有值则传给 helm，否则使用 chart 默认值
[ -n "${OPENAI_BASE_URL:-}" ] && HELM_ARGS+=(--set openai.baseUrl="${OPENAI_BASE_URL}")
[ -n "${OPENAI_MODEL:-}" ] && HELM_ARGS+=(--set openai.model="${OPENAI_MODEL}")
[ -n "${OPENAI_TEMPERATURE:-}" ] && HELM_ARGS+=(--set openai.temperature="${OPENAI_TEMPERATURE}")
[ -n "${OPENAI_MAX_TOKENS:-}" ] && HELM_ARGS+=(--set openai.maxTokens="${OPENAI_MAX_TOKENS}")
[ -n "${OPENAI_MAX_TOOL_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxToolRounds="${OPENAI_MAX_TOOL_ROUNDS}")
[ -n "${OPENAI_MAX_FINAL_SCRIPT_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxFinalScriptRounds="${OPENAI_MAX_FINAL_SCRIPT_ROUNDS}")
[ -n "${OPENAI_MAX_REPAIR_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxRepairRounds="${OPENAI_MAX_REPAIR_ROUNDS}")
[ -n "${OPENAI_TOTAL_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.totalTimeoutSeconds="${OPENAI_TOTAL_TIMEOUT_SECONDS}")
[ -n "${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.explorationRequestTimeoutSeconds="${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS}")
[ -n "${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.finalRequestTimeoutSeconds="${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS}")
[ -n "${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.repairRequestTimeoutSeconds="${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS}")
[ -n "${AI_FACTORY_GIT_PROXY:-}" ] && HELM_ARGS+=(--set gitProxy="${AI_FACTORY_GIT_PROXY}")
[ -n "${MAX_CONCURRENT_TASKS:-}" ] && HELM_ARGS+=(--set server.maxConcurrentTasks="${MAX_CONCURRENT_TASKS}")
[ -n "${CI_WATCH_ENABLED:-}" ] && HELM_ARGS+=(--set server.ciWatchEnabled="${CI_WATCH_ENABLED}")
[ -n "${CI_WATCH_MAX_RETRIES:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxRetries="${CI_WATCH_MAX_RETRIES}")
[ -n "${CI_WATCH_MAX_WAIT:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxWait="${CI_WATCH_MAX_WAIT}")
[ -n "${CI_WATCH_RETRY_INTERVAL:-}" ] && HELM_ARGS+=(--set server.ciWatchPollInterval="${CI_WATCH_RETRY_INTERVAL}")

helm upgrade --install ai-factory "${CHART_PATH}" \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    "${HELM_ARGS[@]}"

echo "   ✓ Helm chart 已升级"

# 4. 等待 server 就绪
echo ""
echo "4. 等待 ai-factory-server 就绪..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s

# 4.1 强制重建 server pod 以加载刚导入的新镜像
# 镜像 tag 固定为 :latest 且 imagePullPolicy 为 IfNotPresent 时,helm upgrade
# 不会因为镜像字符串未变化而重建 pod,导致新编译的 Go 代码(例如 plan.go 的
# push --force 修复)不生效。rollout restart 强制重建,让 kubelet 重新解析
# 本地刚 load/import 的 latest 镜像。
echo ""
echo "4.1 强制重建 ai-factory-server pod..."
kubectl rollout restart deployment/ai-factory-server -n "${NAMESPACE}"
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s
echo "   ✓ ai-factory-server 已重建"

# 5. 更新 agent-sandbox 控制器镜像（确保使用新版本）
echo ""
echo "5. 更新 agent-sandbox 控制器..."
if kubectl get deployment agent-sandbox-controller -n agent-sandbox-system &>/dev/null; then
    # 更新镜像（不只是重启）
    kubectl set image deployment/agent-sandbox-controller \
        agent-sandbox-controller=ai-factory/agent-sandbox-controller:latest \
        -n agent-sandbox-system
    kubectl rollout status deployment/agent-sandbox-controller -n agent-sandbox-system --timeout=120s
    echo "   ✓ agent-sandbox 控制器已更新"
else
    echo "   ⚠ agent-sandbox 控制器未找到，跳过"
fi

# 6. 删除旧的 SandboxClaim（让新控制器重新创建，正确注入环境变量）
echo ""
echo "6. 清理旧的 SandboxClaim..."
kubectl delete sandboxclaim -n "${NAMESPACE}" --all 2>/dev/null || true
echo "   ✓ 旧的 SandboxClaim 已删除"

# 7. 重建 sandbox warm pool pods（使用新镜像）
echo ""
echo "7. 重建 sandbox pods..."
OLD_PODS=$(kubectl get pods -n "${NAMESPACE}" -o name 2>/dev/null | grep go-dev || true)
if [ -n "${OLD_PODS}" ]; then
    echo "${OLD_PODS}" | xargs kubectl delete -n "${NAMESPACE}" 2>/dev/null || true
    sleep 15
fi
echo "   ✓ 等待 warm pool 重建..."

# 8. 显示状态
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
