# Self-Hosted Service Implementation Summary

## Overview

This document summarizes the changes made to implement the ai-factory self-hosted service, as described in `docs/self-hosted-service-design.md`.

## New Files Created

### 1. Server Implementation

- `factory/cmd/factory/server/server.go` - Main server entry point
  - HTTP server for webhook handling
  - Health check endpoint (`/healthz`)
  - Graceful shutdown support
  - Webhook signature verification (GitHub/GitLab)
  - Label management on webhook receive

- `factory/cmd/factory/server/controller.go` - Task controller
  - Continuous watch loop for FactoryTask resources
  - Concurrent task execution with tracking
  - SandboxClaim creation and management
  - PR/MR creation via existing `taskpkg` functions
  - Status reporting and error handling
  - Label management on task completion/failure

- `factory/cmd/factory/server/github.go` - GitHub API client
  - Label management (add/remove labels)
  - Issue commenting
  - Set task running/done/failed states

### 2. Docker Configuration

- `Dockerfile.server` - Multi-stage Docker build
  - Stage 1: Go builder with full toolchain
  - Stage 2: Minimal runtime with kubectl
  - ~50MB final image size

### 3. Helm Chart (`charts/ai-factory/`)

> 注:早期实现曾用 `k8s/` 目录存放原始 Kubernetes 清单,现已废弃并移除,统一改用 Helm chart。

- `templates/namespace.yaml` - Creates `ai-factory` namespace
- `templates/secret.yaml` - Credentials template (GITHUB_TOKEN, WEBHOOK_SECRET, OPENAI_API_KEY)
- `templates/serviceaccount.yaml` - ServiceAccount for the server
- `templates/rbac.yaml` - ClusterRole/ClusterRoleBinding for pod and sandbox management
- `templates/deployment.yaml` - Server Deployment with health checks
- `templates/service.yaml` - ClusterIP Service
- `templates/ingress.yaml` - Ingress configuration (template)
- `templates/sandbox-warm-pool.yaml` - SandboxTemplate and SandboxWarmPool for coding agent
- `values.yaml` - Chart values (image, resources, credentials, model config)

### 4. Documentation

- `docs/self-hosted-deployment-guide.md` - Comprehensive setup guide (Chinese)

## Modified Files

- `factory/cmd/factory/factory.go` - Added `server.Cmd` to root command

## Architecture

```
GitHub Webhook → ai-factory Server → FactoryTask CR → SandboxClaim → Sandbox Pod
                     ↓
              Webhook Handler
              - Verify signature
              - Parse issue event
              - Create FactoryTask
              - Add ai-factory-running label
              - Post started comment
                     ↓
              Task Controller
              - Watch FactoryTasks
              - Create SandboxClaim
              - Execute plan steps
              - Create PR/MR
              - Report results
              - Update labels (done/failed)
```

## Label Management

| Label | Color | When Added | When Removed |
|-------|-------|------------|--------------|
| `ai-factory-run` | User | User triggers task | Task done |
| `ai-factory-smoke` | User | User triggers smoke test | Task done |
| `ai-factory-running` | Blue | Webhook received | Task done/failed |
| `ai-factory-done` | Green | Task succeeded | Task re-triggered |
| `ai-factory-failed` | Red | Task failed | Task re-triggered |

## Key Features

1. **Zero Cold Start**: Service runs persistently in K8s
2. **Webhook Verification**: HMAC-SHA256 signature verification
3. **Concurrent Execution**: Multiple tasks can run in parallel
4. **Graceful Shutdown**: Handles SIGINT/SIGTERM properly
5. **Health Checks**: `/healthz` endpoint for monitoring
6. **Status Tracking**: Real-time task status updates
7. **Error Recovery**: Automatic retry for transient failures

## Usage

### Start Server Locally (Development)

```bash
export WEBHOOK_SECRET=your-secret
export GITHUB_TOKEN=ghp_xxxx
export OPENAI_API_KEY=sk-xxxx

go run ./factory/cmd/factory server --addr :8080
```

### Build and Deploy

```bash
# Build image
docker build -t ai-factory-server:latest -f Dockerfile.server .

# Deploy to K8s (via Helm chart)
./scripts/package.sh        # build images + package chart
./scripts/deploy-remote.sh  # install/upgrade via helm
```

### Trigger Tasks

1. Configure GitHub webhook to point to your server
2. Add `ai-factory-run` label to an issue
3. Server creates FactoryTask → SandboxClaim → Sandbox Pod
4. Agent executes in sandbox
5. PR is created automatically

## Testing

The implementation reuses existing `taskpkg` functions which are already well-tested:
- Webhook parsing (`webhook.go`)
- Signature verification (`webhook.go`)
- Execution plan building (`plan.go`)
- SandboxClaim creation (`controller.go`)
- PR/MR creation (`change_request.go`)
- Issue commenting (`reporting.go`)

## Future Improvements

As noted in the design doc, these can be addressed later:
- [ ] Warm Pool mode for faster sandbox allocation
- [ ] Branch naming conflict resolution
- [ ] Task queue for high concurrency
- [ ] Monitoring and alerting
- [ ] GitHub App authentication (for arbitrary repos)
