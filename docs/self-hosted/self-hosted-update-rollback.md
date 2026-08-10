# ai-factory 自托管服务 - 更新与回滚指南

> 本文档介绍如何更新 ai-factory 版本以及在出现问题时回滚。

## 目录

- [更新流程](#更新流程)
- [回滚流程](#回滚流程)
- [配置更新](#配置更新)
- [CRD 更新](#crd-更新)
- [镜像更新](#镜像更新)

---

## 更新流程

### 完整更新流程

```
代码修改 → 打包 → 传输 → 运行升级脚本 → 验证
```

### 方式 1: 使用升级脚本（`upgrade.sh`，推荐）

> `scripts/upgrade.sh` 封装了完整的升级步骤：导入镜像、加载配置、升级 Helm chart、
> 更新 agent-sandbox 控制器、清理旧 SandboxClaim、重建 warm pool。

#### 1. 打包新版本

```bash
cd ai-factory

# 拉取最新代码
git pull

# 运行打包脚本（构建镜像并导出到 dist/）
./scripts/package.sh
```

#### 2. 传输到目标机器

```bash
# 方式 1: rsync（推荐）
rsync -avz --progress dist/ user@your-vm:/path/to/ai-factory/

# 方式 2: 压缩后传输
tar czf dist.tar.gz dist/
scp dist.tar.gz user@your-vm:/path/to/
ssh user@your-vm "cd /path/to && tar xzf dist.tar.gz"
```

> **注意**：`upgrade.sh` 默认从仓库根目录的 `dist/` 读取镜像，并直接使用源码目录下的
> `charts/ai-factory/` 作为 chart。因此目标机器上需要**完整的仓库源码**（含 `dist/`），
> 而不只是 `dist/` 目录本身。

#### 3. 运行升级脚本

在目标机器上，从仓库根目录执行：

```bash
cd /path/to/ai-factory
./scripts/upgrade.sh
```

脚本会自动完成：
1. 导入 `dist/` 中的 3 个镜像（server、coding-agent-sandbox、agent-sandbox-controller）
2. 加载配置：优先读取 `scripts/ai-factory.env`，否则从 K8s secret 读取凭证
3. `helm upgrade --install` 升级 chart（保留凭证和模型配置）
4. 等待 server 就绪
5. 更新 agent-sandbox 控制器镜像
6. 删除旧的 SandboxClaim（让新控制器重新创建，正确注入环境变量）
7. 重建 sandbox warm pool pods

#### 4. 验证更新

```bash
# 检查 Pod 状态
kubectl get pods -n ai-factory

# 查看更新历史
helm history ai-factory -n ai-factory

# 检查镜像版本
kubectl get deployment ai-factory-server -n ai-factory \
    -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 方式 2: 手动逐步升级

如果需要细粒度控制，也可以手动执行每个步骤：

#### 1. 打包新版本

```bash
cd ai-factory
git pull
./scripts/package.sh
```

#### 2. 导入新镜像

```bash
cd dist/

# 检测容器运行时并导入
./deploy-remote.sh
```

或者手动导入：

```bash
# Docker
docker load < ai-factory-server.tar
docker load < coding-agent-sandbox.tar

# containerd (nerdctl)
nerdctl load < ai-factory-server.tar
nerdctl load < coding-agent-sandbox.tar

# kind 集群
kind load docker-image ai-factory-server:latest coding-agent-sandbox:latest
```

#### 3. 更新 Helm Release

```bash
# 使用新 chart 更新
helm upgrade ai-factory ai-factory-chart/ \
    --namespace ai-factory \
    --reuse-values \
    --set github.token="${GITHUB_TOKEN}" \
    --set webhook.secret="${WEBHOOK_SECRET}" \
    --set openai.apiKey="${OPENAI_API_KEY}"
```

**参数说明**：
- `--reuse-values`：保留现有配置，只更新 chart 变更部分
- 如果需要重置所有配置，去掉 `--reuse-values`

#### 4. 等待更新完成

```bash
kubectl rollout status deployment/ai-factory-server -n ai-factory --timeout=120s
```

#### 5. 验证更新

```bash
# 检查 Pod 状态
kubectl get pods -n ai-factory

# 查看更新历史
helm history ai-factory -n ai-factory

# 检查镜像版本
kubectl get deployment ai-factory-server -n ai-factory \
    -o jsonpath='{.spec.template.spec.containers[0].image}'
```

---

## 回滚流程

### 方式 1: Helm 回滚（推荐）

```bash
# 查看更新历史
helm history ai-factory -n ai-factory
```

输出示例：
```
REVISION    UPDATED                     STATUS      CHART               APP VERSION     DESCRIPTION
1           Mon Aug  3 10:00:00 2026    superseded  ai-factory-0.1.0    0.1.0           Install complete
2           Mon Aug  3 14:00:00 2026    deployed    ai-factory-0.2.0    0.2.0           Upgrade complete
```

```bash
# 回滚到上一个版本
helm rollback ai-factory -n ai-factory

# 回滚到指定版本（例如版本 1）
helm rollback ai-factory 1 -n ai-factory
```

### 方式 2: 重新部署旧版本

```bash
# 1. 恢复旧代码
git checkout <old-tag-or-commit>

# 2. 重新打包
./scripts/package.sh

# 3. 传输并运行升级脚本（自动导入镜像 + 升级 chart）
rsync -avz --progress dist/ user@your-vm:/path/to/ai-factory/
ssh user@your-vm "cd /path/to/ai-factory && ./scripts/upgrade.sh"
```

### 方式 3: 仅回滚镜像

如果只有镜像变更，可以快速回滚：

```bash
# 1. 导入旧镜像
docker load < ai-factory-server-old.tar

# 2. 重启 Deployment
kubectl rollout restart deployment/ai-factory-server -n ai-factory
```

### 回滚后验证

```bash
# 检查 Pod 状态
kubectl get pods -n ai-factory

# 查看日志
kubectl logs -f deployment/ai-factory-server -n ai-factory --tail=50

# 测试功能
kubectl get factorytasks -n ai-factory
```

---

## 配置更新

### 热更新（推荐）

配置更新无需重新部署，使用热更新机制：

```bash
# 1. 编辑配置
vim scripts/ai-factory.env

# 2. 同步到集群
./scripts/update-config.sh
```

**热更新的配置项**：

| 类型 | 配置项 | 存储位置 |
|------|--------|---------|
| 敏感凭证 | GITHUB_TOKEN, WEBHOOK_SECRET, OPENAI_API_KEY, CODEX_API_KEY, GITLAB_TOKEN | Secret |
| 模型配置 | OPENAI_BASE_URL, OPENAI_MODEL, OPENAI_TEMPERATURE, OPENAI_MAX_TOKENS 等 | ConfigMap |
| 网络配置 | AI_FACTORY_GIT_PROXY | ConfigMap |

**生效时间**：~30 秒（K8s 自动同步文件到 Pod）

### 冷更新

如果热更新不生效，可以重启 Pod：

```bash
kubectl rollout restart deployment/ai-factory-server -n ai-factory
```

### 验证配置更新

```bash
# 检查 Secret
kubectl get secret ai-factory-credentials -n ai-factory -o yaml

# 检查 ConfigMap
kubectl get configmap ai-factory-config -n ai-factory -o yaml

# 检查 Pod 内的配置文件
kubectl exec -it deployment/ai-factory-server -n ai-factory -- \
    cat /etc/ai-factory/secret/OPENAI_API_KEY

# 检查 SandboxClaim 的环境变量（验证热更新）
kubectl get sandboxclaims -n ai-factory \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].spec.template.spec.containers[0].env}' | \
    python3 -m json.tool
```

---

## CRD 更新

### 更新 FactoryTask CRD

```bash
kubectl apply -f components/factory-task/crd.yaml
```

### 更新 agent-sandbox CRD

```bash
# 如果有预打包的 CRD
kubectl apply -f components/agent-sandbox/crd.yaml

# 或者从仓库安装
components/agent-sandbox/install
```

### 验证 CRD 更新

```bash
# 检查 CRD 版本
kubectl get crd factorytasks.factory.ai.gke.io -o yaml | grep -A5 "versions:"

# 检查所有相关 CRD
kubectl get crd | grep -E "factorytask|sandbox"
```

---

## 镜像更新

### 更新单个镜像

```bash
# 1. 构建新镜像
docker build -t ai-factory-server:v0.2.0 -f Dockerfile.server .

# 2. 导出
docker save ai-factory-server:v0.2.0 > ai-factory-server-v0.2.0.tar

# 3. 传输到目标机器
scp ai-factory-server-v0.2.0.tar user@your-vm:/path/

# 4. 导入
ssh user@your-vm "docker load < /path/ai-factory-server-v0.2.0.tar"

# 5. 更新 Deployment
kubectl set image deployment/ai-factory-server \
    ai-factory-server=ai-factory-server:v0.2.0 \
    -n ai-factory

# 6. 等待更新
kubectl rollout status deployment/ai-factory-server -n ai-factory
```

### 更新所有镜像

```bash
# 使用打包脚本
./scripts/package.sh

# 传输并运行升级脚本（自动导入全部镜像 + 升级 chart + 重建 warm pool）
rsync -avz --progress dist/ user@your-vm:/path/to/ai-factory/
ssh user@your-vm "cd /path/to/ai-factory && ./scripts/upgrade.sh"
```

### 镜像版本管理

**建议的版本策略**：

```bash
# 语义化版本
ai-factory-server:v1.0.0
ai-factory-server:v1.1.0
ai-factory-server:v1.1.1

# Git commit hash
ai-factory-server:abc1234

# 日期标签
ai-factory-server:20260803
```

**查看当前镜像版本**：

```bash
kubectl get deployment ai-factory-server -n ai-factory \
    -o jsonpath='{.spec.template.spec.containers[0].image}'
```

---

## 最佳实践

### 更新前检查清单

- [ ] 备份当前配置：`kubectl get secret,configmap -n ai-factory -o yaml > backup.yaml`
- [ ] 记录当前版本：`helm history ai-factory -n ai-factory`
- [ ] 测试环境验证（如果有）
- [ ] 选择低峰期更新
- [ ] 通知相关人员

### 回滚策略

1. **快速回滚**：使用 `helm rollback`（< 1 分钟）
2. **完整回滚**：恢复旧代码 + 重新打包（5-15 分钟）
3. **部分回滚**：仅回滚特定组件（镜像/配置）

### 监控更新

```bash
# 实时监控 Pod 状态
watch kubectl get pods -n ai-factory

# 查看更新事件
kubectl get events -n ai-factory --sort-by='.lastTimestamp'

# 查看 Deployment 状态
kubectl describe deployment ai-factory-server -n ai-factory
```

---

## 常见问题

### 更新后 Pod 无法启动

```bash
# 查看 Pod 事件
kubectl describe pod <pod-name> -n ai-factory

# 查看日志
kubectl logs <pod-name> -n ai-factory

# 常见原因：
# - 镜像拉取失败
# - 资源不足
# - 配置错误
```

### 回滚后配置不一致

```bash
# 重新应用配置
./scripts/update-config.sh

# 或手动更新
kubectl apply -f backup.yaml
```

### CRD 更新失败

```bash
# 检查 CRD 状态
kubectl get crd <crd-name> -o yaml

# 强制删除并重建
kubectl delete crd <crd-name>
kubectl apply -f <crd-file>
```

---

## 下一步

- 遇到问题？查看 [故障排查指南](self-hosted-troubleshooting.md)
- 了解部署细节？查看 [详细部署指南](self-hosted-deployment-guide.md)
