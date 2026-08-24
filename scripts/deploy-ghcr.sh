#!/bin/bash
# deploy-ghcr.sh — 从 GHCR 0→1 部署 ai-factory（无需本地 dist/*.tar）
# 对应旧脚本 scripts/deploy-remote.sh 的 GHCR 版；旧脚本原地保留。
# 用法:  VERSION=0.1.0 ./scripts/deploy-ghcr.sh
#     或 ./scripts/deploy-ghcr.sh 0.1.0
#     或 ./scripts/deploy-ghcr.sh              # 默认 latest
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-ai-factory}"
REGISTRY="${REGISTRY:-ghcr.io}"
IMAGE_PREFIX="${IMAGE_PREFIX:-ghcr.io/verdure-oss}"
# 统一去掉尾斜杠：本脚本余下部分都按 "${IMAGE_PREFIX}/name" 拼接，用户若传入
# 带尾斜杠的值（components/ 目录的约定就是带尾斜杠）会拼出双斜杠的非法镜像名。
IMAGE_PREFIX="${IMAGE_PREFIX%/}"

# 版本：参数 > 环境变量 > latest；自动去 v 前缀
VERSION="${1:-${VERSION:-${IMAGE_TAG:-latest}}}"
VERSION="${VERSION#v}"
if [[ "${VERSION}" == "latest" ]]; then
  CHART_REF="oci://${REGISTRY}/verdure-oss/charts/ai-factory"
  CHART_VERSION_ARGS=()
else
  CHART_REF="oci://${REGISTRY}/verdure-oss/charts/ai-factory"
  CHART_VERSION_ARGS=(--version "${VERSION}")
fi

echo "=== ai-factory GHCR 部署脚本 ==="
echo "    Registry: ${REGISTRY}"
echo "    Prefix:   ${IMAGE_PREFIX}"
echo "    Version:  ${VERSION}"
echo "    Chart:    ${CHART_REF} ${CHART_VERSION_ARGS[*]:-}"
echo "    Namespace:${NAMESPACE}"
echo ""

if ! command -v kubectl &>/dev/null; then echo "错误: kubectl 未安装"; exit 1; fi
if ! kubectl cluster-info &>/dev/null; then echo "错误: 无法连接到 K8s 集群"; exit 1; fi
echo "✓ K8s 集群连接正常"
echo ""

# 1. 安装 CRD
echo "1. 安装 CRD..."
if [[ -f "${SCRIPT_DIR}/../components/factory-task/crd.yaml" ]]; then
  kubectl apply -f "${SCRIPT_DIR}/../components/factory-task/crd.yaml"
  echo "   ✓ FactoryTask CRD"
elif [[ -f "${SCRIPT_DIR}/../components/factory-task/install" ]]; then
  bash "${SCRIPT_DIR}/../components/factory-task/install"
  echo "   ✓ FactoryTask CRD"
else
  echo "   ⚠ FactoryTask CRD 未找到，跳过"
fi

# agent-sandbox: 直接用 GHCR 镜像部署，不走本地构建
if [[ -f "${SCRIPT_DIR}/../components/agent-sandbox/install" ]]; then
  # components/agent-sandbox/install 要求 IMAGE_PREFIX 以 / 结尾，而本脚本余下
  # 部分（helm --set）要求不带尾斜杠。只在子进程的环境里加尾斜杠，绝不改动本
  # 脚本自己的 IMAGE_PREFIX —— 否则后面会拼出 ghcr.io/verdure-oss//ai-factory-server
  # 这种双斜杠镜像名，kubelet 报 InvalidImageName。
  AGENT_SANDBOX_BUILD_IMAGES=false \
  IMAGE_TAG="${VERSION}" \
  IMAGE_PREFIX="${IMAGE_PREFIX%/}/" \
    bash "${SCRIPT_DIR}/../components/agent-sandbox/install"
  echo "   ✓ agent-sandbox CRD (image: ${IMAGE_PREFIX%/}/agent-sandbox-controller:${VERSION})"
else
  echo "   ⚠ agent-sandbox 安装脚本未找到，跳过"
fi

# 2. 收集凭证和运行配置（与 deploy-remote.sh / upgrade.sh 同款解析，避免 source 注入）
echo ""
echo "2. 配置凭证..."
ENV_FILE="${SCRIPT_DIR}/ai-factory.env"
if [[ -f "${ENV_FILE}" ]]; then
  echo "   从 ${ENV_FILE} 加载配置..."
  while IFS= read -r line; do
    [[ -z "${line}" || "${line}" =~ ^[[:space:]]*# ]] && continue
    if [[ "${line}" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*) ]]; then
      key="${BASH_REMATCH[1]}"; value="${BASH_REMATCH[2]}"
      value="${value#\"}"; value="${value%\"}"; value="${value#\'}"; value="${value%\'}"
      export "${key}=${value}"
    fi
  done < "${ENV_FILE}"
  echo "   ✓ 已加载"
else
  echo "   配置文件不存在，将交互式收集"
fi

[[ -z "${GITHUB_TOKEN:-}" ]] && read -r -p "GitHub Token: " GITHUB_TOKEN
[[ -z "${WEBHOOK_SECRET:-}" ]] && read -r -p "Webhook Secret: " WEBHOOK_SECRET
[[ -z "${OPENAI_API_KEY:-}" ]] && read -r -p "OpenAI API Key: " OPENAI_API_KEY
[[ -z "${CODEX_API_KEY:-}" ]] && read -r -p "Codex API Key (optional): " CODEX_API_KEY
# GIT_PROVIDER 是必填项：server 启动时强制校验，chart 也会在 install 前 fail。
if [[ -z "${GIT_PROVIDER:-}" ]]; then read -r -p "Git Provider (github/gitlab) [github]: " GIT_PROVIDER; GIT_PROVIDER="${GIT_PROVIDER:-github}"; fi
if [[ "${GIT_PROVIDER}" != "github" && "${GIT_PROVIDER}" != "gitlab" ]]; then
  echo "   ✗ GIT_PROVIDER 必须是 github 或 gitlab，当前为 '${GIT_PROVIDER}'" >&2
  exit 1
fi
if [[ -z "${OPENAI_BASE_URL:-}" ]]; then read -r -p "OpenAI Base URL [https://api.openai.com/v1]: " OPENAI_BASE_URL; OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"; fi
if [[ -z "${OPENAI_MODEL:-}" ]]; then read -r -p "OpenAI Model [gpt-4.1]: " OPENAI_MODEL; OPENAI_MODEL="${OPENAI_MODEL:-gpt-4.1}"; fi
[[ -z "${AI_FACTORY_GIT_PROXY:-}" ]] && read -r -p "Git Proxy (optional): " AI_FACTORY_GIT_PROXY
[[ -z "${GITHUB_FORK_OWNER:-}" ]] && read -r -p "GitHub Fork Owner (optional): " GITHUB_FORK_OWNER
[[ -z "${GITHUB_REPOSITORY_ALLOWLIST:-}" ]] && read -r -p "Repository Allow-List (optional, comma-separated): " GITHUB_REPOSITORY_ALLOWLIST

if [[ ! -f "${ENV_FILE}" ]]; then
  cat > "${ENV_FILE}" <<EOF
# ai-factory 配置 — 自动生成于 $(date '+%Y-%m-%d %H:%M:%S')
GIT_PROVIDER=${GIT_PROVIDER}
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

# 3. 安装 Helm chart（OCI）
echo ""
echo "3. 安装 Helm chart..."
if ! command -v helm &>/dev/null; then
  echo "   helm 未安装，正在安装..."
  curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

# 若 GHCR chart 为私有，尝试用 GITHUB_TOKEN 登录（失败不阻塞公开拉取）
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  echo "${GITHUB_TOKEN}" | helm registry login "${REGISTRY}" --username "$(gh api user --jq .login 2>/dev/null || echo oauth2)" --password-stdin 2>/dev/null || \
  echo "${GITHUB_TOKEN}" | helm registry login ghcr.io --username oauth2 --password-stdin 2>/dev/null || true
fi

HELM_ARGS=(
  --set "server.image.repository=${IMAGE_PREFIX}/ai-factory-server"
  --set "server.image.tag=${VERSION}"
  --set "sandbox.image.repository=${IMAGE_PREFIX}/coding-agent-sandbox"
  --set "sandbox.image.tag=${VERSION}"
  --set "gitProvider=${GIT_PROVIDER}"
  --set "github.token=${GITHUB_TOKEN}"
  --set "webhook.secret=${WEBHOOK_SECRET}"
  --set "openai.apiKey=${OPENAI_API_KEY}"
  --set "openai.baseUrl=${OPENAI_BASE_URL}"
  --set "openai.model=${OPENAI_MODEL}"
)
[[ -n "${CODEX_API_KEY:-}" ]] && HELM_ARGS+=(--set "openai.codexApiKey=${CODEX_API_KEY}")
[[ -n "${GITLAB_TOKEN:-}" ]] && HELM_ARGS+=(--set "gitlab.token=${GITLAB_TOKEN}")
[[ -n "${GITLAB_API_BASE:-}" ]] && HELM_ARGS+=(--set "gitlab.apiBase=${GITLAB_API_BASE}")
[[ -n "${OPENAI_TEMPERATURE:-}" ]] && HELM_ARGS+=(--set "openai.temperature=${OPENAI_TEMPERATURE}")
[[ -n "${OPENAI_MAX_TOKENS:-}" ]] && HELM_ARGS+=(--set "openai.maxTokens=${OPENAI_MAX_TOKENS}")
[[ -n "${OPENAI_MAX_TOOL_ROUNDS:-}" ]] && HELM_ARGS+=(--set "openai.maxToolRounds=${OPENAI_MAX_TOOL_ROUNDS}")
[[ -n "${OPENAI_MAX_FINAL_SCRIPT_ROUNDS:-}" ]] && HELM_ARGS+=(--set "openai.maxFinalScriptRounds=${OPENAI_MAX_FINAL_SCRIPT_ROUNDS}")
[[ -n "${OPENAI_MAX_REPAIR_ROUNDS:-}" ]] && HELM_ARGS+=(--set "openai.maxRepairRounds=${OPENAI_MAX_REPAIR_ROUNDS}")
[[ -n "${OPENAI_TOTAL_TIMEOUT_SECONDS:-}" ]] && HELM_ARGS+=(--set "openai.totalTimeoutSeconds=${OPENAI_TOTAL_TIMEOUT_SECONDS}")
[[ -n "${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS:-}" ]] && HELM_ARGS+=(--set "openai.explorationRequestTimeoutSeconds=${OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS}")
[[ -n "${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS:-}" ]] && HELM_ARGS+=(--set "openai.finalRequestTimeoutSeconds=${OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS}")
[[ -n "${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS:-}" ]] && HELM_ARGS+=(--set "openai.repairRequestTimeoutSeconds=${OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS}")
[[ -n "${OPENAI_VISION_ENABLED:-}" ]] && HELM_ARGS+=(--set "openai.visionEnabled=${OPENAI_VISION_ENABLED}")
[[ -n "${AI_FACTORY_GIT_PROXY:-}" ]] && HELM_ARGS+=(--set "gitProxy=${AI_FACTORY_GIT_PROXY}")
[[ -n "${MAX_CONCURRENT_TASKS:-}" ]] && HELM_ARGS+=(--set "server.maxConcurrentTasks=${MAX_CONCURRENT_TASKS}")
[[ -n "${CI_WATCH_ENABLED:-}" ]] && HELM_ARGS+=(--set "server.ciWatchEnabled=${CI_WATCH_ENABLED}")
[[ -n "${CI_WATCH_MAX_RETRIES:-}" ]] && HELM_ARGS+=(--set "server.ciWatchMaxRetries=${CI_WATCH_MAX_RETRIES}")
[[ -n "${CI_WATCH_MAX_WAIT:-}" ]] && HELM_ARGS+=(--set "server.ciWatchMaxWait=${CI_WATCH_MAX_WAIT}")
[[ -n "${CI_WATCH_SETTLE_INTERVAL:-}" ]] && HELM_ARGS+=(--set "server.ciWatchSettleInterval=${CI_WATCH_SETTLE_INTERVAL}")
[[ -n "${CI_WATCH_MAX_TOOL_ROUNDS:-}" ]] && HELM_ARGS+=(--set "server.ciWatchMaxToolRounds=${CI_WATCH_MAX_TOOL_ROUNDS}")
[[ -n "${CI_WATCH_LOG_SNIPPET_LINES:-}" ]] && HELM_ARGS+=(--set "server.ciWatchLogSnippetLines=${CI_WATCH_LOG_SNIPPET_LINES}")
if [[ -n "${GITHUB_FORK_OWNER:-}" ]]; then HELM_ARGS+=(--set "github.forkOwner=${GITHUB_FORK_OWNER}"); fi
if [[ -n "${GITHUB_REPOSITORY_ALLOWLIST:-}" ]]; then
  IFS=',' read -ra REPOS <<< "${GITHUB_REPOSITORY_ALLOWLIST}"; idx=0
  for repo in "${REPOS[@]}"; do repo="$(echo "${repo}" | xargs)"; [[ -n "${repo}" ]] && HELM_ARGS+=(--set "github.repositoryAllowList[${idx}]=${repo}"); idx=$((idx+1)); done
fi

# shellcheck disable=SC2068
helm upgrade --install ai-factory "${CHART_REF}" \
  ${CHART_VERSION_ARGS[@]:-} \
  --namespace "${NAMESPACE}" --create-namespace \
  ${HELM_ARGS[@]}

# 4. 等待就绪
echo ""
echo "4. 等待部署完成..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s
if kubectl get deployment agent-sandbox-controller -n agent-sandbox-system &>/dev/null; then
  kubectl rollout status deployment/agent-sandbox-controller -n agent-sandbox-system --timeout=120s
  echo "   ✓ agent-sandbox 控制器已就绪"
fi

echo ""
echo "=== GHCR 部署完成 ==="
kubectl get pods -n "${NAMESPACE}"
echo ""
echo "下一步: kubectl port-forward --address=0.0.0.0 svc/ai-factory-server 8080:80 -n ${NAMESPACE}"
echo "       kubectl logs -f deployment/ai-factory-server -n ${NAMESPACE}"
