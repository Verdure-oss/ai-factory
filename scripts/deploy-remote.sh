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
    bash "${SCRIPT_DIR}/components/factory-task/install"
    echo "   ✓ FactoryTask CRD"
else
    echo "   ⚠ FactoryTask CRD 安装脚本未找到"
fi

if [ -d "${SCRIPT_DIR}/components/agent-sandbox" ] && [ -f "${SCRIPT_DIR}/components/agent-sandbox/install" ]; then
    # 镜像已预构建并导入，跳过构建只部署 CRD manifests
    export AGENT_SANDBOX_BUILD_IMAGES=false
    export IMAGE_PREFIX="ai-factory/"
    export IMAGE_TAG="latest"
    bash "${SCRIPT_DIR}/components/agent-sandbox/install"
    echo "   ✓ agent-sandbox CRD"
else
    echo "   ⚠ agent-sandbox CRD 安装脚本未找到"
fi

# 2.1 修正 agent-sandbox 控制器镜像拉取策略
# 上游 deploy-to-kube 生成的控制器 Deployment 对 :latest 镜像默认
# imagePullPolicy=Always：即使本地 containerd 已导入镜像，kubelet 也会先尝试
# 从 docker.io 拉取（离线/受限网络必然超时 → ImagePullBackOff）。
# 这里做两件事，让控制器像 ai-factory-server 一样直接使用本地镜像：
#   1) 若本地缺少控制器镜像引用，从打包 tar 重新导入；
#   2) 将 imagePullPolicy 改为 IfNotPresent。
echo ""
echo "2.1 修正 agent-sandbox 控制器镜像拉取策略..."
CTRL_IMAGE="ai-factory/agent-sandbox-controller:latest"
if command -v ctr &> /dev/null; then
    if ! ctr -n k8s.io images ls 2>/dev/null | grep -q "${CTRL_IMAGE}"; then
        echo "   本地缺少控制器镜像 ${CTRL_IMAGE}，重新导入..."
        ctr -n k8s.io images import "${SCRIPT_DIR}/agent-sandbox-controller.tar" || echo "   ⚠ 控制器镜像导入失败"
    fi
fi
if kubectl get deployment agent-sandbox-controller -n agent-sandbox-system &>/dev/null; then
    kubectl -n agent-sandbox-system patch deployment agent-sandbox-controller \
        -p '{"spec":{"template":{"spec":{"containers":[{"name":"agent-sandbox-controller","imagePullPolicy":"IfNotPresent"}]}}}}'
    echo "   ✓ agent-sandbox 控制器 imagePullPolicy=IfNotPresent"
else
    echo "   ⚠ agent-sandbox 控制器未找到，跳过"
fi

# 3. 收集凭证和运行配置
echo ""
echo "3. 配置凭证..."
echo ""

# 优先从 ai-factory.env 加载。只解析 KEY=VALUE，避免直接 source
# 配置文件中的值被当作 shell 代码执行；其余缺失项再交互式收集。
ENV_FILE="${SCRIPT_DIR}/ai-factory.env"
if [ -f "${ENV_FILE}" ]; then
    echo "   从 ${ENV_FILE} 加载配置..."
    while IFS= read -r line; do
        [[ -z "${line}" || "${line}" =~ ^[[:space:]]*# ]] && continue
        if [[ "${line}" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*) ]]; then
            key="${BASH_REMATCH[1]}"
            value="${BASH_REMATCH[2]}"
            value="${value#\"}"
            value="${value%\"}"
            value="${value#\'}"
            value="${value%\'}"
            export "${key}=${value}"
        fi
    done < "${ENV_FILE}"
    echo "   ✓ 已加载"
else
    echo "   配置文件不存在，将交互式收集"
fi

if [ -z "${GITHUB_TOKEN:-}" ]; then
    read -r -p "GitHub Token: " GITHUB_TOKEN
fi

if [ -z "${WEBHOOK_SECRET:-}" ]; then
    read -r -p "Webhook Secret: " WEBHOOK_SECRET
fi

if [ -z "${OPENAI_API_KEY:-}" ]; then
    read -r -p "OpenAI API Key: " OPENAI_API_KEY
fi

if [ -z "${CODEX_API_KEY:-}" ]; then
    read -r -p "Codex API Key (optional): " CODEX_API_KEY
fi

# 收集可选配置；其余 OPENAI_* 和 CI_WATCH_* 参数从 env 文件读取，
# 未配置时交给 Helm chart 默认值。
if [ -z "${OPENAI_BASE_URL:-}" ]; then
    read -r -p "OpenAI Base URL [https://api.openai.com/v1]: " OPENAI_BASE_URL
    OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
fi

if [ -z "${OPENAI_MODEL:-}" ]; then
    read -r -p "OpenAI Model [gpt-4.1]: " OPENAI_MODEL
    OPENAI_MODEL="${OPENAI_MODEL:-gpt-4.1}"
fi

if [ -z "${AI_FACTORY_GIT_PROXY:-}" ]; then
    read -r -p "Git Proxy (optional, e.g. https://ghproxy.net): " AI_FACTORY_GIT_PROXY
fi

# Fork PR 工作流（可选）：向公共上游仓库提 PR 时，指定 fork 的所有者
if [ -z "${GITHUB_FORK_OWNER:-}" ]; then
    read -r -p "GitHub Fork Owner (optional, for fork PR workflow): " GITHUB_FORK_OWNER
fi

# 仓库 allow-list（可选）：限制哪些 owner/repo 允许触发，逗号分隔
if [ -z "${GITHUB_REPOSITORY_ALLOWLIST:-}" ]; then
    read -r -p "GitHub Repository Allow-List (optional, comma-separated owner/repo): " GITHUB_REPOSITORY_ALLOWLIST
fi

# 保存到 env 文件（首次部署时创建，方便后续热更新）
if [ ! -f "${ENV_FILE}" ]; then
    cat > "${ENV_FILE}" <<EOF
# ai-factory 配置文件 — 自动生成于 $(date '+%Y-%m-%d %H:%M:%S')
# 修改后运行 ./update-config.sh 即可热更新（无需重启 Pod）

GITHUB_TOKEN=${GITHUB_TOKEN}
WEBHOOK_SECRET=${WEBHOOK_SECRET}
OPENAI_API_KEY=${OPENAI_API_KEY}
CODEX_API_KEY=${CODEX_API_KEY:-}
GITLAB_TOKEN=${GITLAB_TOKEN:-}
OPENAI_BASE_URL=${OPENAI_BASE_URL}
OPENAI_MODEL=${OPENAI_MODEL}
OPENAI_TEMPERATURE=${OPENAI_TEMPERATURE:-}
OPENAI_MAX_TOKENS=${OPENAI_MAX_TOKENS:-}
OPENAI_MAX_TOOL_ROUNDS=${OPENAI_MAX_TOOL_ROUNDS:-}
OPENAI_MAX_FINAL_SCRIPT_ROUNDS=${OPENAI_MAX_FINAL_SCRIPT_ROUNDS:-}
OPENAI_MAX_REPAIR_ROUNDS=${OPENAI_MAX_REPAIR_ROUNDS:-}
OPENAI_TOTAL_TIMEOUT_SECONDS=${OPENAI_TOTAL_TIMEOUT_SECONDS:-}
OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS=${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS:-}
OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS=${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS:-}
OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS=${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS:-}
OPENAI_VISION_ENABLED=${OPENAI_VISION_ENABLED:-}
AI_FACTORY_GIT_PROXY=${AI_FACTORY_GIT_PROXY:-}
MAX_CONCURRENT_TASKS=${MAX_CONCURRENT_TASKS:-}
CI_WATCH_ENABLED=${CI_WATCH_ENABLED:-}
CI_WATCH_MAX_RETRIES=${CI_WATCH_MAX_RETRIES:-}
CI_WATCH_MAX_WAIT=${CI_WATCH_MAX_WAIT:-}
CI_WATCH_SETTLE_INTERVAL=${CI_WATCH_SETTLE_INTERVAL:-}
CI_WATCH_MAX_TOOL_ROUNDS=${CI_WATCH_MAX_TOOL_ROUNDS:-}
CI_WATCH_LOG_SNIPPET_LINES=${CI_WATCH_LOG_SNIPPET_LINES:-}
GITHUB_FORK_OWNER=${GITHUB_FORK_OWNER:-}
GITHUB_REPOSITORY_ALLOWLIST=${GITHUB_REPOSITORY_ALLOWLIST:-}
EOF
    echo "   ✓ 配置已保存到 ${ENV_FILE}"
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

# 构建 helm 参数。与 scripts/upgrade.sh 保持相同的运行配置入口。
HELM_ARGS=(
    --set github.token="${GITHUB_TOKEN}"
    --set webhook.secret="${WEBHOOK_SECRET}"
    --set openai.apiKey="${OPENAI_API_KEY}"
    --set openai.baseUrl="${OPENAI_BASE_URL}"
    --set openai.model="${OPENAI_MODEL}"
)

[ -n "${CODEX_API_KEY:-}" ] && HELM_ARGS+=(--set openai.codexApiKey="${CODEX_API_KEY}")
[ -n "${GITLAB_TOKEN:-}" ] && HELM_ARGS+=(--set gitlab.token="${GITLAB_TOKEN}")
[ -n "${OPENAI_TEMPERATURE:-}" ] && HELM_ARGS+=(--set openai.temperature="${OPENAI_TEMPERATURE}")
[ -n "${OPENAI_MAX_TOKENS:-}" ] && HELM_ARGS+=(--set openai.maxTokens="${OPENAI_MAX_TOKENS}")
[ -n "${OPENAI_MAX_TOOL_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxToolRounds="${OPENAI_MAX_TOOL_ROUNDS}")
[ -n "${OPENAI_MAX_FINAL_SCRIPT_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxFinalScriptRounds="${OPENAI_MAX_FINAL_SCRIPT_ROUNDS}")
[ -n "${OPENAI_MAX_REPAIR_ROUNDS:-}" ] && HELM_ARGS+=(--set openai.maxRepairRounds="${OPENAI_MAX_REPAIR_ROUNDS}")
[ -n "${OPENAI_TOTAL_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.totalTimeoutSeconds="${OPENAI_TOTAL_TIMEOUT_SECONDS}")
[ -n "${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.explorationRequestTimeoutSeconds="${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS}")
[ -n "${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.finalRequestTimeoutSeconds="${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS}")
[ -n "${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS:-}" ] && HELM_ARGS+=(--set openai.repairRequestTimeoutSeconds="${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS}")
[ -n "${OPENAI_VISION_ENABLED:-}" ] && HELM_ARGS+=(--set openai.visionEnabled="${OPENAI_VISION_ENABLED}")
[ -n "${AI_FACTORY_GIT_PROXY:-}" ] && HELM_ARGS+=(--set gitProxy="${AI_FACTORY_GIT_PROXY}")
[ -n "${MAX_CONCURRENT_TASKS:-}" ] && HELM_ARGS+=(--set server.maxConcurrentTasks="${MAX_CONCURRENT_TASKS}")
[ -n "${CI_WATCH_ENABLED:-}" ] && HELM_ARGS+=(--set server.ciWatchEnabled="${CI_WATCH_ENABLED}")
[ -n "${CI_WATCH_MAX_RETRIES:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxRetries="${CI_WATCH_MAX_RETRIES}")
[ -n "${CI_WATCH_MAX_WAIT:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxWait="${CI_WATCH_MAX_WAIT}")
[ -n "${CI_WATCH_SETTLE_INTERVAL:-}" ] && HELM_ARGS+=(--set server.ciWatchSettleInterval="${CI_WATCH_SETTLE_INTERVAL}")
[ -n "${CI_WATCH_MAX_TOOL_ROUNDS:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxToolRounds="${CI_WATCH_MAX_TOOL_ROUNDS}")
[ -n "${CI_WATCH_LOG_SNIPPET_LINES:-}" ] && HELM_ARGS+=(--set server.ciWatchLogSnippetLines="${CI_WATCH_LOG_SNIPPET_LINES}")

if [ -n "${GITHUB_FORK_OWNER:-}" ]; then
    HELM_ARGS+=(--set github.forkOwner="${GITHUB_FORK_OWNER}")
fi

# 仓库 allow-list：逗号分隔 → 多个 --set github.repositoryAllowList[i]
if [ -n "${GITHUB_REPOSITORY_ALLOWLIST:-}" ]; then
    IFS=',' read -ra REPOS <<< "${GITHUB_REPOSITORY_ALLOWLIST}"
    idx=0
    for repo in "${REPOS[@]}"; do
        repo="$(echo "${repo}" | xargs)"  # trim whitespace
        [ -n "${repo}" ] && HELM_ARGS+=(--set "github.repositoryAllowList[${idx}]=${repo}")
        idx=$((idx + 1))
    done
fi

helm upgrade --install ai-factory "${CHART_PATH}" \
    --namespace "${NAMESPACE}" \
    --create-namespace \
    "${HELM_ARGS[@]}"

# 5. 等待部署完成
echo ""
echo "5. 等待部署完成..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=60s

# 等待 agent-sandbox 控制器完成首次部署；首次安装无需重启旧控制器。
echo ""
echo "5.1 等待 agent-sandbox 控制器..."
if kubectl get deployment agent-sandbox-controller -n agent-sandbox-system &>/dev/null; then
    kubectl rollout status deployment/agent-sandbox-controller -n agent-sandbox-system --timeout=60s
    echo "   ✓ agent-sandbox 控制器已就绪"
else
    echo "   ⚠ agent-sandbox 控制器未找到"
fi

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
echo ""
echo "  6. 热更新配置（无需重启 Pod）:"
echo "     vim ai-factory.env     # 修改配置"
echo "     ./update-config.sh     # 同步到 K8s，~30s 后自动生效"
