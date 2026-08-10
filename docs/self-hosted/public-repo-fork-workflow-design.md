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

## 前置条件

- **fork 必须已存在并且同名**：系统不会自动创建 fork。用户必须提前在 GitHub 上把自己账号下的同名仓库 fork 出来（例如 `Verdure-oss/matrixhub`），且 fork 名与上游仓库同名（这里的 `matrixhub`）。
- 上游仓库、fork 仓库与服务的 `--fork-owner` 必须位于**同一个 Git 主机**（默认 `github.com`）。

## 自动选择（服务端）

服务端在收到 webhook 时会**自动选择**使用哪种流程，无需在 webhook 配置里显式指定：

- 如果事件仓库的 owner **等于** fork owner（也就是 token 拥有者自己拥有的仓库），走**原有直接流程**：直接克隆上游仓库、推送分支、在目标仓库内创建 PR。
- 否则（事件仓库的 owner 是别人，说明是公共仓库），走 **fork 流程**：根据 `forkOwner` 派生 fork 克隆 URL，注入 `ChangeRequestSpec.ForkOwner` 与 `Source.CloneURL`。

`--fork-owner` 未设置时，服务端会通过 `GITHUB_TOKEN` 调用 `GET /user` **自动探测** token 拥有者的 owner 并缓存（`gitHubLoginCache`）。fork 流程仅在"事件仓库 owner ≠ fork owner"时启用，因此已有仓库的原有流程不受影响。

## 需要修改的代码

### 1. `factory/pkg/task/plan.go` — 执行计划生成

**当前逻辑：**
- 克隆 URL 来自 `task.Spec.Source.CloneURL`（即目标仓库）
- 推分支的 remote 是 `origin`（即目标仓库）

**最终实现：**

fork 模式下（`ChangeRequest.ForkOwner != ""`），克隆 URL 是服务端注入的 fork 克隆地址（`Source.CloneURL`），`origin` 即 fork，推送步骤无需改动。在克隆之后、改代码之前新增 `forkBranchSetupSteps`：添加 `upstream` remote、`git fetch upstream <targetBranch>`，并把变更分支基于 `upstream/<targetBranch>` 创建：

```go
useFork := task.Spec.ChangeRequest.ForkOwner != ""
// ...
if useFork {
    upstreamURL, err := upstreamRepoURL(task)   // 由 Source.Repository 派生，保持上游
    plan.Steps = append(plan.Steps, forkBranchSetupSteps(workDir, changeBranch, targetBranch, upstreamURL)...)
} else {
    plan.Steps = append(plan.Steps, ExecutionStep{
        Name:    "create change branch",
        Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch},
    })
}
```

`forkBranchSetupSteps` 生成三个步骤 `add upstream remote` / `fetch upstream` / `checkout upstream branch`（`git checkout -B <changeBranch> upstream/<targetBranch>`）。

### 2. `factory/pkg/task/change_request.go` — PR 创建与查找

**`buildGitHubPullRequest`**：当 `ChangeRequest.ForkOwner != ""` 时，`head` 参数使用 fork owner：

```go
if task.Spec.ChangeRequest.ForkOwner != "" {
    head = task.Spec.ChangeRequest.ForkOwner + ":" + head  // 例如 "Verdure-oss:fix/xxx"
}
```

**`findExistingChangeRequest`**：查找已有 PR 时，`head` 同样使用 fork owner：

```go
headOwner := owner
if task.Spec.ChangeRequest.ForkOwner != "" {
    headOwner = task.Spec.ChangeRequest.ForkOwner
}
values.Set("head", headOwner+":"+changeBranch)
```

`Source.Repository` 始终保留上游仓库（用于 PR 目标 base 与 Issue 操作）。

### 3. `factory/pkg/task/task.go` — FactoryTask CRD 定义

在 `ChangeRequestSpec` 中新增单一字段 `ForkOwner`（**没有** `forkCloneURL` 字段，fork 克隆 URL 由 `ForkOwner` + 源仓库派生）：

```go
type ChangeRequestSpec struct {
    // ... 现有字段 ...

    // ForkOwner 是 fork 仓库的 owner，用于 PR 的 head 参数与 fork 克隆 URL 派生。
    // 设置后，分支推送到 fork，PR 的 head 使用 ForkOwner:BranchName。
    ForkOwner string `yaml:"forkOwner,omitempty"`
}
```

校验（`Validate`）：
- `forkOwner` 仅支持 github provider，与 `gitlab` 一起使用时报错。
- `forkOwner` 必须是合法的 GitHub owner 名（不允许包含 `/`、`:`、空格、制表符）。

### 4. Webhook 注入与克隆 URL 派生

`factory/pkg/task/webhook.go` 在生成 FactoryTask 时注入 fork 配置：

```go
if opts.ForkOwner != "" {
    task.Spec.ChangeRequest.ForkOwner = opts.ForkOwner
    forkURL, err := task.Spec.Source.ForkCloneURL(opts.ForkOwner)  // 派生 fork 克隆 URL
    task.Spec.Source.CloneURL = forkURL
}
```

`SourceSpec.ForkCloneURL(forkOwner)`（`provider.go`）在同一主机上把 `Repository` 的 owner 替换为 fork owner，派生同名的 fork 克隆 URL（例如 `Verdure-oss/matrixhub` → `https://github.com/Verdure-oss/matrixhub.git`）。

### 5. `factory/cmd/factory/server/server.go` — 服务端自动选择与 flag

新增两个 flag：

```go
Cmd.Flags().StringVar(&opts.ForkOwner, "fork-owner", "",
    "GitHub owner of the fork used for change requests; defaults to the authenticated token owner")
Cmd.Flags().StringArrayVar(&opts.Repositories, "repository", nil,
    "repository allowed to trigger FactoryTasks; can be repeated")
```

`shouldUseFork(eventOwner, forkOwner)` 决定是否走 fork 流程：当事件仓库 owner ≠ fork owner 时返回 true。fork owner 通过 `resolveForkConfig` 解析：`--fork-owner` 未设置时，用 `GITHUB_TOKEN` 调 `GET /user` 自动探测并缓存（`gitHubLoginCache`）。

### 6. `factory/pkg/task/reporting.go` — Issue 评论

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
    baseRef: main
  changeRequest:
    enabled: true
    forkOwner: Verdure-oss                 # fork 的 owner（用于 PR head 参数与 fork 克隆 URL 派生）
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
| `factory/pkg/task/plan.go` | fork 模式下基于 `upstream/<targetBranch>` 创建变更分支（`forkBranchSetupSteps`） |
| `factory/pkg/task/change_request.go` | PR head 参数使用 fork owner、查找已有 PR 时用 fork owner |
| `factory/pkg/task/task.go` | `ChangeRequestSpec` 新增 `ForkOwner` 字段 + github-only 校验 |
| `factory/pkg/task/provider.go` | `SourceSpec.ForkCloneURL` 根据 `ForkOwner` 派生同名的 fork 克隆 URL |
| `factory/pkg/task/webhook.go` | 生成 FactoryTask 时注入 `ForkOwner` 与 fork 克隆 URL |
| `factory/cmd/factory/server/server.go` | 新增 `--fork-owner` / `--repository` flag、`GET /user` 自动探测 + 自动选择 fork/直接流程 |
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
