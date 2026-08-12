# 取消 waiting 任务：移除触发标签即取消

日期：2026-08-12
状态：已批准（用户确认）（已实现，见 plans/2026-08-12-cancel-waiting-task-on-label-removal.md）

## 背景与目标

ai-factory 将 GitHub Issue 的触发标签（`ai-factory-run` / `ai-factory-smoke`）转化为 FactoryTask 并执行。当前一旦触发，任务会一直跑到完成，用户没有"反悔"的出口。

本文档实现：**当 issue 处于 waiting（任务尚未真正执行 agent）时，用户移除触发标签，后台任务即被取消。**

取消的价值：
- **并发排队（waiting ①）**：任务尚未创建 SandboxClaim，删除 FactoryTask 即干净停止，零资源浪费。
- **等 sandbox ready（waiting ②）**：任务已创建 SandboxClaim，取消会释放占用的 warm pool pod，池子自动补建。

## 范围

- **仅 GitHub**。GitLab 无标签操作流程，跳过。
- **仅 waiting 状态**。running（已开始执行 agent）不受影响，任务会跑完并出 PR。
- **尽力而为**：waiting→running 存在竞态窗口，见下文「竞态处理」。

## 触发条件

GitHub issue 的 **`unlabeled`** 事件，且被移除的标签 ∈ `{ai-factory-run, ai-factory-smoke}`。

GitHub 的 `unlabeled` 事件 payload 携带 `label` 字段（被移除的标签对象）。现有 `ParseIssueWebhook` 已解析该字段到 `event.TriggerLabel`：

- `labeled` 事件 → `TriggerLabel` = 刚添加的标签
- `unlabeled` 事件 → `TriggerLabel` = 刚移除的标签
- 其他事件 → 空字符串

（`event.Labels` 返回移除后 issue 剩余的标签列表，不含被移除的 `ai-factory-run`，对 cancel 判断无影响。）

## 数据流

```
用户移除 ai-factory-run
  → GitHub 发 unlabeled webhook
  → issueWebhookHandler: action == "unlabeled" && TriggerLabel 是触发标签
  → handleIssueCancel():
      1. 由 provider-repo-issueNum 推导 FactoryTask 名
      2. getFactoryTaskPhase 查 phase
      3. 非 waiting（running/terminal/不存在）→ 忽略，返回
      4. 删关联 SandboxClaim: kubectl delete sandboxclaim -l factory.ai.gke.io/task=<name> --ignore-not-found
      5. 删 FactoryTask: kubectl delete factorytask <name> --ignore-not-found
      6. GitHubClient.SetTaskCancelled(): 移除 running/waiting 标签 + 评论"已取消：触发标签被移除"
```

## 关键实现

| 改动 | 位置 | 内容 |
|---|---|---|
| unlabeled 分支 | `server.go` `issueWebhookHandler` 开头 | parse 后先判断 action；`unlabeled` 且触发标签被移除 → 走 cancel，不进入 create 流程 |
| `handleIssueCancel` | `server.go`（新函数） | 定位任务 → 判 waiting → 删 claim+task → 收尾 |
| `SetTaskCancelled` | `github.go` | 新增：移除 `ai-factory-running`/`ai-factory-waiting`，发取消评论 |
| 竞态防护（失败路径） | `controller.go` `reportTaskResult` 开头 | FactoryTask 已删除（被取消）→ 静默返回，不打 failed 标签/评论 |
| 竞态防护（成功路径） | `controller.go` `executeTask` 进入 running 前 | `sandbox ready` 后、首个 step exec 前，检查 FactoryTask 仍存在；已被删则中止，不执行、不打标签 |

### 任务定位

name = `dnsName("%s-%s-%d", provider, repository, issueNum)`（与 `FactoryTaskFromIssueWebhook` 同规则，`webhook.go:137`）。

### waiting 判定

- `PhasePending`（含 Queued，无 claim）→ waiting ①，无 SandboxClaim
- `PhaseClaimCreated`（有 claim）→ waiting ②，有 SandboxClaim
- `PhaseRunning` / terminal → 忽略

### SandboxClaim 清理

用 label selector `factory.ai.gke.io/task=<dnsLabel(name)>` 删除（`controller.go:76` 已给 claim 打此标签），无需先读 status。

## 竞态处理

waiting→running 的边界无法用删除 FactoryTask 原子地钉死，因为 `executeTask` goroutine 正阻塞在 `waitForSandboxClaimReady`。取消的"尽力而为"窗口由两道防护收敛到最小：

1. **失败路径**：删除 SandboxClaim 使 `waitForSandboxClaimReady` 返回错误 → `executeTask` 走 `reportTaskResult(PhaseFailed)`。`reportTaskResult` 开头检查 FactoryTask 已被删（取消的唯一信号）→ 静默跳过，不打 failed 标签、不发失败评论。
2. **成功路径**：若 sandbox 恰好在取消瞬间 ready，`waitForSandboxClaimReady` 成功返回 → 进入 running 前（首个 step exec 之前）再检查一次 FactoryTask 仍存在；已被删则直接中止，不执行、不打标签。

两道防护让取消窗口只剩"running 前检查通过后、首个 exec 启动前"的极短瞬间。running 开始后任务正常执行，取消意图不再生效（符合 scope）。

## 边界与错误处理

- 任务不存在 / 非 waiting / 非 GitHub / 非触发标签 → 静默忽略（返回 `{"ignored":true}`）
- waiting ①（无 claim）：label selector 删不到 claim，no-op，删 task 即可
- 删除失败：返回 500，日志记录；删除操作幂等（`--ignore-not-found`）
- GitLab unlabeled 事件 → 忽略

## 测试计划

- `github_test.go`：`TestSetTaskCancelled` —— 评论已发、`ai-factory-running`/`ai-factory-waiting` 被移除
- `server` 层：`handleIssueCancel` 各分支
  - waiting ①（Pending/Queued）：删 task，无 claim
  - waiting ②（ClaimCreated）：删 claim + task
  - running：忽略
  - 任务不存在：忽略
  - 非触发标签被移除：忽略
- `controller_test.go`：`reportTaskResult` 在 task 已删时不更新标签、不发评论
- `executeTask` 进入 running 前检查 task 存在（task 已删 → 中止）