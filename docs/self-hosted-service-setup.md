# ai-factory Self-Hosted Service Setup Guide

This guide explains how to deploy and use the ai-factory self-hosted service.

## Overview

The self-hosted service replaces the GitHub Actions workflow with a persistent Kubernetes service that:
- Receives GitHub/GitLab webhooks
- Creates and manages FactoryTask resources
- Executes coding agents in sandbox pods
- Creates pull requests automatically

## Prerequisites

- Kubernetes cluster (kind, GKE, EKS, AKS, etc.)
- kubectl configured to access the cluster
- Docker for building images
- GitHub Personal Access Token
- OpenAI API Key (or compatible API)

## Quick Start

### 1. Build and Deploy

```bash
# Clone the repository
git clone https://github.com/Verdure-oss/ai-factory.git
cd ai-factory

# Run the installation script
./k8s/install.sh
```

The script will:
1. Build the `ai-factory-server` Docker image
2. Install required CRDs (FactoryTask, agent-sandbox)
3. Deploy the service to your cluster

### 2. Configure Credentials

Before deploying, create the credentials secret:

```bash
kubectl create secret generic ai-factory-credentials -n ai-factory \
  --from-literal=GITHUB_TOKEN=ghp_your_github_token \
  --from-literal=WEBHOOK_SECRET=your_webhook_secret \
  --from-literal=OPENAI_API_KEY=sk_your_openai_key
```

Or edit and apply the template:

```bash
# Edit k8s/secret.yaml with your credentials
vim k8s/secret.yaml

# Apply the secret
kubectl apply -f k8s/secret.yaml
```

### 3. Expose the Service

#### Option A: ngrok (for testing)

```bash
# Port-forward the service
kubectl port-forward svc/ai-factory-server 8080:80 -n ai-factory &

# Start ngrok
ngrok http 8080
```

Copy the ngrok URL (e.g., `https://xxxx.ngrok.io`).

#### Option B: Ingress (for production)

1. Edit `k8s/ingress.yaml` to set your domain
2. Apply the ingress:

```bash
kubectl apply -f k8s/ingress.yaml
```

### 4. Configure GitHub Webhook

In your target repository:

1. Go to **Settings** → **Webhooks** → **Add webhook**
2. Configure:
   - **Payload URL**: `https://your-domain/webhook/github`
   - **Content type**: `application/json`
   - **Secret**: Same as `WEBHOOK_SECRET`
   - **Events**: Select "Let me select individual events" → ✅ **Issues**
3. Click **Add webhook**

### 5. Trigger a Task

Add a label to an issue:
- `ai-factory-run`: Execute the coding agent and create a PR
- `ai-factory-smoke`: Smoke test (verify environment, no code changes)

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    GitHub Repository                      │
│  Settings → Webhooks → POST https://your-domain/webhook/github │
└──────────────────────┬──────────────────────────────────┘
                       │
                       │ GitHub Webhook (HTTPS)
                       │ X-Hub-Signature-256 签名验证
                       ▼
┌─────────────────────────────────────────────────────────┐
│              ai-factory Server (K8s Pod)                  │
│                                                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │ Webhook      │  │ Task         │  │ PR           │   │
│  │ Handler      │→ │ Controller   │→ │ Creator      │   │
│  └──────────────┘  └──────────────┘  └──────────────┘   │
└───────────────────────────┼──────────────────────────────┘
                            │
                            │ Create/Manage Sandbox Pod
                            ▼
┌─────────────────────────────────────────────────────────┐
│              Sandbox Pod (Temporary)                      │
│  1. git clone target repository                          │
│  2. Execute coding agent                                 │
│  3. Run validation commands                              │
│  4. git commit + push                                    │
└─────────────────────────────────────────────────────────┘
```

## Monitoring

### Check Service Status

```bash
# View pods
kubectl get pods -n ai-factory

# View logs
kubectl logs -f deployment/ai-factory-server -n ai-factory

# Check health endpoint
kubectl port-forward svc/ai-factory-server 8080:80 -n ai-factory
curl http://localhost:8080/healthz
```

### View FactoryTasks

```bash
# List all tasks
kubectl get factorytasks -n ai-factory

# View task details
kubectl describe factorytask <task-name> -n ai-factory

# View task status
kubectl get factorytask <task-name> -n ai-factory -o yaml
```

## Troubleshooting

### Service not receiving webhooks

1. Check if the service is running:
   ```bash
   kubectl get pods -n ai-factory
   ```

2. Check service logs:
   ```bash
   kubectl logs -f deployment/ai-factory-server -n ai-factory
   ```

3. Verify webhook configuration in GitHub:
   - Check the Payload URL
   - Check the Secret
   - Check recent deliveries

### Task stuck in Pending

1. Check FactoryTask status:
   ```bash
   kubectl describe factorytask <task-name> -n ai-factory
   ```

2. Check SandboxClaim status:
   ```bash
   kubectl get sandboxclaims -n ai-factory
   kubectl describe sandboxclaim <claim-name> -n ai-factory
   ```

3. Check sandbox pod logs:
   ```bash
   kubectl logs <sandbox-pod-name> -n ai-factory
   ```

### Authentication errors

1. Verify GitHub token permissions:
   - `issues: write`
   - `contents: write`
   - `pull-requests: write`
   - `metadata: read`

2. Check token expiration

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `WEBHOOK_SECRET` | GitHub/GitLab webhook secret | (required) |
| `GITHUB_TOKEN` | GitHub Personal Access Token | (required) |
| `OPENAI_API_KEY` | OpenAI API Key | (required) |

### Command-Line Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--addr` | Listen address | `:8080` |
| `--namespace` | Kubernetes namespace | `default` |
| `--webhook-secret` | Webhook secret | (from env) |
| `--agent` | Agent name | `builder` |
| `--agent-command` | Agent command | `ai-factory-agent openai-compatible` |
| `--sandbox-template` | Sandbox template | `go-dev` |
| `--watch-interval` | Controller poll interval | `15s` |
| `--task-timeout` | Sandbox ready timeout | `30m` |
| `--change-request` | Enable PR creation | `true` |
| `--report` | Enable reporting | `true` |

## Upgrading

1. Build new image:
   ```bash
   docker build -t ai-factory-server:latest -f Dockerfile.server .
   ```

2. Load image (for kind):
   ```bash
   kind load docker-image ai-factory-server:latest
   ```

3. Restart deployment:
   ```bash
   kubectl rollout restart deployment/ai-factory-server -n ai-factory
   ```

## Uninstalling

```bash
# Delete all resources
kubectl delete -f k8s/

# Or delete namespace (removes everything)
kubectl delete namespace ai-factory
```
