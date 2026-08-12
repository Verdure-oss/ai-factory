# FactoryTask runtime

For the complete GitHub Actions + kind and GitLab Runner + kind setup, see
docs/general/guide.md.

`components/factory-task/install` always installs the `FactoryTask` CRD. Set
`INSTALL_FACTORY_TASK_RUNTIME=true` to also deploy the runtime controllers:

- `factory-task-watch-controller`: watches `FactoryTask` resources and executes
  sandbox work.
- `factory-task-webhook`: receives GitHub/GitLab issue webhooks and creates
  `FactoryTask` resources.

## Install

```bash
FACTORY_IMAGE=registry.example.com/ai-factory/factory:latest \
INSTALL_FACTORY_TASK_RUNTIME=true \
GITHUB_TOKEN=... \
GITLAB_TOKEN=... \
OPENAI_API_KEY=... \
OPENAI_BASE_URL=https://api.example.com/v1 \
OPENAI_MODEL=provider-model \
WEBHOOK_SECRET=... \
components/factory-task/install
```

The `FACTORY_IMAGE` image must contain the `factory` CLI and `kubectl`.
Provider credentials are written to the `factory-task-secrets` secret only when
`GITHUB_TOKEN`, `GITLAB_TOKEN`, `WEBHOOK_SECRET`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL`, `OPENAI_MODEL`, `OPENAI_TEMPERATURE`, `OPENAI_MAX_TOKENS`,
the phase round/time limits, or `CODEX_API_KEY` is provided. Re-running the
installer without those values leaves any existing secret untouched.

## Runtime settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `FACTORY_IMAGE` | required | Image used by the webhook and watch controller deployments. |
| `FACTORY_NAMESPACE` | `factory-system` | Namespace for runtime deployments, service account, service, and secret. |
| `FACTORY_TASK_NAMESPACE` | `default` | Namespace where webhook-created tasks are applied and watched. |
| `IMAGE_PULL_POLICY` | `IfNotPresent` | Runtime deployment image pull policy. |
| `WATCH_INTERVAL` | `15s` | FactoryTask polling interval. |
| `SANDBOX_TIMEOUT` | `5m` | SandboxClaim readiness timeout. |
| `REQUIRE_LABEL` | `ai-factory` | Issue label required to trigger tasks. |
| `SANDBOX_TEMPLATE` | `go-dev` | Sandbox template used for generated tasks. |
| `AGENT_NAME` | `builder` | Agent name used for generated tasks. |
| `AGENT_COMMAND` | `ai-factory-agent openai-compatible` | Agent runner command used for generated tasks. |
| `PROMPT_REF` | `.agents/builder/agent.md` | Agent prompt file read from the cloned repository. |
| `TASK_COMMAND` | `go test ./...` | Command inserted into generated FactoryTasks. |
| `CHANGE_REQUEST` | `true` | Whether webhook-generated tasks should branch, commit, push, and create PRs/MRs. |
| `GITHUB_TOKEN` | empty | Token used for GitHub issue comments and pull requests. |
| `GITLAB_TOKEN` | empty | Token used for GitLab issue comments and merge requests. |
| `WEBHOOK_SECRET` | empty | GitHub webhook secret or GitLab webhook token. |
| `OPENAI_API_KEY` | empty | API key injected into OpenAI-compatible agent tasks. `AI_FACTORY_OPENAI_API_KEY` is also accepted by the installer and stored as `OPENAI_API_KEY`. |
| `OPENAI_BASE_URL` | empty | Optional OpenAI-compatible API base URL injected into agent tasks. |
| `OPENAI_MODEL` | empty | Optional OpenAI-compatible model name injected into agent tasks. |
| `OPENAI_TEMPERATURE` | empty | Optional OpenAI-compatible temperature injected into agent tasks. |
| `OPENAI_MAX_TOKENS` | empty | Optional OpenAI-compatible max token limit injected into agent tasks. |
| `OPENAI_MAX_TOOL_ROUNDS` | empty | Optional OpenAI-compatible shell tool round limit injected into agent tasks. |
| `OPENAI_MAX_FINAL_SCRIPT_ROUNDS` | empty | Optional OpenAI-compatible no-tool final script retry limit injected into agent tasks. |
| `OPENAI_MAX_REPAIR_ROUNDS` | empty | Optional OpenAI-compatible generated-script repair round limit injected into agent tasks. |
| `OPENAI_TOTAL_TIMEOUT_SECONDS` | empty | Optional total agent deadline injected into agent tasks. |
| `OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS` | empty | Optional per-request exploration timeout injected into agent tasks. |
| `OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS` | empty | Optional per-request final-script timeout injected into agent tasks. |
| `OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS` | empty | Optional per-request repair-script timeout injected into agent tasks. |
| `CODEX_API_KEY` | empty | Optional Codex API key for tasks that override `AGENT_COMMAND` to Codex. |
| `WEBHOOK_INGRESS_HOST` | empty | Optional host for creating a webhook `Ingress`. |
| `WEBHOOK_INGRESS_CLASS` | empty | Optional ingress class name when `WEBHOOK_INGRESS_HOST` is set. |

## Webhook endpoints

The runtime creates a ClusterIP service named `factory-task-webhook`.

For local testing:

```bash
kubectl -n factory-system port-forward svc/factory-task-webhook 8080:80
```

Use these paths in provider webhook settings:

- GitHub: `http://<host>/webhook/github`
- GitLab: `http://<host>/webhook/gitlab`

GitHub should send issue events with `X-Hub-Signature-256`. GitLab should send
issue events with `X-Gitlab-Token`.

Set `WEBHOOK_INGRESS_HOST` to expose the webhook through an Ingress:

```bash
FACTORY_IMAGE=registry.example.com/ai-factory/factory:latest \
INSTALL_FACTORY_TASK_RUNTIME=true \
WEBHOOK_INGRESS_HOST=ai-factory.example.com \
WEBHOOK_INGRESS_CLASS=nginx \
components/factory-task/install
```

## End-to-end issue validation

After the runtime is installed and reachable through either port-forward or
Ingress:

1. Create a GitHub or GitLab issue in a test repository.
2. Add the configured trigger label. The default label is `ai-factory`.
3. Confirm the provider webhook delivery returns `2xx`.
4. Watch the generated task:

```bash
kubectl get factorytasks -A -w
```

5. Confirm the task reaches `Succeeded`, posts an issue comment, pushes the
   change branch, and creates a PR/MR. If the PR/MR already exists, the
   controller records `ChangeRequestAlreadyExists` and keeps the task
   successful.

For a local webhook test without provider delivery, port-forward the service and
send one of the example payloads:

```bash
kubectl -n factory-system port-forward svc/factory-task-webhook 8080:80
curl -X POST http://127.0.0.1:8080/webhook/github \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: issues' \
  --data-binary @examples/webhook-github-issue.json
```

## GitHub Actions issue validation

The `Issue FactoryTask` workflow validates real GitHub issue events without a
public webhook endpoint. Create or reopen an issue with the `ai-factory` label,
or add that label to an existing issue. The workflow starts a temporary kind
cluster, installs agent-sandbox and the FactoryTask CRD, posts the real GitHub
issue event payload to the local webhook server, and runs the generated
`FactoryTask` with the watch controller.

This workflow runs in agent dry-run mode by default: it captures the generated
agent prompt with `cat >/tmp/ai-factory-agent-prompt.txt`, then runs validation
commands in the coding-agent sandbox image. Use the runtime webhook deployment
with a real `AGENT_COMMAND` and provider tokens for production PR/MR creation.

Add the additional `ai-factory-smoke` label to enable the workflow's safe
change-request path. In that mode the task writes a small file under
`.ai-factory/smoke/`, commits it on a generated branch, pushes the branch with
`AI_FACTORY_GITHUB_TOKEN`, and creates a GitHub pull request. If the repository
does not allow the default Actions `GITHUB_TOKEN` to create pull requests,
configure an `AI_FACTORY_GITHUB_TOKEN` repository secret with `contents:write`
and `pull_requests:write` permissions. Issues without `ai-factory-smoke` never
enable branch, push, or PR creation in this workflow.

Use `ai-factory-run` for real issue-driven tasks. That label enables branch,
commit, push, and PR creation without the smoke file. The workflow runs the
issue instructions through `AI_FACTORY_RUN_AGENT_COMMAND`, defaulting to
`ai-factory-agent openai-compatible`, then runs
`AI_FACTORY_RUN_VALIDATION_COMMAND`, defaulting to `go test ./...`. Configure an
`AI_FACTORY_OPENAI_API_KEY` repository secret, or an `OPENAI_API_KEY`
repository secret, before using this path. Set `AI_FACTORY_OPENAI_BASE_URL` and
`AI_FACTORY_OPENAI_MODEL` repository variables to use an OpenAI-compatible
provider such as Kimi, DeepSeek, Qwen, Ollama, or vLLM. Runtime prompt files
under `.ai-factory/agent-prompt.md` and `.ai-factory/task-instructions.md` are
removed before committing so they do not pollute generated PRs.

## GitHub issue auto-trigger

You can create an issue in a GitHub repository to have ai-factory automatically
generate a pull request. This is useful for turning feature requests, bug
reports, or small tasks into code changes.

### Required labels

Use the `ai-factory` label to mark an issue for processing. Add the
`ai-factory-run` label when you want the workflow to create a real branch,
commit, push, and open a pull request. Without `ai-factory-run`, the issue may
still be processed in a dry-run or validation mode that does not create PRs.

- `ai-factory` — marks the issue for ai-factory.
- `ai-factory-run` — enables the full change-request path (branch, commit, push,
  PR).

You can remove or avoid `ai-factory-run` on draft or experimental issues to
prevent accidental PR creation.

### Required secrets

Create the following repository secrets before using the auto-trigger workflow:

- `AI_FACTORY_OPENAI_API_KEY` or `OPENAI_API_KEY` — the API key for your
  OpenAI-compatible provider. The installer accepts `AI_FACTORY_OPENAI_API_KEY`
  and stores it as `OPENAI_API_KEY`.
- `AI_FACTORY_GITHUB_TOKEN` — a GitHub personal access token with
  `contents:write` and `pull_requests:write` permissions for the target
  repository. Use this when the default Actions `GITHUB_TOKEN` is not allowed
  to create pull requests.

The webhook runtime also accepts `GITHUB_TOKEN` for issue comments and PR
creation.

The GitHub issue workflow updates status labels while it runs:

| Label | Meaning |
| --- | --- |
| `ai-factory-running` | The workflow created a FactoryTask and is processing the issue. |
| `ai-factory-done` | The FactoryTask succeeded. The workflow removes `ai-factory-run` after success. |
| `ai-factory-failed` | The workflow or FactoryTask failed. Check the issue comment for the run URL and failure reason. |

### Repository variables for OpenAI-compatible providers

Set these repository variables to point the agent at an OpenAI-compatible
provider such as Kimi, DeepSeek, Qwen, Ollama, or vLLM:

| Variable | Example | Purpose |
| --- | --- | --- |
| `AI_FACTORY_OPENAI_BASE_URL` | `https://api.example.com/v1` | API base URL. |
| `AI_FACTORY_OPENAI_MODEL` | `provider-model` | Model name. |
| `AI_FACTORY_OPENAI_TEMPERATURE` | `1` | Sampling temperature. |
| `AI_FACTORY_OPENAI_MAX_TOKENS` | `48000` | Maximum response tokens. |
| `AI_FACTORY_OPENAI_MAX_TOOL_ROUNDS` | `40` | Maximum Shell tool call rounds. |
| `AI_FACTORY_OPENAI_MAX_FINAL_SCRIPT_ROUNDS` | `5` | Maximum no-tool final script retries. |
| `AI_FACTORY_OPENAI_MAX_REPAIR_ROUNDS` | `3` | Maximum repair attempts after a failed script. |
| `AI_FACTORY_OPENAI_TOTAL_TIMEOUT_SECONDS` | `1800` | Total agent deadline, below the 45-minute job timeout. |
| `AI_FACTORY_OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS` | `180` | Timeout for each exploration model request. |
| `AI_FACTORY_OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS` | `90` | Timeout for each final-script model request. |
| `AI_FACTORY_OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS` | `90` | Timeout for each repair-script model request. |

### Example issue body

Copy and fill out this template when creating an issue:

```markdown
## Task

Describe the change you want ai-factory to make.

## Acceptance criteria

- [ ] Specific requirement 1
- [ ] Specific requirement 2
- [ ] `go test ./...` passes

## Notes

- Keep the change minimal and focused.
- Do not modify `go.mod` or `go.sum` only to work around the local Go toolchain version.
```

Add the labels `ai-factory` and `ai-factory-run` to the issue after you have
reviewed it.

### Trigger flow

1. A user creates a GitHub issue with the required labels.
2. The webhook or GitHub Actions workflow receives the issue event.
3. A `FactoryTask` is generated with the issue title and body as instructions.
4. The watch controller picks up the `FactoryTask` and provisions a sandbox.
5. The sandbox agent runs inside the cloned repository and executes the
   instructions.
6. The agent runs the validation command, for example `go test ./...`.
7. If validation succeeds and `ai-factory-run` is present, the controller
   creates a branch, commits the changes, pushes the branch, and opens a pull
   request.
8. The issue is updated with a comment linking to the pull request or reporting
   the result.

### Trigger safely

- Review the issue body before adding `ai-factory-run`. Use only `ai-factory` for
  dry-run validation.
- Remove `ai-factory-run` from issues you are not ready to execute.
- Use a test repository first to confirm the webhook and provider settings.
- Keep `AI_FACTORY_OPENAI_API_KEY` and `AI_FACTORY_GITHUB_TOKEN` as repository
  secrets and do not log them in issues.

## GitLab Runner + kind

The repository includes `ci/gitlab-factory-task.yml` and
`ci/gitlab-factory-task.sh` for the GitLab Runner + kind architecture. A
target GitLab project can include the CI template from this repository, set
`AI_FACTORY_ISSUE_IID`, and run the same provider-neutral webhook and
FactoryTask flow used by the GitHub Actions path.

The Runner must provide `docker`, `kind`, `kubectl`, `go`, `jq`,
`curl`, and `git`. Docker-in-Docker or a shell runner with a privileged
Docker daemon is required because kind starts Kubernetes node containers.
Keep `GITLAB_TOKEN`, `GITLAB_WEBHOOK_SECRET`, and `OPENAI_API_KEY`
masked. Set `AI_FACTORY_SOURCE_URL` to a readable GitLab mirror when the
target project does not contain the ai-factory source checkout.

For automatic Issue-to-pipeline triggering, run
`factory task webhook trigger-pipeline` as a small always-on internal service.
It validates the GitLab webhook token and label, then starts the target
pipeline with the Issue IID. The Runner remains the only component that
creates kind and executes the Agent.

## Agent runner

Webhook-generated tasks now use the issue title/body as
`spec.work.instructions`. During reconciliation, the controller runs the
configured coding agent inside the cloned repository before running validation
commands. The generated prompt is the optional `spec.agent.promptRef` file plus
the FactoryTask instructions.

The default runner is `ai-factory-agent openai-compatible`; set
`AGENT_COMMAND` for webhook-generated tasks or `spec.agent.command` on a
hand-written `FactoryTask` to use a specific runner.

`ai-factory-agent openai-compatible` reads the generated prompt from stdin,
calls a `/chat/completions` compatible endpoint, asks the model for a focused
shell script, and executes that script in the cloned repository. It uses:

| Variable | Default | Purpose |
| --- | --- | --- |
| `OPENAI_API_KEY` | required | API key for the compatible provider. |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible API base URL. |
| `OPENAI_MODEL` | `gpt-4.1` | Model name for the provider. |
| `OPENAI_TEMPERATURE` | `1` | Sampling temperature. |
| `OPENAI_MAX_TOKENS` | `48000` | Maximum response tokens for the generated script. |
| `OPENAI_MAX_TOOL_ROUNDS` | `40` | Maximum Shell tool call rounds before the model must return a script. |
| `OPENAI_MAX_FINAL_SCRIPT_ROUNDS` | `5` | Maximum no-tool final script retries if the model still attempts tool calls. |
| `OPENAI_MAX_REPAIR_ROUNDS` | `3` | Maximum generated-script repair attempts after the script exits non-zero. |
| `OPENAI_TOTAL_TIMEOUT_SECONDS` | `1800` | Total agent deadline shared by all phases. |
| `OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS` | `180` | Timeout for each exploration model request. |
| `OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS` | `90` | Timeout for each final-script model request. |
| `OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS` | `90` | Timeout for each repair-script model request. |

## Sandbox git authentication

For tasks with `spec.changeRequest.enabled=true`, the generated execution plan
configures a git credential helper inside the sandbox before cloning. By
default it expects `GITHUB_TOKEN` for GitHub and `GITLAB_TOKEN` for GitLab.
Override those names with `spec.changeRequest.authTokenEnv`.

The runtime controller reads the matching token from its own environment and
injects it into the generated `SandboxClaim` `spec.env`. The target
`SandboxTemplate` must allow environment-variable injection with
`envVarsInjectionPolicy: Allowed` or `Overrides`.

`SandboxClaim` currently supports literal env values rather than
`secretKeyRef`, so keep access to `SandboxClaim` resources restricted to the
FactoryTask controller and trusted operators.

## Permissions

The runtime service account can:

- create and patch `FactoryTask` resources
- create and watch `SandboxClaim` resources
- read pods
- execute commands in pods

Keep provider tokens scoped tightly. GitHub needs issue comment and pull request
permissions for the target repository. GitLab needs issue note and merge request
permissions for the target project.

## Troubleshooting ai-factory-run failures

This section describes common failures that can occur when an issue with the
`ai-factory-run` label is processed, and how to recover from them. The workflow
is: a GitHub issue event creates a `FactoryTask`, the watch controller runs an
agent inside a sandbox, and the controller opens a pull request when the agent
finishes successfully. Failures can happen in the webhook/GitHub Actions step,
during sandbox provisioning, inside the agent, or in the validation command.

### Missing API key

- **Symptom:** The issue receives an `ai-factory-failed` label and a comment
  saying the run failed. The run logs show an authentication error from the
  OpenAI-compatible API (for example, `401 Unauthorized` or a missing API key
  message). No pull request is created.
- **Cause:** The `AI_FACTORY_OPENAI_API_KEY` or `OPENAI_API_KEY` repository
  secret is missing, misspelled, or not readable by the workflow. The agent
  cannot call the language model without it.
- **Recovery:** Add an `AI_FACTORY_OPENAI_API_KEY` or `OPENAI_API_KEY`
  repository secret with a valid provider key. For the webhook runtime, re-run
  the installer with `OPENAI_API_KEY` set so it is written to
  `factory-task-secrets`. Re-add the `ai-factory-run` label to re-trigger the
  issue.

### source_branch_missing

- **Symptom:** The `FactoryTask` fails and the issue is labeled
  `ai-factory-failed`. The task status or log mentions
  `source_branch_missing`. The change branch and pull request are not created.
- **Cause:** The controller could not find the expected source branch in the
  cloned repository. This usually happens when the default branch name has
  changed, the repository is empty, the clone was shallow or incomplete, or
  the issue references a branch that does not exist.
- **Recovery:** Confirm the default branch exists on the remote and is not
  empty. If you changed the default branch name, update the workflow or task
  template to use the correct branch. Remove the `ai-factory-run` label and
  add it again, or re-open the issue with the label, so the webhook or
  workflow creates a fresh `FactoryTask` with a clean clone.

### command not found

- **Symptom:** The agent log or validation step reports `command not found`,
  `exec: \"...\": executable file not found in $PATH`, or a similar shell
  error. The task fails with `ai-factory-failed`.
- **Cause:** The sandbox template does not include the tool required by the
  command, the `AGENT_COMMAND` or `TASK_COMMAND` points to a binary that is
  not installed in the image, or the executable name is misspelled.
- **Recovery:** Check that the command is available in the sandbox template
  (the default is `go-dev`). For custom stacks, use a sandbox template that
  includes the required tools, or install them in the image before the agent
  runs. Verify the spelling of `AGENT_COMMAND`, `AI_FACTORY_RUN_AGENT_COMMAND`,
  and `TASK_COMMAND`. Re-trigger the issue after fixing the command or template.

### Validation failed

- **Symptom:** The agent finishes its work but the task fails with
  `ai-factory-failed`. The log shows the validation command (for example
  `go test ./...`) exited with a non-zero status. No pull request is created.
- **Cause:** The agent's changes did not satisfy the acceptance criteria or
  the repository is already in a failing state. The validation command runs in
  the sandbox after the agent completes, so any test or lint failure blocks the
  change-request path.
- **Recovery:** Read the validation output in the issue comment or run logs,
  fix the underlying test or lint failure, and re-add the `ai-factory-run`
  label. If the issue body is ambiguous, clarify the acceptance criteria so the
  agent can produce a passing change. For persistent failures, run the same
  validation command locally in a clean clone before re-triggering the issue.

## Troubleshooting ai-factory-run failures

This section describes common failures that can occur when an issue with the
`ai-factory-run` label is processed, and how to recover from them. The workflow
is: a GitHub issue event creates a `FactoryTask`, the watch controller runs an
agent inside a sandbox, and the controller opens a pull request when the agent
finishes successfully. Failures can happen in the webhook/GitHub Actions step,
during sandbox provisioning, inside the agent, or in the validation command.

### Missing API key

- **Symptom:** The issue receives an `ai-factory-failed` label and a comment
  saying the run failed. The run logs show an authentication error from the
  OpenAI-compatible API (for example, `401 Unauthorized` or a missing API key
  message). No pull request is created.
- **Cause:** The `AI_FACTORY_OPENAI_API_KEY` or `OPENAI_API_KEY` repository
  secret is missing, misspelled, or not readable by the workflow. The agent
  cannot call the language model without it.
- **Recovery:** Add an `AI_FACTORY_OPENAI_API_KEY` or `OPENAI_API_KEY`
  repository secret with a valid provider key. For the webhook runtime, re-run
  the installer with `OPENAI_API_KEY` set so it is written to
  `factory-task-secrets`. Re-add the `ai-factory-run` label to re-trigger the
  issue.

### source_branch_missing

- **Symptom:** The `FactoryTask` fails and the issue is labeled
  `ai-factory-failed`. The task status or log mentions
  `source_branch_missing`. The change branch and pull request are not created.
- **Cause:** The controller could not find the expected source branch in the
  cloned repository. This usually happens when the default branch name has
  changed, the repository is empty, the clone was shallow or incomplete, or
  the issue references a branch that does not exist.
- **Recovery:** Confirm the default branch exists on the remote and is not
  empty. If you changed the default branch name, update the workflow or task
  template to use the correct branch. Remove the `ai-factory-run` label and
  add it again, or re-open the issue with the label, so the webhook or
  workflow creates a fresh `FactoryTask` with a clean clone.

### command not found

- **Symptom:** The agent log or validation step reports `command not found`,
  `exec: \"...\": executable file not found in $PATH`, or a similar shell
  error. The task fails with `ai-factory-failed`.
- **Cause:** The sandbox template does not include the tool required by the
  command, the `AGENT_COMMAND` or `TASK_COMMAND` points to a binary that is
  not installed in the image, or the executable name is misspelled.
- **Recovery:** Check that the command is available in the sandbox template
  (the default is `go-dev`). For custom stacks, use a sandbox template that
  includes the required tools, or install them in the image before the agent
  runs. Verify the spelling of `AGENT_COMMAND`, `AI_FACTORY_RUN_AGENT_COMMAND`,
  and `TASK_COMMAND`. Re-trigger the issue after fixing the command or template.

### Validation failed

- **Symptom:** The agent finishes its work but the task fails with
  `ai-factory-failed`. The log shows the validation command (for example
  `go test ./...`) exited with a non-zero status. No pull request is created.
- **Cause:** The agent's changes did not satisfy the acceptance criteria or
  the repository is already in a failing state. The validation command runs in
  the sandbox after the agent completes, so any test or lint failure blocks the
  change-request path.
- **Recovery:** Read the validation output in the issue comment or run logs,
  fix the underlying test or lint failure, and re-add the `ai-factory-run`
  label. If the issue body is ambiguous, clarify the acceptance criteria so the
  agent can produce a passing change. For persistent failures, run the same
  validation command locally in a clean clone before re-triggering the issue.
