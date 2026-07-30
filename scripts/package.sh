#!/bin/bash
# 本地打包脚本：构建镜像、导出、打包 chart
# 支持 nerdctl（推荐，无需 Docker daemon）和 Docker
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/dist"
AGENT_SANDBOX_REPO="https://github.com/kubernetes-sigs/agent-sandbox.git"

# 检测容器构建工具（优先 nerdctl，其次 docker）
CONTAINER_CMD=""
if command -v nerdctl &> /dev/null && command -v buildkitd &> /dev/null; then
    CONTAINER_CMD="nerdctl"
    echo "✓ 使用 nerdctl + buildkit 构建镜像"
elif command -v docker &> /dev/null; then
    CONTAINER_CMD="docker"
    echo "✓ 使用 docker 构建镜像"
else
    echo "错误: 未找到 nerdctl+buildkit 或 docker"
    echo "安装 nerdctl: https://github.com/containerd/nerdctl/releases"
    echo "安装 buildkit: https://github.com/moby/buildkit/releases"
    exit 1
fi

echo "=== ai-factory 打包脚本 ==="
echo ""

# 自动检测并设置代理
setup_proxy() {
    # 常见的代理端口
    local proxy_ports=(7890 1080 8080 3128)

    # 如果已经设置了代理，直接返回
    if [[ -n "${http_proxy:-}" ]] || [[ -n "${HTTP_PROXY:-}" ]]; then
        echo "✓ 检测到已配置的代理: ${http_proxy:-${HTTP_PROXY:-}}"
        return 0
    fi

    # 尝试检测本地代理
    for port in "${proxy_ports[@]}"; do
        local proxy_url="http://127.0.0.1:${port}"
        # 测试代理是否可用
        if curl -s --connect-timeout 2 --proxy "${proxy_url}" https://www.google.com > /dev/null 2>&1; then
            echo "✓ 检测到本地代理: ${proxy_url}"
            export http_proxy="${proxy_url}"
            export https_proxy="${proxy_url}"
            export HTTP_PROXY="${proxy_url}"
            export HTTPS_PROXY="${proxy_url}"
            export no_proxy="localhost,127.0.0.1,::1"
            export NO_PROXY="${no_proxy}"
            return 0
        fi
    done

    echo "⚠ 未检测到代理，可能影响镜像拉取速度"
    return 0
}

# 设置代理
setup_proxy
echo ""

# 创建输出目录
mkdir -p "${OUTPUT_DIR}"

# 1. 构建 server 镜像
echo "1. 构建 ai-factory-server 镜像..."
${CONTAINER_CMD} build -t ai-factory-server:latest -f "${ROOT_DIR}/Dockerfile.server" "${ROOT_DIR}"
${CONTAINER_CMD} save ai-factory-server:latest > "${OUTPUT_DIR}/ai-factory-server.tar"
echo "   ✓ ai-factory-server.tar"

# 2. 构建 sandbox 镜像
echo "2. 构建 coding-agent-sandbox 镜像..."
GO_VERSION="$(awk '/^go / {print $2; exit}' "${ROOT_DIR}/go.mod")"
${CONTAINER_CMD} build \
    --build-arg GO_VERSION="${GO_VERSION}" \
    --build-arg INSTALL_CODEX_CLI=true \
    -t coding-agent-sandbox:latest \
    "${ROOT_DIR}/components/agent-sandbox-images/coding-agent"
${CONTAINER_CMD} save coding-agent-sandbox:latest > "${OUTPUT_DIR}/coding-agent-sandbox.tar"
echo "   ✓ coding-agent-sandbox.tar"

# 3. 构建 agent-sandbox-controller 镜像（可选）
echo "3. 构建 agent-sandbox-controller 镜像..."
AGENT_SANDBOX_SRC="${AGENT_SANDBOX_SRC:-}"
CLEANUP_SANDBOX_SRC=false
CONTROLLER_BUILD_SUCCESS=false

# 预期的镜像名称
EXPECTED_IMAGE="ai-factory/agent-sandbox-controller:latest"
# push-images 脚本实际生成的镜像名称（根据实际测试）
ACTUAL_IMAGE="localhost/ai-factory/agent-sandbox-controller:dev"

build_controller() {
    local src_dir="$1"

    # 检查 push-images 脚本是否存在
    if [[ ! -f "${src_dir}/dev/tools/push-images" ]]; then
        echo "   ⚠ push-images 脚本不存在"
        return 1
    fi

    # 运行 push-images 脚本
    echo "   运行 push-images 脚本..."
    if IMAGE_PREFIX="ai-factory" IMAGE_TAG="latest" \
        "${src_dir}/dev/tools/push-images" \
        --image-prefix="ai-factory" \
        --image-tag="latest" \
        --controller-only; then

        # 检查预期的镜像是否存在
        if ${CONTAINER_CMD} image inspect "${EXPECTED_IMAGE}" &>/dev/null; then
            echo "   ✓ 镜像 ${EXPECTED_IMAGE} 构建成功"
        else
            # 检查实际生成的镜像
            if ${CONTAINER_CMD} image inspect "${ACTUAL_IMAGE}" &>/dev/null; then
                echo "   ⚠ 镜像标签不匹配，重新打标签..."
                echo "   预期: ${EXPECTED_IMAGE}"
                echo "   实际: ${ACTUAL_IMAGE}"
                ${CONTAINER_CMD} tag "${ACTUAL_IMAGE}" "${EXPECTED_IMAGE}"
                echo "   ✓ 已重新打标签为 ${EXPECTED_IMAGE}"
            else
                echo "   ⚠ 未找到构建的镜像"
                return 1
            fi
        fi

        # 保存镜像
        ${CONTAINER_CMD} save "${EXPECTED_IMAGE}" > "${OUTPUT_DIR}/agent-sandbox-controller.tar"
        echo "   ✓ agent-sandbox-controller.tar"
        return 0
    else
        echo "   ⚠ push-images 脚本执行失败"
        return 1
    fi
}

if [[ -z "${AGENT_SANDBOX_SRC}" ]]; then
    AGENT_SANDBOX_SRC=$(mktemp -d)
    CLEANUP_SANDBOX_SRC=true
    echo "   克隆 agent-sandbox 仓库..."
    if git clone --depth=1 "${AGENT_SANDBOX_REPO}" "${AGENT_SANDBOX_SRC}" 2>/dev/null; then
        if build_controller "${AGENT_SANDBOX_SRC}"; then
            CONTROLLER_BUILD_SUCCESS=true
        fi
    else
        echo "   ⚠ 无法克隆 agent-sandbox 仓库（可能需要网络代理）"
    fi
else
    # 使用本地源
    if build_controller "${AGENT_SANDBOX_SRC}"; then
        CONTROLLER_BUILD_SUCCESS=true
    fi
fi

if [[ "${CLEANUP_SANDBOX_SRC}" == "true" ]]; then
    rm -rf "${AGENT_SANDBOX_SRC}"
fi

# 4. 打包 Helm chart
echo "4. 打包 Helm chart..."
if command -v helm &> /dev/null; then
    helm package "${ROOT_DIR}/charts/ai-factory" -d "${OUTPUT_DIR}"
    echo "   ✓ ai-factory-*.tgz"
else
    # 如果没有 helm，直接复制目录
    cp -r "${ROOT_DIR}/charts/ai-factory" "${OUTPUT_DIR}/ai-factory-chart"
    echo "   ✓ ai-factory-chart/"
fi

# 5. 复制部署脚本
echo "5. 复制部署脚本..."
cp "${ROOT_DIR}/scripts/deploy-remote.sh" "${OUTPUT_DIR}/"
chmod +x "${OUTPUT_DIR}/deploy-remote.sh"
echo "   ✓ deploy-remote.sh"

# 6. 复制 CRD 安装脚本
echo "6. 复制 CRD 安装脚本..."
mkdir -p "${OUTPUT_DIR}/components"
cp -r "${ROOT_DIR}/components/factory-task" "${OUTPUT_DIR}/components/" 2>/dev/null || true
cp -r "${ROOT_DIR}/components/agent-sandbox" "${OUTPUT_DIR}/components/" 2>/dev/null || true
echo "   ✓ components/"

echo ""
echo "=== 打包完成 ==="
echo ""
echo "输出目录: ${OUTPUT_DIR}/"
ls -lh "${OUTPUT_DIR}/"
echo ""
echo "下一步："
echo "  1. 将 dist/ 目录传到虚拟机: scp -r dist/ user@your-vm:/path/"
echo "  2. 在虚拟机上运行: ./deploy-remote.sh"
