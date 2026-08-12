# ai-factory setup guide

This guide configures the architecture used by this repository:

    GitHub project -> GitHub Actions -> temporary kind -> FactoryTask -> PR
    GitLab project -> GitLab Runner -> temporary kind -> FactoryTask -> MR

ai-factory is the common orchestration layer. It creates the FactoryTask,
prepares the sandbox, runs a configurable agent, validates the result, writes
the branch and commit, creates the provider change request, and reports the
result back to the Issue.

## 1. Prepare the model provider

The default runner uses the OpenAI-compatible chat/completions protocol. The
variable names describe that protocol and do not require a particular vendor.

Set these values in the CI secret/variable system:

    OPENAI_API_KEY=<masked provider key>
    OPENAI_BASE_URL=https://api.example.com/v1
    OPENAI_MODEL=provider-model

Optional execution limits:

    OPENAI_TEMPERATURE=1
    OPENAI_MAX_TOKENS=48000
    OPENAI_MAX_TOOL_ROUNDS=40
    OPENAI_MAX_FINAL_SCRIPT_ROUNDS=5
    OPENAI_MAX_REPAIR_ROUNDS=3
    OPENAI_TOTAL_TIMEOUT_SECONDS=1800
    OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS=180
    OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS=90
    OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS=90

Each model request is attempted at most twice. The total timeout covers tool
exploration, final-script generation, repair requests, and script execution.

For example, a Kimi-compatible endpoint can use:

    OPENAI_BASE_URL=https://api.kimi.com/coding/v1
    OPENAI_MODEL=kimi-for-coding

Other OpenAI-compatible gateways, hosted models, and self-hosted endpoints
use the same configuration. To use another coding CLI, set:

    AGENT_COMMAND=ai-factory-agent codex

or set spec.agent.command on a hand-written FactoryTask. Do not put API keys in
Issue descriptions, repository files, or CI logs.

## 2. Common task contract

Create the labels used by the automation:

- ai-factory: identifies an Issue intended for the factory.
- ai-factory-run: permits the real agent and PR/MR change request flow.
- ai-factory-smoke: permits a safe smoke change when supported by the workflow.

Use a small first task:

    ## Task

    Add a short section to README.md explaining how to run the project locally.

    ## Acceptance Criteria

    - The new section is clear and accurate.
    - Existing documentation remains valid.
    - Run the project's normal validation command.

Review the Issue before adding ai-factory-run. The label is the approval to
create a branch, push code, and open a PR/MR.

## 3. GitHub Actions + kind

### 3.1 Enable the repository workflow

For this repository, the workflow is already at:

    .github/workflows/issue-factorytask.yaml

The workflow listens for Issue labeled and reopened events. It creates a
temporary kind cluster on the GitHub-hosted runner, installs agent-sandbox,
builds the coding-agent image, and runs the FactoryTask.

For a different GitHub project, make the workflow available in that project
through the reusable workflow in
.github/workflows/ai-factory-issue-reusable.yaml. Add a small caller workflow
to the target repository:

    name: ai-factory issue

    on:
      issues:
        types: [labeled, reopened]

    jobs:
      run:
        if: contains(github.event.issue.labels.*.name, 'ai-factory') && contains(github.event.issue.labels.*.name, 'ai-factory-run') && (github.event.action != 'labeled' || github.event.label.name == 'ai-factory-run')
        uses: liyuerich/ai-factory/.github/workflows/ai-factory-issue-reusable.yaml@main
        with:
          repository: ${{ github.repository }}
          issue_number: ${{ github.event.issue.number }}
        secrets:
          AI_FACTORY_OPENAI_API_KEY: ${{ secrets.AI_FACTORY_OPENAI_API_KEY || secrets.OPENAI_API_KEY }}
          AI_FACTORY_GITHUB_TOKEN: ${{ secrets.AI_FACTORY_GITHUB_TOKEN }}

The reusable workflow checks out both the target repository and ai-factory,
creates the temporary kind cluster, and runs the provider-neutral FactoryTask
flow. Keep the referenced ai-factory ref pinned to a reviewed release or
commit in production.

### 3.2 Configure Actions permissions

In the GitHub repository, open:

    Settings -> Actions -> General

Allow Actions when repository policy requires it. The workflow needs:

    contents: write
    issues: write
    pull-requests: write

The default GITHUB_TOKEN may be unable to create a pull request in some
organizations. In that case create a fine-grained personal access token with
contents write, pull requests write, and issue write for the target repository.
Store it as the AI_FACTORY_GITHUB_TOKEN repository secret.

### 3.3 Configure GitHub secrets

Open:

    Settings -> Secrets and variables -> Actions -> Secrets

Create:

    AI_FACTORY_OPENAI_API_KEY=<masked provider key>

or use OPENAI_API_KEY. For PR creation when the default token is restricted,
also create:

    AI_FACTORY_GITHUB_TOKEN=<masked GitHub token>

### 3.4 Configure GitHub variables

Open:

    Settings -> Secrets and variables -> Actions -> Variables

Set:

    AI_FACTORY_OPENAI_BASE_URL=https://api.example.com/v1
    AI_FACTORY_OPENAI_MODEL=provider-model
    AI_FACTORY_OPENAI_TEMPERATURE=1
    AI_FACTORY_OPENAI_MAX_TOKENS=48000
    AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS=40
    AI_FACTORY_OPENAI_MAX_FINAL_SCRIPT_ROUNDS=5
    AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS=3
    AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS=1800
    AI_FACTORY_OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS=180
    AI_FACTORY_OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS=90
    AI_FACTORY_OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS=90

To select a different runner, set:

    AI_FACTORY_RUN_AGENT_COMMAND=ai-factory-agent codex

The default remains:

    ai-factory-agent openai-compatible

### 3.5 Run a GitHub task

1. Create or reopen an Issue.
2. Fill in the task and acceptance criteria.
3. Add ai-factory.
4. Review the Issue.
5. Add ai-factory-run.
6. Watch the Actions run and the Issue comments.
7. Review the generated pull request and its checks.

The workflow adds ai-factory-running while processing, ai-factory-done after a
successful run, and ai-factory-failed after a failure. It removes the
ai-factory-run label after success to prevent accidental retriggers.

For local or CI debugging, the workflow can be started manually from the
Kind Run Once workflow. It exercises the same temporary kind and
agent-sandbox path without creating a PR.

## 4. GitLab Runner + kind

### 4.1 Make ai-factory available to the project

Mirror this repository into the internal GitLab instance, for example:

    https://gitlab.example.com/platform/ai/ai-factory

The target project can include the reusable CI template:

    include:
      - project: platform/ai/ai-factory
        ref: main
        file: /ci/gitlab-factory-task.yml

The target project does not need to copy the FactoryTask implementation. The
template clones the configured ai-factory source when the current checkout is
not ai-factory itself.

Set AI_FACTORY_SOURCE_URL to the readable internal mirror. If the mirror is
private, set AI_FACTORY_SOURCE_TOKEN to a masked token with read access, or
allow the target project CI_JOB_TOKEN to read the mirror.

### 4.2 Prepare the Runner

Register a self-hosted Runner with the tag:

    ai-factory

The Runner host must provide:

- docker
- kind
- kubectl
- Go matching the FactoryTask source
- git
- jq
- curl

The Runner needs permission to start Docker containers because kind creates
Kubernetes node containers. A Docker executor normally needs privileged mode
and Docker-in-Docker; a shell executor can use a controlled host Docker daemon.
Do not run this job on an untrusted shared Runner.

### 4.3 Configure GitLab CI/CD variables

Open:

    Project -> Settings -> CI/CD -> Variables

Add the following masked, protected variables:

    GITLAB_TOKEN=<project or group token>
    GITLAB_WEBHOOK_SECRET=<webhook secret>
    OPENAI_API_KEY=<masked provider key>

The GitLab token must be able to read the Issue, write Issue notes, clone and
push the repository, and create Merge Requests. A project access token normally
needs api and write_repository scopes.

Add these normal variables:

    OPENAI_BASE_URL=https://api.example.com/v1
    OPENAI_MODEL=provider-model
    OPENAI_TEMPERATURE=1
    AI_FACTORY_RUN_AGENT_COMMAND=ai-factory-agent openai-compatible
    AI_FACTORY_RUN_VALIDATION_COMMAND=go test ./...
    AI_FACTORY_SOURCE_URL=https://gitlab.example.com/platform/ai/ai-factory.git

If the ai-factory mirror is private, set AI_FACTORY_SOURCE_TOKEN as a masked
variable and allow the target project to read the mirror.

### 4.4 Start a GitLab Issue task

The CI template accepts the Issue IID as a pipeline variable. Start a
pipeline from:

    Build -> Pipelines -> New pipeline

Set:

    AI_FACTORY_ISSUE_IID=<issue iid>

The Issue must already have ai-factory-run. The job then:

1. Creates a temporary kind cluster.
2. Installs the FactoryTask CRD and agent-sandbox.
3. Builds and loads the coding-agent image.
4. Reads the Issue and project metadata from the GitLab API.
5. Converts the metadata into the provider-neutral GitLab webhook payload.
6. Creates and runs a FactoryTask.
7. Pushes the change branch and creates a Merge Request.
8. Writes the result back to the GitLab Issue.
9. Deletes the temporary kind cluster.

The job uses a resource group based on project and Issue IID, so repeated
pipeline attempts for one Issue do not run concurrently.

### 4.5 Automatic GitLab Issue triggering

GitLab Issue webhooks do not provide a native way to pass a dynamic Issue IID
directly into a new CI pipeline. For automatic triggering, run the
provider-neutral router from the ai-factory CLI on a small internal VM or
container:

    factory task webhook trigger-pipeline \
      --addr :8080 \
      --secret "$GITLAB_WEBHOOK_SECRET" \
      --token-env GITLAB_TOKEN \
      --require-label ai-factory-run

The router:

1. Verifies X-Gitlab-Token.
2. Checks the ai-factory-run label.
3. Calls the GitLab pipeline trigger API.
4. Passes AI_FACTORY_ISSUE_IID and the target ref as pipeline variables.

The Runner and kind remain the execution environment. The router only starts
the pipeline and does not run the Agent. For an initial rollout, manual
pipeline triggering is simpler and validates the complete Runner + kind path.

Configure the GitLab project webhook at:

    Project -> Settings -> Webhooks

Use the router URL, the same GITLAB_WEBHOOK_SECRET, JSON content type, and
Issues events. Do not expose a Runner Docker socket or model API key to the
router.

## 5. Observe and debug

GitHub:

    gh run list
    gh run view <run-id>

GitLab:

    inspect the pipeline job log
    inspect the generated Merge Request

Inside a temporary kind job:

    kubectl get factorytasks,sandboxclaims,sandboxwarmpools,pods -A
    kubectl get factorytask <task-name> -n default -o yaml

Common failures:

- Missing API key: verify the masked CI secret name and job access.
- source_branch_missing: verify the repository, base branch, and push token.
- command not found: add the required tool to the coding-agent image or choose
  an Agent command that exists in the image.
- validation failed: reproduce the configured validation command in the target
  repository before retrying.
- model timeout or truncated output: lower task scope or increase the
  configured token and tool-round limits.

## 6. Security checklist

- Use project-scoped GitHub/GitLab tokens.
- Keep provider keys and repository tokens masked and protected.
- Use dedicated or tightly controlled privileged Runners for kind.
- Limit the Issue labels that can start real changes.
- Use repository allowlists when one router serves multiple projects.
- Require normal GitHub checks or GitLab CI checks before merging.
- Keep agent network access and Kubernetes RBAC minimal.
- Never include secrets in Issue bodies, prompts, comments, artifacts, or logs.
