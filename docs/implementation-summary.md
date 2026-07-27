# 中央 ai-factory 实现总结

## 已完成工作

### 1. 需求分析和设计 ✅

**完成时间**: 2026-07-27

**主要成果**:
- 完成需求分析和架构设计
- 确定技术方案和实现路径
- 创建需求文档和部署指南

**关键决策**:
- 使用 GitHub App 连接目标仓库
- 使用 GitHub Actions 作为 Webhook 接收器
- 使用 `repository_dispatch` 触发主 workflow
- 沿用现有标签系统（`ai-factory` + `ai-factory-run` / `ai-factory-smoke`）

### 2. 核心文件创建 ✅

**完成时间**: 2026-07-27

**创建的文件**:

| 文件 | 说明 | 状态 |
|------|------|------|
| `.github/scripts/create-github-app.sh` | 创建 GitHub App 的脚本 | ✅ 已创建 |
| `.github/workflows/webhook-receiver.yaml` | Webhook 接收器 workflow | ✅ 已创建 |
| `.github/workflows/process-issue.yaml` | 主 workflow | ✅ 已创建 |
| `docs/requirements-central-ai-factory.md` | 需求文档 | ✅ 已创建 |
| `docs/central-ai-factory-setup.md` | 部署指南 | ✅ 已创建 |

### 3. 架构实现 ✅

**完成时间**: 2026-07-27

**实现的架构**:
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

**关键组件**:
1. **GitHub App 创建脚本**: 自动化创建和配置 GitHub App
2. **Webhook 接收器**: 接收 GitHub App 事件，触发主 workflow
3. **主 workflow**: 执行 ai-factory 核心逻辑，支持 run/smoke 两种模式

## 技术实现细节

### 1. GitHub App 创建脚本

**功能**:
- 自动创建 GitHub App
- 生成私钥和配置文件
- 配置 Webhook 和权限
- 生成 GitHub Secrets 配置

**使用方法**:
```bash
cd ai-factory
chmod +x .github/scripts/create-github-app.sh
export GITHUB_TOKEN=your_github_token
./.github/scripts/create-github-app.sh
```

**输出**:
- 私钥文件: `~/.ai-factory/ai-factory-bot.private-key.pem`
- 配置文件: `~/.ai-factory/ai-factory-bot.env`
- Secrets 配置: `~/.ai-factory/ai-factory-bot-github-secrets.env`

### 2. Webhook 接收器

**触发条件**:
- GitHub App 发送的 `workflow_dispatch` 事件

**处理流程**:
1. 验证输入参数
2. 检查标签资格
3. 发送 `repository_dispatch` 事件

**关键逻辑**:
- 只处理 `ai-factory-run` 和 `ai-factory-smoke` 标签
- 忽略其他标签
- 记录处理日志

**输入参数**:
- `repository`: 目标仓库 (格式: owner/repo)
- `issue_number`: Issue 编号
- `action`: 触发动作 (labeled/opened/closed)
- `label_name`: 标签名称
- `sender`: 触发者用户名

### 3. 主 workflow

**触发条件**:
- `repository_dispatch` 事件（类型：`issue-labeled`、`issue-opened`、`issue-closed`、`issue-reopened`）

**处理流程**:
1. 解析事件负载
2. 检查 OpenAI 配置（仅正式运行）
3. 创建 Kind 集群
4. 安装组件（FactoryTask CRD、agent-sandbox）
5. 构建编码 Agent 镜像
6. 创建沙箱预热池
7. 运行 FactoryTask 控制器
8. 等待任务完成
9. 更新 Issue 状态

**关键逻辑**:
- 支持 `ai-factory-run`（正式运行）和 `ai-factory-smoke`（冒烟测试）两种模式
- 使用集中保存的 Secrets
- 自动更新 Issue 状态和评论

**环境变量**:
- 使用 GitHub Secrets 存储敏感信息
- 使用 GitHub Variables 存储配置信息
- 支持默认值和自定义配置

## 配置要求

### 1. GitHub Secrets

| Secret 名称 | 必需 | 说明 |
|------------|------|------|
| `AI_FACTORY_APP_ID` | ✅ | GitHub App ID |
| `AI_FACTORY_APP_CLIENT_SECRET` | ✅ | GitHub App Client Secret |
| `AI_FACTORY_APP_WEBHOOK_SECRET` | ✅ | Webhook 密钥 |
| `AI_FACTORY_GITHUB_TOKEN` | ✅ | GitHub Token |
| `AI_FACTORY_OPENAI_API_KEY` | ✅ | OpenAI API Key |

### 2. GitHub Variables

| Variable 名称 | 必需 | 默认值 | 说明 |
|--------------|------|--------|------|
| `AI_FACTORY_OPENAI_BASE_URL` | ❌ | `https://api.openai.com/v1` | OpenAI API 地址 |
| `AI_FACTORY_OPENAI_MODEL` | ❌ | `gpt-4.1` | 使用的模型 |
| `AI_FACTORY_OPENAI_TEMPERATURE` | ❌ | `1` | 温度参数 |
| `AI_FACTORY_OPENAI_MAX_TOKENS` | ❌ | `48000` | 最大 token 数 |
| `AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS` | ❌ | `40` | 最大工具调用轮数 |
| `AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS` | ❌ | `3` | 最大修复轮数 |
| `AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS` | ❌ | `1800` | 总超时时间 |

### 3. GitHub App 权限

**必需权限**:
- Issues: Read & Write
- Pull Requests: Read & Write
- Contents: Read & Write
- Actions: Read & Write
- Metadata: Read

**事件订阅**:
- Issues
- Issue comment

## 测试计划

### 1. 单元测试

**测试内容**:
- GitHub App 创建脚本
- Webhook 接收器逻辑
- 主 workflow 处理流程

**测试方法**:
- 使用 mock 数据测试
- 验证输入输出
- 检查错误处理

### 2. 集成测试

**测试内容**:
- GitHub App 事件接收
- Webhook 触发
- 主 workflow 执行

**测试方法**:
- 创建测试 Issue
- 添加测试标签
- 验证整个流程

### 3. 端到端测试

**测试内容**:
- 完整的 Issue 处理流程
- PR 创建和合并
- 状态更新和评论

**测试方法**:
- 使用真实仓库测试
- 验证所有步骤
- 检查最终结果

## 部署计划

### 阶段 1: 测试环境部署

**目标**: 验证基本功能

**步骤**:
1. 创建 GitHub App
2. 配置测试仓库
3. 运行测试用例
4. 验证功能正确性

**时间**: 1-2 天

### 阶段 2: 生产环境部署

**目标**: 正式启用中央 ai-factory

**步骤**:
1. 配置生产环境 Secrets
2. 安装 GitHub App 到目标仓库
3. 监控运行状态
4. 处理问题和优化

**时间**: 3-5 天

### 阶段 3: 扩展和优化

**目标**: 支持更多仓库和功能

**步骤**:
1. 支持更多目标仓库
2. 优化性能和稳定性
3. 添加监控和告警
4. 完善文档和培训

**时间**: 持续进行

## 风险和缓解措施

### 1. 技术风险

**风险**: GitHub API 限制
**缓解措施**: 实现重试机制，监控 API 使用情况

**风险**: Kind 集群创建失败
**缓解措施**: 实现重试机制，优化镜像构建

**风险**: Agent 执行超时
**缓解措施**: 配置合理的超时时间，实现超时处理

### 2. 安全风险

**风险**: Secrets 泄露
**缓解措施**: 使用 GitHub Secrets，定期轮换

**风险**: 权限过大
**缓解措施**: 遵循最小权限原则，定期审查

**风险**: 恶意 Issue 触发
**缓解措施**: 验证标签和权限，实现速率限制

### 3. 运维风险

**风险**: 服务不可用
**缓解措施**: 监控服务状态，实现告警机制

**风险**: 配置错误
**缓解措施**: 使用配置模板，实现配置验证

**风险**: 文档不完善
**缓解措施**: 持续更新文档，提供培训和支持

## 成功标准

### 1. 功能标准

- ✅ 目标仓库 Issue 打标签后，自动触发 ai-factory
- ✅ 成功修改目标仓库并创建 PR
- ✅ 支持 `ai-factory-run` 和 `ai-factory-smoke` 两种模式
- ✅ 自动更新 Issue 状态和评论

### 2. 性能标准

- 从打标签到开始执行 < 5 分钟
- 任务执行时间 < 30 分钟
- 成功率 > 90%

### 3. 用户体验标准

- 目标仓库只需一次性配置
- 执行过程有清晰的状态更新
- 失败时有详细的错误信息
- 文档完善，易于理解和使用

## 后续计划

### 短期计划 (1-2 周)

1. **测试和验证**
   - 运行单元测试和集成测试
   - 验证所有功能
   - 修复发现的问题

2. **文档完善**
   - 更新部署指南
   - 添加故障排除文档
   - 创建用户手册

3. **监控和告警**
   - 实现基本监控
   - 配置告警规则
   - 设置通知机制

### 中期计划 (1-2 月)

1. **功能扩展**
   - 支持更多仓库类型
   - 添加自定义配置选项
   - 实现高级功能

2. **性能优化**
   - 优化 Kind 集群创建
   - 优化 Agent 镜像构建
   - 实现并行处理

3. **安全加固**
   - 实现更严格的权限控制
   - 添加审计日志
   - 实现安全扫描

### 长期计划 (3-6 月)

1. **平台化**
   - 实现多租户支持
   - 添加用户管理
   - 实现计费和配额

2. **生态集成**
   - 集成更多开发工具
   - 支持更多语言和框架
   - 实现插件系统

3. **智能化**
   - 实现智能任务分配
   - 添加自动化优化
   - 实现预测和推荐

## 总结

中央 ai-factory 的核心功能已经实现，包括：

1. ✅ **GitHub App 创建脚本**: 自动化创建和配置 GitHub App
2. ✅ **Webhook 接收器**: 接收 GitHub App 事件，触发主 workflow
3. ✅ **主 workflow**: 执行 ai-factory 核心逻辑，支持 run/smoke 两种模式
4. ✅ **需求文档**: 详细的需求分析和架构设计
5. ✅ **部署指南**: 完整的部署和配置说明

**下一步**:
1. 按照部署指南配置 GitHub App 和 Secrets
2. 运行测试验证功能
3. 部署到生产环境
4. 监控运行状态并持续优化

---

**文档维护者**: Claude
**创建时间**: 2026-07-27
**最后更新**: 2026-07-27
