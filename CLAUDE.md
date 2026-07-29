# CLAUDE.md - AI 快速上手指南

本文件供 Claude Code 及其他 AI coding 工具自动加载，帮助快速理解项目。

## 项目简介

ai-factory 是一个 Kubernetes 原生的自动化系统，将 GitHub/GitLab Issue 转化为编码 agent 执行：

```
Issue → FactoryTask → sandbox → agent → validation → branch/commit → PR/MR
```

## 技术栈

- **语言**: Go (主要), Python (agent runner), Shell (脚本)
- **运行时**: Kubernetes (kind/GKE)
- **CI/CD**: GitHub Actions, GitLab CI
- **Agent 框架**: OpenAI-compatible API

## 核心目录结构

```
├── factory/              # 核心 Go 代码
│   ├── cmd/factory/      # CLI 入口
│   └── pkg/task/         # 任务模型、webhook、agent-plan 逻辑
├── components/           # Kubernetes 组件安装
│   ├── agent-sandbox/    # Sandbox 控制器
│   └── factory-task/     # FactoryTask CRD
├── .agents/              # Agent 定义（运行时使用）
│   ├── top-level/        # 顶层编排 agent
│   ├── speccer/          # 规范编写
│   ├── planner/          # 计划制定
│   ├── builder/          # 构建协调
│   ├── reviewer/         # PR 审查
│   └── cleanup/          # 代码清理
├── specs/                # 规范文档
├── plans/                # 执行计划
├── examples/             # 示例配置
└── docs/                 # 文档
```

## 理解项目的切入点

想快速理解项目如何运作，按以下顺序阅读关键文件：

1. **`.github/workflows/issue-factorytask.yaml`** ⭐ 最重要
   - 项目的主入口，描述了从 Issue 到 PR 的完整流程
   - 看这个文件就能理解：创建 kind 集群 → 安装 CRD → 构建镜像 → 启动 webhook → 运行控制器 → 等待结果
   - 包含所有环境变量配置（OpenAI API、agent 命令、验证命令等）

2. **`.github/workflows/process-issue.yaml`**
   - 供其他仓库调用 ai-factory

3. **`factory/pkg/task/controller.go`**
   - FactoryTask 控制器核心逻辑，负责 watch 和执行任务

4. **`factory/cmd/factory/task.go`**
   - CLI 入口，所有 `go run ./factory/cmd/factory task ...` 命令的实现

5. **`components/factory-task/install`** 和 **`components/agent-sandbox/install`**
   - Kubernetes 组件的安装脚本，了解部署细节

## 常用命令

```bash
# 运行测试
go test ./...

# 验证 FactoryTask
go run ./factory/cmd/factory task validate examples/factory-task-github.yaml

# 查看执行计划
go run ./factory/cmd/factory task plan examples/factory-task-github.yaml

# 运行控制器（本地开发）
go run ./factory/cmd/factory task controller watch --namespace default
```

## Agent Runner 配置

```bash
# 环境变量
OPENAI_API_KEY=...
OPENAI_BASE_URL=https://api.example.com/v1
OPENAI_MODEL=provider-model

# 或使用自定义命令
AGENT_COMMAND="ai-factory-agent codex"
```

## 开发流程

### 简单任务（bug 修复、文档更新）
直接修改代码 → 提交 PR

### 复杂任务（新功能、架构变更）
遵循规范驱动开发：
1. `speccer` 生成规范 → 审查
2. `planner` 生成计划 → 审查
3. `builder` 执行构建 → 审查
4. 关闭关联的 GitHub Issue

## 关键设计原则

1. **提供商中立**: 支持任何 OpenAI-compatible API
2. **Kubernetes 原生**: 组件可在任何 K8s 集群运行
3. **幂等性**: 任务可重复执行，结果一致
4. **自组装**: Agent 系统可自主演化

## 相关文档

- `AGENTS.md`: Agent 工作指令和项目状态（AI 应首先阅读）
- `SOUL.md`: 项目核心原则和目标
- `docs/guide.md`: 完整的设置和使用指南
- `specs/`: 详细的规范文档
- `plans/`: 执行计划文档

## 注意事项

- 不要在代码中提交 API key 或敏感信息
- 生成的 Python 代码不要使用 `py_compile`（会产生字节码文件）
- 修改架构决策后同步更新 `AGENTS.md`
