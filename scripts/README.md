# 部署脚本说明

## 目录结构

```
scripts/
├── package.sh          # 本地打包脚本
├── deploy-remote.sh    # 远程部署脚本
└── README.md           # 本文件
```

## 使用流程

### Step 1: 本地打包

在你的开发机器上执行：

```bash
# 进入项目根目录
cd ai-factory

# 执行打包脚本
./scripts/package.sh
```

打包完成后，会在 `dist/` 目录生成：

```
dist/
├── ai-factory-server.tar       # server 镜像
├── coding-agent-sandbox.tar    # sandbox 镜像
├── ai-factory-0.1.0.tgz        # Helm chart 包
├── deploy-remote.sh            # 部署脚本
└── components/                 # CRD 安装脚本
    ├── factory-task/
    └── agent-sandbox/
```

### Step 2: 上传到虚拟机

```bash
# 方式 1: scp 整个目录
scp -r dist/ user@your-vm:/path/to/ai-factory-deploy/

# 方式 2: rsync（增量同步）
rsync -avz dist/ user@your-vm:/path/to/ai-factory-deploy/
```

### Step 3: 在虚拟机上部署

```bash
# SSH 到虚拟机
ssh user@your-vm

# 进入部署目录
cd /path/to/ai-factory-deploy

# 执行部署脚本
./deploy-remote.sh
```

部署脚本会：
1. 导入 Docker 镜像
2. 安装 CRD
3. 提示输入凭证（GitHub Token、Webhook Secret、OpenAI API Key）
4. 安装 Helm chart
5. 等待部署完成
6. 显示状态

### Step 4: 配置 Webhook

1. 暴露服务：
   ```bash
   kubectl port-forward svc/ai-factory-server 8080:80 -n ai-factory
   ```

2. 或者使用 NodePort/Ingress 暴露

3. 在 GitHub 仓库配置 Webhook：
   - Payload URL: `http://your-vm-ip:8080/webhook/github`
   - Content type: `application/json`
   - Secret: 你设置的 webhook secret
   - Events: Issues

### Step 5: 测试

给 issue 打标签：
- `ai-factory-run`: 执行 agent 并创建 PR
- `ai-factory-smoke`: 冒烟测试

## 环境变量

部署脚本支持通过环境变量预设凭证：

```bash
export GITHUB_TOKEN=ghp_xxxx
export WEBHOOK_SECRET=your-secret
export OPENAI_API_KEY=sk-xxxx

./deploy-remote.sh
```

## 注意事项

1. 虚拟机需要安装 Docker、kubectl、helm
2. 如果是 kind 集群，脚本会自动加载镜像到 kind
3. 镜像文件较大（~1-2GB），上传需要时间
