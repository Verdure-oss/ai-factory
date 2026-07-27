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
GitHub App Webhook Handler
用途：接收 GitHub App 的 Webhook 事件，发送 repository_dispatch 到 ai-factory 仓库
"""

import json
import os
import sys
import hmac
import hashlib
from http.server import HTTPServer, BaseHTTPRequestHandler
import requests

# 配置
WEBHOOK_SECRET = os.environ.get("WEBHOOK_SECRET", "your-webhook-secret")
GITHUB_TOKEN = os.environ.get("GITHUB_TOKEN", "")
AI_FACTORY_REPO = "Verdure-oss/ai-factory"

class WebhookHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        # 读取请求体
        content_length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(content_length)

        # 验证 Webhook 签名
        signature = self.headers.get('X-Hub-Signature-256')
        if not self.verify_signature(body, signature):
            self.send_response(401)
            self.end_headers()
            self.wfile.write(b'Unauthorized')
            return

        # 解析事件
        try:
            event = json.loads(body)
            event_type = self.headers.get('X-GitHub-Event')
            print(f"Received event: {event_type}")

            # 处理 issues 事件
            if event_type == 'issues':
                self.handle_issues_event(event)
            elif event_type == 'issue_comment':
                self.handle_issue_comment_event(event)
            else:
                print(f"Ignoring event: {event_type}")

            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'OK')

        except Exception as e:
            print(f"Error processing event: {e}")
            self.send_response(500)
            self.end_headers()
            self.wfile.write(b'Internal Server Error')

    def verify_signature(self, body, signature):
        """验证 Webhook 签名"""
        if not signature:
            return False

        expected_signature = 'sha256=' + hmac.new(
            WEBHOOK_SECRET.encode('utf-8'),
            body,
            hashlib.sha256
        ).hexdigest()

        return hmac.compare_digest(signature, expected_signature)

    def handle_issues_event(self, event):
        """处理 issues 事件"""
        action = event.get('action')
        issue = event.get('issue', {})
        repository = event.get('repository', {})
        sender = event.get('sender', {})

        issue_number = issue.get('number')
        repo_full_name = repository.get('full_name')
        sender_login = sender.get('login')

        # 获取标签名称
        label_name = ''
        if action == 'labeled':
            label = event.get('label', {})
            label_name = label.get('name', '')

        print(f"Issue #{issue_number} in {repo_full_name}: {action} by {sender_login}")

        # 检查是否是 ai-factory 相关标签
        if action == 'labeled' and label_name.startswith('ai-factory'):
            print(f"Triggering ai-factory for label: {label_name}")
            self.send_repository_dispatch(repo_full_name, issue_number, action, label_name, sender_login)

    def handle_issue_comment_event(self, event):
        """处理 issue_comment 事件"""
        action = event.get('action')
        issue = event.get('issue', {})
        repository = event.get('repository', {})
        sender = event.get('sender', {})

        issue_number = issue.get('number')
        repo_full_name = repository.get('full_name')
        sender_login = sender.get('login')

        print(f"Issue comment #{issue_number} in {repo_full_name}: {action} by {sender_login}")

        # 检查 Issue 是否有 ai-factory 标签
        labels = issue.get('labels', [])
        has_ai_factory_label = any(label.get('name', '').startswith('ai-factory') for label in labels)

        if has_ai_factory_label:
            print(f"Issue has ai-factory label, triggering...")
            self.send_repository_dispatch(repo_full_name, issue_number, action, '', sender_login)

    def send_repository_dispatch(self, repository, issue_number, action, label_name, sender):
        """发送 repository_dispatch 事件到 ai-factory 仓库"""
        payload = {
            'event_type': f'issue-{action}',
            'client_payload': {
                'repository': repository,
                'issue_number': issue_number,
                'action': action,
                'label_name': label_name,
                'sender': sender,
                'trigger_type': 'run' if label_name == 'ai-factory-run' else 'smoke' if label_name == 'ai-factory-smoke' else 'other',
                'timestamp': self.get_timestamp()
            }
        }

        headers = {
            'Authorization': f'Bearer {GITHUB_TOKEN}',
            'Accept': 'application/vnd.github.v3+json',
            'Content-Type': 'application/json'
        }

        url = f'https://api.github.com/repos/{AI_FACTORY_REPO}/dispatches'

        try:
            response = requests.post(url, json=payload, headers=headers)
            if response.status_code == 204:
                print(f"✓ repository_dispatch sent successfully")
            else:
                print(f"Error sending repository_dispatch: {response.status_code} - {response.text}")
        except Exception as e:
            print(f"Error sending repository_dispatch: {e}")

    def get_timestamp(self):
        """获取当前时间戳"""
        from datetime import datetime, timezone
        return datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')

def main():
    """主函数"""
    port = int(os.environ.get('PORT', 8080))
    server = HTTPServer(('0.0.0.0', port), WebhookHandler)
    print(f"Starting webhook handler on port {port}")
    server.serve_forever()

if __name__ == '__main__':
    main()
