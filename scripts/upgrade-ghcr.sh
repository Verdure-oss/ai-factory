#!/bin/bash
# upgrade-ghcr.sh — 从 GHCR 升级已部署的 ai-factory
# 对应旧脚本 scripts/upgrade.sh 的 GHCR 版；旧脚本原地保留。
# 用法:  VERSION=0.1.0 ./scripts/upgrade-ghcr.sh
#     或 ./scripts/upgrade-ghcr.sh 0.1.0
#     或 ./scripts/upgrade-ghcr.sh            # 默认 latest
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-ai-factory}"
REGISTRY="${REGISTRY:-ghcr.io}"
IMAGE_PREFIX="${IMAGE_PREFIX:-ghcr.io/verdure-oss}"
# 统一去掉尾斜杠：下面都按 "${IMAGE_PREFIX}/name" 拼接，带尾斜杠会拼出
# ghcr.io/verdure-oss//name 这种非法镜像名（kubelet 报 InvalidImageName）。
IMAGE_PREFIX="${IMAGE_PREFIX%/}"
VERSION="${1:-${VERSION:-${IMAGE_TAG:-latest}}}"
VERSION="${VERSION#v}"
if [[ "${VERSION}" == "latest" ]]; then
  CHART_REF="oci://${REGISTRY}/verdure-oss/charts/ai-factory"
  CHART_VERSION_ARGS=()
else
  CHART_REF="oci://${REGISTRY}/verdure-oss/charts/ai-factory"
  CHART_VERSION_ARGS=(--version "${VERSION}")
fi

echo "=== ai-factory GHCR 升级脚本 ==="
echo "    Registry: ${REGISTRY}"
echo "    Prefix:   ${IMAGE_PREFIX}"
echo "    Version:  ${VERSION}"
echo "    Chart:    ${CHART_REF} ${CHART_VERSION_ARGS[*]:-}"
echo "    Namespace:${NAMESPACE}"
echo ""

if ! command -v kubectl &>/dev/null; then echo "错误: kubectl 未安装"; exit 1; fi

# 1. 加载配置（ai-factory.env 优先，否则从 K8s secret 回退）
echo "1. 加载配置..."
ENV_FILE="${SCRIPT_DIR}/ai-factory.env"
GITHUB_TOKEN=""; WEBHOOK_SECRET=""; OPENAI_API_KEY=""; CODEX_API_KEY=""
if [[ -f "${ENV_FILE}" ]]; then
  echo "   从 ${ENV_FILE} 加载..."
  while IFS= read -r line; do
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    if [[ "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*) ]]; then
      key="${BASH_REMATCH[1]}"; value="${BASH_REMATCH[2]}"
      value="${value#\"}"; value="${value%\"}"; value="${value#\'}"; value="${value%\'}"
      export "${key}=${value}"
    fi
  done < "${ENV_FILE}"
  echo "   ✓ 已加载"
fi
[[ -z "${GITHUB_TOKEN:-}" ]] && GITHUB_TOKEN="$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.GITHUB_TOKEN}' 2>/dev/null | base64 -d || echo "")"
[[ -z "${WEBHOOK_SECRET:-}" ]] && WEBHOOK_SECRET="$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.WEBHOOK_SECRET}' 2>/dev/null | base64 -d || echo "")"
[[ -z "${OPENAI_API_KEY:-}" ]] && OPENAI_API_KEY="$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.OPENAI_API_KEY}' 2>/dev/null | base64 -d || echo "")"
[[ -z "${CODEX_API_KEY:-}" ]] && CODEX_API_KEY="$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" -o jsonpath='{.data.CODEX_API_KEY}' 2>/dev/null | base64 -d || echo "")"

# 2. 升级 Helm chart（OCI）；私有 chart 时用 token 登录
echo ""
echo "2. 升级 Helm chart..."
if ! command -v helm &>/dev/null; then
  echo "   helm 未安装，正在安装..."; curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  echo "${GITHUB_TOKEN}" | helm registry login "${REGISTRY}" --username oauth2 --password-stdin 2>/dev/null || \
  echo "${GITHUB_TOKEN}" | helm registry login ghcr.io --username oauth2 --password-stdin 2>/dev/null || true
fi

# GIT_PROVIDER 是必填项：server 启动时强制校验，chart 也会在 upgrade 前 fail。
# 升级场景下必须来自 env 文件——老的 env 文件没有这一项，需要用户补上。
if [[ -z "${GIT_PROVIDER:-}" ]]; then
  echo "   ✗ GIT_PROVIDER 未设置。请在 ai-factory.env 中添加 GIT_PROVIDER=github 或 GIT_PROVIDER=gitlab 后重试。" >&2
  exit 1
fi
if [[ "${GIT_PROVIDER}" != "github" && "${GIT_PROVIDER}" != "gitlab" ]]; then
  echo "   ✗ GIT_PROVIDER 必须是 github 或 gitlab，当前为 '${GIT_PROVIDER}'" >&2
  exit 1
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
  --set "openai.codexApiKey=${CODEX_API_KEY}"
)
# Agent 运行模式：设置 agent.command（含 "codex" 时 webhook 自动置 delegated）。
# 来自 ai-factory.env 的 AGENT_COMMAND；留空则沿用 chart 现值。
[[ -n "${AGENT_COMMAND:-}" ]] && HELM_ARGS+=(--set "agent.command=${AGENT_COMMAND}")
[[ -n "${CODEX_MODEL:-}" ]] && HELM_ARGS+=(--set "codex.model=${CODEX_MODEL}")
[[ -n "${GITLAB_API_BASE:-}" ]] && HELM_ARGS+=(--set "gitlab.apiBase=${GITLAB_API_BASE}")
[[ -n "${GITLAB_TOKEN:-}" ]] && HELM_ARGS+=(--set "gitlab.token=${GITLAB_TOKEN}")
[[ -n "${OPENAI_BASE_URL:-}" ]] && HELM_ARGS+=(--set "openai.baseUrl=${OPENAI_BASE_URL}")
[[ -n "${OPENAI_MODEL:-}" ]] && HELM_ARGS+=(--set "openai.model=${OPENAI_MODEL}")
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

# Codex delegated-mode assets (optional). Publish scripts/auth.json as the
# codex-auth Secret and the in-repo issue-fix skill as a ConfigMap, then enable
# the mounts. No-ops when the files are absent. The warm pool is recreated later
# in this script (steps 5-6), so recycled pods pick up the new mounts.
CODEX_AUTH_FILE=""
for f in "${SCRIPT_DIR}/auth.json" "${SCRIPT_DIR}/codex/auth.json"; do
  [[ -f "${f}" ]] && CODEX_AUTH_FILE="${f}" && break
done
SKILL_FILE="${SCRIPT_DIR}/../skills/issue-fix/SKILL.md"
if [[ -n "${CODEX_AUTH_FILE}" ]]; then
  kubectl create secret generic codex-auth --namespace "${NAMESPACE}" \
    --from-file=auth.json="${CODEX_AUTH_FILE}" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  HELM_ARGS+=(--set "codex.authSecretName=codex-auth")
  echo "   ✓ codex-auth Secret (来自 ${CODEX_AUTH_FILE})"
fi
if [[ -f "${SKILL_FILE}" ]]; then
  kubectl create configmap issue-fix-skill --namespace "${NAMESPACE}" \
    --from-file=SKILL.md="${SKILL_FILE}" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  HELM_ARGS+=(--set "codex.skillConfigMapName=issue-fix-skill")
  echo "   ✓ issue-fix-skill ConfigMap"
fi

# HELM_ARGS 需带引号展开：agent.command 取值可能含空格（"ai-factory-agent codex"），
# 不加引号会被词分割成多个参数传给 helm。
helm upgrade --install ai-factory "${CHART_REF}" \
  "${CHART_VERSION_ARGS[@]}" \
  --namespace "${NAMESPACE}" --create-namespace \
  "${HELM_ARGS[@]}"
echo "   ✓ Helm chart 已升级"

# 3. 等待 server 就绪 + 强制重建（:latest/固定 tag 都可能被缓存，需 restart）
echo ""
echo "3. 等待 ai-factory-server 就绪..."
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s
echo ""
echo "3.1 强制重建 ai-factory-server..."
kubectl rollout restart deployment/ai-factory-server -n "${NAMESPACE}"
kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=120s
echo "   ✓ ai-factory-server 已重建"

# 4. 更新 agent-sandbox 控制器到 GHCR 版本
echo ""
echo "4. 更新 agent-sandbox 控制器..."
if kubectl get deployment agent-sandbox-controller -n agent-sandbox-system &>/dev/null; then
  kubectl set image deployment/agent-sandbox-controller \
    agent-sandbox-controller="${IMAGE_PREFIX}/agent-sandbox-controller:${VERSION}" -n agent-sandbox-system
  kubectl -n agent-sandbox-system patch deployment agent-sandbox-controller \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"agent-sandbox-controller","imagePullPolicy":"IfNotPresent"}]}}}}' >/dev/null || true
  kubectl rollout status deployment/agent-sandbox-controller -n agent-sandbox-system --timeout=120s
  echo "   ✓ agent-sandbox 控制器已更新"
else
  echo "   ⚠ agent-sandbox 控制器未找到，跳过"
fi

# 5. 清理旧 SandboxClaim 并重建 warm pool
echo ""
echo "5. 清理旧 SandboxClaim..."
kubectl delete sandboxclaim -n "${NAMESPACE}" --all 2>/dev/null || true
echo ""
echo "6. 重建 sandbox pods..."
OLD_PODS="$(kubectl get pods -n "${NAMESPACE}" -o name 2>/dev/null | grep go-dev || true)"
if [[ -n "${OLD_PODS}" ]]; then echo "${OLD_PODS}" | xargs kubectl delete -n "${NAMESPACE}" 2>/dev/null || true; sleep 15; fi
echo "   ✓ warm pool 重建中"

echo ""
echo "=== GHCR 升级完成 ==="
kubectl get pods -n "${NAMESPACE}" | grep -E "go-dev|ai-factory-server" || kubectl get pods -n "${NAMESPACE}"
kubectl get sandboxwarmpool -n "${NAMESPACE}" 2>/dev/null || echo "  (等待初始化)"
