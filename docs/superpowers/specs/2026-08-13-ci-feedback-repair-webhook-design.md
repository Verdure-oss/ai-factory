# CI 反馈修复（webhook 事件驱动版）设计

> 状态：**候选方案**。当前无更合适方法替代时采用；后续若讨论出更合适的方法，以新方案为准。
> 日期：2026-08-13

## 背景

coding-agent 生成的 PR 可能无法通过目标仓库的 CI 测试。此前实现过一个"服务端轮询 GitHub PR CI、失败则读注解并进 sandbox 修复"的闭环，但**轮询方案已废弃并 revert**（提交 `4a2cccf`），原因：

- 服务端不应主动、定时地访问 GitHub API。GitHub 的事件应由 GitHub 主动推送（webhook）。
- 轮询期引入额外的 goroutine、间隔配置、settle-window 逻辑，复杂且浪费 API 配额。

用户保留修复逻辑本身（CI 失败 → 携带失败注解 → 进入 sandbox 定向修复），只重新设计"如何获知 CI 失败"的触发器。

## 决策摘要

| # | 决策点 | 选择 |
|---|---|---|
| 1 | 触发方式 | **webhook 事件驱动**（`check_suite` / `check_run` 事件），完全去掉轮询 |
| 2 | 等待期间 sandbox/claim | **同步等待、占住**（同方案 A）：`executeTask` 阻塞在等待上，收到事件立即进 sandbox 修复 |
| 3 | 绿色判定 | **纯事件 + 静默窗口**：GitHub 推事件 → 重置 60s 窗口计时器 → 窗口安静到期才做 1 次 API 评估 |
| 4 | 事件丢失兜底 | **硬超时收场**：等待同时设 `maxWait` 硬 deadline，到期无事件则标记失败、释放 sandbox、issue 评论说明，不碰任何 GitHub API |
| 5 | 修复输入 | **完整 job 日志**（已验证可通过 actions jobs logs API 获取，含 stack trace 与所有错误行）+ annotations + 原任务指令 |
| 6 | 修复上下文 | **只继承主任务会话**：修复轮从主任务 dump 的 messages 开始（加载为初始上下文），轮间不累积；避免 session 因多轮修复无限膨胀 |
| 7 | 修复探索预算 | **独立配置** `CI_WATCH_MAX_TOOL_ROUNDS`：修复轮重跑三阶段的探索上限不绑死主任务的 `OPENAI_MAX_TOOL_ROUNDS`。因 `kubectl exec` 无法附加 env，在 `BuildCIRepairScript` 脚本内 `export` 覆盖，仅影响该修复进程 |

## 保留复用（来自已 revert 的实现）

- `GitHubClient.PullRequestHeadSHA / ListCheckRuns / ListCheckRunAnnotations`
- `evaluateCheckRuns` / `isNonFailingConclusion` / `collectFailedAnnotations` / `formatCIFailures`
- 修复机制：`BuildCIRepairScript`（runAgentScript → commitChangesScript → pushChangeBranchScript 带 `--force`）+ `ciRepairRunnerFor`（`kubectl exec` 进原 sandbox）
- `CIFeedbackFailed` 失败分类；CI_WATCH_* 配置走 ReadConfig 热更新

**新增能力（已验证）**：

- job 日志获取：失败 check-run 的 `details_url` 直接指向 `/actions/runs/{run}/job/{job}`；`GET /repos/{o}/{r}/actions/jobs/{job_id}/logs` → 303 + 10 分钟签名 URL → 全文；ANSI 清洗后错误清晰。实测 PR #929 lint job 拿到 `generator_test.go:256` typecheck 错误全文。注：`GitHubClient` 需新增 `ActionsJobLogs(jobID)` 方法。
- 会话继承：`ai-factory-agent.py` 增加可选 `AI_FACTORY_SESSION_FILE`——启动时存在则加载为初始 `messages[]`，结束时（经 `redact()` 脱敏）dump 回该文件。运行在 `/tmp`（不污染仓库）。

## 修复上下文继承（对照三段式）

CI 修复**不是新机制，而是用新 prompt 重跑一遍完整 `ai-factory-agent`**（`BuildCIRepairScript` 第一段即 `runAgentScript`），内部同样经历 tool-exploration → final-script → repair-loop 三阶段。区别联系：

| 维度 | 三段式内部 repair-loop | CI 修复（顶层重跑） |
|---|---|---|
| 触发 | 生成脚本退出码非 0 | CI check_run 事件 + job 日志回调 |
| 驱动 | 同进程追加一轮模型请求 | 新进程、第二次 `kubectl exec` |
| 工具 | `tool_choice=none` 无工具 | 全新探索阶段，有 Shell 工具 |
| 上下文 | 进程内 messages 历史 | **默认无继承**（新进程从零开始） |
| 修复对象 | bash 脚本 | 仓库真实代码（逻辑/mock/测试） |
| 提交 | 不提交（由 executeTask 提交） | commit + force-push 回原 PR |

**上下文继承设计**（决策 6）：主任务结束时 agent 将完整 messages（含探索中的文件内容、工具输出、推理）dump 到 `/tmp/ai-factory-session.json`；修复轮 agent 启动时 load 该文件作为初始 messages，再追加修复指令——agent 无需重新 `find .`/全仓 `grep` 重建主任务已建立过的仓库认知，直接基于继承上下文对着 CI 日志改代码。修复轮之间**不累积**（每轮都从主任务快照重新开始），避免 token 逐轮膨胀。

**修复探索预算**（决策 7）：修复轮重跑三阶段仍以探索阶段开头（有 Shell 工具、上限独立为 `CI_WATCH_MAX_TOOL_ROUNDS`），随后 final-script 与脚本修复轮沿用主任务预算。探索被静态指令定向收窄（只涉事文件）+ 会话继承，故默认可给较少的轮数。

## 静态修复指令（附带修复）

`buildCIRepairInstructions` 增强：

- 嵌入**完整 job 日志的错误段**（按 `##[error]` / `FAIL` / `Error` 上下文截取 ±N 行，默认 `CI_WATCH_LOG_SNIPPET_LINES=20`；日志为空降级为 annotations）。
- 约束加严：只读 annotation 命名的文件、只修 `file:line` 报错、**禁全仓探索**（禁 `find .`、禁全仓 `grep -rn`、禁读与报错无关的 lint 配置）。
- 允许修改测试代码（PR #929 案例：修复对象是 `generator_test.go` 中缺方法的 mock；只改主代码无法过 CI）。

## 架构与数据流

```
GitHub                              ai-factory-server
  CI 完成                           /webhook/github (X-GitHub-Event 分流)
  │  push check_suite/check_run      │  verifyWebhook(同一 secret)
  └──────────────────────────────────→ 解析 payload(repo, head_branch, head_sha)
                                        │  匹配注册表 (repo, 分支) → waiter
                                        │  无匹配 → 忽略(不是我们建的 PR)
                                        ▼
                                     waiter: 重置静默窗口 timer(60s)
                                        │  窗口安静到期 → 查一次 check-runs
                                        │    green → executeTask 返回, 释放 claim
                                        │    red   → 收集 annotations
                                        │             → repair(kubectl exec sandbox)
                                        │             → force push 新 commit
                                        │             → 更新注册的 head_sha, 继续等
                                        │  契 maxWait → 失败收场, 释放 claim
```

### 匹配键：`(repo, 分支)` 而非 head_sha

- 修复 force-push 后 head_sha 必变、分支名不变；事件 payload 的 `head_branch` 即分支名（fork PR 是 fork 上同名分支）。
- 等待注册表：`map[(owner/repo, branch)] *ciWaiter`，`executeTask` 建完 PR 后注册，结束时注销。

### 静默窗口（替代原 settle-window）

- 每个 waiter 一个 `time.Timer`；收到匹配事件 → `timer.Reset(60s)`。
- 到期才评估 → 惰性注册的 check-run 创建时会推 `check_run created` 事件 → 重置窗口 → 不会在它出现前误判全绿。
- 窗口到期评估前先查 `taskExists`：任务已被取消（label 移除）则退出不修（不新增轮询，借事件时机检查）。

### 事件处理

- `/webhook/github` 按 `X-GitHub-Event` 头分流：`issues` → 原逻辑；`check_suite` / `check_run` → CI 事件处理器。
- webhook handler 不做重活：只解析、查注册表、reset timer，不阻塞。

## 错误处理

| 场景 | 行为 |
|---|---|
| 签名错误 | 401（复用 verifyWebhook） |
| 未知事件类型 | 忽略，快速返回 |
| 事件匹配不到 waiter | 忽略 |
| maxWait 到期无事件 | CIFeedbackFailed，释放 sandbox，issue 评论"未收到 CI 事件" |
| 修复 push 失败 | 即失败收场 |
| 服务器重启 | 内存注册表丢失 → controller 重新 reconcile 该任务，PR 已存在走 AlreadyExists 再注册 |

## 配置变化

保留：`CI_WATCH_ENABLED`、`CI_WATCH_MAX_RETRIES`、`CI_WATCH_MAX_WAIT`、`CI_WATCH_SETTLE_INTERVAL`（用作静默窗口时长）。
删除：`CI_WATCH_RETRY_INTERVAL`（不再轮询）。
新增：`CI_WATCH_LOG_SNIPPET_LINES`（job 日志错误段截取行数，默认 20）、`CI_WATCH_MAX_TOOL_ROUNDS`（修复轮探索上限，默认继承主任务值）。

同步修改：`charts/ai-factory/templates/configmap.yaml`、`charts/ai-factory/values.yaml`、`scripts/upgrade.sh`、`scripts/update-config.sh`、`scripts/ai-factory.env`。

## 附带修复（上一轮遗留 bug）

`buildCIRepairInstructions` 收紧：annotation 的 `file:line` 转为"只读这些文件、只修这些行"的强约束，禁止全仓探索（禁 `find .`、禁全仓 `grep -rn`、禁读 lint 配置文件），修掉"修复 agent 把工具轮次烧在探索上"的问题。

## 实施范围

- `components/agent-sandbox-images/coding-agent/ai-factory-agent.py`：`AI_FACTORY_SESSION_FILE` 会话 dump/load
- `factory/cmd/factory/server/ci.go` 重建（事件驱动版）
- `factory/cmd/factory/server/controller.go`：等待注册表 + executeTask 接线
- `factory/cmd/factory/server/server.go`：webhook 事件分流
- `factory/cmd/factory/server/github.go`：`ActionsJobLogs` 方法
- `factory/pkg/task/plan.go`：恢复 `BuildCIRepairScript`（接会话继承）
- `factory/pkg/task/failure.go`：恢复 `CIFeedbackFailed`
- 部署配置四处 + `scripts/ai-factory.env`

## 后续讨论空间

- 静默窗口时长、`maxWait` 取值是否合理
- 是否保留"窗口到期后的最终一次 API 评估"（当前 yes，否则纯事件无法收敛）
- `AI_FACTORY_SESSION_FILE` 的 token 成本（继承的 messages 全量重放给每轮修复请求）
- GitLab 侧暂不覆盖（GitLab MR CI 事件后续单独设计）