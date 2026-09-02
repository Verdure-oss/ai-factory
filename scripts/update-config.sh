#!/bin/bash
# ai-factory 配置热更新脚本
# 用法: ./scripts/update-config.sh [path-to-env-file]
#
# 从 .env 文件读取配置，分别更新 K8s Secret 和 ConfigMap。
#
# 生效方式分两类：
#   - ai-factory-server：Secret/ConfigMap 以文件挂载，K8s 会在 ~30s 内自动同步，
#     无需重启（MAX_CONCURRENT_TASKS 除外，见文末）。
#   - go-dev 预热 pod：通过 envFrom 引用 Secret/ConfigMap，env 是 pod 启动时的快照，
#     因此脚本会重建 go-dev pod 使新配置生效。
#
# 默认读取: scripts/ai-factory.env

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-${SCRIPT_DIR}/ai-factory.env}"
NAMESPACE="${NAMESPACE:-ai-factory}"

if [ ! -f "${ENV_FILE}" ]; then
    echo "错误: 配置文件不存在: ${ENV_FILE}"
    echo "用法: $0 [path-to-env-file]"
    echo "默认: scripts/ai-factory.env"
    exit 1
fi

if ! command -v kubectl &> /dev/null; then
    echo "错误: kubectl 未安装"
    exit 1
fi

if ! kubectl cluster-info &> /dev/null; then
    echo "错误: 无法连接到 K8s 集群"
    exit 1
fi

echo "📋 读取配置: ${ENV_FILE}"

# 解析 .env 文件，跳过注释和空行
declare -A CONFIG
while IFS= read -r line; do
    # 跳过注释和空行
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    # 提取 KEY=VALUE
    if [[ "$line" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*) ]]; then
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        # 去除两端引号
        value="${value#\"}"
        value="${value%\"}"
        value="${value#\'}"
        value="${value%\'}"
        CONFIG[$key]="$value"
    fi
done < "${ENV_FILE}"

echo "✓ 读取到 ${#CONFIG[@]} 个配置项"

# 分类：敏感凭证 vs 普通配置
SECRET_KEYS=("GITHUB_TOKEN" "WEBHOOK_SECRET" "OPENAI_API_KEY" "CODEX_API_KEY" "GITLAB_TOKEN")
CONFIG_KEYS=("OPENAI_BASE_URL" "OPENAI_MODEL" "OPENAI_TEMPERATURE" "OPENAI_MAX_TOKENS"
             "OPENAI_MAX_TOOL_ROUNDS" "OPENAI_MAX_FINAL_SCRIPT_ROUNDS" "OPENAI_MAX_REPAIR_ROUNDS"
             "OPENAI_TOTAL_TIMEOUT_SECONDS" "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS"
             "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS" "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS"
             "OPENAI_VISION_ENABLED"
             "MAX_CONCURRENT_TASKS"
             "CI_WATCH_ENABLED" "CI_WATCH_MAX_RETRIES" "CI_WATCH_MAX_WAIT" "CI_WATCH_SETTLE_INTERVAL" "CI_WATCH_MAX_TOOL_ROUNDS" "CI_WATCH_LOG_SNIPPET_LINES"
             "GIT_PROVIDER" "GITLAB_API_BASE"
             "CODEX_MODEL" "CODEX_WIRE_API"
             "AI_FACTORY_CODEX_PLUGIN_SOURCE" "AI_FACTORY_CODEX_PLUGIN_NAME"
             "AI_FACTORY_CODEX_MARKETPLACE_NAME" "AI_FACTORY_CODEX_PLUGIN_REF"
             "AI_FACTORY_GIT_PROXY")

# 构建 Secret --from-literal 参数
SECRET_ARGS=()
for key in "${SECRET_KEYS[@]}"; do
    value="${CONFIG[$key]:-}"
    if [ -n "$value" ]; then
        SECRET_ARGS+=("--from-literal=${key}=${value}")
    fi
done

if [ ${#SECRET_ARGS[@]} -eq 0 ]; then
    echo "⚠️  警告: 没有敏感凭证配置，跳过 Secret 更新"
else
    echo ""
    echo "🔒 更新 Secret (ai-factory-credentials)..."
    kubectl create secret generic ai-factory-credentials \
        "${SECRET_ARGS[@]}" \
        -n "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
    echo "✓ Secret 已更新"
fi

# 构建 ConfigMap --from-literal 参数
CONFIG_ARGS=()
for key in "${CONFIG_KEYS[@]}"; do
    value="${CONFIG[$key]:-}"
    if [ -n "$value" ]; then
        CONFIG_ARGS+=("--from-literal=${key}=${value}")
    fi
done

if [ ${#CONFIG_ARGS[@]} -eq 0 ]; then
    echo "⚠️  警告: 没有模型配置，跳过 ConfigMap 更新"
else
    echo ""
    echo "⚙️  更新 ConfigMap (ai-factory-config)..."
    kubectl create configmap ai-factory-config \
        "${CONFIG_ARGS[@]}" \
        -n "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
    echo "✓ ConfigMap 已更新"
fi

echo ""
echo "✅ 配置更新完成！"


echo ""
echo "📌 ai-factory-server 会在 ~30s 内自动同步挂载文件，无需重启。"

# 重建 go-dev 预热 pod，使新配置生效。
# go-dev pod 通过 envFrom 引用 secret/configmap，env 是 pod 创建时的快照，
# 运行中的 pod 不会自动刷新，必须重建才能加载新配置。agent-sandbox 会自动补建。
# 注意: 若某个 go-dev 正在被任务绑定，删除会中断该任务。
echo ""
echo "🧹 重建 go-dev 预热 pod（使新配置生效）..."
WARM_PODS=$(kubectl get pods -n "${NAMESPACE}" -l agents.x-k8s.io/warm-pool-sandbox -o name 2>/dev/null || true)
if [ -n "${WARM_PODS}" ]; then
    echo "${WARM_PODS}" | xargs kubectl delete -n "${NAMESPACE}" 2>/dev/null || true
    echo "   ✓ go-dev 预热 pod 已删除，agent-sandbox 将用新配置补建"
else
    echo "   ⚠ 未找到 go-dev 预热 pod，跳过"
fi
echo ""
echo "📌 验证方法: 触发新 issue，然后检查任务绑定的 sandbox 是否为 go-dev（而非独立的 claim pod）:"
echo "   kubectl get sandboxclaims -n ${NAMESPACE} --sort-by=.metadata.creationTimestamp \\"
echo "       -o jsonpath='{.items[-1].status.sandbox.name}'"

# MAX_CONCURRENT_TASKS 需要重启 server 才生效（并发信号量 sem 在启动时创建）。
# 仅当 .env 中显式配置了该值时重启；其余配置走文件热更新，无需重启。
echo ""
if [ -n "${CONFIG[MAX_CONCURRENT_TASKS]:-}" ]; then
    echo "🔄 重启 ai-factory-server（使 MAX_CONCURRENT_TASKS 生效）..."
    if kubectl rollout restart deployment ai-factory-server -n "${NAMESPACE}" 2>/dev/null; then
        echo "   ✓ ai-factory-server 正在滚动重启，新并发数将在新的 Pod 生效"
    else
        echo "   ⚠ 重启 ai-factory-server 失败，请手动执行: kubectl rollout restart deployment ai-factory-server -n ${NAMESPACE}"
    fi
else
    echo "ℹ️  MAX_CONCURRENT_TASKS 未配置，跳过 server 重启（其余配置热更新已生效）"
fi
