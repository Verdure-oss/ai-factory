#!/bin/bash
# ai-factory 开发环境快速设置脚本
# 用法: ./scripts/setup-dev.sh

set -e

echo "🚀 ai-factory 开发环境设置"
echo ""

# 1. 检查 Go 是否已安装
if ! command -v go &> /dev/null; then
    echo "❌ Go 未安装"
    echo "   请运行: 参考 scripts/README-dev.md 安装 Go"
    exit 1
fi

echo "✅ Go 已安装: $(go version)"

# 2. 设置 Go 代理
echo "📦 配置 Go 代理（国内加速）..."
go env -w GOPROXY=https://goproxy.cn,direct
echo "   GOPROXY=$(go env GOPROXY)"

# 3. 下载依赖
echo ""
echo "📥 下载 Go 依赖（首次可能较慢）..."
go mod download
echo "✅ 依赖下载完成"

# 4. 测试编译
echo ""
echo "🔨 测试编译..."
go build -o /tmp/ai-factory-test ./factory/cmd/factory/
if [ -f /tmp/ai-factory-test ]; then
    echo "✅ 编译成功"
    rm /tmp/ai-factory-test
else
    echo "❌ 编译失败"
    exit 1
fi

# 5. 检查 K8s 配置
echo ""
echo "🔍 检查 K8s 配置..."
if ! kubectl cluster-info &> /dev/null; then
    echo "❌ 无法连接到 K8s 集群"
    exit 1
fi
echo "✅ K8s 集群连接正常"

# 6. 检查 Secret
echo ""
echo "🔐 检查 Secrets..."
if ! kubectl get secret ai-factory-credentials -n ai-factory &> /dev/null; then
    echo "❌ Secret ai-factory-credentials 不存在"
    exit 1
fi
echo "✅ Secrets 已配置"

# 7. 释放端口（如果需要）
echo ""
echo "🔌 检查端口 32519..."
if kubectl get svc ai-factory-server -n ai-factory &> /dev/null; then
    echo "⚠️  K8s Service 存在，是否删除以释放端口？(y/n)"
    read -r response
    if [[ "$response" == "y" ]]; then
        kubectl get svc ai-factory-server -n ai-factory -o yaml > /tmp/ai-factory-svc-backup.yaml
        kubectl delete svc ai-factory-server -n ai-factory
        echo "✅ Service 已删除（备份到 /tmp/ai-factory-svc-backup.yaml）"
    fi
else
    echo "✅ 端口 32519 可用"
fi

# 8. 暂停 Deployment
echo ""
echo "⏸️  暂停 K8s Deployment..."
kubectl scale deployment/ai-factory-server -n ai-factory --replicas=0 2>/dev/null || true
echo "✅ Deployment 已暂停"

echo ""
echo "=========================================="
echo "✅ 开发环境设置完成！"
echo "=========================================="
echo ""
echo "📝 下一步："
echo "   1. 手动启动: ./scripts/dev.sh"
echo "   2. 热重载:   ./scripts/watch.sh"
echo "   3. 查看详情: cat scripts/README-dev.md"
echo ""
echo "🌐 服务地址："
echo "   Webhook: http://$(hostname -I | awk '{print $1}'):32519/webhook/github"
echo "   健康检查: curl http://localhost:32519/healthz"
echo ""
