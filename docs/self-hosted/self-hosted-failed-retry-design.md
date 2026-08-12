# 自托管服务问题与设计文档

> 状态：问题一、二、三、六已解决，其他问题设计中
> 日期：2026-07-30
> 最后更新：2026-08-03

## 问题一实现状态 ✅ 已完成

**实现日期**：2026-08-03

**实现内容**：
1. ✅ Webhook 层终态检测和删除逻辑（`server/server.go`）
2. ✅ 失败时移除触发标签（`server/github.go` - `SetTaskFailed()`）
3. ✅ 辅助函数 `getFactoryTaskPhase()` 和 `isTerminalPhase()`
4. ✅ 修复 `kubectl wait` 与 CRD 兼容性问题，改用轮询机制（`server/controller.go`）

**已知问题修复**：
- 发现 `kubectl wait --for=condition=Ready` 对 SandboxClaim CRD 不工作
- 解决方案：实现 `waitForSandboxClaimReady()` 轮询函数替代 `kubectl wait`

**验证结果**：
- ✅ 失败的 FactoryTask 可以被正确删除和重建
- ✅ 重试流程正常工作：删除标签 → 重新添加标签 → 任务重新执行
- ✅ SandboxClaim 等待逻辑正常工作

---

## 问题二实现状态 ✅ 已完成

**实现日期**：2026-08-03

**实现内容**：
1. ✅ 新增 `labelWaiting` 常量和 `SetTaskWaiting()` 方法（`server/github.go`）
2. ✅ `SetTaskRunning()` 中增加清理 `labelWaiting`（避免 waiting→running 切换时残留）
3. ✅ `SetTaskDone()` / `SetTaskFailed()` 中增加清理 `labelWaiting`
4. ✅ 新增 `updateIntermediateLabel()` 辅助函数（`server/controller.go`）
5. ✅ `executeTask()` 中 `ClaimCreated` 阶段调用 `SetTaskWaiting()`
6. ✅ `executeTask()` 中 `SandboxReady` 阶段调用 `SetTaskRunning()`
7. ✅ 单元测试覆盖（`server/github_test.go`）

**标签流转**：
```
Webhook 创建 → ai-factory-running
控制器 ClaimCreated → ai-factory-waiting（等待 warm pool pod）
SandboxClaim Ready → ai-factory-running（开始执行）
完成 → ai-factory-done / ai-factory-failed
```

**验证结果**：
- ✅ 标签切换正常工作，GitHub issue 时间线可见 waiting→running 转换
- ✅ 所有标签方法互相清理，不存在残留
- ✅ 单元测试 `TestFullLabelTransitionFlow` 验证完整生命周期

---

## 问题三实现状态 ✅ 已完成

**实现日期**：2026-08-03

**实现内容**：
1. ✅ 新增 `isAlreadyExistsError()` 辅助函数（`server/server.go`）
2. ✅ Webhook handler 中 `kubectl apply` 错误处理：捕获 AlreadyExists 视为成功
3. ✅ 并发请求返回 200 + JSON（`concurrent: true`），不重复设置标签
4. ✅ 单元测试覆盖（`server/github_test.go`）

**行为**：
```
并发 webhook 到达：
  请求 A → kubectl apply 成功 → SetTaskRunning → 返回 200
  请求 B → kubectl apply AlreadyExists → 日志记录 → 返回 200 (concurrent: true)
```

GitHub 不再收到 500 错误，不会触发无效重试。

### 附加修复：Webhook 连锁触发导致评论重复 ✅

**问题发现日期**：2026-08-03

**问题描述**：
在实际运行中发现 issue 评论被发送两次，标签被移除两次。

**根因分析**：
当 `SetTaskDone()` 执行时，操作顺序为：
1. `AddLabel("ai-factory-done")` → GitHub 发送 `issues.labeled` webhook
2. `RemoveLabel("ai-factory-running")` → ...
3. `RemoveLabel("ai-factory-run")` → ...

在第 1 步时，`issues.labeled` webhook 被触发。此时 issue 上还残留着 `ai-factory-run` 标签（第 3 步还没执行）。旧代码的 `ShouldTriggerIssue` 检查 `event.Labels`（包含 issue 上所有标签），发现 `ai-factory-run` 存在，于是认为这是一个有效的触发请求。

Webhook handler 发现 FactoryTask 处于终态（Succeeded），删除旧任务并创建新任务，导致第二次执行。

**解决方案**：
给 `IssueWebhookEvent` 新增 `TriggerLabel` 字段，记录触发 webhook 的那个具体标签（来自 GitHub 的 `label.name` 字段）。`RequiredLabels` 检查改为只检查 `TriggerLabel`，而非所有标签。

**改动**：
1. ✅ `IssueWebhookEvent` 新增 `TriggerLabel` 字段（`pkg/task/webhook.go`）
2. ✅ GitHub 解析器从 `raw.Label.Name` 填充 `TriggerLabel`
3. ✅ `ShouldTriggerIssue` 中 `RequiredLabels` 检查改为优先使用 `TriggerLabel`
4. ✅ 单元测试覆盖（`pkg/task/webhook_test.go`）

**修复后行为**：
```
SetTaskDone() 添加 ai-factory-done → issues.labeled (TriggerLabel=ai-factory-done)
  → TriggerLabel "ai-factory-done" 不在 RequiredLabels ["ai-factory-run", "ai-factory-smoke"] 中
  → Webhook 被忽略 ✅

用户添加 ai-factory-run → issues.labeled (TriggerLabel=ai-factory-run)
  → TriggerLabel "ai-factory-run" 在 RequiredLabels 中
  → Webhook 被处理 ✅
```

---

## 待办列表

- [x] 实现问题一：Failed Issue 重试机制（高优先级）✅ 已完成
- [x] 实现问题二：并发任务状态可见性 — `ai-factory-waiting` 标签（高优先级）✅ 已完成
- [x] 实现问题三：并发 Webhook 竞态处理（高优先级）✅ 已完成
- [ ] 讨论问题四：服务器重启后任务恢复（低优先级）
- [ ] 讨论问题五：Issue 关闭时任务处理（低优先级）
- [x] 编写执行文档：打包与部署流程 ✅ 已完成
  - [x] 本地打包流程（`scripts/package.sh` 使用说明）✅ 已完成
  - [x] 远程部署流程（`dist/deploy-remote.sh` 使用说明）✅ 已完成
  - [x] 环境要求（K8s 集群、kubectl、helm 版本等）✅ 已完成
  - [x] 凭证配置说明（GitHub Token、Webhook Secret、OpenAI API Key）✅ 已完成
  - [x] 验证部署是否成功（健康检查、warm pool 状态、webhook 测试）✅ 已完成
  - [x] GitHub 仓库 Webhook 配置指引 ✅ 已完成
  - [x] 常见问题排查 ✅ 已完成
- [x] 实现配置热更新：Secret 文件挂载 + 补齐 model/url/base_url 交互式配置 ✅ 已完成

## 问题描述

### 当前行为

自托管服务中，当 FactoryTask 执行失败后：

1. Issue 标签状态：`ai-factory` + `ai-factory-run` + `ai-factory-failed`
2. FactoryTask CRD 状态：`status.phase = Failed`
3. 控制器 `shouldReconcile()` 跳过 `PhaseFailed` 状态的任务
4. 用户重新打标签无法触发重试

### 根本原因

- FactoryTask 名称是确定性的：`github-{repo}-{issue}`，同一 issue 永远生成同一个名字
- `kubectl apply` 是 merge 操作，不会重置 `status` 字段
- 控制器只处理 `""` / `Pending` / `ClaimCreated` / `SandboxReady` / `Running` 状态
- 没有任何机制将 `Failed` 状态重置为可执行状态

### 与 GitHub Actions 工作流的差异

GitHub Actions 工作流每次执行都在全新的临时 kind 集群中，不存在状态残留问题。自托管服务使用持久化 CRD，状态会永久保留。

## 用户期望的重试方式

从用户操作直觉出发，最自然的重试方式是：

```
删除所有 ai-factory 相关标签 → 重新添加 ai-factory + ai-factory-run 标签
```

这符合"打标签 = 触发"的心智模型，且与成功后的操作一致（成功时系统会移除 `ai-factory-run`，用户再加回来就应该能重跑）。

## 解决方案

### Webhook 层自动删除重建

当 Webhook 收到请求时，如果发现已存在的 FactoryTask 处于终态（Failed/Succeeded），先删除旧实例再创建新实例。

#### 流程

```
用户删除标签 → (无事发生)
用户重新添加 ai-factory + ai-factory-run 标签
    ↓
Webhook 触发 → 解析 issue event
    ↓
生成 FactoryTask 名称 → 检查是否已存在
    ├─ 不存在 → 正常创建
    └─ 存在 → 检查 status.phase
         ├─ Pending/Running/ClaimCreated/SandboxReady → 忽略（正在执行中）
         └─ Failed/Succeeded → kubectl delete → kubectl apply（全新实例）
```

#### 关键设计点

1. **只在终态时删除重建**：Pending/Running 等中间状态的任务不应被删除，避免打断正在执行的任务

2. **删除重建而非状态重置**：
   - 删除重建产生全新实例，无状态残留
   - 避免了 status 重置带来的不一致窗口

3. **标签管理同步**：
   - 失败时应移除 `ai-factory-run` 标签（当前未移除，与成功行为不一致）
   - 这样用户删除再添加标签的操作更直观

4. **Webhook 层的 label event 过滤**：
   - GitHub `issues.labeled` 事件会携带触发标签的名称
   - 只有当触发标签是 `ai-factory-run` 或 `ai-factory-smoke` 时才处理
   - 移除标签时不应触发

#### 需要修改的组件

| 组件 | 修改内容 |
|------|---------|
| `server/server.go` | Webhook handler 中添加终态检测和删除逻辑 |
| `server/github.go` | `SetTaskFailed()` 中添加移除 `ai-factory-run` 标签 |

#### 涉及的代码位置

- Webhook handler: `factory/cmd/factory/server/server.go` - `issueWebhookHandler()`
- 标签管理: `factory/cmd/factory/server/github.go` - `SetTaskFailed()`
- 控制器协调: `factory/cmd/factory/server/controller.go` - `shouldReconcile()`

## 具体实现方案

### 改动 1：Webhook handler 添加终态删除逻辑

**文件**：`factory/cmd/factory/server/server.go` - `issueWebhookHandler()`

当前代码（约第 207-237 行）：

```go
// 当前逻辑
alreadyExists := factoryTaskExists(namespaceForTask(task), task.Metadata.Name)
// ... 直接 apply，不管旧任务状态
```

改为：

```go
ns := namespaceForTask(task)
name := task.Metadata.Name
existingPhase := getFactoryTaskPhase(ns, name)

if existingPhase != "" {
    // 任务已存在
    if isTerminalPhase(existingPhase) {
        // 终态：删除旧任务，创建新实例
        fmt.Fprintf(cmd.ErrOrStderr(), "webhook: deleting terminal FactoryTask %s/%s (phase=%s) for re-run\n", ns, name, existingPhase)
        if err := runKubectl(nil, "delete", "factorytask", name, "-n", ns, "--ignore-not-found"); err != nil {
            http.Error(w, fmt.Sprintf("delete existing task: %v", err), http.StatusInternalServerError)
            return
        }
        // 继续执行下面的 apply 创建新实例
    } else {
        // 中间态：忽略，不打断正在执行的任务
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintf(w, `{"triggered":false,"reason":"task already running","task":"%s","phase":"%s"}`+"\n", name, existingPhase)
        return
    }
}

// apply 新的 FactoryTask
data, err := taskpkg.FactoryTaskYAML(task)
// ...
```

### 改动 2：添加辅助函数

**文件**：`factory/cmd/factory/server/server.go`

```go
// getFactoryTaskPhase 返回指定 FactoryTask 的 status.phase，不存在返回 ""
func getFactoryTaskPhase(namespace, name string) string {
    phase, err := kubectlOutput("get", "factorytask", name, "-n", namespace, "-o", "jsonpath={.status.phase}")
    if err != nil {
        return ""
    }
    return strings.TrimSpace(phase)
}

// isTerminalPhase 判断是否为终态（不再会被控制器处理的状态）
func isTerminalPhase(phase string) bool {
    switch phase {
    case taskpkg.PhaseFailed, taskpkg.PhaseSucceeded:
        return true
    default:
        return false
    }
}
```

### 改动 3：失败时移除触发标签

**文件**：`factory/cmd/factory/server/github.go` - `SetTaskFailed()`

当前代码（第 219-229 行）：

```go
func (c *GitHubClient) SetTaskFailed(ctx context.Context, repo string, issueNumber int) error {
    // ... 添加 ai-factory-failed
    // 移除 running/done
    // 但没有移除 ai-factory-run ← 问题所在
}
```

改为：

```go
func (c *GitHubClient) SetTaskFailed(ctx context.Context, repo string, issueNumber int) error {
    if !c.HasToken() {
        return nil
    }
    if err := c.AddLabel(ctx, repo, issueNumber, labelFailed); err != nil {
        return err
    }
    _ = c.RemoveLabel(ctx, repo, issueNumber, labelRunning)
    _ = c.RemoveLabel(ctx, repo, issueNumber, labelDone)
    _ = c.RemoveLabel(ctx, repo, issueNumber, labelRun)     // ← 新增：移除触发标签
    _ = c.RemoveLabel(ctx, repo, issueNumber, labelSmoke)   // ← 新增：移除 smoke 标签
    return nil
}
```

这样失败后 issue 的标签状态变为：`ai-factory` + `ai-factory-failed`，与成功后的状态一致（成功后也是只有 `ai-factory` + `ai-factory-done`）。

### 改动 4：控制器日志优化（可选）

**文件**：`factory/cmd/factory/server/controller.go` - `startControllerLoop()`

当检测到终态任务时，打印日志便于排查：

```go
for i := range tasks {
    task := tasks[i]
    if !shouldReconcile(task) {
        if isTerminalPhase(task.Status.Phase) {
            // 终态任务，跳过（不打印，避免日志刷屏）
        }
        continue
    }
    // ...
}
```

---

## 问题二：并发任务状态不可见

### 问题描述

当多个 issue 同时触发时，warm pool 中的 pod 数量有限（当前配置为 2）。第 3 个及之后的任务需要等待 pod 释放，但用户在 GitHub issue 页面上无法区分"正在执行"和"排队等待"。

```
Issue #42: ai-factory-running ← 正在执行 agent（占用 Pod 1）
Issue #43: ai-factory-running ← 正在执行 agent（占用 Pod 2）
Issue #44: ai-factory-running ← 等待 warm pool pod...
```

三个 issue 看起来完全一样，但 #44 实际上在排队。

### 内部状态差异

FactoryTask 的 `status.phase` 已经区分了这两个阶段：

| 阶段 | 含义 | Issue 标签 |
|------|------|-----------|
| `ClaimCreated` | SandboxClaim 已创建，等待 pod 分配 | `ai-factory-running` ❌ 不准确 |
| `SandboxReady` | Pod 已分配，准备执行 | `ai-factory-running` ✅ |
| `Running` | 正在执行 agent | `ai-factory-running` ✅ |

### 解决方案：增加 `ai-factory-waiting` 标签

在 SandboxClaim 等待 pod 分配阶段（`ClaimCreated`），使用 `ai-factory-waiting` 标签替代 `ai-factory-running`。

#### 标签状态流转

```
Webhook 收到
    ↓
添加 ai-factory-running（Webhook handler，已有）
    ↓
控制器接管 → Pending → ClaimCreated
    ↓
【新增】替换为 ai-factory-waiting（等待 pod）
    ↓
SandboxClaim Ready → SandboxReady
    ↓
【新增】替换为 ai-factory-running（开始执行）
    ↓
执行完成 → Succeeded/Failed
    ↓
替换为 ai-factory-done / ai-factory-failed（已有）
```

#### 用户视角

```
Issue #42: ai-factory-running  ← 正在执行
Issue #43: ai-factory-running  ← 正在执行
Issue #44: ai-factory-waiting  ← 排队等待中
```

#### 需要修改的组件

| 组件 | 修改内容 |
|------|---------|
| `server/github.go` | 新增 `SetTaskWaiting()` 方法 |
| `server/controller.go` | `executeTask()` 中在 `ClaimCreated` 阶段调用 `SetTaskWaiting()`，在 `SandboxReady` 阶段调用 `SetTaskRunning()` |

#### 涉及的代码位置

- 标签管理: `factory/cmd/factory/server/github.go`
- 控制器: `factory/cmd/factory/server/controller.go` - `executeTask()` 第 152-196 行

---

## 问题三：并发 Webhook 竞态导致 AlreadyExists 错误

### 问题描述

当同一个 issue 的多个 webhook 请求几乎同时到达时，`factoryTaskExists` 检查和 `kubectl apply` 之间存在时间窗口，导致多个请求都通过检查，但只有一个能成功创建资源，其余报 `AlreadyExists` 错误。

### 日志复现

```
# Issue #31 webhook 第一次到达
factorytask .../github-verdure-oss-test-31 created        ← 创建成功
webhook: github issue 31 -> FactoryTask (exists=false)

# Issue #31 webhook 第二次到达（几乎同时）
Error from server (AlreadyExists): ... "github-verdure-oss-test-31" already exists  ← 报错
```

### 根因分析

```
请求 A                    请求 B
    ↓                        ↓
factoryTaskExists → false    factoryTaskExists → false（A 还没写入）
    ↓                        ↓
kubectl apply → 成功         kubectl apply → 失败（A 已创建）
```

`kubectl apply` 在资源不存在时使用 `create` 路径，已存在时使用 `patch` 路径。当两个请求几乎同时到达时，第二个请求的 `apply` 内部仍走 `create` 路径，但资源已被第一个请求创建，因此报 `AlreadyExists`。

### 来源

这种并发请求通常来自：
- GitHub webhook 重试机制（响应超时时自动重投递）
- 用户快速多次操作标签
- 网络抖动导致的重复投递

### 影响

1. **Webhook 返回错误给 GitHub** — GitHub 可能会再次重试，形成错误循环
2. **标签可能未正确设置** — `alreadyExists=false` 分支的 `SetTaskRunning` 未执行
3. **FactoryTask 本身没问题** — 资源最终被正确创建，控制器可以正常执行

### 解决方案：捕获 AlreadyExists 错误

**文件**：`factory/cmd/factory/server/server.go` - `issueWebhookHandler()`

当前代码：

```go
if err := runKubectlWithInput(data, "apply", "-f", "-"); err != nil {
    http.Error(w, err.Error(), http.StatusInternalServerError)
    return
}
```

改为：

```go
if err := runKubectlWithInput(data, "apply", "-f", "-"); err != nil {
    if isAlreadyExistsError(err) {
        // 并发 webhook 导致资源已被另一个请求创建，视为成功
        fmt.Fprintf(cmd.ErrOrStderr(), "webhook: %s issue %s -> FactoryTask already created (concurrent webhook)\n", provider, task.Spec.Trigger.ID)
    } else {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
}
```

添加辅助函数：

```go
func isAlreadyExistsError(err error) bool {
    return err != nil && strings.Contains(err.Error(), "AlreadyExists")
}
```

---

## 问题四：服务器重启后任务恢复（优先级：低）

### 问题描述

服务器 pod 重启后，正在执行的任务会被重复执行，旧的 SandboxClaim 和 Pod 会泄漏。

### 当前行为

```
服务器重启前：Task #42 状态 = PhaseRunning，SandboxClaim 已存在
服务器重启后：
  1. taskTracker 清空（内存丢失）
  2. 控制器 list FactoryTasks → #42 状态是 PhaseRunning → shouldReconcile = true
  3. executeTask() 从头执行
  4. kubectl apply SandboxClaim（已存在，幂等）
  5. kubectl wait Ready（已 Ready，立即通过）
  6. 重新执行所有步骤（clone、agent、验证、push、PR）← 重复执行
```

### 问题

1. **重复执行**：agent 被重新运行，浪费时间和 token
2. **旧 Pod 泄漏**：重启前的 Pod 继续运行，占用 warm pool slot
3. **分支冲突**：旧任务已 push 过分支，新任务再次 push 可能冲突

### 可能的解决方向

**方向 A：检测已有 SandboxClaim 并复用**

executeTask 开始时检查 SandboxClaim 是否已存在且 Ready，直接复用。

**方向 B：任务完成后清理 SandboxClaim**

executeTask 成功/失败分支都删除 SandboxClaim，避免泄漏。

**方向 C：两者结合**

任务完成后清理 + 重启时检测并复用。

---

## 问题五：Issue 关闭时任务处理（优先级：低）

### 问题描述

任务正在执行中时用户关闭 issue，系统不会取消任务，PR 仍然会被创建。

### 当前行为

```
任务正在执行 → 用户关闭 issue → 任务继续执行 → 创建 PR
```

### 讨论

- 关闭 issue 可能只是整理 issue 列表，不代表不想继续
- 已经在执行了，让它跑完也无妨
- PR 创建后用户可以选择关闭 PR
- 如果需要取消，可以实现 issue close 事件监听，但这增加了复杂度

暂不处理，保持当前行为。

---

## 问题六：配置热更新与完整配置项（优先级：高）

### 问题描述

1. **缺少交互配置**：部署时只收集 `OPENAI_API_KEY`，`OPENAI_BASE_URL` 和 `OPENAI_MODEL` 写死在 values.yaml 默认值中
2. **无热更新机制**：修改 key/url/model 后需要重启 server pod 才能生效

### 当前配置流转

```
部署时 collect → helm --set → Secret → Deployment env (secretKeyRef)
    → Server Pod 环境变量 → os.Getenv() → SandboxClaim env → Agent Pod
```

问题：K8s Secret 通过 `secretKeyRef` 挂载到 env var 时，Pod 不会自动感知 Secret 变更。

### 解决方案

#### 1. 补齐部署时交互配置

`deploy-remote.sh` 收集三个 OpenAI 参数：

```bash
read -p "OpenAI API Key: " OPENAI_API_KEY
read -p "OpenAI Base URL [https://api.openai.com/v1]: " OPENAI_BASE_URL
read -p "OpenAI Model [gpt-4.1]: " OPENAI_MODEL

# 设置默认值
OPENAI_BASE_URL=${OPENAI_BASE_URL:-https://api.openai.com/v1}
OPENAI_MODEL=${OPENAI_MODEL:-gpt-4.1}

helm upgrade --install ai-factory "${CHART_PATH}" \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}" \
    --set openai.baseUrl="${OPENAI_BASE_URL}" \
    --set openai.model="${OPENAI_MODEL}"
```

#### 2. Secret 文件挂载实现热更新

**Deployment 改造**：将 Secret 从 env var 改为文件挂载。

当前（需重启）：
```yaml
env:
- name: OPENAI_API_KEY
  valueFrom:
    secretKeyRef:
      name: ai-factory-credentials
      key: OPENAI_API_KEY
```

改为（热更新）：
```yaml
volumes:
- name: config
  secret:
    secretName: ai-factory-credentials
containers:
  volumeMounts:
  - name: config
    mountPath: /etc/ai-factory/config
    readOnly: true
```

**Server 代码改造**：从文件读取而非 os.Getenv()。

```go
// 从挂载的 Secret 文件读取，K8s 会自动同步 Secret 变更
func readSecretFile(name string) string {
    data, err := os.ReadFile("/etc/ai-factory/config/" + name)
    if err != nil {
        return ""
    }
    return strings.TrimSpace(string(data))
}

// 在 Reconcile 时读取
apiKey := readSecretFile("OPENAI_API_KEY")
```

#### 3. 热更新操作

```bash
# 更新 Secret（server 自动感知，无需重启）
kubectl create secret generic ai-factory-credentials \
    --from-literal=OPENAI_API_KEY=sk-new-xxx \
    --from-literal=GITHUB_TOKEN=... \
    --from-literal=WEBHOOK_SECRET=... \
    -n ai-factory --dry-run=client -o yaml | kubectl apply -f -
```

### 问题六实现状态 ✅ 已完成

**实现日期**：2026-08-03

**实现内容**：
1. ✅ 新增 `ReadConfig()` 工具函数（`factory/pkg/task/config.go`）
2. ✅ 所有 `os.Getenv` 调用改为 `ReadConfig`（server 和 pkg/task 共 7 个文件）
3. ✅ Helm chart：Secret volume mount + ConfigMap volume mount（替代 env var）
4. ✅ `deploy-remote.sh` 补齐 OPENAI_BASE_URL / OPENAI_MODEL 交互配置
5. ✅ Secret 增加 GITLAB_TOKEN 字段

**配置读取优先级**：
```
1. /etc/ai-factory/secret/<name>   ← K8s Secret volume（自动同步 ~30s）
2. /etc/ai-factory/config/<name>   ← K8s ConfigMap volume（自动同步 ~30s）
3. os.Getenv(name)                 ← 本地 dev.sh 兼容回退
```

**文件与配置项映射**：

| 文件 | 说明 |
|------|------|
| `scripts/ai-factory.env` | 本地配置文件，编辑后运行 `scripts/update-config.sh` 同步到集群 |
| `scripts/update-config.sh` | 配置同步脚本，读取 .env 文件更新 K8s Secret + ConfigMap |

| Pod 内挂载路径 | K8s 资源 | 配置项 |
|--------------|---------|--------|
| `/etc/ai-factory/secret/` | Secret/ai-factory-credentials | GITHUB_TOKEN, WEBHOOK_SECRET, OPENAI_API_KEY, CODEX_API_KEY, GITLAB_TOKEN |
| `/etc/ai-factory/config/` | ConfigMap/ai-factory-config | OPENAI_BASE_URL, OPENAI_MODEL, OPENAI_TEMPERATURE, OPENAI_MAX_TOKENS, 其他超时/轮次配置, AI_FACTORY_GIT_PROXY |

**热更新操作**：

编辑 `scripts/ai-factory.env` 文件，然后运行：

```bash
./scripts/update-config.sh
```

脚本会自动读取 .env 文件，分别更新 K8s Secret 和 ConfigMap。~30s 后新配置自动生效，无需重启 Pod。

**验证方式**（无需进入容器）：

触发一个新 issue，然后检查最新 SandboxClaim 的环境变量：

```bash
kubectl get sandboxclaims -n ai-factory \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].spec.template.spec.containers[0].env}' | \
    python3 -m json.tool | grep -A1 OPENAI_MODEL
```

如果值已更新，说明热更新生效。

---

## dist/ 目录结构优化（待做）

### 当前问题

1. **嵌套重复目录**：`ai-factory-chart/ai-factory/` 是外层的副本，由 `package.sh` 的 `cp -r` 造成
2. **agent-sandbox install 离线不可用**：脚本需要 `git clone` 仓库，在无网络 VM 上会失败
3. **factory-task 目录有未使用文件**：`render-runtime`、`runtime.yaml`、`README.md` 未被使用
4. **smoke-test 未使用**：`components/agent-sandbox/smoke-test` 未被 `deploy-remote.sh` 调用
5. **agent-sandbox 控制器部署依赖网络**：需要 clone 仓库才能获取 manifest

### 优化方案

预提取所有依赖，消除运行时网络依赖：

```
dist/
├── deploy-remote.sh
├── crd/
│   ├── factory-task-crd.yaml      ← 从 components/factory-task/crd.yaml 复制
│   └── agent-sandbox-crd.yaml     ← 从 agent-sandbox 仓库预提取
├── agent-sandbox-controller/
│   └── deploy.yaml                ← 控制器 Deployment/RBAC 等（预提取）
└── ai-factory-chart/              ← Helm chart（无嵌套）
    ├── Chart.yaml
    ├── values.yaml
    └── templates/...
```

### 远程镜像模式

如果三个镜像推送到远程仓库（Docker Hub、GCR、私有 registry），dist/ 可以大幅简化：

```
dist/
├── deploy-remote.sh               ← 简化：不再需要导入镜像步骤
├── crd/
│   ├── factory-task-crd.yaml
│   └── agent-sandbox-crd.yaml
├── agent-sandbox-controller/
│   └── deploy.yaml
└── ai-factory-chart/
    ├── Chart.yaml
    ├── values.yaml                ← image.repository 改为远程地址
    └── templates/...
```

values.yaml 中的镜像配置：

```yaml
server:
  image:
    repository: registry.example.com/ai-factory/ai-factory-server
    tag: v0.1.0
    pullPolicy: IfNotPresent

sandbox:
  image:
    repository: registry.example.com/ai-factory/coding-agent-sandbox
    tag: v0.1.0
    pullPolicy: IfNotPresent
```

deploy-remote.sh 简化为：

```bash
#!/bin/bash
set -euo pipefail

# 1. 安装 CRD
kubectl apply -f crd/factory-task-crd.yaml
kubectl apply -f crd/agent-sandbox-crd.yaml

# 2. 安装 agent-sandbox 控制器
kubectl apply -f agent-sandbox-controller/deploy.yaml

# 3. 收集凭证
read -p "GitHub Token: " GITHUB_TOKEN
read -p "Webhook Secret: " WEBHOOK_SECRET
read -p "OpenAI API Key: " OPENAI_API_KEY

# 4. 安装 Helm chart
helm upgrade --install ai-factory ai-factory-chart/ \
    --namespace ai-factory --create-namespace \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}"

# 5. 等待部署
kubectl rollout status deployment/ai-factory-server -n ai-factory --timeout=60s
```

dist/ 体积对比：

| 模式 | 体积 | 说明 |
|------|------|------|
| 离线模式（当前） | ~700MB | 3 个 tar 镜像 + chart + scripts |
| 远程镜像模式 | ~200KB | chart + CRDs + scripts |

### 远程 Helm 仓库模式

如果 Helm chart 也推送到远程仓库，dist/ 进一步简化为纯引导包：

```
dist/
├── deploy-remote.sh
├── crd/
│   ├── factory-task-crd.yaml
│   └── agent-sandbox-crd.yaml
└── agent-sandbox-controller/
    └── deploy.yaml
```

体积：< 100KB。Helm chart 和镜像都从远程拉取。

部署流程：

```bash
# 1. 安装 CRD（Helm 无法在同一个 release 中安装和使用 CRD）
kubectl apply -f crd/factory-task-crd.yaml
kubectl apply -f crd/agent-sandbox-crd.yaml

# 2. 安装 agent-sandbox 控制器
kubectl apply -f agent-sandbox-controller/deploy.yaml

# 3. 收集凭证
read -p "GitHub Token: " GITHUB_TOKEN
read -p "Webhook Secret: " WEBHOOK_SECRET
read -p "OpenAI API Key: " OPENAI_API_KEY

# 4. 从远程 Helm 仓库安装
helm repo add ai-factory https://charts.example.com/ai-factory
helm repo update
helm upgrade --install ai-factory ai-factory/ai-factory \
    --namespace ai-factory --create-namespace \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}"

# 5. 等待部署
kubectl rollout status deployment/ai-factory-server -n ai-factory --timeout=60s
```

### 职责划分

```
dist/ 引导包（先部署）：
  ├─ CRD 定义（FactoryTask + SandboxClaim 等）
  └─ agent-sandbox 控制器

Helm chart（后部署）：
  ├─ ai-factory-server Deployment
  ├─ Service / Ingress
  ├─ Secret
  ├─ RBAC
  └─ SandboxTemplate + SandboxWarmPool
```

### 版本发布清单

```
远程镜像仓库：
  registry.example.com/ai-factory/ai-factory-server:v0.1.0
  registry.example.com/ai-factory/coding-agent-sandbox:v0.1.0
  registry.example.com/ai-factory/agent-sandbox-controller:v0.1.0

远程 Helm 仓库：
  https://charts.example.com/ai-factory/ai-factory-0.1.0.tgz

dist/ 引导包：
  ai-factory-bootstrap-v0.1.0.tar.gz  (~50KB)
```

三者版本号保持一致，一起发布。

---

## 附录：其他方案（未采用）

### 方案 B：重置状态
Webhook 层将 status.phase 重置为空，控制器重新接管。
- 缺点：需要两次 kubectl 操作，有不一致窗口

### 方案 C：re-run annotation
添加 annotation 触发重试，控制器检测到后重置状态。
- 缺点：改动两处（webhook + controller），用户需要了解 annotation 机制

### 方案 D：用户手动操作
用户手动 `kubectl patch` 重置状态。
- 缺点：用户体验差，需要了解 K8s 内部机制
