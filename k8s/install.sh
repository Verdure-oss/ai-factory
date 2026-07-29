#!/bin/bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default values
CODING_AGENT_IMAGE="${CODING_AGENT_IMAGE:-ai-factory/coding-agent-sandbox:ci}"
NAMESPACE="${NAMESPACE:-ai-factory}"

echo "=== ai-factory Self-Hosted Service Installation ==="
echo ""

# Check prerequisites
check_prerequisites() {
    echo "Checking prerequisites..."

    if ! command -v kubectl &> /dev/null; then
        echo "Error: kubectl is not installed"
        exit 1
    fi

    if ! command -v docker &> /dev/null; then
        echo "Error: docker is not installed"
        exit 1
    fi

    if ! kubectl cluster-info &> /dev/null; then
        echo "Error: cannot connect to Kubernetes cluster"
        exit 1
    fi

    echo "✓ Prerequisites satisfied"
}

# Build coding agent sandbox image (same as GitHub Actions workflow)
build_coding_agent_image() {
    echo ""
    echo "Building coding agent sandbox image..."

    GO_VERSION="$(awk '/^go / {print $2; exit}' "${ROOT_DIR}/go.mod")"
    echo "  Go version: ${GO_VERSION}"
    echo "  Image: ${CODING_AGENT_IMAGE}"

    docker build \
        --build-arg GO_VERSION="${GO_VERSION}" \
        --build-arg INSTALL_CODEX_CLI=true \
        -t "${CODING_AGENT_IMAGE}" \
        "${ROOT_DIR}/components/agent-sandbox-images/coding-agent"

    echo "✓ Coding agent image built: ${CODING_AGENT_IMAGE}"

    # If using kind, load the image
    if command -v kind &> /dev/null; then
        local cluster_name
        cluster_name=$(kind get clusters 2>/dev/null | head -1)
        if [ -n "${cluster_name}" ]; then
            echo "Loading image into kind cluster: ${cluster_name}"
            kind load docker-image "${CODING_AGENT_IMAGE}" --name "${cluster_name}"
            echo "✓ Image loaded into kind"
        fi
    fi
}

# Build ai-factory server image
build_server_image() {
    local image="${1:-ai-factory-server:latest}"

    echo ""
    echo "Building ai-factory-server image..."
    cd "${ROOT_DIR}"
    docker build -t "${image}" -f Dockerfile.server .

    echo "✓ Server image built: ${image}"

    # If using kind, load the image
    if command -v kind &> /dev/null; then
        local cluster_name
        cluster_name=$(kind get clusters 2>/dev/null | head -1)
        if [ -n "${cluster_name}" ]; then
            echo "Loading image into kind cluster: ${cluster_name}"
            kind load docker-image "${image}" --name "${cluster_name}"
            echo "✓ Image loaded into kind"
        fi
    fi
}

# Install FactoryTask CRD
install_crd() {
    echo ""
    echo "Checking for FactoryTask CRD..."

    if kubectl get crd factorytasks.factory.ai.gke.io &> /dev/null; then
        echo "✓ FactoryTask CRD already installed"
    else
        echo "Installing FactoryTask CRD..."
        if [ -f "${ROOT_DIR}/components/factory-task/install" ]; then
            "${ROOT_DIR}/components/factory-task/install"
            echo "✓ FactoryTask CRD installed"
        else
            echo "Warning: FactoryTask CRD installer not found at components/factory-task/install"
            echo "Please install the CRD manually"
        fi
    fi
}

# Install agent-sandbox CRD (required for SandboxClaim/SandboxTemplate/SandboxWarmPool)
install_sandbox_crd() {
    echo ""
    echo "Checking for agent-sandbox CRD..."

    if kubectl get crd sandboxclaims.extensions.agents.x-k8s.io &> /dev/null; then
        echo "✓ agent-sandbox CRD already installed"
    else
        echo "Installing agent-sandbox CRD..."
        if [ -f "${ROOT_DIR}/components/agent-sandbox/install" ]; then
            "${ROOT_DIR}/components/agent-sandbox/install"
            echo "✓ agent-sandbox CRD installed"
        else
            echo "Warning: agent-sandbox CRD installer not found at components/agent-sandbox/install"
            echo "Please install the CRD manually"
        fi
    fi
}

# Create sandbox warm pool
create_warm_pool() {
    echo ""
    echo "Creating sandbox warm pool..."

    # Update the image in the warm pool YAML
    local warm_pool_yaml="${ROOT_DIR}/k8s/sandbox-warm-pool.yaml"

    # Apply with the correct image
    sed "s|image: ai-factory/coding-agent-sandbox:ci|image: ${CODING_AGENT_IMAGE}|g" "${warm_pool_yaml}" | \
        sed "s|namespace: ai-factory|namespace: ${NAMESPACE}|g" | \
        kubectl apply -f -

    echo "Waiting for warm pool to be ready..."
    kubectl wait sandboxwarmpool go-dev -n "${NAMESPACE}" \
        --for=jsonpath='{.status.readyReplicas}'>=1 \
        --timeout=180s

    echo "✓ Sandbox warm pool created and ready"
}

# Deploy the service
deploy_service() {
    echo ""
    echo "Deploying ai-factory service..."

    # Create namespace
    sed "s|name: ai-factory|name: ${NAMESPACE}|g" "${ROOT_DIR}/k8s/namespace.yaml" | kubectl apply -f -

    # Check if secret exists
    if kubectl get secret ai-factory-credentials -n "${NAMESPACE}" &> /dev/null; then
        echo "✓ Secret 'ai-factory-credentials' already exists"
    else
        echo ""
        echo "⚠ Secret 'ai-factory-credentials' not found!"
        echo ""
        echo "Please create the secret with your credentials:"
        echo ""
        echo "  kubectl create secret generic ai-factory-credentials -n ${NAMESPACE} \\"
        echo "    --from-literal=GITHUB_TOKEN=ghp_xxxx \\"
        echo "    --from-literal=WEBHOOK_SECRET=your-webhook-secret \\"
        echo "    --from-literal=OPENAI_API_KEY=sk-xxxx"
        echo ""
        read -p "Press Enter after creating the secret, or Ctrl+C to abort..."
    fi

    # Deploy RBAC
    sed "s|namespace: ai-factory|namespace: ${NAMESPACE}|g" "${ROOT_DIR}/k8s/serviceaccount.yaml" | kubectl apply -f -
    sed "s|namespace: ai-factory|namespace: ${NAMESPACE}|g" "${ROOT_DIR}/k8s/rbac.yaml" | kubectl apply -f -

    # Deploy the server
    sed "s|namespace: ai-factory|namespace: ${NAMESPACE}|g" "${ROOT_DIR}/k8s/deployment.yaml" | \
        sed "s|image: ai-factory-server:latest|image: ai-factory-server:latest|g" | \
        kubectl apply -f -
    sed "s|namespace: ai-factory|namespace: ${NAMESPACE}|g" "${ROOT_DIR}/k8s/service.yaml" | kubectl apply -f -

    echo ""
    echo "Waiting for deployment to be ready..."
    kubectl rollout status deployment/ai-factory-server -n "${NAMESPACE}" --timeout=60s

    echo ""
    echo "✓ ai-factory service deployed"
}

# Print access instructions
print_instructions() {
    echo ""
    echo "=== Deployment Complete ==="
    echo ""
    echo "Service Status:"
    kubectl get pods -n "${NAMESPACE}" -l app=ai-factory-server
    echo ""
    echo "Warm Pool Status:"
    kubectl get sandboxwarmpool -n "${NAMESPACE}"
    echo ""
    echo "=== Next Steps ==="
    echo ""
    echo "1. Expose the service to receive GitHub webhooks:"
    echo ""
    echo "   Option A: Use ngrok (for testing)"
    echo "   $ kubectl port-forward svc/ai-factory-server 8080:80 -n ${NAMESPACE}"
    echo "   $ ngrok http 8080"
    echo ""
    echo "   Option B: Apply Ingress (for production)"
    echo "   $ kubectl apply -f ${ROOT_DIR}/k8s/ingress.yaml"
    echo "   (Edit ingress.yaml to set your domain first)"
    echo ""
    echo "2. Configure GitHub webhook in your target repository:"
    echo "   - Payload URL: https://your-domain/webhook/github"
    echo "   - Content type: application/json"
    echo "   - Secret: (same as WEBHOOK_SECRET)"
    echo "   - Events: Select 'Issues' only"
    echo ""
    echo "3. Trigger a task:"
    echo "   - Add label 'ai-factory-run' to an issue"
    echo "   - Or add label 'ai-factory-smoke' for smoke test"
    echo ""
    echo "4. Check status:"
    echo "   $ kubectl get factorytasks -n ${NAMESPACE}"
    echo "   $ kubectl logs -f deployment/ai-factory-server -n ${NAMESPACE}"
    echo ""
}

# Main
main() {
    local server_image="${1:-ai-factory-server:latest}"

    check_prerequisites
    build_coding_agent_image
    build_server_image "${server_image}"
    install_crd
    install_sandbox_crd
    create_warm_pool
    deploy_service
    print_instructions
}

main "$@"
