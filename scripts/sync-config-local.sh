#!/bin/bash
# 本地配置同步脚本
# 将 K8s Secret/ConfigMap 同步到本地文件目录，模拟 K8s volume mount
#
# 用法:
#   ./scripts/sync-config-local.sh          # 同步一次
#   ./scripts/sync-config-local.sh --watch  # 持续监听（每 10s 同步）
#
# 配合 watch.sh 使用，实现本地热更新测试

set -euo pipefail

NAMESPACE="${NAMESPACE:-ai-factory}"
SECRET_DIR="/etc/ai-factory/secret"
CONFIG_DIR="/etc/ai-factory/config"
INTERVAL=10

sync_secret() {
    echo "🔒 同步 Secret → ${SECRET_DIR}"
    mkdir -p "${SECRET_DIR}"

    # 获取 Secret 中的所有 key
    secret_keys=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" \
        -o jsonpath='{.data}' | python3 -c "import sys,json; [print(k) for k in json.load(sys.stdin).keys()]" 2>/dev/null)

    if [ -z "$secret_keys" ]; then
        echo "   ⚠ Secret 不存在或为空"
        return
    fi

    # 删除本地存在但 Secret 中不存在的文件
    if [ -d "${SECRET_DIR}" ]; then
        for local_file in "${SECRET_DIR}"/*; do
            [ -f "$local_file" ] || continue
            key=$(basename "$local_file")
            if ! echo "$secret_keys" | grep -qx "$key"; then
                rm -f "$local_file"
                echo "   删除已移除的 key: ${key}"
            fi
        done
    fi

    # 逐个 key 导出为文件
    for key in $secret_keys; do
        value=$(kubectl get secret ai-factory-credentials -n "${NAMESPACE}" \
            -o jsonpath="{.data.${key}}" | base64 -d)
        echo -n "$value" > "${SECRET_DIR}/${key}"
        echo "   ${key} ✓"
    done
}

sync_configmap() {
    echo "⚙️  同步 ConfigMap → ${CONFIG_DIR}"
    mkdir -p "${CONFIG_DIR}"

    # 检查 ConfigMap 是否存在
    if ! kubectl get configmap ai-factory-config -n "${NAMESPACE}" &>/dev/null; then
        echo "   ⚠ ConfigMap 不存在"
        return
    fi

    # 获取 ConfigMap 中的所有 key
    cm_keys=$(kubectl get configmap ai-factory-config -n "${NAMESPACE}" \
        -o jsonpath='{.data}' | python3 -c "import sys,json; [print(k) for k in json.load(sys.stdin).keys()]" 2>/dev/null)

    # 删除本地存在但 ConfigMap 中不存在的文件
    if [ -d "${CONFIG_DIR}" ]; then
        for local_file in "${CONFIG_DIR}"/*; do
            [ -f "$local_file" ] || continue
            key=$(basename "$local_file")
            if ! echo "$cm_keys" | grep -qx "$key"; then
                rm -f "$local_file"
                echo "   删除已移除的 key: ${key}"
            fi
        done
    fi

    # 逐个 key 导出为文件
    for key in $cm_keys; do
        value=$(kubectl get configmap ai-factory-config -n "${NAMESPACE}" \
            -o jsonpath="{.data.${key}}")
        echo -n "$value" > "${CONFIG_DIR}/${key}"
        echo "   ${key} ✓"
    done
}

sync_all() {
    echo "📋 同步配置 (namespace: ${NAMESPACE})"
    echo ""
    sync_secret
    echo ""
    sync_configmap
    echo ""
    echo "✅ 同步完成"
}

# 主逻辑
if [ "${1:-}" = "--watch" ]; then
    echo "👀 持续监听模式 (每 ${INTERVAL}s 同步)"
    echo "   按 Ctrl+C 停止"
    echo ""
    sync_all
    while true; do
        sleep "${INTERVAL}"
        sync_all
        echo ""
    done
else
    sync_all
fi
