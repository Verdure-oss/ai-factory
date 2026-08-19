#!/bin/bash
# 本地打包脚本：构建镜像、导出、打包 chart
# 支持 nerdctl（推荐，无需 Docker daemon）和 Docker
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/dist"
AGENT_SANDBOX_REPO="https://github.com/kubernetes-sigs/agent-sandbox.git"

# 强制清理旧的 agent-sandbox 源码（确保使用最新版本）
if [[ -n "${AGENT_SANDBOX_SRC:-}" ]] && [[ -d "${AGENT_SANDBOX_SRC}" ]]; then
    echo "⚠ 清理旧的 agent-sandbox 源码: ${AGENT_SANDBOX_SRC}"
    rm -rf "${AGENT_SANDBOX_SRC}"
fi

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

# 生成代理 build-arg（用于容器内部网络请求）。
# 注意：构建容器内 127.0.0.1/localhost 指向容器自身而非宿主机，必须把代理地址改写为
# 宿主机别名 host.docker.internal，并配合 --add-host=host-gateway（Linux / Docker Desktop 均可解析）。
BUILD_HOST_ALIAS="host.docker.internal"
BUILD_EXTRA_ARGS=(--add-host="${BUILD_HOST_ALIAS}:host-gateway")

# 把宿主机代理地址改写为容器内可访问的地址
container_proxy_url() {
    local url="$1"
    url="${url//127.0.0.1/${BUILD_HOST_ALIAS}}"
    url="${url//localhost/${BUILD_HOST_ALIAS}}"
    printf '%s' "${url}"
}

PROXY_BUILD_ARGS=()
if [[ -n "${http_proxy:-}" ]]; then
    PROXY_BUILD_ARGS+=(--build-arg "http_proxy=$(container_proxy_url "${http_proxy}")")
    PROXY_BUILD_ARGS+=(--build-arg "HTTP_PROXY=$(container_proxy_url "${HTTP_PROXY:-${http_proxy}}")")
fi
if [[ -n "${https_proxy:-}" ]]; then
    PROXY_BUILD_ARGS+=(--build-arg "https_proxy=$(container_proxy_url "${https_proxy}")")
    PROXY_BUILD_ARGS+=(--build-arg "HTTPS_PROXY=$(container_proxy_url "${HTTPS_PROXY:-${https_proxy}}")")
fi
if [[ -n "${no_proxy:-}" ]]; then
    PROXY_BUILD_ARGS+=(--build-arg "no_proxy=${no_proxy}")
    PROXY_BUILD_ARGS+=(--build-arg "NO_PROXY=${NO_PROXY:-${no_proxy}}")
fi

# 创建输出目录
mkdir -p "${OUTPUT_DIR}"

# 1. 构建 server 镜像
echo "1. 构建 ai-factory-server 镜像..."
${CONTAINER_CMD} build "${PROXY_BUILD_ARGS[@]}" "${BUILD_EXTRA_ARGS[@]}" \
    --provenance=false --sbom=false \
    -t ai-factory-server:latest -f "${ROOT_DIR}/Dockerfile.server" "${ROOT_DIR}"
${CONTAINER_CMD} save ai-factory-server:latest > "${OUTPUT_DIR}/ai-factory-server.tar"
echo "   ✓ ai-factory-server.tar"

# 2. 构建 sandbox 镜像
echo "2. 构建 coding-agent-sandbox 镜像..."
# Sandbox Go toolchain version. Defaults to the value baked into the
# Dockerfile (compatible with known target repos); AI_FACTORY_GO_VERSION
# overrides it when a target repo requires a newer toolchain than the sandbox
# ships. Do NOT default this to ai-factory's own go.mod version — the sandbox
# must serve target repos, whose go directive may be higher, and it is offline
# (no proxy.golang.org), so a toolchain auto-download would hang the build.
GO_VERSION="${GO_VERSION:-${AI_FACTORY_GO_VERSION:-}}"
SANDBOX_BUILD_ARGS=(--build-arg INSTALL_CODEX_CLI=true)
if [[ -n "${GO_VERSION}" ]]; then
    SANDBOX_BUILD_ARGS+=(--build-arg "GO_VERSION=${GO_VERSION}")
fi
${CONTAINER_CMD} build "${PROXY_BUILD_ARGS[@]}" "${BUILD_EXTRA_ARGS[@]}" \
    --provenance=false --sbom=false \
    "${SANDBOX_BUILD_ARGS[@]}" \
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

build_controller() {
    local src_dir="$1"

    # 检查控制器 Dockerfile 是否存在
    if [[ ! -f "${src_dir}/Dockerfile" ]]; then
        echo "   ⚠ agent-sandbox 仓库中没有控制器 Dockerfile"
        return 1
    fi

    # 不用上游 push-images（依赖 python3、buildx 且推送到 registry，不适合本地打包），
    # 直接 docker build 并导出 tar，与前面 server/sandbox 镜像的构建方式保持一致。
    echo "   构建 agent-sandbox-controller 镜像..."
    local git_version git_sha build_date
    git_version=$(git -C "${src_dir}" describe --always --dirty 2>/dev/null || echo unknown)
    git_sha=$(git -C "${src_dir}" rev-parse --short HEAD 2>/dev/null || echo unknown)
    build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)

    if ${CONTAINER_CMD} build "${PROXY_BUILD_ARGS[@]}" "${BUILD_EXTRA_ARGS[@]}" \
        --provenance=false --sbom=false \
        --build-arg "GIT_VERSION=${git_version}" \
        --build-arg "GIT_SHA=${git_sha}" \
        --build-arg "BUILD_DATE=${build_date}" \
        -t "${EXPECTED_IMAGE}" \
        -f "${src_dir}/Dockerfile" \
        "${src_dir}"; then
        ${CONTAINER_CMD} save "${EXPECTED_IMAGE}" > "${OUTPUT_DIR}/agent-sandbox-controller.tar"
        echo "   ✓ agent-sandbox-controller.tar"
        return 0
    else
        echo "   ⚠ 控制器镜像构建失败"
        return 1
    fi
}

if [[ -z "${AGENT_SANDBOX_SRC}" ]]; then
    AGENT_SANDBOX_SRC=$(mktemp -d)
    CLEANUP_SANDBOX_SRC=true
    echo "   克隆 agent-sandbox 仓库（最新版本）..."
    if git clone --depth=1 "${AGENT_SANDBOX_REPO}" "${AGENT_SANDBOX_SRC}" 2>/dev/null; then
        # 显示克隆的版本信息
        echo "   ✓ 克隆成功，版本: $(cd "${AGENT_SANDBOX_SRC}" && git log --oneline -1)"
        if build_controller "${AGENT_SANDBOX_SRC}"; then
            CONTROLLER_BUILD_SUCCESS=true
        fi
    else
        echo "   ⚠ 无法克隆 agent-sandbox 仓库（可能需要网络代理）"
        echo "   提示: 设置 AGENT_SANDBOX_SRC 环境变量指向本地仓库"
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
cp "${ROOT_DIR}/scripts/ai-factory.env.example" "${OUTPUT_DIR}/ai-factory.env"
echo "   ✓ ai-factory.env（配置模板，部署前填写真实值）"

# 6. 复制 CRD 安装脚本
echo "6. 复制 CRD 安装脚本..."
mkdir -p "${OUTPUT_DIR}/components"
cp -r "${ROOT_DIR}/components/factory-task" "${OUTPUT_DIR}/components/" 2>/dev/null || true
chmod +x "${OUTPUT_DIR}/components/factory-task/install" 2>/dev/null || true
cp -r "${ROOT_DIR}/components/agent-sandbox" "${OUTPUT_DIR}/components/" 2>/dev/null || true
chmod +x "${OUTPUT_DIR}/components/agent-sandbox/install" 2>/dev/null || true

# 规范化打包目录中的文本文件，避免跨机器传输后 Bash 遇到 CRLF。
while IFS= read -r -d '' file; do
    sed -i 's/\r$//' "${file}"
done < <(find "${OUTPUT_DIR}" -type f ! -name '*.tar' -print0)

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
