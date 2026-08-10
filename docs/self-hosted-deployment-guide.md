# ai-factory 自托管服务 - 详细部署指南

> 本文档提供完整的部署流程，包括环境准备、打包、部署、配置和验证。

## 目录

- [环境要求](#环境要求)
- [打包流程](#打包流程)
- [部署流程](#部署流程)
- [配置说明](#配置说明)
- [验证部署](#验证部署)
- [配置 GitHub Webhook](#配置-github-webhook)
- [热更新配置](#热更新配置)

---

## 环境要求

### Kubernetes 集群

| 项目 | 要求 |
|------|------|
| K8s 版本 | 1.24+ |
| 节点数量 | ≥ 2（推荐 3+） |
| 节点配置 | 2C4G+（agent 执行需要资源） |

**支持的集群类型**：
- kind（本地开发推荐）
- minikube
- GKE / EKS / AKS
- 自建集群

### 本地工具

| 工具 | 版本 | 安装说明 |
|------|------|---------|
| kubectl | 1.24+ | [安装指南](https://kubernetes.io/docs/tasks/tools/) |
| helm | 3.x | [安装指南](https://helm.sh/docs/intro/install/) |
| git | 2.x+ | 系统包管理器 |

**可选（推荐）**：
- nerdctl + buildkit：无需 Docker daemon，更轻量
  - nerdctl: https://github.com/containerd/nerdctl/releases
  - buildkit: https://github.com/moby/buildkit/releases

### 网络要求

- 能访问 GitHub（克隆 agent-sandbox 仓库）
- 能访问 OpenAI API 或兼容端点
- 如果在中国大陆，建议配置代理

---

## 打包流程

### 1. 克隆仓库

```bash
git clone https://github.com/verdure-oss/ai-factory.git
cd ai-factory
```

### 2. 运行打包脚本

```bash
./scripts/package.sh
```

**脚本功能**：
1. 检测容器构建工具（优先 nerdctl+buildkit，其次 docker）
2. 自动检测本地代理（常见端口：7890、1080、8080、3128）
3. 构建 3 个镜像：
   - `ai-factory-server:latest`（~87MB）
   - `coding-agent-sandbox:latest`（~622MB）
   - `ai-factory/agent-sandbox-controller:latest`（~16MB）
4. 导出镜像为 tar 文件
5. 打包 Helm chart
6. 复制部署脚本和 CRD 安装脚本

**输出目录结构**：

```
dist/
├── ai-factory-server.tar           # Server 镜像
├── coding-agent-sandbox.tar        # Sandbox 镜像
├── agent-sandbox-controller.tar    # 控制器镜像
├── ai-factory-chart/               # Helm chart
├── components/                     # CRD 安装脚本
├── deploy-remote.sh                # 部署脚本
└── ai-factory.env                  # 配置模板
```

### 3. 传输到目标机器

```bash
# 方式 1: scp
scp -r dist/ user@your-vm:/path/to/

# 方式 2: rsync（推荐，支持断点续传）
rsync -avz --progress dist/ user@your-vm:/path/to/dist/

# 方式 3: 压缩后传输
tar czf dist.tar.gz dist/
scp dist.tar.gz user@your-vm:/path/to/
ssh user@your-vm "cd /path/to && tar xzf dist.tar.gz"
```

---

## 部署流程

### 1. 准备配置文件（可选）

在部署前，可以预先填写配置文件，避免交互式输入：

```bash
cd dist/

# 复制模板
cp ai-factory.env ai-factory.env.local

# 编辑配置
vim ai-factory.env.local
```

**配置项说明**：

```bash
# 必填
GITHUB_TOKEN=ghp_xxxxxxxxxxxx        # GitHub Personal Access Token
WEBHOOK_SECRET=your-webhook-secret   # Webhook 签名密钥
OPENAI_API_KEY=sk-xxxxxxxxxxxx       # OpenAI API Key

# 可选
OPENAI_BASE_URL=https://api.openai.com/v1  # API 端点
OPENAI_MODEL=gpt-4.1                        # 模型名称
CODEX_API_KEY=                              # Codex CLI 密钥
GITLAB_TOKEN=                               # GitLab Token
```

**获取 GitHub Token**：
1. 访问 https://github.com/settings/tokens
2. 点击 "Generate new token (classic)"
3. 勾选 `repo` 权限
4. 复制生成的 token

### 2. 运行部署脚本

```bash
# 方式 1: 使用预配置文件
cp ai-factory.env.local ai-factory.env
./deploy-remote.sh

# 方式 2: 交互式输入
./deploy-remote.sh
```

**部署脚本执行步骤**：

1. **检查环境**：验证 kubectl 和集群连接
2. **导入镜像**：根据容器运行时（docker/containerd）导入镜像
3. **kind 集群支持**：如果检测到 kind，自动加载镜像到 kind
4. **安装 CRD**：
   - FactoryTask CRD
   - agent-sandbox CRD（SandboxClaim、SandboxTemplate、SandboxWarmPool）
5. **收集凭证**：从 ai-factory.env 或交互式输入
6. **安装 Helm chart**：部署 ai-factory-server 和相关资源
7. **等待部署**：自动等待 Deployment 就绪

**预计时间**：5-15 分钟（取决于网络和镜像大小）

### 3. 部署完成后的输出

```
=== 部署完成 ===

Pod 状态:
NAME                                  READY   STATUS    RESTARTS   AGE
ai-factory-server-xxxxxxxxxx-xxxxx    1/1     Running   0          30s

Warm Pool 状态:
NAME                    READY   AGE
coding-agent-warm-pool  2/2     30s

下一步:
  1. 暴露服务:
     kubectl port-forward --address=0.0.0.0 svc/ai-factory-server 8080:80 -n ai-factory

  2. 配置 GitHub webhook:
     http://your-vm-ip:8080/webhook/github

  3. 给 issue 打标签触发:
     ai-factory-run   → 完整 agent 流程
     ai-factory-smoke → sandbox 环境检查
```

---

## 配置说明

### 凭证配置

| 配置项 | 说明 | 存储位置 |
|--------|------|---------|
| GITHUB_TOKEN | GitHub Personal Access Token | Secret |
| WEBHOOK_SECRET | Webhook 签名密钥 | Secret |
| OPENAI_API_KEY | OpenAI API Key | Secret |
| CODEX_API_KEY | Codex CLI 密钥（可选） | Secret |
| GITLAB_TOKEN | GitLab Token（可选） | Secret |

### 模型配置

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| OPENAI_BASE_URL | https://api.openai.com/v1 | API 端点 |
| OPENAI_MODEL | gpt-4.1 | 模型名称 |
| OPENAI_TEMPERATURE | 1 | 温度参数 |
| OPENAI_MAX_TOKENS | 48000 | 最大 token 数 |
| OPENAI_MAX_TOOL_ROUNDS | 40 | Agent 执行轮次限制 |
| OPENAI_MAX_FINAL_SCRIPT_ROUNDS | 5 | 最终脚本轮次 |
| OPENAI_MAX_REPAIR_ROUNDS | 3 | 修复轮次 |
| OPENAI_TOTAL_TIMEOUT_SECONDS | 1800 | 总超时（秒） |
| OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS | 180 | 探索请求超时 |
| OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS | 90 | 最终请求超时 |
| OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS | 90 | 修复请求超时 |

### 网络配置

| 配置项 | 说明 |
|--------|------|
| AI_FACTORY_GIT_PROXY | Git 克隆代理（可选） |

---

## 验证部署

### 1. 检查 Pod 状态

```bash
kubectl get pods -n ai-factory
```

预期输出：
```
NAME                                  READY   STATUS    RESTARTS   AGE
ai-factory-server-xxxxxxxxxx-xxxxx    1/1     Running   0          5m
```

### 2. 检查 Warm Pool

```bash
kubectl get sandboxwarmpool -n ai-factory
```

预期输出：
```
NAME                    READY   AGE
coding-agent-warm-pool  2/2     5m
```

### 3. 检查 CRD

```bash
kubectl get crd | grep -E "factorytask|sandbox"
```

预期输出：
```
factorytasks.factory.ai-factory.dev         2026-08-03
sandboxclaims.sandbox.ai-factory.dev        2026-08-03
sandboxtemplates.sandbox.ai-factory.dev     2026-08-03
sandboxwarmpools.sandbox.ai-factory.dev     2026-08-03
```

### 4. 查看服务日志

```bash
kubectl logs -f deployment/ai-factory-server -n ai-factory --tail=50
```

### 5. 健康检查

```bash
# 端口转发
kubectl port-forward svc/ai-factory-server 8080:80 -n ai-factory &

# 健康检查
curl http://localhost:8080/healthz
```

预期输出：`ok`

### 6. 测试 Webhook

```bash
# 发送测试请求
curl -X POST http://localhost:8080/webhook/github \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: ping" \
  -d '{"zen": "Design for failure."}'
```

预期输出：
```json
{"message": "pong"}
```

---

## 配置 GitHub Webhook

### 1. 获取服务地址

**方式 1: 端口转发（本地测试）**

```bash
kubectl port-forward --address=0.0.0.0 svc/ai-factory-server 8080:80 -n ai-factory
```

服务地址：`http://your-ip:8080`

**方式 2: NodePort（生产环境）**

默认 NodePort: `32519`

服务地址：`http://your-node-ip:32519`

**方式 3: Ingress（生产环境推荐）**

配置 Ingress 后，使用域名访问。

### 2. 配置 Webhook

1. 进入 GitHub 仓库 → Settings → Webhooks → Add webhook
2. 填写配置：

| 配置项 | 值 |
|--------|-----|
| Payload URL | `http://your-service/webhook/github` |
| Content type | `application/json` |
| Secret | 部署时设置的 `WEBHOOK_SECRET` |
| SSL verification | 根据环境选择 |
| Events | 选择 "Let me select individual events" → 勾选 "Issues" |

3. 点击 "Add webhook" 保存

### 3. 验证 Webhook

1. 在 GitHub 仓库创建一个 Issue
2. 给 Issue 添加标签 `ai-factory-run`
3. 观察：
   - Issue 标签变化：`ai-factory-run` → `ai-factory-running` → `ai-factory-done`
   - 服务日志：`kubectl logs -f deployment/ai-factory-server -n ai-factory`
   - FactoryTask：`kubectl get factorytasks -n ai-factory`

---

## 热更新配置

### 修改配置文件

```bash
# 编辑配置文件
vim scripts/ai-factory.env
```

### 同步到集群

```bash
./scripts/update-config.sh
```

**脚本功能**：
1. 读取 ai-factory.env 文件
2. 分类：敏感凭证 → Secret，普通配置 → ConfigMap
3. 更新 K8s 资源
4. 提示：~30s 后自动生效

### 验证热更新

```bash
# 触发新 issue，然后检查 SandboxClaim 的环境变量
kubectl get sandboxclaims -n ai-factory \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].spec.template.spec.containers[0].env}' | \
    python3 -m json.tool | grep -A1 OPENAI_MODEL
```

**配置读取优先级**：
1. `/etc/ai-factory/secret/<name>` ← K8s Secret volume
2. `/etc/ai-factory/config/<name>` ← K8s ConfigMap volume
3. `os.Getenv(name)` ← 本地开发兼容

---

## 卸载服务

```bash
# 卸载 Helm release(保留 namespace 和 CRD)
helm uninstall ai-factory -n ai-factory

# 删除 namespace(移除全部资源)
kubectl delete namespace ai-factory

# 如需清理 FactoryTask CRD
kubectl delete crd factorytasks.factory.ai.gke.io
```

> agent-sandbox 的 CRD(SandboxClaim / SandboxTemplate / SandboxWarmPool,`agents.x-k8s.io` 组)
> 由 `components/agent-sandbox/install` 安装,如需一并清除请按该脚本对应的 agent-sandbox 文档操作。

---

## 下一步

- 遇到问题？查看 [故障排查指南](self-hosted-troubleshooting.md)
- 需要更新或回滚？查看 [更新与回滚指南](self-hosted-update-rollback.md)
- 了解设计细节？查看 [设计文档](self-hosted-failed-retry-design.md)
