#!/bin/bash
# ai-factory 开发启动脚本
# 用法: ./scripts/dev.sh

set -e

cd "$(dirname "$0")/.."

# 设置 PATH
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin

# 从 K8s secret 获取环境变量
echo "📋 加载环境变量..."
export WEBHOOK_SECRET=$(kubectl get secret ai-factory-credentials -n ai-factory -o jsonpath='{.data.WEBHOOK_SECRET}' | base64 -d)
export GITHUB_TOKEN=$(kubectl get secret ai-factory-credentials -n ai-factory -o jsonpath='{.data.GITHUB_TOKEN}' | base64 -d)
export OPENAI_API_KEY=$(kubectl get secret ai-factory-credentials -n ai-factory -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d)
export CODEX_API_KEY=$(kubectl get secret ai-factory-credentials -n ai-factory -o jsonpath='{.data.CODEX_API_KEY}' | base64 -d)

# OpenAI 配置
export OPENAI_BASE_URL="https://api.openai.com/v1"
export OPENAI_MODEL="gpt-4.1"
export OPENAI_TEMPERATURE="1"
export OPENAI_MAX_TOKENS="48000"
export OPENAI_MAX_TOOL_ROUNDS="40"
export OPENAI_MAX_FINAL_SCRIPT_ROUNDS="5"
export OPENAI_MAX_REPAIR_ROUNDS="3"
export OPENAI_TOTAL_TIMEOUT_SECONDS="1800"
export OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS="180"
export OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS="90"
export OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS="90"

# GitHub 克隆代理（可通过外部环境变量覆盖）
export AI_FACTORY_GIT_PROXY="${AI_FACTORY_GIT_PROXY:-https://ghproxy.net}"

# 代码托管方（必填，server 启动时强制校验）。本地开发默认 github，
# 调试 GitLab 时用 GIT_PROVIDER=gitlab ./scripts/dev.sh 覆盖。
export GIT_PROVIDER="${GIT_PROVIDER:-github}"

# 设置 Go 代理（国内加速）
export GOPROXY=https://goproxy.cn,direct

echo "✅ 环境变量已加载"
echo ""
echo "🚀 启动 ai-factory-server..."
echo "   监听地址: :32519"
echo "   命名空间: ai-factory"
echo "   提供商:   ${GIT_PROVIDER}"
echo "   Webhook: http://$(hostname -I | awk '{print $1}'):32519/webhook/${GIT_PROVIDER}"
echo ""

# 运行服务
go run ./factory/cmd/factory server \
    --addr=:32519 \
    --namespace=ai-factory \
    --agent=builder \
    --agent-command="ai-factory-agent openai-compatible" \
    --smoke-agent-command="cat >/tmp/ai-factory-agent-prompt.txt" \
    --smoke-command="mkdir -p .ai-factory/smoke && echo smoke-\$(date +%s) > .ai-factory/smoke/placeholder.txt && test -s /tmp/ai-factory-agent-prompt.txt && git --version && go version && node --version && python3 --version && command -v ai-factory-agent" \
    --validation-command="echo 'validation skipped' && true" \
    --sandbox-template=go-dev \
    --watch-interval=15s \
    --task-timeout=30m \
    --change-request=true \
    --report=true \
    --agent-env=CODEX_API_KEY \
    --agent-env=OPENAI_API_KEY \
    --agent-env=OPENAI_BASE_URL \
    --agent-env=OPENAI_MODEL \
    --agent-env=OPENAI_TEMPERATURE \
    --agent-env=OPENAI_MAX_TOKENS \
    --agent-env=OPENAI_MAX_TOOL_ROUNDS \
    --agent-env=OPENAI_MAX_FINAL_SCRIPT_ROUNDS \
    --agent-env=OPENAI_MAX_REPAIR_ROUNDS \
    --agent-env=OPENAI_TOTAL_TIMEOUT_SECONDS \
    --agent-env=OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS \
    --agent-env=OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS \
    --agent-env=OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS
