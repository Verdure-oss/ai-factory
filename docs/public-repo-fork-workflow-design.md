# 公共仓库 Fork 工作流设计文档

## 背景

当前 ai-factory 的设计假设用户拥有目标仓库的写权限，可以直接推分支并创建 PR。但在开源协作场景下，目标仓库（如 `matrixhub-ai/matrixhub`）是公共仓库，用户没有写权限，只能通过 fork + PR 的方式贡献代码。

本文档记录需要修改的内容，以支持"推送到自己的 fork，向上游公共仓库提 PR"的工作流。

## 示例场景

| 仓库 | 角色 |
|---|---|
| `matrixhub-ai/matrixhub` | 上游公共仓库（目标仓库） |
| `Verdure-oss/matrixhub` | 用户的 fork |
| `Verdure-oss` 的 Classic PAT | 唯一需要的 token（`public_repo` scope） |

## 当前流程 vs 目标流程

### 当前流程（需要写权限）

```
1. 接收 matrixhub-ai/matrixhub 的 Issue 事件（webhook）
2. 克隆 matrixhub-ai/matrixhub
3. 改代码、创建分支
4. 推分支到 matrixhub-ai/matrixhub        ← ❌ 需要写权限
5. 创建 PR: matrixhub-ai/matrixhub:fix/xxx → matrixhub-ai/matrixhub:main
```

### 目标流程（Fork 模式）

```
1. 接收 matrixhub-ai/matrixhub 的 Issue 事件（webhook）
2. 克隆 Verdure-oss/matrixhub（用户的 fork）
3. 添加 upstream remote（matrixhub-ai/matrixhub）
4. git fetch upstream && git merge upstream/main  ← 同步上游最新代码
5. 改代码、创建分支
6. 推分支到 Verdure-oss/matrixhub         ← ✅ 用户有权限
7. 创建 PR: Verdure-oss:fix/xxx → matrixhub-ai/matrixhub:main  ← ✅ 公共仓库允许
8. 给 matrixhub-ai/matrixhub 写 Issue / 发评论（可选）
```

## Token 配置

### 推荐：Classic PAT + `public_repo` scope

| 操作 | 所需权限 | Classic PAT (`public_repo`) |
|---|---|---|
| Fork 仓库 | `public_repo` | ✅ |
| 推分支到自己的 fork | `public_repo` | ✅ |
| 向公共仓库创建 PR | `public_repo` | ✅ |
| 给公共仓库写 Issue / 发评论 | `public_repo` | ✅ |

**一个 token 搞定所有操作，不需要额外的 token。**

### 不推荐：Fine-grained PAT

Fine-grained PAT 只能选择用户自己拥有或管理的仓库，无法选择别人的公共仓库（如 `matrixhub-ai/matrixhub`）。因此创建 PR 和写 Issue 的 API 调用会因权限不足而失败。

## 需要修改的代码

### 1. `factory/pkg/task/plan.go` — 执行计划生成

**当前逻辑：**
- 克隆 URL 来自 `task.Spec.Source.CloneURL`（即目标仓库）
- 推分支的 remote 是 `origin`（即目标仓库）

**需要修改：**

```go
// 当前：直接克隆目标仓库
cloneURL = task.Spec.Source.CloneURLOrDefault()  // matrixhub-ai/matrixhub

// 目标：克隆用户的 fork
// 需要新增配置项，如 spec.source.forkCloneURL 或 spec.changeRequest.pushToFork
cloneURL = task.Spec.Source.ForkCloneURL  // Verdure-oss/matrixhub
```

**新增步骤：在克隆之后、改代码之前，添加上游同步步骤：**

```go
// 新增：添加 upstream remote 并同步
{
    Name: "add upstream remote",
    Command: []string{"git", "-C", workDir, "remote", "add", "upstream", upstreamURL},
},
{
    Name: "sync with upstream",
    Command: []string{"/bin/sh", "-lc", fmt.Sprintf("cd %s && git fetch upstream && git merge upstream/%s --no-edit", workDir, targetBranch)},
},
```

### 2. `factory/pkg/task/change_request.go` — PR 创建

**当前逻辑（`buildGitHubPullRequest`）：**
```go
// head 参数使用 task.Spec.Source.Repository（目标仓库）
"head": head,  // 例如 "matrixhub-ai:fix/xxx"
```

**需要修改：**
```go
// head 参数需要使用 fork 仓库
"head": forkOwner + ":" + changeBranch,  // 例如 "Verdure-oss:fix/xxx"
```

**当前逻辑（`findExistingChangeRequest`）：**
```go
// 查找已有 PR 时，head 用的是目标仓库的 owner
values.Set("head", owner+":"+changeBranch)
```

**需要修改：**
```go
// 查找已有 PR 时，head 需要用 fork 的 owner
values.Set("head", forkOwner+":"+changeBranch)
```

### 3. `factory/pkg/task/task.go` — FactoryTask CRD 定义

需要在 `SourceSpec` 或 `ChangeRequestSpec` 中新增配置字段：

```go
type SourceSpec struct {
    // ... 现有字段 ...

    // ForkCloneURL 是用于克隆的 fork 仓库地址（可选）。
    // 设置后，系统会克隆此 URL 而非 Repository，并自动添加 upstream remote 同步。
    ForkCloneURL string `yaml:"forkCloneURL,omitempty"`
}

// 或者在 ChangeRequestSpec 中：
type ChangeRequestSpec struct {
    // ... 现有字段 ...

    // ForkOwner 是 fork 仓库的 owner，用于 PR 的 head 参数。
    // 设置后，分支推送到 fork，PR 的 head 使用 ForkOwner:BranchName。
    ForkOwner string `yaml:"forkOwner,omitempty"`
}
```

### 4. `factory/pkg/task/reporting.go` — Issue 评论

当前评论功能已经支持向任意仓库的 Issue 发评论（通过解析 Issue URL），无需修改。只需确保 token 对目标仓库有 `public_repo` 权限即可。

## 配置示例（改造后的 FactoryTask）

```yaml
apiVersion: ai-factory/v1
kind: FactoryTask
metadata:
  name: fix-issue-123
spec:
  source:
    provider: github
    repository: matrixhub-ai/matrixhub     # 上游仓库（用于 PR 目标和 Issue 操作）
    forkCloneURL: https://github.com/Verdure-oss/matrixhub.git  # fork 仓库（用于克隆和推送）
    baseRef: main
    cloneURL: https://github.com/Verdure-oss/matrixhub.git  # 实际克隆地址
  changeRequest:
    enabled: true
    forkOwner: Verdure-oss                 # fork 的 owner（用于 PR head 参数）
    branchPrefix: factory-task
    targetBranch: main
  agent:
    name: builder
    command: ai-factory-agent openai-compatible
  work:
    instructions: |
      Fix the issue described in the GitHub issue.
```

## 涉及的文件清单

| 文件 | 修改内容 |
|---|---|
| `factory/pkg/task/plan.go` | 新增 fork 克隆 URL 支持、upstream 同步步骤 |
| `factory/pkg/task/change_request.go` | PR head 参数使用 fork owner、查找已有 PR 时用 fork owner |
| `factory/pkg/task/task.go` | FactoryTask CRD 新增 fork 相关字段 |
| `factory/pkg/task/webhook.go` | 可能需要在生成 FactoryTask 时填入 fork 信息 |
| `factory/cmd/factory/server/server.go` | webhook handler 中传递 fork 配置 |
| `factory/cmd/factory/server/controller.go` | 无需修改（执行计划由 plan.go 生成） |

## Webhook 配置

目标仓库的 webhook 配置不变：

| 配置项 | 值 |
|---|---|
| Payload URL | `https://your-server/webhook/github` |
| Content type | `application/json` |
| Secret | 与 ai-factory 服务端的 `WEBHOOK_SECRET` 一致 |
| Events | 只勾选 **Issues** |

## 安全注意事项

- Classic PAT 的 `public_repo` scope 权限较宽，能操作用户所有公共仓库。建议配合 `--repository` 参数限制 ai-factory 只处理指定仓库的事件。
- Token 不要提交到代码仓库，通过 Kubernetes Secret 或环境变量注入。
