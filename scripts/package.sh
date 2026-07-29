#!/bin/bash
# 本地打包脚本：构建镜像、导出、打包 chart
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_DIR="${ROOT_DIR}/dist"

echo "=== ai-factory 打包脚本 ==="
echo ""

# 创建输出目录
mkdir -p "${OUTPUT_DIR}"

# 1. 构建 server 镜像
echo "1. 构建 ai-factory-server 镜像..."
docker build -t ai-factory-server:latest -f "${ROOT_DIR}/Dockerfile.server" "${ROOT_DIR}"
docker save ai-factory-server:latest > "${OUTPUT_DIR}/ai-factory-server.tar"
echo "   ✓ ai-factory-server.tar"

# 2. 构建 sandbox 镜像
echo "2. 构建 coding-agent-sandbox 镜像..."
GO_VERSION="$(awk '/^go / {print $2; exit}' "${ROOT_DIR}/go.mod")"
docker build \
    --build-arg GO_VERSION="${GO_VERSION}" \
    --build-arg INSTALL_CODEX_CLI=true \
    -t coding-agent-sandbox:latest \
    "${ROOT_DIR}/components/agent-sandbox-images/coding-agent"
docker save coding-agent-sandbox:latest > "${OUTPUT_DIR}/coding-agent-sandbox.tar"
echo "   ✓ coding-agent-sandbox.tar"

# 3. 打包 Helm chart
echo "3. 打包 Helm chart..."
if command -v helm &> /dev/null; then
    helm package "${ROOT_DIR}/charts/ai-factory" -d "${OUTPUT_DIR}"
    echo "   ✓ ai-factory-*.tgz"
else
    # 如果没有 helm，直接复制目录
    cp -r "${ROOT_DIR}/charts/ai-factory" "${OUTPUT_DIR}/ai-factory-chart"
    echo "   ✓ ai-factory-chart/"
fi

# 4. 复制部署脚本
echo "4. 复制部署脚本..."
cp "${ROOT_DIR}/scripts/deploy-remote.sh" "${OUTPUT_DIR}/"
chmod +x "${OUTPUT_DIR}/deploy-remote.sh"
echo "   ✓ deploy-remote.sh"

# 5. 复制 CRD 安装脚本
echo "5. 复制 CRD 安装脚本..."
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
