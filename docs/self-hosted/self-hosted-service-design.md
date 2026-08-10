# ai-factory 自托管服务设计文档

> 本文档记录将 ai-factory 从 GitHub Actions 工作流迁移到 K8s 自托管服务的架构设计。
> 基于 2026-07-28 的讨论整理，供后续开发参考。
>
> **实现说明（2026-08）**：本文是历史设计记录。其中规划的 `k8s/` 原始清单最终以
> **Helm Chart（`charts/ai-factory/`）**形式落地，`k8s/` 目录已废弃并移除。
> 部署请使用 `scripts/package.sh` + `scripts/deploy-remote.sh`。

## 1. 背景与目标

### 当前方案（GitHub Actions）

```
目标仓库 Issue → repository_dispatch → ai-factory 的 GH Actions
  → 每次从零搭建: kind 集群 + Docker 镜像 + CRD + warm pool
  → 执行 agent → 创建 PR
  → 集群销毁，下次重来
```

**问题：**
- 冷启动 ~10-15min（每次重建基础设施）
- 每个目标仓库需要配置 workflow 文件 + secret
- OpenAI Key 等凭证分散在各仓库的 Actions secrets 中
- 按分钟计费，长时间任务成本高

### 目标方案（K8s 自托管服务）

```
目标仓库 Issue → GitHub Webhook → ai-factory 服务（K8s 集群常驻）
  → 基础设施常驻，无冷启动
  → 接收事件 → 执行 agent → 推送代码 → 创建 PR
```

**收益：**
- 零冷启动（环境常驻）
- 目标仓库只需配置一个 Webhook，无需代码改动
- 所有凭证集中在 ai-factory 服务端
- 新增仓库零配置（ai-factory 侧）

## 2. 整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    目标仓库（GitHub）                      │
│  Settings → Webhooks → POST https://your-vm/webhook/github  │
│  Events: ✅ Issues                                      │
└──────────────────────┬──────────────────────────────────┘
                       │
                       │ GitHub Webhook (HTTPS)
                       │ X-Hub-Signature-256 签名验证
                       ▼
┌─────────────────────────────────────────────────────────┐
│              ai-factory 服务（K8s Pod）                    │
│                                                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │ Webhook      │  │ Task         │  │ PR           │   │
│  │ Handler      │→ │ Controller   │→ │ Creator      │   │
│  │              │  │              │  │              │   │
│  │ - 验证签名    │  │ - 创建任务    │  │ - 创建 PR    │   │
│  │ - 解析事件    │  │ - 管理生命周期 │  │ - 发评论     │   │
│  │ - 创建任务    │  │ - 执行 agent  │  │ - 改标签     │   │
│  └──────────────┘  └──────┬───────┘  └──────────────┘   │
│                           │                              │
└───────────────────────────┼──────────────────────────────┘
                            │
                            │ 创建/管理 Sandbox Pod
                            ▼
┌─────────────────────────────────────────────────────────┐
│              Sandbox Pod（临时，任务结束即销毁）              │
│                                                          │
│  1. git clone 目标仓库                                    │
│  2. checkout base branch                                 │
│  3. 创建 work branch                                     │
│  4. 执行 agent（传入 issue 内容作为 prompt）                │
│  5. 运行验证命令（go test 等）                             │
│  6. git commit + push                                    │
│                                                          │
│  工具: git, go, node, python, ripgrep, jq               │
└─────────────────────────────────────────────────────────┘
```

## 3. 一个请求的完整生命周期

```
1. 目标仓库有人给 Issue #42 打了 ai-factory-run 标签

2. GitHub 发 Webhook POST 到 ai-factory 服务
   POST https://your-vm/webhook/github
   Content-Type: application/json
   X-Hub-Signature-256: sha256=xxx
   Body: {
     "action": "labeled",
     "issue": {
       "number": 42,
       "title": "Add dark mode support",
       "body": "Please add a dark mode toggle...",
       "labels": [{"name": "ai-factory-run"}]
     },
     "repository": {
       "full_name": "your-org/your-repo",
       "clone_url": "https://github.com/your-org/your-repo.git",
       "default_branch": "main"
     }
   }

3. Webhook Handler
   - 用 WEBHOOK_SECRET 验证签名
   - 检查 label 是否为 ai-factory-run 或 ai-factory-smoke
   - 提取 issue 真实内容（title + body）
   - 创建 FactoryTask 对象，加入任务队列

4. Task Controller（持续运行）
   - 从队列取出 FactoryTask
   - 创建 Sandbox Pod（注入 GITHUB_TOKEN 作为环境变量）
   - 通过 kubectl exec 在 Pod 内执行：
     a. git clone https://github.com/your-org/your-repo.git
     b. git checkout main
     c. git checkout -b ai-factory/issue-42
     d. 运行 agent，prompt = issue title + body
     e. 运行验证命令（go test ./...）
     f. git commit -am "feat: implement issue #42"
     g. git push origin ai-factory/issue-42
   - 收集执行结果
   - 销毁 Sandbox Pod

5. PR Creator
   - 调 GitHub API 创建 PR（从 ai-factory/issue-42 → main）
   - 给 Issue #42 发评论，附 PR 链接
   - 更新 Issue 标签：移除 ai-factory-run，添加 ai-factory-done

6. 异常处理
   - agent 执行失败 → 添加 ai-factory-failed 标签 + 失败评论
   - 验证不通过 → 同上
   - 超时 → 同上
```

## 4. 权限与凭证模型

### ai-factory 服务端（一次性配置）

所有凭证存在一个 K8s Secret 中：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ai-factory-credentials
type: Opaque
stringData:
  GITHUB_TOKEN: "ghp_xxxxxxxxxxxx"        # 操作目标仓库
  WEBHOOK_SECRET: "your-webhook-secret"    # 验证 GitHub 签名
  OPENAI_API_KEY: "sk-xxxxxxxxxxxx"        # 给 agent 用
```

**GitHub Token 权限范围：**
```
你的 GitHub 账号（比如 Verdure-oss）
├── 你自己的仓库 ✅ 直接能操作
├── 别人给你 Collaborator 写权限的仓库 ✅ 也能操作
└── 无权限的仓库 ❌ 操作不了
```

需要的权限：
- `issues: write` — 给 Issue 发评论、改标签
- `contents: write` — 推送分支到目标仓库
- `pull-requests: write` — 创建 PR
- `metadata: read` — 读仓库信息（默认分支等）

### 目标仓库端（每个仓库配一次，5 分钟）

**不需要任何 secret，不需要装代码。** 只需要：

1. **添加 Webhook**
   - Payload URL: `https://你的服务器/webhook/github`
   - Content type: `application/json`
   - Secret: 和 ai-factory 服务端同一个 `WEBHOOK_SECRET`
   - Events: 选 "Let me select individual events" → ✅ Issues

2. **给 ai-factory 账号写权限**（如果需要推送代码）
   - Settings → Collaborators → 添加 ai-factory 使用的 GitHub 账号

3. **打标签触发**
   - 给 Issue 打 `ai-factory-run` → 正式运行
   - 给 Issue 打 `ai-factory-smoke` → 冒烟测试

## 5. 新增仓库的接入流程

**ai-factory 服务端：零改动。**

```
部署完 ai-factory 服务之后：

第 1 天：repo-A 配了 webhook → Issue 打标签 → 自动处理 ✅
第 2 天：repo-B 配了 webhook → Issue 打标签 → 自动处理 ✅
第 30 天：repo-C 配了 webhook → Issue 打标签 → 自动处理 ✅

ai-factory 服务端不需要做任何事。
```

## 6. 技术要点

### 6.1 不再需要 kind 集群

现在 GH Actions 里用 kind 创建嵌套集群。自托管服务直接在当前 K8s 集群里创建 Sandbox Pod，不需要 kind。

```yaml
# Sandbox Pod 示例
apiVersion: v1
kind: Pod
metadata:
  name: sandbox-issue-42
  namespace: ai-factory
spec:
  containers:
  - name: dev
    image: ai-factory/coding-agent:latest
    command: ["/bin/bash", "-lc", "sleep 3600"]
    env:
    - name: GITHUB_TOKEN
      valueFrom:
        secretKeyRef:
          name: ai-factory-credentials
          key: GITHUB_TOKEN
    - name: OPENAI_API_KEY
      valueFrom:
        secretKeyRef:
          name: ai-factory-credentials
          key: OPENAI_API_KEY
  restartPolicy: Never
```

### 6.2 需要安装 agent-sandbox CRD（可选）

如果要用 SandboxClaim/SandboxTemplate（warm pool 预热），需要在集群上安装 agent-sandbox CRD。

如果直接创建普通 Pod，则不需要额外 CRD。

### 6.2.1 Sandbox 生命周期管理

有两种模式管理 Sandbox Pod：

#### 模式 1：每次重建（测试环境推荐）

```
Issue #42 → 创建 Sandbox Pod → 执行 → 销毁
Issue #43 → 创建 Sandbox Pod → 执行 → 销毁
```

**优点**：
- 简单，不需要管理池子
- 完全隔离，不会互相影响
- 调试方便（每个任务的 Pod 独立）

**缺点**：
- 每次都要创建 Pod（秒级开销）

#### 模式 2：Warm Pool（生产环境推荐）

```
预先创建一批 Pod（warm pool）：
├── Pod 1（空闲）
├── Pod 2（空闲）
└── Pod 3（空闲）

Issue 到达 → 从池子里取 Pod → 执行 → 销毁（或放回池子）
后台自动补充新 Pod 到池子
```

**优点**：
- 省去创建 Pod 的时间（毫秒级响应）
- 适合高并发场景

**缺点**：
- 需要管理池子（CRD 或自定义逻辑）
- 资源占用（池子里的 Pod 一直运行）

#### Pod 清理策略

任务执行完成后，需要清理 Sandbox Pod：

**策略 1：直接销毁（推荐）**
```
任务完成 → 销毁 Pod → 后台自动补充新 Pod 到池子
```

**策略 2：清理后放回池子**
```
任务完成 → 清理环境（git clean -fdx） → 放回池子
缺点：可能残留环境污染，增加复杂度
```

**建议**：测试阶段用"每次重建"模式，生产阶段用"Warm Pool + 直接销毁"模式。

### 6.3 Webhook 签名验证

GitHub 用 HMAC-SHA256 对 payload 签名。ai-factory 服务需要验证：

```go
func verifySignature(payload []byte, signature string, secret string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(payload)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(signature), []byte(expected))
}
```

### 6.4 服务需要暴露到公网

GitHub 需要能 POST 到你的服务。需要：
- 公网 IP 或域名
- HTTPS 证书（GitHub 强制要求）
- K8s Ingress 或 LoadBalancer

## 7. GH Actions 步骤 → 自托管服务对照

| GH Actions 步骤 | 自托管服务 | 说明 |
|-----------------|-----------|------|
| Set up Go | ❌ 不需要 | Go 在 Dockerfile build 阶段用，运行时是编译好的二进制 |
| OpenAI config preflight | ✅ 保留 | 变成服务启动时的健康检查 |
| Create kind cluster | ❌ 不需要 | 服务直接跑在 K8s 集群里 |
| Install FactoryTask CRD | ⚠️ 集群装一次 | 不再每次运行都装 |
| Install agent-sandbox | ⚠️ 可选 | 如果用 SandboxClaim 模式才需要 |
| Build coding agent image | ⚠️ 预构建 | 预构建推 registry，不再每次 build |
| Create sandbox warm pool | ⚠️ 可选 | 预热池常驻 vs 按需创建 Pod |
| Run FactoryTask | ✅ 核心逻辑 | 变成服务的 controller 循环 |
| Show status | ✅ 日志/监控 | 变成服务的日志输出 |

### 两个镜像

部署需要两个镜像，职责不同：

```
镜像 1: ai-factory-server（主服务）
├── 包含：Go 编译好的二进制
├── 职责：接收 webhook、管理任务、调 GitHub API
├── 大小：~50MB
├── 构建：Dockerfile 多阶段构建
└── 部署：K8s Deployment，常驻运行

镜像 2: coding-agent-sandbox（沙箱环境）
├── 包含：git, go, node, python, ripgrep, jq, ai-factory-agent
├── 职责：在 Pod 里执行 agent、编译、测试
├── 大小：~1-2GB
├── 构建：预构建，推到 GHCR 或私有 registry
└── 部署：不常驻，每次任务创建一个 Pod，完成即销毁
```

### 部署流程（一次性）

```
1. 构建 ai-factory-server 镜像，推到 registry
2. 构建 coding-agent-sandbox 镜像，推到 registry
3. 集群安装 FactoryTask CRD（如果用 CRD 模式）
4. 创建 K8s Secret（GitHub Token, Webhook Secret, OpenAI Key）
5. 部署 ai-factory-server（Deployment + Service + Ingress）
6. 验证：curl /healthz 返回 OK
```

### 运行流程（每次请求）

```
1. GitHub Webhook 到达 → 签名验证
2. 创建任务 → 从 registry 拉取 sandbox 镜像 → 创建 Sandbox Pod
3. 在 Pod 内执行 agent
4. 推送代码 → 创建 PR → 发评论
5. 销毁 Sandbox Pod
```

### 需要新建的文件

```
├── Dockerfile.server                    # 打包 ai-factory 服务
├── Dockerfile.sandbox                   # 打包 coding-agent 环境（可复用现有的）
├── k8s/
│   ├── deployment.yaml                  # ai-factory 服务 Deployment
│   ├── service.yaml                     # ClusterIP Service
│   ├── ingress.yaml                     # HTTPS Ingress（接收 GitHub Webhook）
│   ├── secret.yaml                      # 凭证模板（不提交到 git）
│   └── factory-task-crd.yaml            # CRD 安装（一次性）
└── cmd/factory/server.go                # 服务入口（合并 webhook + controller + PR）
```

## 8. 网络暴露方案

### 问题

虚拟机 IP `172.30.41.45` 是内网地址，GitHub 无法直接访问。需要一个方案让 GitHub Webhook 能到达服务。

### 方案对比

| 方案 | 需要公网 IP | HTTPS | 成本 | 稳定性 | 适合场景 |
|------|-----------|-------|------|--------|---------|
| ngrok | ❌ | ✅ 自动 | 免费 | 中 | **测试环境推荐** |
| Cloudflare Tunnel | ❌ | ✅ 自动 | 免费 | 高 | 生产环境推荐 |
| 反向代理 + 公网 IP | ✅ | 需自己配 | 取决于云 | 高 | 有云服务器时 |
| Tailscale Funnel | ❌ | ✅ 自动 | 免费 | 中 | 已用 Tailscale 时 |

### 测试环境推荐：ngrok

**5 分钟快速测试，不需要注册账号**：

```bash
# 1. 安装 ngrok（在 VM 上）
curl -s https://ngrok-agent.s3.amazonaws.com/ngrok-v3-stable-linux-amd64.tgz | tar xz -C /usr/local/bin

# 2. 启动隧道（假设服务跑在 8080 端口）
ngrok http 8080

# 3. 会显示公网 URL
# https://xxxx.ngrok.io → GitHub Webhook 填这个地址
```

**优点**：
- 最快，5 分钟测试
- 不需要注册（免费版）
- HTTPS 自动搞定

**缺点**：
- 免费版 URL 每次重启会变（测试阶段无所谓）
- 不适合生产

**流程**：
```
GitHub ──POST──→ https://xxxx.ngrok.io/webhook/github
                      │
                      ▼
              VM 上的 ngrok 客户端
                      │
                      ▼
              K8s Ingress → ai-factory Pod
```

### 生产环境推荐：Cloudflare Tunnel

不需要公网 IP，不需要开端口，不需要配证书。Cloudflare 在你的 VM 上运行一个客户端，建立到 Cloudflare 边缘的出站隧道，GitHub 通过 Cloudflare 的公网地址访问你的服务。

```
GitHub ──POST──→ https://ai-factory.your-domain.com
                      │
                      ▼
              Cloudflare 边缘节点
                      │
                      ▼ (隧道，出站连接，不需要开端口)
              VM 上的 cloudflared 客户端
                      │
                      ▼
              K8s Ingress → ai-factory Pod
```

配置步骤：
1. 注册 Cloudflare 账号，添加域名（或用 Cloudflare 提供的免费子域名）
2. VM 上安装 cloudflared 客户端
3. 创建 Tunnel，指向 K8s Service
4. 配置 DNS 解析指向 Tunnel

### 替代方案：ngrok（开发测试用）

最简单但不适合生产：

```bash
# VM 上安装 ngrok
ngrok http https://ai-factory-service:443

# 获得公网 URL
# https://xxxx.ngrok.io → GitHub Webhook 填这个地址
```

免费版的 URL 每次重启会变，不适合生产环境。

### 替代方案：有公网 IP 时

如果有公网 IP（比如云服务器），直接配反向代理：

```
GitHub ──POST──→ https://your-public-ip/webhook/github
                      │
                      ▼ (Nginx/Traefik 反向代理，SSL 终止)
              K8s Ingress → ai-factory Pod
```

需要：公网 IP + 域名 + SSL 证书（Let's Encrypt 免费）

## 9. 当前代码改造点

| 组件 | 现状 | 改造内容 |
|------|------|---------|
| `factory/cmd/factory/task.go` | 三个独立 CLI 命令 | 合并为一个常驻服务入口 |
| `factory/pkg/task/webhook.go` | 解析 GitHub 事件 | 复用，增加签名验证 |
| `factory/pkg/task/controller.go` | `watch --once` 模式 | 改为持续 watch + 并发执行 |
| `factory/pkg/task/plan.go` | 构造执行计划 | 复用 |
| `factory/pkg/task/change_request.go` | 创建 PR | 复用 |
| `factory/pkg/task/reporting.go` | 发 Issue 评论 | 复用 |
| 新增 `Dockerfile` | 无 | 打包服务镜像 |
| 新增 `k8s/` 部署清单 | 无 | Deployment + Service + Secret + Ingress |

## 10. 待讨论的开放问题

- [ ] Sandbox 策略：直接创建普通 Pod，还是用 agent-sandbox CRD 的 SandboxClaim？
- [ ] 并发模型：同时来多个 Issue，如何调度 Sandbox？
- [ ] 高可用：服务重启后，正在执行的任务如何恢复？
- [ ] 监控告警：任务失败如何通知？
- [ ] GitHub App 升级：后续是否需要支持任意仓库接入（不局限于自己账号的仓库）？

## 11. 标签生命周期

| 标签 | 含义 |
|------|------|
| `ai-factory-run` | 正式运行：agent 修改代码 + 创建 PR |
| `ai-factory-smoke` | 冒烟测试：验证环境，不修改代码 |
| `ai-factory-running` | 处理中（ai-factory 自动添加） |
| `ai-factory-done` | 处理完成（ai-factory 自动添加） |
| `ai-factory-failed` | 处理失败（ai-factory 自动添加） |
