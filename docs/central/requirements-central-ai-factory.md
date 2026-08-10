# 中央 ai-factory 需求文档

## 1. 核心目标

实现中央式 ai-factory 架构，目标仓库只需一次性配置权限，之后完全自动化。

### 目标流程
```
目标仓库 Issue 打标签
    ↓
GitHub App 接收事件
    ↓
Webhook workflow 触发
    ↓
repository_dispatch 到主 workflow
    ↓
ai-factory 执行后续操作
    ↓
修改目标仓库并创建 PR
```

## 2. 架构设计

### 2.1 组件架构
```
┌─────────────────────────────────────────────────────────────┐
│                    目标仓库 (不需要配置 workflow)                │
│  Issue 打标签 → GitHub App 接收事件                           │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│                    GitHub App (一次性配置)                      │
│  监听 Issue 事件 → 触发 ai-factory 仓库的 workflow             │
└─────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────┐
│                    中央 ai-factory 仓库                        │
│  Webhook workflow → 主 workflow → 执行任务                     │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 关键组件

| 组件 | 说明 | 状态 |
|------|------|------|
| **GitHub App** | 监听目标仓库 Issue 事件 | ✅ 脚本已创建 |
| **Webhook workflow** | 接收 GitHub App 事件，触发主 workflow | ✅ 已创建 |
| **主 workflow** | 执行 ai-factory 核心逻辑 | ✅ 已创建 |
| **FactoryTask CRD** | 任务模型定义 | ✅ 完成 |
| **控制器** | 执行任务 | ✅ 完成 |
| **Agent 编排** | 多层 agent 系统 | ✅ 完成 |

## 3. 技术决策

### 3.1 触发方式
- **使用 `repository_dispatch`**: Webhook workflow 发送 `repository_dispatch` 事件到主 workflow
- **原因**: 更灵活，可以传递任意数据

### 3.2 标签系统
沿用现有标签系统：
- `ai-factory`: 基础标签，标识 ai-factory 相关 Issue
- `ai-factory-run`: 触发正式运行（使用 AI token）
- `ai-factory-smoke`: 触发冒烟测试（不使用 AI token）

### 3.3 GitHub App 权限
标准权限：
- **Issues**: 读取 Issue 信息（标题、body、标签等）
- **Actions**: 触发 ai-factory 仓库的 workflow（`repository_dispatch`）
- **Pull Requests**: 创建 PR、评论
- **Contents**: 读取仓库内容

### 3.4 实现方式
- **GitHub App 创建**: 使用 Shell 脚本（后续优化为 Terraform/Pulumi）
- **Webhook 接收器**: 专用 Webhook workflow（仅转发，不处理逻辑）

## 4. 工作流程

### 4.1 GitHub App 配置流程
1. 使用 Shell 脚本创建 GitHub App
2. 配置 Webhook 指向 ai-factory 仓库的 Webhook workflow
3. 配置权限：Issues + Actions + Pull Requests + Contents
4. 安装 GitHub App 到目标仓库

### 4.2 事件处理流程
1. 目标仓库 Issue 被打上 `ai-factory-run` 或 `ai-factory-smoke` 标签
2. GitHub App 接收 `issues` 事件（`action: labeled`）
3. GitHub App 触发 ai-factory 仓库的 Webhook workflow
4. Webhook workflow 发送 `repository_dispatch` 事件到主 workflow
5. 主 workflow 执行 ai-factory 核心逻辑
6. 修改目标仓库并创建 PR

### 4.3 主 workflow 执行流程
1. 接收 `repository_dispatch` 事件
2. 解析 payload（仓库名、Issue 编号、标签等）
3. 创建 Kind 集群（或使用现有集群）
4. 安装组件（FactoryTask CRD、agent-sandbox）
5. 构建编码 Agent 镜像
6. 创建沙箱预热池
7. 运行 FactoryTask 控制器
8. 等待任务完成
9. 更新 Issue 状态

## 5. 文件结构

### 5.1 已创建的文件
```
.github/
├── workflows/
│   ├── webhook-receiver.yaml          # ✅ Webhook workflow (接收 GitHub App 事件)
│   ├── process-issue.yaml             # ✅ 主 workflow (执行 ai-factory 逻辑)
│   └── issue-factorytask.yaml         # 现有 workflow (保留，用于测试)
├── scripts/
│   └── create-github-app.sh           # ✅ 创建 GitHub App 的脚本
└── ISSUE_TEMPLATE/
    └── ai-factory-task.yml            # 现有模板 (保留)
```

### 5.2 现有文件
```
.github/
├── workflows/
│   ├── issue-factorytask.yaml         # 现有 workflow (分散式)
│   ├── ai-factory-issue-reusable.yaml # 可重用 workflow
│   └── kind-run-once.yaml            # 测试 workflow
└── ISSUE_TEMPLATE/
    └── ai-factory-task.yml            # Issue 模板
```

## 6. 配置项

### 6.1 GitHub App 配置
- **App ID**: GitHub App 的 ID
- **Private Key**: GitHub App 的私钥（用于签名）
- **Webhook Secret**: Webhook 的密钥（用于验证）

### 6.2 Repository Secrets
- `AI_FACTORY_APP_ID`: GitHub App ID
- `AI_FACTORY_APP_PRIVATE_KEY`: GitHub App 私钥
- `AI_FACTORY_APP_WEBHOOK_SECRET`: Webhook 密钥
- `AI_FACTORY_OPENAI_API_KEY`: OpenAI API Key（用于正式运行）
- `AI_FACTORY_GITHUB_TOKEN`: GitHub Token（用于操作目标仓库）

### 6.3 Repository Variables
- `AI_FACTORY_OPENAI_BASE_URL`: OpenAI API 地址
- `AI_FACTORY_OPENAI_MODEL`: 使用的模型
- `AI_FACTORY_OPENAI_TEMPERATURE`: 温度参数
- `AI_FACTORY_OPENAI_MAX_TOKENS`: 最大 token 数
- `AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS`: 最大工具调用轮数
- `AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS`: 最大修复轮数
- `AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS`: 总超时时间

## 7. 实现步骤

### 7.1 第一阶段：基础框架
1. 创建 GitHub App（使用 Shell 脚本）
2. 创建 Webhook workflow（接收事件）
3. 创建主 workflow（执行逻辑）
4. 测试基本流程

### 7.2 第二阶段：功能完善
1. 完善错误处理
2. 添加日志和监控
3. 优化性能
4. 添加文档

### 7.3 第三阶段：生产就绪
1. 安全性加固
2. 多租户支持
3. 扩展性优化
4. 监控和告警

## 8. 待确认问题

### 8.1 已确认
- ✅ 使用 GitHub App 连接目标仓库
- ✅ 使用 GitHub Actions 作为 Webhook 接收器
- ✅ 使用 `repository_dispatch` 触发主 workflow
- ✅ GitHub App 有标准权限
- ✅ 使用 Shell 脚本创建 GitHub App
- ✅ 沿用现有标签系统
- ✅ Webhook workflow 仅负责接收事件
- ✅ GitHub App 名称：ai-factory-bot
- ✅ 主 workflow 名称：process-issue.yaml
- ✅ 保留现有 workflow，新建两个 workflow：webhook-receiver.yaml + process-issue.yaml

### 8.2 待确认
- ❓ Webhook workflow 的具体实现细节
- ❓ 主 workflow 的具体实现细节
- ❓ 错误处理和重试机制
- ❓ 日志和监控方案

## 9. 风险和注意事项

### 9.1 安全风险
- GitHub App 私钥需要安全存储
- Webhook 密钥需要验证
- 目标仓库权限需要最小化

### 9.2 性能风险
- GitHub Actions 可能有排队延迟
- Kind 集群创建可能耗时
- Agent 执行可能超时

### 9.3 兼容性风险
- 现有 workflow 需要保留（向后兼容）
- 新架构需要与现有组件兼容
- 需要处理多种仓库类型（个人/Organization）

## 10. 成功标准

### 10.1 功能标准
- 目标仓库 Issue 打标签后，自动触发 ai-factory
- 成功修改目标仓库并创建 PR
- 支持 `ai-factory-run` 和 `ai-factory-smoke` 两种模式

### 10.2 性能标准
- 从打标签到开始执行 < 5 分钟
- 任务执行时间 < 30 分钟
- 成功率 > 90%

### 10.3 用户体验标准
- 目标仓库只需一次性配置
- 执行过程有清晰的状态更新
- 失败时有详细的错误信息

---

**文档维护者**: Claude
**创建时间**: 2026-07-27
**最后更新**: 2026-07-27
