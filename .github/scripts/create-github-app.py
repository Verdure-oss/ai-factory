#!/usr/bin/env python3
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

"""
AI Factory Bot GitHub App 创建脚本
用途：创建 GitHub App 用于监听目标仓库的 Issue 事件
"""

import json
import os
import sys
import subprocess
from pathlib import Path

# 配置
APP_NAME = "ai-factory-bot"
APP_DESCRIPTION = "AI Factory Bot - 自动处理 Issue 事件"
APP_URL = f"https://github.com/apps/{APP_NAME}"

def check_dependencies():
    """检查必要的工具"""
    missing = []

    # 检查 curl
    try:
        subprocess.run(["curl", "--version"], capture_output=True, check=True)
    except (subprocess.CalledProcessError, FileNotFoundError):
        missing.append("curl")

    # 检查 openssl
    try:
        subprocess.run(["openssl", "version"], capture_output=True, check=True)
    except (subprocess.CalledProcessError, FileNotFoundError):
        missing.append("openssl")

    if missing:
        print(f"错误: 缺少以下工具: {', '.join(missing)}")
        print("请先安装这些工具")
        sys.exit(1)

def check_github_token():
    """检查 GitHub Token"""
    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("错误: 未设置 GITHUB_TOKEN 环境变量")
        print("请设置具有 admin:org 权限的 GitHub Token")
        print("")
        print("设置方法：")
        print("  export GITHUB_TOKEN=your_github_token")
        print("")
        print("Token 需要以下权限：")
        print("  - admin:org (管理组织)")
        print("  - repo (仓库访问)")
        print("  - workflow (工作流访问)")
        sys.exit(1)

    print("[OK] GITHUB_TOKEN 已设置")
    return token

def create_github_app(token):
    """创建 GitHub App"""
    print("")
    print("步骤 1: 创建 GitHub App")
    print("------------------------")

    # GitHub App 的配置
    app_config = {
        "name": APP_NAME,
        "description": APP_DESCRIPTION,
        "url": APP_URL,
        "hook_attributes": {
            "active": True
        },
        "redirect_url": f"https://github.com/apps/{APP_NAME}",
        "callback_urls": [],
        "setup_url": f"https://github.com/apps/{APP_NAME}/installations/new",
        "public": False,
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

    print("创建 GitHub App...")

    # 使用 curl 创建 App
    config_json = json.dumps(app_config)
    result = subprocess.run(
        [
            "curl", "-s", "-X", "POST",
            "-H", f"Authorization: Bearer {token}",
            "-H", "Accept: application/vnd.github.v3+json",
            "-H", "Content-Type: application/json",
            "-d", config_json,
            "https://api.github.com/apps-manifests/apps"
        ],
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        print(f"错误: curl 命令执行失败: {result.stderr}")
        sys.exit(1)

    try:
        response = json.loads(result.stdout)
    except json.JSONDecodeError:
        print(f"错误: 无法解析响应: {result.stdout}")
        sys.exit(1)

    # 检查响应
    app_id = response.get("id")
    client_id = response.get("client_id")
    client_secret = response.get("client_secret")
    pem_file = response.get("pem_file")
    webhook_secret = response.get("webhook_secret")

    if not app_id:
        print(f"错误: 创建 GitHub App 失败")
        print(f"响应: {json.dumps(response, indent=2)}")
        sys.exit(1)

    print("[OK] GitHub App 创建成功")
    print(f"  App ID: {app_id}")
    print(f"  Client ID: {client_id}")

    return app_id, client_id, client_secret, pem_file, webhook_secret

def save_app_config(app_id, client_id, client_secret, pem_file, webhook_secret):
    """保存 App 配置"""
    print("")
    print("步骤 2: 保存配置")
    print("----------------")

    # 创建配置目录
    config_dir = Path.home() / ".ai-factory"
    config_dir.mkdir(exist_ok=True)

    # 保存私钥
    if pem_file:
        pem_path = config_dir / f"{APP_NAME}.private-key.pem"
        pem_path.write_text(pem_file)
        pem_path.chmod(0o600)
        print(f"[OK] 私钥已保存到: {pem_path}")

    # 保存配置文件
    config_path = config_dir / f"{APP_NAME}.env"
    config_content = f"""# GitHub App 配置
# 生成时间: $(date)

# App 基本信息
AI_FACTORY_APP_ID={app_id}
AI_FACTORY_APP_CLIENT_ID={client_id}
AI_FACTORY_APP_CLIENT_SECRET={client_secret}
AI_FACTORY_APP_WEBHOOK_SECRET={webhook_secret}

# App 名称
AI_FACTORY_APP_NAME={APP_NAME}

# 私钥路径
AI_FACTORY_APP_PRIVATE_KEY_PATH={config_dir}/{APP_NAME}.private-key.pem

# Webhook 配置（纯 GitHub Actions Bridge 架构下不再使用）
# 仅在部署外部 webhook-handler.py 时需要
AI_FACTORY_APP_WEBHOOK_URL=https://github.com/apps/{APP_NAME}/webhooks
AI_FACTORY_APP_WEBHOOK_CONTENT_TYPE=json
AI_FACTORY_APP_WEBHOOK_INSECURE_SSL=0

# 事件配置
AI_FACTORY_APP_EVENTS=issues,issue_comment

# 权限配置
AI_FACTORY_APP_PERMISSIONS=issues:write,pull_requests:write,contents:write,actions:write,metadata:read
"""
    config_path.write_text(config_content)
    config_path.chmod(0o600)
    print(f"[OK] 配置已保存到: {config_path}")

    # 生成 GitHub Secrets 配置
    generate_github_secrets(app_id, client_secret, webhook_secret)

def generate_github_secrets(app_id, client_secret, webhook_secret):
    """生成 GitHub Secrets 配置"""
    print("")
    print("步骤 3: 生成 GitHub Secrets 配置")
    print("--------------------------------")

    config_dir = Path.home() / ".ai-factory"
    secrets_path = config_dir / f"{APP_NAME}-github-secrets.env"
    secrets_content = f"""# GitHub Secrets 配置
# 需要在 ai-factory 仓库中设置这些 Secrets

# GitHub App 认证
AI_FACTORY_APP_ID={app_id}
AI_FACTORY_APP_CLIENT_SECRET={client_secret}
AI_FACTORY_APP_WEBHOOK_SECRET={webhook_secret}

# GitHub Token (用于 ai-factory 访问目标仓库，创建 PR 等)
# 需要手动设置，具有 repo 权限
# AI_FACTORY_GITHUB_TOKEN=your_github_token

# OpenAI API Key (用于正式运行)
# 需要手动设置
# AI_FACTORY_OPENAI_API_KEY=your_openai_api_key

# --- 目标仓库需要设置的 Secrets ---
# 以下 Secret 需要在每个目标仓库中设置（不是 ai-factory 仓库）

# AI_FACTORY_DISPATCH_TOKEN: 用于 Bridge Workflow 调用 ai-factory 的 workflow_dispatch API
# 需要 PAT (Personal Access Token)，权限: repo + workflow
# AI_FACTORY_DISPATCH_TOKEN=your_pat_token

# AI_FACTORY_REPO (可选): ai-factory 中央仓库地址，默认 Verdure-oss/ai-factory
# AI_FACTORY_REPO=Verdure-oss/ai-factory
"""
    secrets_path.write_text(secrets_content)
    print(f"[OK] GitHub Secrets 配置已保存到: {secrets_path}")
    print("")
    print("=== ai-factory 中央仓库 Secrets ===")
    print("  1. AI_FACTORY_APP_ID")
    print("  2. AI_FACTORY_APP_CLIENT_SECRET")
    print("  3. AI_FACTORY_APP_WEBHOOK_SECRET")
    print("  4. AI_FACTORY_GITHUB_TOKEN")
    print("  5. AI_FACTORY_OPENAI_API_KEY")
    print("")
    print("=== 每个目标仓库 Secrets ===")
    print("  1. AI_FACTORY_DISPATCH_TOKEN (PAT with repo+workflow)")
    print("  2. AI_FACTORY_REPO (可选, 默认: Verdure-oss/ai-factory)")

def configure_bridge():
    """配置 Bridge Workflow（替代 Webhook）"""
    print("")
    print("步骤 4: 部署 Bridge Workflow 到目标仓库")
    print("------------------------------------------")
    print("")
    print("【重要】纯 GitHub Actions 架构不再需要配置 Webhook URL。")
    print("改为在目标仓库中部署 Bridge Workflow 来转发事件。")
    print("")
    print("1. 创建 Personal Access Token (PAT)：")
    print("   访问: https://github.com/settings/tokens")
    print("   权限要求: repo + workflow")
    print("   记为: AI_FACTORY_DISPATCH_TOKEN")
    print("")
    print("2. 在目标仓库中设置 Secrets：")
    print("   进入目标仓库 → Settings → Secrets → Actions → New secret")
    print("")
    print("   Secret 1:")
    print("     Name:  AI_FACTORY_DISPATCH_TOKEN")
    print("     Value: 上面创建的 PAT")
    print("")
    print("   Secret 2 (可选，默认为 Verdure-oss/ai-factory):")
    print("     Name:  AI_FACTORY_REPO")
    print("     Value: ai-factory 中央仓库地址 (格式: owner/repo)")
    print("")
    print("3. 部署 Bridge Workflow 文件到目标仓库：")
    print("   将以下文件复制到目标仓库的 .github/workflows/ 目录下:")
    print("")
    print("   源文件: .github/workflows/templates/ai-factory-bridge.yaml")
    print("   目标路径: <目标仓库>/.github/workflows/ai-factory-bridge.yaml")
    print("")
    print("   Bridge Workflow 会监听 issues: labeled 事件，")
    print("   并通过 workflow_dispatch API 调用 ai-factory 中央仓库的")
    print("   webhook-receiver.yaml 来处理事件。")
    print("")
    print("4. GitHub App 权限配置（安装时自动设置）：")
    print("   - Issues: Read & Write")
    print("   - Pull Requests: Read & Write")
    print("   - Contents: Read & Write")
    print("   - Actions: Read & Write")
    print("   - Metadata: Read")

def install_github_app():
    """安装 GitHub App 并部署 Bridge Workflow"""
    print("")
    print("步骤 5: 安装 GitHub App 并部署 Bridge Workflow")
    print("------------------------------------------------")

    print("5a. 安装 GitHub App：")
    print("")
    print("1. 访问 GitHub App 安装页面：")
    print(f"   https://github.com/apps/{APP_NAME}/installations/new")
    print("")
    print("2. 选择要安装的仓库：")
    print("   - 选择需要接入 ai-factory 的目标仓库")
    print("   - 可以选择所有仓库或特定仓库")
    print("")
    print("3. 确认安装：")
    print("   - 确认权限设置")
    print("   - 点击 Install")
    print("")
    print("5b. 部署 Bridge Workflow 到目标仓库：")
    print("")
    print("1. 复制 Bridge Workflow 文件：")
    print("   源: ai-factory/.github/workflows/templates/ai-factory-bridge.yaml")
    print("   目标: <目标仓库>/.github/workflows/ai-factory-bridge.yaml")
    print("")
    print("2. 在目标仓库设置 Secrets：")
    print("   - AI_FACTORY_DISPATCH_TOKEN: 具有 repo+workflow 权限的 PAT")
    print("   - AI_FACTORY_REPO (可选): 默认为 Verdure-oss/ai-factory")
    print("")
    print("3. 验证部署：")
    print("   - 在目标仓库创建 Issue")
    print("   - 添加 'ai-factory-run' 标签")
    print("   - 检查目标仓库的 Actions 中 Bridge Workflow 是否运行")
    print("   - 检查 ai-factory 中央仓库是否收到事件并触发处理")

def show_usage():
    """显示使用说明"""
    print("")
    print("==========================================")
    print("使用说明（纯 GitHub Actions 架构）")
    print("==========================================")
    print("")
    print("架构流程：")
    print("  目标仓库 Issue labeled")
    print("    → Bridge Workflow (on: issues: labeled)")
    print("    → workflow_dispatch API 调用")
    print("    → ai-factory webhook-receiver.yaml")
    print("    → repository_dispatch")
    print("    → ai-factory process-issue.yaml")
    print("")
    print("1. 创建 GitHub App：")
    print("   python create-github-app.py")
    print("")
    print("2. 设置 ai-factory 中央仓库 Secrets：")
    print("   - AI_FACTORY_APP_ID")
    print("   - AI_FACTORY_APP_CLIENT_SECRET")
    print("   - AI_FACTORY_APP_WEBHOOK_SECRET")
    print("   - AI_FACTORY_GITHUB_TOKEN (用于访问目标仓库)")
    print("   - AI_FACTORY_OPENAI_API_KEY")
    print("")
    print("3. 安装 GitHub App 到目标仓库")
    print("")
    print("4. 在目标仓库部署 Bridge Workflow：")
    print("   - 复制 ai-factory-bridge.yaml 到目标仓库 .github/workflows/")
    print("   - 设置 AI_FACTORY_DISPATCH_TOKEN secret (PAT with repo+workflow)")
    print("")
    print("5. 测试流程：")
    print("   - 在目标仓库创建 Issue")
    print("   - 添加 'ai-factory-run' 标签")
    print("   - 检查目标仓库 Bridge Workflow 是否运行")
    print("   - 检查 ai-factory 仓库 process-issue 是否触发")
    print("")
    print("==========================================")

def main():
    """主函数"""
    print("AI Factory Bot GitHub App 创建工具")
    print("版本: 2.0.0 (Bridge Architecture)")
    print("")

    # 检查依赖
    check_dependencies()

    # 检查 GitHub Token
    token = check_github_token()

    # 创建 GitHub App
    app_id, client_id, client_secret, pem_file, webhook_secret = create_github_app(token)

    # 保存配置
    save_app_config(app_id, client_id, client_secret, pem_file, webhook_secret)

    # 配置 Bridge Workflow
    configure_bridge()

    # 安装 GitHub App
    install_github_app()

    # 显示使用说明
    show_usage()

    print("")
    print("[OK] GitHub App 创建完成！")
    print("")
    print("下一步：")
    print("1. 配置 ai-factory 中央仓库的 GitHub Secrets")
    print("2. 安装 GitHub App 到目标仓库")
    print("3. 在目标仓库部署 Bridge Workflow 并设置 AI_FACTORY_DISPATCH_TOKEN")
    print("4. 测试完整流程")

if __name__ == "__main__":
    main()
