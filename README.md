# ai-factory

ai-factory turns a repository task into a controlled coding-agent run:

    Issue -> FactoryTask -> sandbox -> agent -> validation -> branch/commit -> PR or MR

The orchestration layer is provider-neutral. It can read GitHub or GitLab issue
events, clone the repository described by the event, run a configured agent in
an agent-sandbox environment, report status back to the issue, and create an
idempotent pull request or merge request.

## Supported execution architectures

The project uses two execution paths:

- GitHub projects use GitHub Actions and a temporary kind cluster.
- GitLab projects use GitLab Runner and a temporary kind cluster.

In both paths ai-factory supplies the common task model, sandbox plan, agent
runner, Git authentication, validation, failure classification, retry policy,
issue comments, and PR/MR creation. A permanent Kubernetes cluster is optional;
it is only needed for a shared, always-on runtime or high task volume.

The step-by-step setup for both providers is in
docs/general/guide.md.

## Repository structure

- components/agent-sandbox: installs the agent-sandbox controllers.
- components/agent-sandbox-images/coding-agent: builds the generic coding image.
- components/factory-task: installs the FactoryTask CRD and runtime components.
- factory/pkg/task: provider-neutral task, webhook, agent-plan, status, and
  change-request logic.
- .github/workflows/issue-factorytask.yaml: GitHub Issue to kind to PR flow.
- .github/workflows/ai-factory-issue-reusable.yaml: reusable GitHub workflow
  for other repositories.
- .gitlab-ci.yml: GitLab Runner to kind to MR flow.
- examples: hand-written GitHub and GitLab FactoryTask examples.

## Agent model and runner configuration

The default command is:

    ai-factory-agent openai-compatible

This runner works with any provider that exposes a compatible
chat/completions endpoint. The model provider is selected at runtime:

    OPENAI_API_KEY=...
    OPENAI_BASE_URL=https://api.example.com/v1
    OPENAI_MODEL=provider-model

Kimi, OpenAI-compatible gateways, self-hosted models, and other compatible
providers can use the same runner. The names OPENAI_API_KEY, OPENAI_BASE_URL,
and OPENAI_MODEL describe the protocol contract, not a required vendor.

An alternate CLI can be selected without changing ai-factory:

    AGENT_COMMAND="ai-factory-agent codex"

or with spec.agent.command on a hand-written FactoryTask. The coding image
does not require a specific provider CLI.

## Local Kubernetes setup

The installers use the active kubectl context and work with kind, a native
Kubernetes cluster, or GKE. Select a context first:

    kubectl config use-context <cluster-context>
    kubectl cluster-info

Install agent-sandbox, then build or load the coding-agent image. For a kind
cluster:

    export KUBECONFIG=/path/to/kubeconfig
    IMAGE_PREFIX=localhost/ai-factory/ \
    IMAGE_TAG=dev \
    KIND_CLUSTER_NAME=<kind-cluster-name> \
    AGENT_SANDBOX_SRC=/path/to/agent-sandbox \
    components/agent-sandbox/install

Install the FactoryTask CRD:

    components/factory-task/install

Validate, inspect, or run a hand-written task:

    go run ./factory/cmd/factory task validate examples/factory-task-github.yaml
    go run ./factory/cmd/factory task plan examples/factory-task-gitlab.yaml
    go run ./factory/cmd/factory task controller manifest examples/factory-task-github.yaml
    go run ./factory/cmd/factory task controller run-once examples/factory-task-github.yaml

For a long-running local controller:

    go run ./factory/cmd/factory task controller watch --namespace default

The full provider setup, secrets, labels, runner prerequisites, and
troubleshooting steps are in docs/general/guide.md and
components/factory-task/README.md.

## GitHub issue tasks

The GitHub Actions workflow listens for Issue labeled and reopened events,
creates a temporary kind cluster, installs agent-sandbox, creates a
FactoryTask, and runs the watch controller.

Use these labels:

- ai-factory: identifies a task for this repository's automation.
- ai-factory-run: permits the real agent, branch, commit, push, and PR flow.
- ai-factory-smoke: runs the safe smoke change-request flow.

Configure the API key as a repository secret and provider settings as repository
variables. See docs/general/guide.md for the exact GitHub UI steps.

## GitLab issue tasks

The GitLab CI template runs the same FactoryTask flow on a GitLab Runner. The
runner creates a temporary kind cluster, installs agent-sandbox, builds the
coding image, reads the Issue through the GitLab API, and creates a merge
request after successful validation.

Set AI_FACTORY_ISSUE_IID when a pipeline is started for an Issue. Set
AI_FACTORY_SOURCE_URL when the target project consumes ai-factory from a
separate GitLab mirror or repository. See docs/general/guide.md for protected
variables, runner tags, webhook routing, and the required Docker privileges.

## Development

Run the Go test suite:

    go test ./...

Validate shell scripts and YAML files before committing. Do not place API keys,
provider tokens, generated prompts, or task instructions in commits.

This project is licensed under the Apache 2.0 License. It is not an officially
supported Google product.
