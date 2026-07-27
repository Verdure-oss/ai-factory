# 中央 ai-factory 部署指南

## 概述

本指南将帮助您部署中央 ai-factory 系统，实现目标仓库打标签后自动触发 ai-factory 处理的功能。

## 架构说明

```
目标仓库 Issue 打标签
    ↓
GitHub App 接收事件
    ↓
Webhook workflow 触发 (repository_dispatch)
    ↓
主 workflow 执行 ai-factory 逻辑
    ↓
修改目标仓库并创建 PR
```

## 部署步骤

### 步骤 1: 创建 GitHub App

1. **运行创建脚本**：
   ```bash
   cd ai-factory
   chmod +x .github/scripts/create-github-app.sh
   export GITHUB_TOKEN=your_github_token
   ./.github/scripts/create-github-app.sh
   ```

2. **按照脚本提示完成配置**：
   - 脚本会自动创建 GitHub App
   - 生成私钥和配置文件
   - 显示需要设置的 GitHub Secrets

3. **保存生成的配置**：
   - 私钥位置：`~/.ai-factory/ai-factory-bot.private-key.pem`
   - 配置文件：`~/.ai-factory/ai-factory-bot.env`

### 步骤 2: 配置 GitHub Secrets

在 ai-factory 仓库中设置以下 Secrets：

1. **进入仓库设置**：
   - 访问 `https://github.com/your-org/ai-factory/settings/secrets/actions`

2. **添加以下 Secrets**：

   | Secret 名称 | 说明 | 来源 |
   |------------|------|------|
   | `AI_FACTORY_APP_ID` | GitHub App ID | 创建脚本输出 |
   | `AI_FACTORY_APP_CLIENT_SECRET` | GitHub App Client Secret | 创建脚本输出 |
   | `AI_FACTORY_APP_WEBHOOK_SECRET` | Webhook 密钥 | 创建脚本输出 |
   | `AI_FACTORY_GITHUB_TOKEN` | GitHub Token（访问目标仓库） | 手动创建 |
   | `AI_FACTORY_OPENAI_API_KEY` | OpenAI API Key（正式运行） | 手动创建 |

3. **配置 Variables**（可选）：

   | Variable 名称 | 默认值 | 说明 |
  --------------|--------|------|
   | `AI_FACTORY_OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI API 地址 |
   | `AI_FACTORY_OPENAI_MODEL` | `gpt-4.1` | 使用的模型 |
   | `AI_FACTORY_OPENAI_TEMPERATURE` | `1` | 温度参数 |
   | `AI_FACTORY_OPENAI_MAX_TOKENS` | `48000` | 最大 token 数 |
   | `AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS` | `40` | 最大工具调用轮数 |
   | `AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS` | `3` | 最大修复轮数 |
   | `AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS` | `1800` | 总超时时间 |

### 步骤 3: 配置 GitHub App Webhook

1. **访问 GitHub App 设置**：
   - 访问 `https://github.com/apps/ai-factory-bot`

2. **配置 Webhook URL**：
   - URL: `https://api.github.com/repos/your-org/ai-factory/actions/workflows/webhook-receiver.yaml/dispatches`
   - Content Type: `application/json`
   - Secret: 创建脚本生成的 Webhook 密钥

3. **配置事件**：
   - ✅ Issues
   - ✅ Issue comment

4. **配置权限**：
   - Issues: Read & Write
   - Pull Requests: Read & Write
   - Contents: Read & Write
   - Actions: Read & Write
   - Metadata: Read

### 步骤 4: 安装 GitHub App 到目标仓库

1. **访问安装页面**：
   - 访问 `https://github.com/apps/ai-factory-bot/installations/new`

2. **选择仓库**：
   - 选择需要接入 ai-factory 的目标仓库
   - 可以选择所有仓库或特定仓库

3. **确认安装**：
   - 确认权限设置
   - 点击 Install

### 步骤 5: 测试流程

1. **在目标仓库创建 Issue**：
   - 访问目标仓库
   - 创建新 Issue

2. **添加标签**：
   - 添加 `ai-factory` 标签
   - 添加 `ai-factory-run` 或 `ai-factory-smoke` 标签

3. **检查 ai-factory 仓库**：
   - 访问 ai-factory 仓库的 Actions 页面
   - 查看是否触发了 workflow

4. **查看结果**：
   - 检查 Issue 评论
   - 查看是否创建了 PR

## 工作流程详解

### 1. Webhook 接收器 (`webhook-receiver.yaml`)

**触发条件**：
- GitHub App 发送的 `workflow_dispatch` 事件

**处理流程**：
1. 验证输入参数
2. 检查标签资格
3. 发送 `repository_dispatch` 事件到主 workflow

**关键逻辑**：
- 只处理 `ai-factory-run` 和 `ai-factory-smoke` 标签
- 忽略其他标签
- 记录处理日志

### 2. 主 workflow (`process-issue.yaml`)

**触发条件**：
- `repository_dispatch` 事件（类型：`issue-labeled`、`issue-opened`、`issue-closed`、`issue-reopened`）

**处理流程**：
1. 解析事件负载
2. 检查 OpenAI 配置（仅正式运行）
3. 创建 Kind 集群
4. 安装组件（FactoryTask CRD、agent-sandbox）
5. 构建编码 Agent 镜像
6. 创建沙箱预热池
7. 运行 FactoryTask 控制器
8. 等待任务完成
9. 更新 Issue 状态

**关键逻辑**：
- 支持 `ai-factory-run`（正式运行）和 `ai-factory-smoke`（冒烟测试）两种模式
- 使用集中保存的 Secrets
- 自动更新 Issue 状态和评论

## 配置说明

### 环境变量

| 变量名 | 必需 | 默认值 | 说明 |
|--------|------|--------|------|
| `AI_FACTORY_APP_ID` | ✅ | - | GitHub App ID |
| `AI_FACTORY_APP_CLIENT_SECRET` | ✅ | - | GitHub App Client Secret |
| `AI_FACTORY_APP_WEBHOOK_SECRET` | ✅ | - | Webhook 密钥 |
| `AI_FACTORY_GITHUB_TOKEN` | ✅ | - | GitHub Token |
| `AI_FACTORY_OPENAI_API_KEY` | ✅ | - | OpenAI API Key |
| `AI_FACTORY_OPENAI_BASE_URL` | ❌ | `https://api.openai.com/v1` | OpenAI API 地址 |
| `AI_FACTORY_OPENAI_MODEL` | ❌ | `gpt-4.1` | 使用的模型 |
| `AI_FACTORY_OPENAI_TEMPERATURE` | ❌ | `1` | 温度参数 |
| `AI_FACTORY_OPENAI_MAX_TOKENS` | ❌ | `48000` | 最大 token 数 |
| `AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS` | ❌ | `40` | 最大工具调用轮数 |
| `AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS` | ❌ | `3` | 最大修复轮数 |
| `AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS` | ❌ | `1800` | 总超时时间 |

### 标签系统

| 标签 | 颜色 | 说明 |
|------|------|------|
| `ai-factory` | - | 基础标签，标识 ai-factory 相关 Issue |
| `ai-factory-run` | - | 触发正式运行（使用 AI token） |
| `ai-factory-smoke` | - | 触发冒烟测试（不使用 AI token） |
| `ai-factory-running` | `1D76DB` | 正在处理中 |
| `ai-factory-done` | `0E8A16` | 处理完成 |
| `ai-factory-failed` | `B60205` | 处理失败 |

## 故障排除

### 1. GitHub App 事件未触发

**检查项**：
- GitHub App 是否正确安装到目标仓库
- Webhook URL 是否正确配置
- 事件类型是否包含 Issues

**解决方案**：
1. 访问 GitHub App 设置页面
2. 检查 Webhook 配置
3. 查看 Webhook 交付记录

### 2. Webhook workflow 未触发

**检查项**：
- GitHub App 是否有 Actions 权限
- Webhook URL 是否指向正确的 workflow
- workflow 文件是否存在

**解决方案**：
1. 检查 GitHub App 权限
2. 验证 Webhook URL
3. 确认 workflow 文件存在

### 3. 主 workflow 执行失败

**检查项**：
- GitHub Secrets 是否正确配置
- OpenAI API Key 是否有效
- Kind 集群是否正常创建

**解决方案**：
1. 检查 GitHub Secrets 配置
2. 验证 OpenAI API Key
3. 查看 workflow 日志

### 4. FactoryTask 执行失败

**检查项**：
- 目标仓库权限是否足够
- Agent 镜像是否正常构建
- 沙箱是否正常运行

**解决方案**：
1. 检查 GitHub Token 权限
2. 查看 Agent 镜像构建日志
3. 检查 Kind 集群状态

## 监控和日志

### 1. GitHub Actions 日志

- 访问 ai-factory 仓库的 Actions 页面
- 查看 workflow 运行记录
- 检查每一步的详细日志

### 2. Issue 评论

- ai-factory 会在 Issue 上添加评论
- 包含任务状态和链接
- 失败时会显示错误信息

### 3. GitHub App Webhook 日志

- 访问 GitHub App 设置页面
- 查看 Webhook 交付记录
- 检查事件处理状态

## 安全注意事项

### 1. Secrets 管理

- 所有 Secrets 都存储在 GitHub 仓库设置中
- 不要在代码中硬编码 Secrets
- 定期轮换 Secrets

### 2. 权限最小化

- GitHub App 只授予必要的权限
- GitHub Token 只授予必要的仓库访问权限
- 定期审查权限设置

### 3. 网络安全

- Kind 集群运行在 GitHub Actions 环境中
- 不需要暴露外部端口
- 所有通信通过 GitHub API 进行

## 扩展和优化

### 1. 支持更多仓库

- 安装 GitHub App 到更多仓库
- 配置仓库特定的 Variables
- 监控所有仓库的处理状态

### 2. 优化性能

- 使用 Kind 集群预热
- 优化 Agent 镜像大小
- 并行处理多个任务

### 3. 增强监控

- 添加 Slack/Teams 通知
- 集成监控系统
- 设置告警规则

### 4. 自定义处理逻辑

- 修改主 workflow 的处理逻辑
- 添加自定义验证步骤
- 集成其他工具和服务

## 版本历史

### v1.0.0 (2026-07-27)
- 初始版本
- 支持 GitHub App 集成
- 支持 `ai-factory-run` 和 `ai-factory-smoke` 标签
- 支持中央式架构

---

**文档维护者**: Claude
**创建时间**: 2026-07-27
**最后更新**: 2026-07-27
