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

# 发送 repository_dispatch 事件到 ai-factory 仓库
# 用法: ./send-dispatch.sh <repository> <issue_number> <action> <label_name> <sender>

set -euo pipefail

# 参数检查
if [ $# -lt 5 ]; then
    echo "用法: $0 <repository> <issue_number> <action> <label_name> <sender>"
    echo "示例: $0 Verdure-oss/Test 3 labeled ai-factory-smoke Verdure-oss"
    exit 1
fi

REPOSITORY="$1"
ISSUE_NUMBER="$2"
ACTION="$3"
LABEL_NAME="$4"
SENDER="$5"

# 配置
AI_FACTORY_REPO="Verdure-oss/ai-factory"
GITHUB_TOKEN="${AI_FACTORY_GITHUB_TOKEN:-}"

# 检查 GitHub Token
if [ -z "$GITHUB_TOKEN" ]; then
    echo "错误: 未设置 AI_FACTORY_GITHUB_TOKEN 环境变量"
    exit 1
fi

# 确定触发类型
if [ "$ACTION" == "labeled" ]; then
    case "$LABEL_NAME" in
        ai-factory-run)
            TRIGGER_TYPE="run"
            ;;
        ai-factory-smoke)
            TRIGGER_TYPE="smoke"
            ;;
        ai-factory)
            TRIGGER_TYPE="base"
            ;;
        *)
            TRIGGER_TYPE="none"
            ;;
    esac
else
    TRIGGER_TYPE="other"
fi

# 检查是否应该触发
if [ "$TRIGGER_TYPE" == "none" ] || [ "$TRIGGER_TYPE" == "base" ]; then
    echo "跳过: 非触发标签或条件不满足"
    exit 0
fi

# 构造 payload
PAYLOAD=$(cat <<EOF
{
    "event_type": "issue-${ACTION}",
    "client_payload": {
        "repository": "${REPOSITORY}",
        "issue_number": ${ISSUE_NUMBER},
        "action": "${ACTION}",
        "label_name": "${LABEL_NAME}",
        "sender": "${SENDER}",
        "trigger_type": "${TRIGGER_TYPE}",
        "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    }
}
EOF
)

echo "发送 repository_dispatch 事件..."
echo "仓库: ${REPOSITORY}"
echo "Issue: #${ISSUE_NUMBER}"
echo "动作: ${ACTION}"
echo "标签: ${LABEL_NAME}"
echo "触发者: ${SENDER}"
echo "触发类型: ${TRIGGER_TYPE}"

# 发送 repository_dispatch
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/vnd.github.v3+json" \
    -H "Content-Type: application/json" \
    -d "${PAYLOAD}" \
    "https://api.github.com/repos/${AI_FACTORY_REPO}/dispatches")

HTTP_CODE=$(echo "${RESPONSE}" | tail -n1)
BODY=$(echo "${RESPONSE}" | sed '$d')

if [[ "${HTTP_CODE}" -ge 200 && "${HTTP_CODE}" -lt 300 ]]; then
    echo "✓ repository_dispatch 发送成功"
else
    echo "错误: repository_dispatch 发送失败"
    echo "HTTP 状态码: ${HTTP_CODE}"
    echo "响应: ${BODY}"
    exit 1
fi
