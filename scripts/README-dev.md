# 本地开发指南

本指南说明如何在服务器上直接运行和调试 ai-factory 代码，无需打包镜像。

## 快速开始

### 方式 1：手动启动（简单）

```bash
# 启动服务
./scripts/dev.sh
```

修改代码后，按 `Ctrl+C` 停止，再次运行 `./scripts/dev.sh` 重启。

### 方式 2：热重载（推荐）⭐

```bash
# 自动监听文件变更，自动重启
./scripts/watch.sh
```

修改任何 `.go` 文件后，服务会自动重新编译和重启。

### 方式 3：使用 air（需安装）

如果网络允许，可以安装 air 获得更好的开发体验：

```bash
# 安装 air
go install github.com/air-verse/air@latest

# 启动（会自动监听文件变更）
air
```

## 访问服务

启动后，服务监听在 `:32519` 端口：

```bash
# Webhook 地址
http://<服务器IP>:32519/webhook/github

# 健康检查
curl http://localhost:32519/healthz
```

## 与 Helm 部署的对比

| 特性 | 本地开发 | Helm 部署 |
|------|---------|----------|
| 代码逻辑 | ✅ 完全一致 | ✅ 完全一致 |
| K8s API 交互 | ✅ 直接连接 | ✅ 通过 ServiceAccount |
| 公网访问 | ✅ 端口 32519 | ✅ NodePort 32519 |
| 启动速度 | ⚡ 秒级 | 🐢 需要构建镜像 |
| 调试能力 | ✅ 可加断点 | ⚠️ 需要日志 |
| 热重载 | ✅ 自动 | ❌ 手动部署 |

## 切回 Helm 部署

如果需要切回 K8s 部署：

```bash
# 恢复 Service
kubectl apply -f /tmp/ai-factory-svc-backup.yaml

# 恢复 Deployment
kubectl scale deployment/ai-factory-server -n ai-factory --replicas=1
```

## 环境变量

环境变量自动从 K8s Secret 加载，包括：
- `WEBHOOK_SECRET` - Webhook 密钥
- `GITHUB_TOKEN` - GitHub API Token
- `OPENAI_API_KEY` - OpenAI API Key
- `CODEX_API_KEY` - Codex API Key
- 其他 OpenAI 配置参数

## 调试技巧

### 使用 VS Code 远程调试

1. 安装 VS Code Remote-SSH 扩展
2. 连接到服务器
3. 打开项目目录
4. 配置 `.vscode/launch.json`：

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug AI Factory",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/factory/cmd/factory",
      "args": [
        "server",
        "--addr=:32519",
        "--namespace=ai-factory"
      ],
      "env": {
        "WEBHOOK_SECRET": "从 K8s secret 获取",
        "GITHUB_TOKEN": "从 K8s secret 获取",
        "OPENAI_API_KEY": "从 K8s secret 获取"
      }
    }
  ]
}
```

5. 按 F5 启动调试

### 查看日志

```bash
# 直接查看终端输出（开发模式）
# 日志直接打印到 stdout

# 查看 K8s 部署的日志（生产模式）
kubectl logs -f deployment/ai-factory-server -n ai-factory
```

## 常见问题

### Q: 端口 32519 被占用？

A: 确保 K8s Service 已删除：
```bash
kubectl get svc -n ai-factory
# 如果看到 ai-factory-server，执行：
kubectl delete svc ai-factory-server -n ai-factory
```

### Q: 如何测试 GitHub Webhook？

A: 
1. 使用 ngrok 暴露本地端口：
   ```bash
   ngrok http 32519
   ```
2. 在 GitHub 仓库设置 Webhook：
   - Payload URL: `https://xxx.ngrok.io/webhook/github`
   - Content type: `application/json`
   - Secret: 你的 `WEBHOOK_SECRET`
   - Events: Issues

### Q: 代码改了没生效？

A: 
- 如果用 `dev.sh`：需要手动重启
- 如果用 `watch.sh` 或 `air`：检查文件是否在监听范围内

## 目录结构

```
scripts/
├── dev.sh      # 手动启动脚本
├── watch.sh    # 热重载脚本
└── README-dev.md  # 本文档
```

## 下一步

- 修改代码后自动重启，快速迭代
- 使用 IDE 调试功能排查问题
- 测试完成后，切回 Helm 部署进行集成测试
