# CI 反馈修复闭环 设计文档

> 版本: 2026-08-12

## 问题

自托管部署的 `--validation-command` 为空（`charts/ai-factory/values.yaml` 默认 `validationCommands: []`），coding-agent 提的 PR **零验证直接推上去**，CI 成了唯一强制关卡。体现为：PR #929 的 agent 改接口时漏掉了手写 mock（`generator_test.go:329` 的 `mockRegistryRepo` 缺 `GetRegistryByName`），`go build` 不编译 `_test.go` 所以 sandbox 内没发现，CI 的 `go test ./...` 编译测试文件时抓到了。

约束：**不能往目标仓库添加任何文件**，只能用仓库已有的 `.github/workflows/*.yml`。因此放弃"解析 workflow 提取命令前置跑"（方案 1，Actions YAML 的 `uses:`/matrix/services 无法在 sandbox 复现，易误报），采用**以真实 CI 结果为准**的反馈闭环（方案 2）。

## 方案

```
agent 在 sandbox 跑完 → 推 PR → 拿 PR URL
→ server 进入「CI 观察」循环（方案 A：观察期间占并发槽位）：
   - 轮询 GET /repos/{owner}/{repo}/commits/{sha}/check-runs
   - 全部 success/neutral/skipped → 绿 → 任务 Succeeded
   - 任一 failure/cancelled/timed_out → 红 → 进修复
   - 有 queued/in_progress → 继续等（受 CI_WATCH_MAX_WAIT 预算约束）
→ 修复（复用原 sandbox pod，kubectl exec）：
   - 读失败 job 的 check-runs/{id}/annotations，拿到「文件:行 + 错误」
   - 构建修复指令（原始任务 + PR URL + 失败注解）
   - 用修复指令重新跑 coding-agent（带工具，复用 tool-exploration loop）
   - agent 修完 → server 重新 commit + push --force 更新同一 PR
-> 用新 SHA 回到观察循环
→ 超过 CI_WATCH_MAX_RETRIES 仍红 → 任务 Failed（CIFeedbackFailed）
```

## 关键决策

| 决策点 | 结论 | 理由 |
|---|---|---|
| 前置验证 vs CI 反馈 | CI 反馈闭环 | 不能加仓库文件；真实 CI 结果为准，零解析、全覆盖、语言无关 |
| 修复用工具吗 | **用** | 修的是仓库代码逻辑，必须读代码再改；无工具盲改靠猜。现有 repair 是脚本式是因为它修的是"生成的 bash 脚本"（代码改动 explore 阶段已做） |
| 修复是重跑整个任务吗 | 否，**定向修复** | 不做 explore→final→repair 全流程，prompt 限定"只修 CI 错误，别推翻已有实现" |
| 复用哪个 sandbox | **原 sandbox pod** | 改动、依赖、checkout 都在里面；`defer cleanupSandboxClaim` 在 executeTask 返回时才清理，观察期间 claim 存活 |
| 观察期并发槽位 | 方案 A：观察占槽位 | 一期从简；二期（方案 B）观察期释放槽位 |
| agent 如何提交/推送 | server 重新跑 commit + push（agent 始终不 commit/push） | 沿用现有约定 |

## 权限

- 查 check-runs / annotations：公开仓库**无需 token**（实测 PR #929 无 token 可查）
- `public_repo` scope 对公开仓库可读写：推修复分支、更新 PR 够用
- 私有仓库需 `repo` scope（本期不支持，立项时确认目标仓库公开性）

## 配置项（走 ReadConfig + configmap 通道，同 MAX_CONCURRENT_TASKS）

| 环境变量 | 默认 | 含义 |
|---|---|---|
| `CI_WATCH_ENABLED` | true | 开关（仅当创建了 PR 时生效） |
| `CI_WATCH_MAX_RETRIES` | 3 | 最多几轮"红→修复→重推"循环 |
| `CI_WATCH_MAX_WAIT` | 30m | 每轮观察的最长等待预算（含 CI pending 时间） |
| `CI_WATCH_RETRY_INTERVAL` | 60s | 轮询 check-runs 的间隔 |

## 状态与汇报

- 观察期间任务保持 `Running`（标签 ai-factory-running 不变）
- 触发修复时：任务 reason 更新为 `CIRepairing`，消息含 CI 失败摘要
- 绿：正常走 Succeeded 路径（消息含 PR URL）
- 超预算仍红：`Failed` + 新 failure reason `CIFeedbackFailed`，消息含最后一批失败注解

## 不做（未来）

- 方案 B：观察期释放并发槽位
- 重启恢复：把 watch 状态写进 task status，server 重启后恢复观察而非重跑 plan
- 完整检查日志下载（annotations 足够时不需要）