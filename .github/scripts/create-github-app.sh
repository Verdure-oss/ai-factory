#!/bin/bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

# AI Factory Bot GitHub App 创建脚本
# 用途：创建 GitHub App 用于监听目标仓库的 Issue 事件

APP_NAME="${1:-ai-factory-bot}"
APP_DESCRIPTION="${2:-AI Factory Bot - 自动处理 Issue 事件}"
APP_URL="${3:-https://github.com/apps/${APP_NAME}}"

echo "=========================================="
echo "创建 GitHub App: ${APP_NAME}"
echo "=========================================="

# 检查必要的工具
check_dependencies() {
    local missing=()

    if ! command -v curl &> /dev/null; then
        missing+=("curl")
    fi

    if ! command -v jq &> /dev/null; then
        missing+=("jq")
    fi

    if ! command -v openssl &> /dev/null; then
        missing+=("openssl")
    fi

    if [ ${#missing[@]} -gt 0 ]; then
        echo "错误: 缺少以下工具: ${missing[*]}"
        echo "请先安装这些工具"
        exit 1
    fi
}

# 检查 GitHub Token
check_github_token() {
    if [ -z "${GITHUB_TOKEN:-}" ]; then
        echo "错误: 未设置 GITHUB_TOKEN 环境变量"
        echo "请设置具有 admin:org 权限的 GitHub Token"
        echo ""
        echo "设置方法："
        echo "  export GITHUB_TOKEN=your_github_token"
        echo ""
        echo "Token 需要以下权限："
        echo "  - admin:org (管理组织)"
        echo "  - repo (仓库访问)"
        echo "  - workflow (工作流访问)"
        exit 1
    fi

    echo "✓ GITHUB_TOKEN 已设置"
}

# 创建 GitHub App
create_github_app() {
    echo ""
    echo "步骤 1: 创建 GitHub App"
    echo "------------------------"

    # GitHub App 的配置
    local app_config=$(cat <<EOF
{
    "name": "${APP_NAME}",
    "description": "${APP_DESCRIPTION}",
    "url": "${APP_URL}",
    "hook_attributes": {
        "active": true
    },
    "redirect_url": "https://github.com/apps/${APP_NAME}",
    "callback_urls": [],
    "setup_url": "https://github.com/apps/${APP_NAME}/installations/new",
    "public": false,
    "default_events": [
        "issues",
        "issue_comment"
    ],
    "default_permissions": {
        "issues": "write",
        "pull_requests": "write",
        "contents": "write",
        "actions": "write",
        "metadata": "read"
    }
}
EOF
)

    echo "创建 GitHub App..."
    local response=$(curl -s -X POST \
        -H "Authorization: Bearer ${GITHUB_TOKEN}" \
        -H "Accept: application/vnd.github.v3+json" \
        -H "Content-Type: application/json" \
        -d "${app_config}" \
        "https://api.github.com/apps-manifests/apps")

    # 检查响应
    local app_id=$(echo "${response}" | jq -r '.id // empty')
    local client_id=$(echo "${response}" | jq -r '.client_id // empty')
    local client_secret=$(echo "${response}" | jq -r '.client_secret // empty')
    local pem_file=$(echo "${response}" | jq -r '.pem_file // empty')
    local webhook_secret=$(echo "${response}" | jq -r '.webhook_secret // empty')

    if [ -z "${app_id}" ]; then
        echo "错误: 创建 GitHub App 失败"
        echo "响应: ${response}"
        exit 1
    fi

    echo "✓ GitHub App 创建成功"
    echo "  App ID: ${app_id}"
    echo "  Client ID: ${client_id}"

    # 保存配置
    save_app_config "${app_id}" "${client_id}" "${client_secret}" "${pem_file}" "${webhook_secret}"
}

# 保存 App 配置
save_app_config() {
    local app_id="$1"
    local client_id="$2"
    local client_secret="$3"
    local pem_file="$4"
    local webhook_secret="$5"

    echo ""
    echo "步骤 2: 保存配置"
    echo "----------------"

    # 创建配置目录
    local config_dir="${HOME}/.ai-factory"
    mkdir -p "${config_dir}"

    # 保存私钥
    if [ -n "${pem_file}" ]; then
        local pem_path="${config_dir}/${APP_NAME}.private-key.pem"
        echo "${pem_file}" > "${pem_path}"
        chmod 600 "${pem_path}"
        echo "✓ 私钥已保存到: ${pem_path}"
    fi

    # 保存配置文件
    local config_path="${config_dir}/${APP_NAME}.env"
    cat > "${config_path}" <<EOF
# GitHub App 配置
# 生成时间: $(date)

# App 基本信息
AI_FACTORY_APP_ID=${app_id}
AI_FACTORY_APP_CLIENT_ID=${client_id}
AI_FACTORY_APP_CLIENT_SECRET=${client_secret}
AI_FACTORY_APP_WEBHOOK_SECRET=${webhook_secret}

# App 名称
AI_FACTORY_APP_NAME=${APP_NAME}

# 私钥路径
AI_FACTORY_APP_PRIVATE_KEY_PATH=${config_dir}/${APP_NAME}.private-key.pem

# Webhook 配置
AI_FACTORY_APP_WEBHOOK_URL=https://github.com/apps/${APP_NAME}/webhooks
AI_FACTORY_APP_WEBHOOK_CONTENT_TYPE=json
AI_FACTORY_APP_WEBHOOK_INSECURE_SSL=0

# 事件配置
AI_FACTORY_APP_EVENTS=issues,issue_comment

# 权限配置
AI_FACTORY_APP_PERMISSIONS=issues:write,pull_requests:write,contents:write,actions:write,metadata:read
EOF

    chmod 600 "${config_path}"
    echo "✓ 配置已保存到: ${config_path}"

    # 生成 GitHub Secrets 配置
    generate_github_secrets "${app_id}" "${client_secret}" "${webhook_secret}"
}

# 生成 GitHub Secrets 配置
generate_github_secrets() {
    local app_id="$1"
    local client_secret="$2"
    local webhook_secret="$3"

    echo ""
    echo "步骤 3: 生成 GitHub Secrets 配置"
    echo "--------------------------------"

    local secrets_file="${HOME}/.ai-factory/${APP_NAME}-github-secrets.env"
    cat > "${secrets_file}" <<EOF
# GitHub Secrets 配置
# 需要在 ai-factory 仓库中设置这些 Secrets

# GitHub App 认证
AI_FACTORY_APP_ID=${app_id}
AI_FACTORY_APP_CLIENT_SECRET=${client_secret}
AI_FACTORY_APP_WEBHOOK_SECRET=${webhook_secret}

# GitHub Token (用于访问目标仓库)
# 需要手动设置，具有 repo 权限
# AI_FACTORY_GITHUB_TOKEN=your_github_token

# OpenAI API Key (用于正式运行)
# 需要手动设置
# AI_FACTORY_OPENAI_API_KEY=your_openai_api_key
EOF

    echo "✓ GitHub Secrets 配置已保存到: ${secrets_file}"
    echo ""
    echo "请将以下 Secrets 添加到 ai-factory 仓库："
    echo "  1. AI_FACTORY_APP_ID"
    echo "  2. AI_FACTORY_APP_CLIENT_SECRET"
    echo "  3. AI_FACTORY_APP_WEBHOOK_SECRET"
    echo "  4. AI_FACTORY_GITHUB_TOKEN"
    echo "  5. AI_FACTORY_OPENAI_API_KEY"
}

# 配置 Webhook
configure_webhook() {
    echo ""
    echo "步骤 4: 配置 Webhook"
    echo "--------------------"

    local webhook_url="https://api.github.com/repos/${GITHUB_REPOSITORY:-}/actions/workflows/webhook-receiver.yaml/dispatches"

    echo "Webhook 配置说明："
    echo ""
    echo "1. 访问 GitHub App 设置页面："
    echo "   https://github.com/apps/${APP_NAME}"
    echo ""
    echo "2. 配置 Webhook URL："
    echo "   ${webhook_url}"
    echo ""
    echo "3. 配置事件："
    echo "   - issues"
    echo "   - issue_comment"
    echo ""
    echo "4. 配置权限："
    echo "   - Issues: Read & Write"
    echo "   - Pull Requests: Read & Write"
    echo "   - Contents: Read & Write"
    echo "   - Actions: Read & Write"
    echo "   - Metadata: Read"
}

# 安装 GitHub App
install_github_app() {
    echo ""
    echo "步骤 5: 安装 GitHub App"
    echo "----------------------"

    echo "安装说明："
    echo ""
    echo "1. 访问 GitHub App 安装页面："
    echo "   https://github.com/apps/${APP_NAME}/installations/new"
    echo ""
    echo "2. 选择要安装的仓库："
    echo "   - 选择需要接入 ai-factory 的目标仓库"
    echo "   - 可以选择所有仓库或特定仓库"
    echo ""
    echo "3. 确认安装："
    echo "   - 确认权限设置"
    echo "   - 点击 Install"
    echo ""
    echo "4. 验证安装："
    echo "   - 在目标仓库创建 Issue"
    echo "   - 添加 'ai-factory-run' 标签"
    echo "   - 检查 ai-factory 仓库是否收到事件"
}

# 显示使用说明
show_usage() {
    echo ""
    echo "=========================================="
    echo "使用说明"
    echo "=========================================="
    echo ""
    echo "1. 创建 GitHub App："
    echo "   ./create-github-app.sh [app-name] [description] [url]"
    echo ""
    echo "2. 设置 GitHub Secrets："
    echo "   在 ai-factory 仓库中设置以下 Secrets："
    echo "   - AI_FACTORY_APP_ID"
    echo "   - AI_FACTORY_APP_CLIENT_SECRET"
    echo "   - AI_FACTORY_APP_WEBHOOK_SECRET"
    echo "   - AI_FACTORY_GITHUB_TOKEN"
    echo "   - AI_FACTORY_OPENAI_API_KEY"
    echo ""
    echo "3. 安装 GitHub App："
    echo "   在目标仓库中安装 GitHub App"
    echo ""
    echo "4. 测试流程："
    echo "   - 在目标仓库创建 Issue"
    echo "   - 添加 'ai-factory-run' 标签"
    echo "   - 检查 ai-factory 仓库的 workflow 是否触发"
    echo ""
    echo "=========================================="
}

# 主函数
main() {
    echo "AI Factory Bot GitHub App 创建工具"
    echo "版本: 1.0.0"
    echo ""

    # 检查依赖
    check_dependencies

    # 检查 GitHub Token
    check_github_token

    # 创建 GitHub App
    create_github_app

    # 配置 Webhook
    configure_webhook

    # 安装 GitHub App
    install_github_app

    # 显示使用说明
    show_usage

    echo ""
    echo "✓ GitHub App 创建完成！"
    echo ""
    echo "下一步："
    echo "1. 按照上述说明配置 GitHub Secrets"
    echo "2. 安装 GitHub App 到目标仓库"
    echo "3. 测试流程"
}

# 执行主函数
main "$@"
