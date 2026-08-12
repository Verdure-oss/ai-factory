# ai-factory 自托管服务 - 快速开始指南

> 本文档面向开发者，帮助快速部署 ai-factory 自托管服务。

## 前置条件

- Kubernetes 集群（kind、minikube 或云服务）
- kubectl 已配置
- Docker 或 nerdctl+buildkit
- Helm 3.x

## 快速部署（3 步）

### 1. 打包

```bash
# 克隆仓库
git clone https://github.com/verdure-oss/ai-factory.git
cd ai-factory

# 运行打包脚本
./scripts/package.sh
```

打包完成后，`dist/` 目录包含所有部署文件。

### 2. 部署

```bash
cd dist/

# 运行部署脚本（交互式收集凭证）
./deploy-remote.sh
```

脚本会自动完成：
- 导入镜像到集群
- 安装 CRD
- 收集凭证
- 安装 Helm chart
- 等待部署完成

### 3. 验证

```bash
# 检查 Pod 状态
kubectl get pods -n ai-factory

# 检查 Warm Pool
kubectl get sandboxwarmpool -n ai-factory

# 查看服务日志
kubectl logs -f deployment/ai-factory-server -n ai-factory
```

## 下一步

- 配置 GitHub Webhook → [详细部署指南](self-hosted-deployment-guide.md#配置-github-webhook)
- 修改配置 → [热更新配置](self-hosted-deployment-guide.md#热更新配置)
- 向公共仓库提 PR → [Fork PR 工作流](self-hosted-deployment-guide.md#fork-pr-工作流向公共仓库提-pr)
- 遇到问题 → [故障排查指南](self-hosted-troubleshooting.md)

## 常用命令

```bash
# 查看所有 FactoryTask
kubectl get factorytasks -n ai-factory

# 查看 SandboxClaim
kubectl get sandboxclaims -n ai-factory

# 查看服务日志
kubectl logs -f deployment/ai-factory-server -n ai-factory --tail=100

# 端口转发（本地测试）
kubectl port-forward --address=0.0.0.0 svc/ai-factory-server 8080:80 -n ai-factory
```
