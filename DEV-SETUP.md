# 开发环境搭建总结

## ✅ 已完成

### 1. 安装 Go
- Go 1.25.4 已安装到 `/usr/local/go`
- PATH 已配置到 `~/.bashrc`
- 验证：`go version` → go1.25.4 linux/amd64

### 2. 配置开发脚本

创建了 3 个脚本：

#### `scripts/dev.sh` - 手动启动
```bash
./scripts/dev.sh
```
- 自动从 K8s Secret 加载环境变量
- 监听端口 32519（与原 Helm 部署一致）
- 直接运行 Go 代码，无需镜像

#### `scripts/watch.sh` - 热重载
```bash
./scripts/watch.sh
```
- 监听 `factory/` 目录的 `.go` 文件变更
- 自动重新编译和重启服务
- 依赖 `inotify-tools`（会自动安装）

#### `.air.toml` - air 配置
- 如果后续安装了 air，可以直接使用 `air` 命令
- 配置了完整的启动参数

### 3. 释放端口

- K8s Deployment 已缩容到 0 副本
- K8s Service 已删除（备份在 `/tmp/ai-factory-svc-backup.yaml`）
- 端口 32519 现在可以被本地开发使用

### 4. 创建文档

- `scripts/README-dev.md` - 完整的开发指南

## 🚀 使用方法

### 方式 1：简单启动（推荐先用这个测试）

```bash
cd /root/ai-factory/ai-factory
./scripts/dev.sh
```

看到类似输出表示成功：
```
📋 加载环境变量...
✅ 环境变量已加载

🚀 启动 ai-factory-server...
   监听地址: :32519
   命名空间: ai-factory
   Webhook: http://172.23.10.3:32519/webhook/github
```

测试健康检查：
```bash
curl http://localhost:32519/healthz
# 应该返回: {"status":"ok"}
```

### 方式 2：热重载开发

```bash
./scripts/watch.sh
```

然后修改任何 `.go` 文件，服务会自动重启。

### 方式 3：使用 air（如果安装成功）

```bash
air
```

## 📝 开发工作流

```
1. 修改代码（任意编辑器）
   ↓
2. 保存文件
   ↓
3. watch.sh 自动检测变更
   ↓
4. 自动重新编译和重启
   ↓
5. 测试功能（webhook、controller 等）
   ↓
6. 重复 1-5
```

## 🔄 切回 Helm 部署

当需要测试完整的 K8s 部署时：

```bash
# 恢复 Service
kubectl apply -f /tmp/ai-factory-svc-backup.yaml

# 恢复 Deployment（使用最新镜像）
kubectl scale deployment/ai-factory-server -n ai-factory --replicas=1

# 查看状态
kubectl get pods -n ai-factory
```

## ⚠️ 注意事项

### 1. 首次编译可能较慢

Go 需要下载依赖和编译，首次运行可能需要几分钟。后续会快很多。

### 2. 环境变量

脚本会自动从 K8s Secret 加载环境变量，确保：
- K8s 集群可访问
- `ai-factory-credentials` Secret 存在

### 3. 端口冲突

如果端口 32519 被占用：
```bash
# 检查谁占用了端口
ss -tlnp | grep 32519

# 如果是 K8s Service，删除它
kubectl delete svc ai-factory-server -n ai-factory
```

### 4. 进程管理

- `dev.sh` 在前台运行，按 `Ctrl+C` 停止
- `watch.sh` 也在前台运行，会持续监听文件变更
- 如需后台运行，可以用 `nohup` 或 `tmux`/`screen`

## 🎯 优势对比

| 操作 | 之前（镜像模式） | 现在（开发模式） |
|------|----------------|----------------|
| 改代码 | 修改代码 | 修改代码 |
| 构建 | docker build (分钟) | 自动（秒级） |
| 打包 | docker save (分钟) | 无需 |
| 传输 | scp tar 文件 | 无需 |
| 加载 | nerdctl load (分钟) | 无需 |
| 部署 | kubectl rollout restart | 自动 |
| **总计** | **10-15 分钟** | **几秒** |

## 📚 相关文档

- `scripts/README-dev.md` - 详细开发指南
- `CLAUDE.md` - 项目概览
- `AGENTS.md` - Agent 工作指令

## 🔧 故障排查

### 问题：编译超时

**解决**：设置 Go 代理加速
```bash
export GOPROXY=https://goproxy.cn,direct
```

### 问题：找不到 go 命令

**解决**：重新加载 PATH
```bash
source ~/.bashrc
# 或
export PATH=$PATH:/usr/local/go/bin
```

### 问题：端口被占用

**解决**：删除 K8s Service
```bash
kubectl delete svc ai-factory-server -n ai-factory
```

### 问题：环境变量加载失败

**解决**：检查 K8s Secret
```bash
kubectl get secret ai-factory-credentials -n ai-factory
```

## ✨ 下一步

1. 运行 `./scripts/dev.sh` 测试
2. 修改一些代码，观察效果
3. 使用 `./scripts/watch.sh` 开启热重载
4. 开始高效开发！
