# ai-factory K8s Deployment Files

This directory contains Kubernetes manifests for deploying the ai-factory self-hosted service.

## Files

| File | Description |
|------|-------------|
| `namespace.yaml` | Creates the `ai-factory` namespace |
| `secret.yaml` | Template for credentials (DO NOT commit with real values) |
| `serviceaccount.yaml` | ServiceAccount for the server |
| `rbac.yaml` | ClusterRole and ClusterRoleBinding for pod/sandbox management |
| `deployment.yaml` | Server Deployment with health checks |
| `service.yaml` | ClusterIP Service for internal access |
| `ingress.yaml` | Ingress configuration for external access |
| `sandbox-warm-pool.yaml` | SandboxTemplate and SandboxWarmPool for coding agent |
| `install.sh` | Automated installation script |

## Quick Deployment

```bash
# 1. Create namespace
kubectl apply -f k8s/namespace.yaml

# 2. Create credentials secret
kubectl create secret generic ai-factory-credentials -n ai-factory \
  --from-literal=GITHUB_TOKEN=ghp_xxxx \
  --from-literal=WEBHOOK_SECRET=your-webhook-secret \
  --from-literal=OPENAI_API_KEY=sk-xxxx

# 3. Deploy RBAC
kubectl apply -f k8s/serviceaccount.yaml
kubectl apply -f k8s/rbac.yaml

# 4. Deploy server
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml

# 5. Create sandbox warm pool
kubectl apply -f k8s/sandbox-warm-pool.yaml

# 6. (Optional) Deploy ingress
kubectl apply -f k8s/ingress.yaml
```

## Using the Install Script

```bash
./k8s/install.sh
```

The script will:
1. Build the coding agent sandbox image
2. Build the ai-factory-server image
3. Install required CRDs (FactoryTask, agent-sandbox)
4. Create sandbox warm pool
5. Deploy the service
6. Print access instructions

## Using the Install Script

```bash
./k8s/install.sh
```

The script will:
1. Build the Docker image
2. Install required CRDs
3. Deploy the service
4. Print access instructions

## Accessing the Service

### Port Forward (for testing)

```bash
kubectl port-forward svc/ai-factory-server 8080:80 -n ai-factory
```

### Ingress (for production)

Edit `ingress.yaml` to set your domain, then:

```bash
kubectl apply -f k8s/ingress.yaml
```

## Required Permissions

The server needs these Kubernetes permissions:
- Pods: get, list, watch, create, delete
- Pods/exec: create
- SandboxClaims: get, list, watch, create, delete
- FactoryTasks: get, list, watch, create, update, patch
