# GitLab Provider 支持设计

- 日期：2026-08-21
- 状态：待评审
- 分支：`feat/gitlab-provider-support`

## 目标

让 ai-factory 完整支持 GitLab，用户在 GitLab issue（work item）上的体验与 GitHub 完全一致：打标签触发、实时状态标签反馈、移除标签取消任务、自动创建 MR、issue 回帖。

本次不实现 CI 失败自动修复（GitLab pipeline 监听），但保留代码结构以便后续扩展。

## 部署拓扑（决定设计的前提）

ai-factory 干活时的绝大多数动作是主动连接代码托管方：clone、push、建 MR、issue 回帖。用户的 GitLab 是自建实例，无公网入口，但可访问外界。

由此推出两个约束：

1. ai-factory 必须部署在能访问 GitLab 的内网，否则连仓库都拉不下来。
2. 内网部署收不到 GitHub 的 webhook（公网无法访问内网）。

因此 GitHub 与 GitLab 分开部署：GitLab 部署在内网，GitHub 部署在公网服务器。一个 server 实例只服务一个提供商，由 `GIT_PROVIDER` 环境变量指定。

## 现状盘点

### 已 provider-neutral，可直接复用

- 执行计划 `BuildExecutionPlan`：clone → checkout → agent 改码 → commit → push。token env 与 git username 已按 provider 切换（GitLab 用 `GITLAB_TOKEN` + `oauth2`）。
- MR 创建 `buildGitLabMergeRequest`：`PRIVATE-TOKEN` header + `/merge_requests` 端点。
- issue 回帖 `buildGitLabCommentRequest`：`/notes` 端点。
- clone URL 推导 `CloneURLOrDefault`：使用 project 的 `git_http_url`，自建 host 亦可推导。

### GitHub 专属，GitLab 需新建

- webhook 触发解析：现有 `parseGitLabIssueWebhook` 残缺，不解析标签变更。
- 标签操作客户端：只有 `GitHubClient`，没有 `GitLabClient`。
- 标签状态机与取消任务：controller 与 server 里以 `if provider != github { return }` 写死。

## 触发解析：把 GitLab 的 update 归一化成 GitHub 的 labeled/unlabeled

GitHub 的 "Issues" 事件在加标签时 `action=labeled`，payload 直接带刚加的 label；移除时 `action=unlabeled`。

GitLab 的 "Work item events" 没有独立的 labeled 动作。加/删标签都归入 `action=update`，标签变更放在 payload 的 `changes.labels.{previous,current}` 两个数组里。

设计做法：在 `parseGitLabIssueWebhook` 里计算差集，归一化到现有 GitHub 模型，下游 `ShouldTriggerIssue` 与 `handleIssueCancel` 无需改动。

- `added = current − previous`，`removed = previous − current`
- `added` 含触发标签（`ai-factory-run` / `ai-factory-smoke`）→ `Action="labeled"`，`TriggerLabel=该标签`
- 否则 `removed` 含触发标签 → `Action="unlabeled"`，`TriggerLabel=该标签`（供取消）
- 否则回落到 `object_attributes.action`（open / reopen）
- `Labels` 填 `current` 全集

非 issue 的 `object_kind`（例如未来的 pipeline 事件）返回 ignored，不再报 400。

## GIT_PROVIDER 开关（必填）

- `runServer` 启动时读 `ReadConfig("GIT_PROVIDER")`。值必须是 `github` 或 `gitlab`，否则报错退出。
- 只挂载对应端点：`github` 挂 `/webhook/github`，`gitlab` 挂 `/webhook/gitlab`。另一端点不存在（404），杜绝误触发。
- token env 与 git username 已由 `source.Provider` 驱动，无需额外处理。

## IssueReporter 接口

把 controller 与 server 里散落的 provider 判断收敛成一次分发：

```go
type IssueReporter interface {
    SetTaskRunning(ctx context.Context, repo string, issueNum int) error
    SetTaskWaiting(ctx context.Context, repo string, issueNum int) error
    SetTaskDone(ctx context.Context, repo string, issueNum int) error
    SetTaskFailed(ctx context.Context, repo string, issueNum int) error
    SetTaskCancelled(ctx context.Context, repo string, issueNum int, removedTriggerLabel string) error
    PostComment(ctx context.Context, repo string, issueNum int, body string) error
    HasToken() bool
}

func newIssueReporter(provider string) IssueReporter
```

`GitHubClient` 已满足这套方法，直接声明实现接口。

## GitLabClient

新建 `factory/cmd/factory/server/gitlab.go`，对标 `GitHubClient`。GitLab API 差异：

- 标签增删：`PUT /projects/:id/issues/:iid?add_labels=x&remove_labels=y`。GitLab 应用标签时自动创建，省去 GitHub 的 EnsureLabel 步骤。
- 评论：`POST /projects/:id/issues/:iid/notes`。
- project id：URL-encode 后的 `path_with_namespace`。
- API base：`GITLAB_API_BASE` 优先，否则从 source host 推导 `https://<host>/api/v4`。
- 认证：`PRIVATE-TOKEN` header。
- 标签名沿用 `ai-factory-running/waiting/done/failed`，状态机语义与 GitHub 一致。

## 取消任务

`handleIssueCancel` 现在显式拒绝非 GitHub。改为 provider 分发调 `IssueReporter.SetTaskCancelled`。归一化后 GitLab 的移除触发标签已是 `Action="unlabeled"`，走同一路径。

## CI 修复：保留结构，本次不接线

controller 里 `task.Spec.Source.Provider == ProviderGitHub` 的 CI watch 门控保持不变。GitLab 任务走到该步自然跳过，建完 MR 即视为成功。后续接 GitLab pipeline 时在此扩展。

## 涉及文件

| 文件 | 改动 |
| --- | --- |
| `factory/pkg/task/webhook.go` | 重写 `parseGitLabIssueWebhook`：差集归一化 |
| `factory/cmd/factory/server/gitlab.go` | 新建 GitLabClient |
| `factory/cmd/factory/server/github.go` | GitHubClient 声明实现 IssueReporter |
| `factory/cmd/factory/server/server.go` | GIT_PROVIDER 校验、端点单挂、cancel 分发 |
| `factory/cmd/factory/server/controller.go` | 标签状态机 provider 分发 |
| `scripts/ai-factory.env.example` | 新增 GIT_PROVIDER、GITLAB_API_BASE |
| `charts/ai-factory/values.yaml` 及模板 | 透传 GIT_PROVIDER |
| `docs/self-hosted/guide.md` | 3.6 节改为实际支持说明 + 内网拓扑 |
| 相关 `_test.go` | 触发解析、GitLabClient、状态机测试 |

## 待实测校准（不阻塞设计）

自建 GitLab 版本未知。各版本 work item 的 `changes.labels` 结构一致（`previous` / `current` 数组），但需用真实 payload 核对 `object_kind` 是否仍为 `issue`。实现按主流稳定版进行，留校准点。

## 非目标

- GitLab pipeline 监听与 CI 失败自动修复。
- 一个 server 实例同时服务两个提供商（拓扑不允许）。
- GitLab fork 工作流（`forkOwner` 仍限 GitHub）。
