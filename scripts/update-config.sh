#!/bin/bash
# ai-factory 配置热更新脚本
# 用法: ./scripts/update-config.sh [path-to-env-file]
#
# 从 .env 文件读取配置，分别更新 K8s Secret 和 ConfigMap。
# K8s 会在 ~30s 内自动同步文件到 Pod，无需重启。
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
echo "📌 K8s 会在 ~30s 内自动同步文件到 Pod，无需重启。"
echo "📌 验证方法: 触发新 issue，然后检查 SandboxClaim 的 env:"
echo "   kubectl get sandboxclaims -n ${NAMESPACE} --sort-by=.metadata.creationTimestamp \\"
echo "       -o jsonpath='{.items[-1].spec.template.spec.containers[0].env}' | python3 -m json.tool"
