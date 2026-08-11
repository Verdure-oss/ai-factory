# 沙箱环境变量注入方式重构：claim 不传 env，前置到 go-dev 模板

> 状态：设计中
> 日期：2026-08-11
> 目标：让 ai-factory-server 真正 `kubectl exec` 进预热池的 go-dev 环境执行任务，而不是每次都现场新建独立 claim pod。

## 背景问题

当前 ai-factory 在创建 SandboxClaim 时，会把运行时配置（`GITHUB_TOKEN`、agent LLM 配置、`AI_FACTORY_GIT_PROXY`）通过 `appendSandboxEnv` 动态注入到 `claim.spec.env`。

**代价**：标准 agent-sandbox 控制器（`kubernetes-sigs/agent-sandbox`）对带自定义 `env` 或 `volumeClaimTemplates` 的 claim 会**跳过 warm pool adoption**，改为用模板现场新建独立 pod（命名为 `github-xxx-claim`）。

**实测现象**：
- 所有任务的 `sandboxName` 都是 `github-xxx-claim`（现场新建），**没有一个**是预热池的 `go-dev-xxx`。
- 预热池的 2 个 go-dev pod 一直闲置空转，从未被任务采用。
- 控制器日志原话：
  ```
  Bypassing warm pool adoption because custom configuration is provided (env or volume claim templates)
  ```

**副作用**：用户的"复用 go-dev 环境"设计无法生效——server 总是 exec 进独立的 `github-xxx-claim` pod，而不是预热池的 go-dev。

## 方案概述

把运行时环境变量从"claim 逐任务动态注入"改为"**go-dev 模板层提前注入**"：

1. go-dev 模板（`SandboxTemplate`）用 `envFrom` 引用 K8s secret / configmap，所有 go-dev pod 创建时自动加载完整 env。
2. claim 不再携带 env（`spec.env` 为空），变回纯粹的"绑定声明"。
3. 这样 agent-sandbox 不再跳过 warm pool adoption，claim 能 adopt 预热池 go-dev，server 从 claim 读回 `go-dev-xxx` 并 exec 进去。
4. 配置热更新改为：更新 secret/configmap 后重建 go-dev 预热 pod，而非依赖运行中的 pod 动态刷新。

## 背景认知（实测确认）

- **SandboxClaim 本身没有镜像**，镜像是 `SandboxTemplate go-dev`（`coding-agent-sandbox:latest`），claim 通过 `warmPoolRef: go-dev` 间接获得环境。
- **命名规则**：claim 走"现场新建"路径时 sandbox 与 claim 同名（`github-xxx-claim`）；走"warm pool adopt"路径时 sandbox 是预热实例原始名（`go-dev-xxx`）。
- **agent-sandbox 的池子模型**：预热池维持恒定的 `replicas` 个 ready pod；claim adopt 一个 go-dev，任务跑完（无论 `shutdownPolicy: Delete` 还是 `Retain`）该 go-dev 实例都会被清理，池子自动补建新实例。**标准版没有"同一实例清空后复用"的机制**——本方案接受"用完即删、池子补全"，不做深度定制。

## 改动点

| 文件 | 改动 |
|---|---|
| `charts/ai-factory/templates/sandbox-warm-pool.yaml` | go-dev 模板 container 加 `envFrom`，引用 `ai-factory-credentials`（secret）+ `ai-factory-config`（configmap） |
| `factory/pkg/task/controller.go` | 删除 `Reconcile` 中 `appendSandboxEnv` 的 3 段注入逻辑（GITHUB_TOKEN / agent env / AI_FACTORY_GIT_PROXY） |
| `scripts/update-config.sh` | 更新 secret/configmap 后，删除 go-dev 预热 pod 触发热更新（让池子用新 env 重建） |

## env 来源

`ai-factory-credentials`（secret）：
- `GITHUB_TOKEN`、`GITLAB_TOKEN`、`OPENAI_API_KEY`、`CODEX_API_KEY`、`WEBHOOK_SECRET`

`ai-factory-config`（configmap）：
- `AI_FACTORY_GIT_PROXY`、`OPENAI_BASE_URL`、`OPENAI_MODEL`、`OPENAI_*` 配置、`OPENAI_VISION_ENABLED`

## 权衡与注意

1. **envFrom 注入 secret 全部 keys**：包括 go-dev 用不到的 `WEBHOOK_SECRET`、`GITLAB_TOKEN`。无害，但若不希望可改为精确指定。
2. **单 token 假设**：env 前置后，所有 go-dev 共用同一个全局 `GITHUB_TOKEN`。当前单 token 场景成立；未来多仓库/多账号需重新设计。
3. **热更新语义变化**：env 是 pod 创建时的快照，改配置后运行中的 go-dev 不更新，需重建预热 pod。`update-config.sh` 已覆盖此语义。
4. **`envVarsInjectionPolicy: Allowed`**：go-dev 模板已允许 env 注入，方案兼容。

## 验证方式

改动后触发一个任务，观察：
- claim 的 `spec.env` 为空。
- claim 绑定的 `sandboxName` 为 `go-dev-xxx`（预热池实例），而非 `github-xxx-claim`。
- server 日志显示 `kubectl exec -n ai-factory go-dev-xxx ...`。