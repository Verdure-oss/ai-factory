# ai-factory 自托管服务 - 故障排查指南

> 本文档提供常见问题的排查方法和解决方案。

## 目录

- [部署问题](#部署问题)
- [运行时问题](#运行时问题)
- [Webhook 问题](#webhook-问题)
- [Agent 执行问题](#agent-执行问题)
- [配置问题](#配置问题)
- [性能问题](#性能问题)
- [日志分析](#日志分析)

---

## 部署问题

### 问题：打包脚本失败

**错误信息**：
```
错误: 未找到 nerdctl+buildkit 或 docker
```

**解决方案**：
```bash
# 安装 Docker
curl -fsSL https://get.docker.com | sh

# 或安装 nerdctl + buildkit
# nerdctl: https://github.com/containerd/nerdctl/releases
# buildkit: https://github.com/moby/buildkit/releases
```

**错误信息**：
```
无法克隆 agent-sandbox 仓库（可能需要网络代理）
```

**解决方案**：
```bash
# 设置代理
export http_proxy=http://your-proxy:port
export https_proxy=http://your-proxy:port

# 或使用本地源
export AGENT_SANDBOX_SRC=/path/to/agent-sandbox
./scripts/package.sh
```

### 问题：镜像导入失败

**错误信息**：
```
Error: No such image
```

**解决方案**：
```bash
# 检查镜像是否构建成功
docker images | grep ai-factory

# 重新构建
./scripts/package.sh

# 检查 tar 文件
ls -lh dist/*.tar
```

**错误信息**：
```
Error: failed to resolve reference
```

**解决方案**：
```bash
# kind 集群需要手动加载
kind load docker-image ai-factory-server:latest coding-agent-sandbox:latest

# 检查 kind 集群
kind get clusters
```

### 问题：CRD 安装失败

**错误信息**：
```
error: unable to recognize "crd.yaml": no matches for kind "CustomResourceDefinition"
```

**解决方案**：
```bash
# 检查 CRD 文件
kubectl apply --dry-run=client -f components/factory-task/crd.yaml

# 检查集群权限
kubectl auth can-i create customresourcedefinitions

# 使用 admin 权限
kubectl apply -f components/factory-task/crd.yaml --as=system:admin
```

### 问题：Helm 安装失败

**错误信息**：
```
Error: Kubernetes cluster unreachable
```

**解决方案**：
```bash
# 检查 kubectl 连接
kubectl cluster-info

# 检查 kubeconfig
echo $KUBECONFIG
cat ~/.kube/config

# 重新配置
kubectl config use-context <context-name>
```

**错误信息**：
```
Error: failed to create resource: secrets "ai-factory-credentials" already exists
```

**解决方案**：
```bash
# 删除旧的 secret
kubectl delete secret ai-factory-credentials -n ai-factory

# 或使用 --force
helm upgrade --install ai-factory ai-factory-chart/ \
    --namespace ai-factory \
    --create-namespace \
    --force
```

---

## 运行时问题

### 问题：Pod 无法启动

**检查步骤**：
```bash
# 1. 查看 Pod 状态
kubectl get pods -n ai-factory

# 2. 查看 Pod 事件
kubectl describe pod <pod-name> -n ai-factory

# 3. 查看日志
kubectl logs <pod-name> -n ai-factory

# 4. 检查资源
kubectl top nodes
kubectl top pods -n ai-factory
```

**常见原因**：

| 现象 | 原因 | 解决方案 |
|------|------|---------|
| `ImagePullBackOff` | 镜像不存在或拉取失败 | 导入镜像或检查镜像名 |
| `CrashLoopBackOff` | 应用启动失败 | 查看日志修复错误 |
| `Pending` | 资源不足 | 增加节点或调整资源限制 |
| `OOMKilled` | 内存不足 | 增加内存限制 |

### 问题：Warm Pool 为空

**检查步骤**：
```bash
# 查看 SandboxWarmPool
kubectl get sandboxwarmpool -n ai-factory

# 查看 SandboxTemplate
kubectl get sandboxtemplate -n ai-factory

# 查看 SandboxClaim
kubectl get sandboxclaims -n ai-factory

# 查看 Pod
kubectl get pods -n ai-factory
```

**解决方案**：
```bash
# 检查 SandboxTemplate 配置
kubectl get sandboxtemplate go-dev -n ai-factory -o yaml

# 检查控制器日志
kubectl logs -l app=agent-sandbox-controller -n ai-factory

# 手动创建 SandboxClaim 测试
kubectl apply -f - <<EOF
apiVersion: sandbox.ai-factory.dev/v1alpha1
kind: SandboxClaim
metadata:
  name: test-claim
  namespace: ai-factory
spec:
  templateRef: go-dev
EOF
```

### 问题：FactoryTask 卡在 Pending

**检查步骤**：
```bash
# 查看 FactoryTask 状态
kubectl get factorytasks -n ai-factory
kubectl get factorytask <task-name> -n ai-factory -o yaml

# 查看 SandboxClaim
kubectl get sandboxclaims -n ai-factory
```

**解决方案**：
```bash
# 检查 SandboxClaim 是否创建
kubectl get sandboxclaim <claim-name> -n ai-factory

# 如果没有 SandboxClaim，检查控制器日志
kubectl logs deployment/ai-factory-server -n ai-factory

# 如果 SandboxClaim 存在但未 Ready，检查事件
kubectl describe sandboxclaim <claim-name> -n ai-factory
```

---

## Webhook 问题

### 问题：Webhook 请求失败

**检查步骤**：
```bash
# 1. 测试健康检查
curl http://your-service:port/healthz

# 2. 测试 ping
curl -X POST http://your-service:port/webhook/github \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: ping" \
    -d '{"zen": "test"}'

# 3. 查看服务日志
kubectl logs -f deployment/ai-factory-server -n ai-factory
```

**常见错误**：

| 错误码 | 原因 | 解决方案 |
|--------|------|---------|
| 401 | Secret 不匹配 | 检查 WEBHOOK_SECRET 配置 |
| 404 | 路由错误 | 检查 Payload URL 配置 |
| 500 | 服务内部错误 | 查看服务日志 |

### 问题：Webhook 触发但无反应

**检查步骤**：
```bash
# 1. 检查 GitHub Webhook 配置
# - Payload URL: http://your-service/webhook/github
# - Content type: application/json
# - Events: Issues

# 2. 检查服务日志
kubectl logs -f deployment/ai-factory-server -n ai-factory

# 3. 检查 FactoryTask
kubectl get factorytasks -n ai-factory
```

**解决方案**：
```bash
# 确保 Issue 有正确标签
# - ai-factory-run: 完整 agent 流程
# - ai-factory-smoke: sandbox 环境检查

# 检查 GitHub Token 权限
# 需要 repo 权限
```

### 问题：并发 Webhook 导致重复执行

**现象**：
- Issue 评论被发送两次
- 标签被移除两次
- FactoryTask 被创建两次

**原因**：
- GitHub Webhook 重试机制
- 网络抖动导致重复投递

**解决方案**：
系统已内置处理：
- 捕获 `AlreadyExists` 错误，视为成功
- 使用 `TriggerLabel` 过滤，避免连锁触发

**验证**：
```bash
# 查看日志中的并发处理
kubectl logs deployment/ai-factory-server -n ai-factory | grep -i "concurrent"
```

---

## Agent 执行问题

### 问题：Agent 执行超时

**检查步骤**：
```bash
# 查看 FactoryTask 状态
kubectl get factorytask <task-name> -n ai-factory -o yaml

# 查看 SandboxClaim
kubectl get sandboxclaim <claim-name> -n ai-factory -o yaml

# 查看 Sandbox Pod 日志
kubectl logs <sandbox-pod-name> -n ai-factory
```

**解决方案**：
```bash
# 增加超时配置
vim scripts/ai-factory.env

# 修改以下配置：
OPENAI_TOTAL_TIMEOUT_SECONDS=3600  # 增加到 1 小时
OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS=300
OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS=180
OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS=180

# 同步配置
./scripts/update-config.sh
```

### 问题：Agent 执行失败

**检查步骤**：
```bash
# 查看 FactoryTask 状态
kubectl get factorytask <task-name> -n ai-factory -o yaml

# 查看 status.message
kubectl get factorytask <task-name> -n ai-factory \
    -o jsonpath='{.status.message}'

# 查看 Sandbox Pod 日志
kubectl logs <sandbox-pod-name> -n ai-factory --tail=100
```

**常见失败原因**：

| 原因 | 现象 | 解决方案 |
|------|------|---------|
| API Key 无效 | `401 Unauthorized` | 检查 OPENAI_API_KEY |
| API 端点错误 | `connection refused` | 检查 OPENAI_BASE_URL |
| 模型不存在 | `model not found` | 检查 OPENAI_MODEL |
| Token 超限 | `max tokens exceeded` | 增加 OPENAI_MAX_TOKENS |
| 网络问题 | `timeout` | 配置代理或增加超时 |

### 问题：Sandbox Pod 崩溃

**检查步骤**：
```bash
# 查看 Pod 状态
kubectl get pods -n ai-factory

# 查看 Pod 事件
kubectl describe pod <pod-name> -n ai-factory

# 查看容器日志
kubectl logs <pod-name> -n ai-factory -c dev

# 查看资源使用
kubectl top pod <pod-name> -n ai-factory
```

**解决方案**：
```bash
# 增加资源限制
kubectl patch sandboxtemplate go-dev -n ai-factory --type=merge \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"dev","resources":{"limits":{"memory":"4Gi","cpu":"2"}}}]}}}}'

# 或修改 Helm values
vim charts/ai-factory/values.yaml
```

---

## 配置问题

### 问题：配置不生效

**检查步骤**：
```bash
# 1. 检查 Secret
kubectl get secret ai-factory-credentials -n ai-factory -o yaml

# 2. 检查 ConfigMap
kubectl get configmap ai-factory-config -n ai-factory -o yaml

# 3. 检查 Pod 内的文件
kubectl exec -it deployment/ai-factory-server -n ai-factory -- \
    ls -la /etc/ai-factory/secret/
kubectl exec -it deployment/ai-factory-server -n ai-factory -- \
    ls -la /etc/ai-factory/config/

# 4. 检查 SandboxClaim 的环境变量
kubectl get sandboxclaims -n ai-factory \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].spec.template.spec.containers[0].env}' | \
    python3 -m json.tool
```

**解决方案**：
```bash
# 重新同步配置
./scripts/update-config.sh

# 等待 30 秒
sleep 30

# 重启 Pod（如果热更新不生效）
kubectl rollout restart deployment/ai-factory-server -n ai-factory
```

### 问题：配置文件权限错误

**错误信息**：
```
permission denied: /etc/ai-factory/secret/OPENAI_API_KEY
```

**解决方案**：
```bash
# 检查 Pod 的 securityContext
kubectl get deployment ai-factory-server -n ai-factory -o yaml | grep -A10 securityContext

# 检查 Volume 挂载
kubectl get deployment ai-factory-server -n ai-factory -o yaml | grep -A20 volumes

# 如果需要，修改 Helm chart 的 securityContext
```

---

## 性能问题

### 问题：任务执行慢

**检查步骤**：
```bash
# 1. 检查 API 响应时间
time curl -X POST https://api.openai.com/v1/chat/completions \
    -H "Authorization: Bearer ${OPENAI_API_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4.1","messages":[{"role":"user","content":"test"}]}'

# 2. 检查网络延迟
ping api.openai.com
traceroute api.openai.com

# 3. 检查资源使用
kubectl top nodes
kubectl top pods -n ai-factory
```

**解决方案**：
```bash
# 配置代理
vim scripts/ai-factory.env

# 添加：
AI_FACTORY_GIT_PROXY=http://your-proxy:port

# 同步配置
./scripts/update-config.sh
```

### 问题：Warm Pool 耗尽

**现象**：
- Issue 标签显示 `ai-factory-waiting`
- 新任务排队等待

**检查步骤**：
```bash
# 查看 Warm Pool 状态
kubectl get sandboxwarmpool -n ai-factory

# 查看 SandboxClaim
kubectl get sandboxclaims -n ai-factory

# 查看 Pod 使用情况
kubectl get pods -n ai-factory | grep sandbox
```

**解决方案**：
```bash
# 增加 Warm Pool 大小
kubectl patch sandboxwarmpool coding-agent-warm-pool -n ai-factory \
    --type=merge -p '{"spec":{"replicas":4}}'

# 或修改 Helm values
vim charts/ai-factory/values.yaml
# 修改 sandbox.warmPoolReplicas: 4

# 重新部署
helm upgrade ai-factory ai-factory-chart/ --namespace ai-factory --reuse-values
```

---

## 日志分析

### 查看服务日志

```bash
# 实时查看
kubectl logs -f deployment/ai-factory-server -n ai-factory

# 查看最近 100 行
kubectl logs deployment/ai-factory-server -n ai-factory --tail=100

# 查看指定时间之后的日志
kubectl logs deployment/ai-factory-server -n ai-factory --since=1h

# 查看所有 Pod 的日志
kubectl logs -l app=ai-factory-server -n ai-factory --all-containers
```

### 常见日志模式

**正常流程**：
```
webhook: github issue 123 -> FactoryTask (exists=false)
controller: processing factorytask github-owner-repo-123
controller: ClaimCreated -> waiting for sandbox
controller: SandboxReady -> starting agent
controller: agent completed successfully
controller: creating PR
```

**错误模式**：
```
# API Key 错误
error: 401 Unauthorized

# 网络错误
error: connection refused
error: timeout

# 配置错误
error: OPENAI_API_KEY not set
```

### 日志聚合

```bash
# 使用 stern 聚合多个 Pod 日志
stern -n ai-factory .

# 过滤特定关键词
stern -n ai-factory . | grep -i error
stern -n ai-factory . | grep -i "factorytask"

# 保存日志到文件
kubectl logs deployment/ai-factory-server -n ai-factory > server.log
```

---

## 诊断命令速查

```bash
# 集群状态
kubectl cluster-info
kubectl get nodes -o wide

# ai-factory 资源
kubectl get all -n ai-factory
kubectl get factorytasks -n ai-factory
kubectl get sandboxclaims -n ai-factory
kubectl get sandboxwarmpool -n ai-factory
kubectl get sandboxtemplate -n ai-factory

# 详细信息
kubectl describe deployment ai-factory-server -n ai-factory
kubectl describe pod <pod-name> -n ai-factory

# 日志
kubectl logs -f deployment/ai-factory-server -n ai-factory
kubectl logs <pod-name> -n ai-factory

# 资源使用
kubectl top nodes
kubectl top pods -n ai-factory

# 配置
kubectl get secret ai-factory-credentials -n ai-factory -o yaml
kubectl get configmap ai-factory-config -n ai-factory -o yaml

# 网络测试
curl http://your-service:port/healthz
curl -X POST http://your-service:port/webhook/github \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: ping" \
    -d '{"zen": "test"}'
```

---

## 获取帮助

如果以上方法都无法解决问题：

1. **收集信息**：
   ```bash
   # 生成诊断报告
   kubectl cluster-info dump --namespaces=ai-factory --output-directory=/tmp/ai-factory-dump
   ```

2. **查看文档**：
   - [详细部署指南](self-hosted-deployment-guide.md)
   - [更新与回滚指南](self-hosted-update-rollback.md)
   - [设计文档](self-hosted-failed-retry-design.md)

3. **提交 Issue**：
   - 访问 https://github.com/verdure-oss/ai-factory/issues
   - 附上诊断报告和错误日志

---

## 下一步

- 了解部署细节？查看 [详细部署指南](self-hosted-deployment-guide.md)
- 需要更新或回滚？查看 [更新与回滚指南](self-hosted-update-rollback.md)
- 了解设计细节？查看 [设计文档](self-hosted-failed-retry-design.md)
