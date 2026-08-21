#!/bin/bash
# 卸载 ai-factory 及其 Kubernetes 资源
# 默认删除 Helm release、两个 namespace 以及 FactoryTask/agent-sandbox CRD。
set -euo pipefail

NAMESPACE="${NAMESPACE:-ai-factory}"
AGENT_SANDBOX_NAMESPACE="${AGENT_SANDBOX_NAMESPACE:-agent-sandbox-system}"
KEEP_CRDS=false
KEEP_NAMESPACES=false
ASSUME_YES=false

usage() {
    cat <<EOF
用法: $0 [选项]

卸载 ai-factory Helm release，并清理相关 namespace 和 CRD。

选项:
  --keep-crds          保留 FactoryTask 和 agent-sandbox CRD
  --keep-namespaces    保留 ai-factory 和 agent-sandbox-system namespace
  --yes                跳过交互确认
  -h, --help           显示帮助

环境变量:
  NAMESPACE                 ai-factory namespace（默认: ai-factory）
  AGENT_SANDBOX_NAMESPACE  agent-sandbox namespace（默认: agent-sandbox-system）
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --keep-crds)
            KEEP_CRDS=true
            ;;
        --keep-namespaces)
            KEEP_NAMESPACES=true
            ;;
        --yes|-y)
            ASSUME_YES=true
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            echo "错误: 未知选项: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
    shift
done

if ! command -v kubectl &>/dev/null; then
    echo "错误: kubectl 未安装" >&2
    exit 1
fi

if ! command -v helm &>/dev/null; then
    echo "错误: helm 未安装" >&2
    exit 1
fi

echo "=== ai-factory 卸载 ==="
echo "  ai-factory namespace:       ${NAMESPACE}"
echo "  agent-sandbox namespace:    ${AGENT_SANDBOX_NAMESPACE}"
echo "  保留 CRD:                   ${KEEP_CRDS}"
echo "  保留 namespace:             ${KEEP_NAMESPACES}"
echo ""
echo "此操作将删除 Kubernetes 资源，且可能导致正在运行的任务终止。"

if [[ "${ASSUME_YES}" != "true" ]]; then
    read -r -p "继续卸载？请输入 yes 确认: " confirmation
    if [[ "${confirmation}" != "yes" ]]; then
        echo "已取消。"
        exit 0
    fi
fi

# 1. 卸载 Helm release。namespace 和 CRD 默认保留到后续步骤处理。
echo ""
echo "1. 卸载 Helm release..."
helm uninstall ai-factory -n "${NAMESPACE}" --ignore-not-found
 echo "   ✓ Helm release 已卸载"

# 2. 删除 ai-factory namespace。
if [[ "${KEEP_NAMESPACES}" == "true" ]]; then
    echo "2. 保留 namespace ${NAMESPACE}"
else
    echo "2. 删除 namespace ${NAMESPACE}..."
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found
    echo "   ✓ namespace 已删除"
fi

# 3. 删除 FactoryTask CRD。
FACTORY_TASK_CRD="factorytasks.factory.ai.gke.io"
if [[ "${KEEP_CRDS}" == "true" ]]; then
    echo "3. 保留 CRD ${FACTORY_TASK_CRD}"
else
    echo "3. 删除 CRD ${FACTORY_TASK_CRD}..."
    kubectl delete crd "${FACTORY_TASK_CRD}" --ignore-not-found
    echo "   ✓ FactoryTask CRD 已删除"
fi

# 4. 删除 agent-sandbox 控制器 namespace。
if [[ "${KEEP_NAMESPACES}" == "true" ]]; then
    echo "4. 保留 namespace ${AGENT_SANDBOX_NAMESPACE}"
else
    echo "4. 删除 namespace ${AGENT_SANDBOX_NAMESPACE}..."
    kubectl delete namespace "${AGENT_SANDBOX_NAMESPACE}" --ignore-not-found
    echo "   ✓ agent-sandbox namespace 已删除"
fi

# 5. 删除 agent-sandbox CRD。
AGENT_SANDBOX_CRDS=(
    sandboxes.agents.x-k8s.io
    sandboxclaims.extensions.agents.x-k8s.io
    sandboxtemplates.extensions.agents.x-k8s.io
    sandboxwarmpools.extensions.agents.x-k8s.io
)

if [[ "${KEEP_CRDS}" == "true" ]]; then
    echo "5. 保留 agent-sandbox CRD"
else
    echo "5. 删除 agent-sandbox CRD..."
    kubectl delete crd "${AGENT_SANDBOX_CRDS[@]}" --ignore-not-found
    echo "   ✓ agent-sandbox CRD 已删除"
fi

echo ""
echo "=== 卸载完成 ==="
if [[ "${KEEP_NAMESPACES}" == "true" || "${KEEP_CRDS}" == "true" ]]; then
    echo "注意：部分 namespace 或 CRD 按选项保留。"
fi
