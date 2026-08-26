# Codex + Skill 委托模式 Agent 设计

- 日期：2026-08-26
- 状态：设计评审中（Design Review）
- 相关模块：`components/agent-sandbox-images/coding-agent`、`factory/pkg/task`、`factory/cmd/factory/server`、`charts/ai-factory`

## 1. 背景与动机

当前 ai-factory 的编码 agent（`ai-factory-agent openai-compatible`）是"**单脚本**"模型：模型经三段式（tool-exploration → final-script → repair）产出一个 POSIX shell 脚本，执行一次；agent **不做** git/PR/CI，这些由 Go 控制器（server）在 agent 退出后完成。

我们希望**新增一种可选模式**：在 sandbox 中**预装 Codex CLI** 作为引擎，把"从 issue 到 PR"的整套流程（改码、本地跑 CI、commit、push、开 PR/MR，乃至可选的 CI 修复）写进一份**外部加载、随时可改的 skill**，由 Codex 依照 skill 自主执行。参考项目 `orign_code/looper`（一个用 Codex 驱动完整 GitHub 流程的 Go 协调器）验证了本方案的可行性，尤其是其 `skills/pr-takeover/SKILL.md`——把整套流程写成可移植 markdown、任何 agent 用 `gh`+`git` 照做。

**核心原则**：引擎（怎么起 Codex）与流程（做哪些步骤）彻底分层——配一次引擎，随时改 skill，不必改 Go 代码或重建镜像。

## 2. 目标与非目标

### 目标
- 保留现有 `openai-compatible` 模式，且**仍为默认**，行为不变。
- 新增 `delegated` 委托模式：Codex + skill 自主完成 issue → PR。
- 引擎烤进共享 sandbox 镜像；skill 从外部加载（K8s ConfigMap），镜像内置一份默认兜底。
- 认证用 Codex 自己的 `auth.json`，经 K8s Secret 挂载为文件。
- GitHub 与 GitLab 均支持开 PR/MR。

### 非目标（YAGNI）
- 不引入 looper 的四角色状态机（planner/reviewer/fixer/worker）。
- 不引入本地 daemon / worktree / SQLite / 人接管 / native resume。
- 不改动 ai-factory 的 K8s 立身架构（CRD + controller + warm pool 保留）。
- 不把 "provider 中立(任意 OpenAI 兼容端点)" 强加到 Codex 路径——Codex 走自身账号接入。

## 3. 两种模式与选择方式

新增 FactoryTask 字段 `spec.agent.workflow`，取值：

- `scripted`（默认）：现状不变，openai-compatible 出脚本、控制器做 git/PR/CI。
- `delegated`：Codex + skill 自主跑完整流程，控制器退化为瘦启动器。

选择规则：webhook 在 `AGENT_COMMAND` / `spec.agent.command` 指向 codex（字符串含 `codex`）时自动置 `delegated`；也可在 FactoryTask 上显式设置。默认 `scripted` 保证既有行为零回归。

## 4. 架构与控制流

```
scripted（默认，一字不改）:
  claim → clone → agent(出脚本) → controller: 校验/commit/push/建PR → webhook CI 修复循环

delegated（新增）:
  issue 事件 ─webhook→ 建 FactoryTask → 并发控制 → 建/收养 warm pod
    → clone 到 /workspace/repo → 注入 env/token → 拉起 codex
    ── 交接线 ──
    codex exec(读 skill 自主干全套: 改码→本地跑CI→commit→push→开PR/MR)
    → 打印结果标记
    → controller 读标记 → 更新 CRD status/上报 → pod TTL 清理
```

**交接线**：控制器负责"受理 + 备料（clone、token、拉起 codex）"；从 clone 之后到开出 PR 的动作全归 Codex + skill。

## 5. 分离契约（controller ↔ skill）

这是"分离二者"的落点，是控制器与 skill 之间唯一的接口。

### 5.1 入：控制器注入
- 已就绪环境：`/workspace/repo` 已 clone；`GITHUB_TOKEN`/`GITLAB_TOKEN` 已在 env；Codex 已通过 `auth.json` 认证。
- 任务参数（env）：`AI_FACTORY_REPO`、`AI_FACTORY_BASE_REF`、`AI_FACTORY_BRANCH`、`AI_FACTORY_ISSUE_URL`、`AI_FACTORY_PR_TITLE`、`AI_FACTORY_PR_BODY`、`AI_FACTORY_REMOTE`、`AI_FACTORY_PROVIDER`。
- 任务指令 + 一行 `Read and follow <skill 路径>`：走 stdin 传给 `ai-factory-agent codex`。

### 5.2 出：skill 回传
- 结束时打印**恰好一行**：`__AI_FACTORY_RESULT__={"pr_url":"…","branch":"…","summary":"…"}`。
- 并写文件 `/workspace/repo/.ai-factory/result-url.txt`（冗余兜底）。
- 控制器 grep 该标记（借鉴 looper 的完成契约：在合并输出里搜标记，与传输方式无关），拿 PR URL 更新 CRD 状态。

## 6. 引擎层（烤进镜像，配一次）

### 6.1 镜像 `agent-sandbox-images/coding-agent/Dockerfile`
- 预装 `@openai/codex`（默认安装，不再依赖 `INSTALL_CODEX_CLI=false` 关闭；共享 warm-pool 镜像两模式共用）。
- 预装 `gh`（GitHub CLI）与 `glab`（GitLab CLI）——Codex 靠它们开 PR/MR。
- 设 `CODEX_HOME=/home/agent/.codex`（`HOME=/home/agent`，即默认路径）。

### 6.2 启动器 `ai-factory-agent`（`codex` 分支）
把现有 `exec codex "$@"` 改写为真正的 runner：
- 从 stdin 读入 prompt：`prompt="$(cat)"`。
- `cd /workspace/repo`（工作目录经 cwd 设置，不用 CLI flag——参考 looper）。
- 执行：`codex exec --dangerously-bypass-approvals-and-sandbox [--model "$CODEX_MODEL"] "$prompt"`。
  - `--dangerously-bypass-approvals-and-sandbox`：pod 已隔离，需关闭 Codex 自带沙箱/审批，否则挡住写文件、联网、`gh`（官方明确该 flag 仅在容器/CI 内使用）。
  - prompt 作为**最后一个位置参数**（参考 looper 实测，非 stdin/文件）。
- prompt 内容 = 任务指令 + 契约 env 说明 + 一行 `Read and follow /opt/ai-factory/skills/issue-fix/SKILL.md`。

### 6.3 认证
- K8s Secret 挂载 `auth.json` 到 `/home/agent/.codex/auth.json`（用户 `codex login` 生成）。
- Codex 从 `CODEX_HOME` 读 `auth.json`/`config.toml`；引擎不注入 API key（继承 vendor 认证，参考 looper）。

### 6.4 env 净化
- 保留 allowlist：`PATH,HOME,CODEX_HOME,GITHUB_TOKEN,GITLAB_TOKEN,SSL_*` 等。
- 剥离 git-plumbing 变量（`GIT_DIR`/`GIT_WORK_TREE` 等），避免污染 Codex 的 git 操作。

## 7. Skill 层（外部加载，随时可改）

### 7.1 结构（照抄 looper skill 目录范式）
```
skills/issue-fix/
  SKILL.md                 # frontmatter(name/description) + 主流程 + 安全护栏 + scope 约束
  references/github.md     # gh + GraphQL 配方（开 PR、关联 issue、label；可选 review 处理）
  references/gitlab.md     # glab 对应配方
  scripts/local-ci.sh      # 可选：本地 CI 预跑器
```

### 7.2 投递方式
- 主：**K8s ConfigMap** 挂载到 pod 的 `/opt/ai-factory/skills/issue-fix/`——改 ConfigMap 即生效，pod 无需重建镜像。
- 兜底：镜像内置一份默认 skill（ConfigMap 未提供时使用）。
- Codex 用到它：靠 prompt 里的 `Read and follow <路径>` 显式引用。

### 7.3 SKILL.md 内容要点
1. 主流程：理解 issue → 定位 → 最小完整改码（遵循 repo 自身规则/AGENTS.md）→ 本地预跑 CI → commit（语义前缀）→ push（**绝不 force**）→ `gh`/`glab` 开 PR（`Closes #N`）→ 打印结果标记。
2. 本地 CI 预跑：优先跑控制器注入的 `spec.work.commands`；补充通用检查（`go build/test`、`gofmt`、`npm test`、lint）；红了就地修，带迭代上限。
3. scope 约束：把每条要求分类 IN/OUT/UNCERTAIN；最小改动；拒绝越界=成功（借鉴 looper `fixerRepairScopeInstruction`）。
4. 安全护栏：绝不 force-push、绝不带病合并、**绝不打印/提交 secret**、连续 N 轮无进展即停并报告。
5. provider 差异：GitHub 走 `gh`+GraphQL；GitLab 走 `glab`（放 references/）。

## 8. 控制器改动（delegated 瘦启动器）

`factory/pkg/task/plan.go` 与 `factory/cmd/factory/server/controller.go` 增加 `delegated` 分支：

- **跳过**：`spec.work.commands` 校验步骤、git 凭据/proxy 配置、`commitChangesScript`、`pushChangeBranchScript`、`createTaskChangeRequest`、`watchAndRepairCI`（webhook CI 循环）。
- **保留**：建/收养 pod、clone、注入契约 env/token、拉起 codex、读 `__AI_FACTORY_RESULT__`、更新 CRD status/上报、pod TTL 清理、`--task-timeout` 兜底、并发控制。
- **注入**：把 FactoryTask 上原本用于 controller git 动作的参数（分支名、目标分支、PR 标题/正文、remote、provider）改为**上下文 env**注入给 skill。
- `delegated` 模式下不再依赖 CI webhook 事件（issue webhook 入口保留）。

## 9. 职责移交对照（设计动机）

| 原 scripted 由谁做 | delegated 下由谁做 |
|---|---|
| 校验命令（server 步骤） | Codex 本地预跑 CI |
| commit / push（`commitChangesScript`/`pushChangeBranchScript`） | Codex 自己做 |
| 建 PR/MR（`createTaskChangeRequest`） | Codex 用 `gh`/`glab` |
| CI 修复循环（`watchAndRepairCI`，仅 GitHub） | skill 本地预跑（+ phase2 远程轮询） |
| 分支/commit msg/目标分支策略（plan.go 硬编码） | skill 里的规矩，server 仅注入参数 |

## 10. 文件改动清单

| 文件/目录 | 改动 |
|---|---|
| `agent-sandbox-images/coding-agent/Dockerfile` | 预装 codex/gh/glab；`CODEX_HOME` |
| `agent-sandbox-images/coding-agent/ai-factory-agent` | `codex` 分支改为 `codex exec` runner |
| `components/factory-task/crd.yaml` | 新增 `spec.agent.workflow` CRD schema |
| `factory/pkg/task/task.go` | 新增 `workflow` 字段到 agent 结构体 |
| `factory/pkg/task/webhook.go` | 自动判定 `delegated` |
| `factory/pkg/task/plan.go` | `delegated` 分支：跳过 git/PR 步骤、注入契约 env |
| `factory/cmd/factory/server/controller.go` | `delegated` 分支：瘦启动器 + 结果标记解析 |
| `charts/ai-factory/templates/sandbox-warm-pool.yaml` | codex-auth Secret 挂载、skill ConfigMap 挂载 |
| `charts/ai-factory/values.yaml` | codex 相关开关/镜像/Secret/ConfigMap 配置 |
| `skills/issue-fix/`（新增） | SKILL.md + references/ + scripts/ |
| 文档/测试 | README/AGENTS.md；启动器 dispatch、结果解析、契约注入单测 |

## 11. v1 范围 / 已知限制

- **v1 做**：codex 预装 + auth.json 挂载 + `delegated` 瘦控制器 + skill（理解 issue→修→本地预跑 CI→push→开 PR）+ 结果回传；GitHub/GitLab 均可开 PR/MR。
- **已知限制（诚实标注）**：
  1. 本地预跑 CI 是高度近似——依赖密钥/外部服务/matrix/联网的步骤本地跑不了，远程仍可能挂。**远程 CI 兜底轮询留作 phase 2**（skill 可扩展，不改架构）。
  2. delegated 下 openai-compatible 的脚本安全校验、secret 脱敏、精细预算、session 快照均不适用——**secret 脱敏须在 skill/引擎层重新保证**（见 §12）。
  3. Codex 需联网到其后端，受 `--task-timeout`（默认 30m）约束。

## 12. 安全考量

- **secret 不外泄**：Codex 输出/日志可能含 token。skill 明令"绝不打印 secret"；引擎层在把 Codex 输出回传/落日志前做一层脱敏（复用现有 `redact` 思路，覆盖 `GITHUB_TOKEN`/`GITLAB_TOKEN`/`OPENAI_API_KEY`/`CODEX_API_KEY`）。
- `auth.json` 经 Secret 挂载、只读；限制可读该 Secret 的 RBAC。
- `--dangerously-bypass-approvals-and-sandbox` 仅在隔离 pod 内使用，不外泄到其他执行路径。

## 13. 测试策略

- 离线单测：启动器 `codex` 分支命令拼装、控制器结果标记解析、契约 env 注入、CRD 字段校验（沿用 `check-runtime` + Go 表驱动测试）。
- smoke/e2e：提供 `auth.json`，在低风险仓库跑通一条 issue → PR。

## 14. 分阶段落地

1. 引擎：镜像预装 + 启动器 runner + auth.json 挂载 → 能在 pod 内手动 `codex exec` 修一个 issue。
2. 控制器 `delegated` 分支 + 契约注入 + 结果解析 → 端到端自动 issue → PR。
3. skill 成型（先内置默认，再切 ConfigMap 外部加载）。
4. phase 2（可选）：远程 CI 兜底轮询 + review 处理（pr-takeover 范式）。
