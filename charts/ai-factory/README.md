# ai-factory Helm Chart

## Prerequisites

Before installing the chart, you need to:

1. Build the Docker images
2. Load images into your cluster (if using kind)
3. Install CRDs

## Build Images

```bash
# Build server image
docker build -t ai-factory-server:latest -f Dockerfile.server .

# Build sandbox image
GO_VERSION="$(awk '/^go / {print $2; exit}' go.mod)"
docker build \
  --build-arg GO_VERSION="${GO_VERSION}" \
  --build-arg INSTALL_CODEX_CLI=true \
  -t coding-agent-sandbox:latest \
  components/agent-sandbox-images/coding-agent

# If using kind, load images
kind load docker-image ai-factory-server:latest
kind load docker-image coding-agent-sandbox:latest
```

## Install CRDs

```bash
# Install FactoryTask CRD
components/factory-task/install

# Install agent-sandbox CRD
components/agent-sandbox/install
```

## Install the Chart

```bash
helm install ai-factory ./charts/ai-factory \
  --set githubToken=ghp_your_github_token \
  --set webhookSecret=your_webhook_secret \
  --set openaiApiKey=sk_your_openai_key
```

Or create a values file:

```yaml
# my-values.yaml
githubToken: ghp_your_github_token
webhookSecret: your_webhook_secret
openaiApiKey: sk_your_openai_key

server:
  ingress:
    enabled: true
    hosts:
      - host: ai-factory.example.com
        paths:
          - path: /webhook
            pathType: Prefix
```

Then install:

```bash
helm install ai-factory ./charts/ai-factory -f my-values.yaml
```

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `server.image.repository` | Server image repository | `ai-factory-server` |
| `server.image.tag` | Server image tag | `latest` |
| `server.replicaCount` | Number of server replicas | `1` |
| `server.service.type` | Service type | `ClusterIP` |
| `server.service.port` | Service port | `80` |
| `server.ingress.enabled` | Enable ingress | `false` |
| `sandbox.image.repository` | Sandbox image repository | `coding-agent-sandbox` |
| `sandbox.image.tag` | Sandbox image tag | `latest` |
| `sandbox.warmPoolReplicas` | Number of warm sandbox pods | `2` |
| `agent.name` | Agent name | `builder` |
| `agent.command` | Agent command | `ai-factory-agent openai-compatible` |
| `github.token` | GitHub token | `""` |
| `webhook.secret` | Webhook secret | `""` |
| `openai.apiKey` | OpenAI API key | `""` |
| `watchInterval` | Controller watch interval | `15s` |
| `taskTimeout` | Task timeout | `30m` |
| `changeRequestEnabled` | Enable PR creation | `true` |
| `reportEnabled` | Enable issue comments | `true` |

## Upgrading

```bash
helm upgrade ai-factory ./charts/ai-factory \
  --set githubToken=ghp_your_github_token \
  --set webhookSecret=your_webhook_secret \
  --set openaiApiKey=sk_your_openai_key
```

## Uninstalling

```bash
helm uninstall ai-factory
```
