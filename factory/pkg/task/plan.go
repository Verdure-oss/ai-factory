// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package task

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// ExecutionPlan is the normalized controller input produced from a FactoryTask.
type ExecutionPlan struct {
	TaskName        string          `yaml:"taskName"`
	Provider        string          `yaml:"provider"`
	Repository      string          `yaml:"repository"`
	CloneURL        string          `yaml:"cloneURL"`
	BaseRef         string          `yaml:"baseRef"`
	ChangeBranch    string          `yaml:"changeBranch,omitempty"`
	TargetBranch    string          `yaml:"targetBranch,omitempty"`
	GitAuthTokenEnv string          `yaml:"gitAuthTokenEnv,omitempty"`
	GitAuthUsername string          `yaml:"gitAuthUsername,omitempty"`
	AgentName       string          `yaml:"agentName"`
	AgentCommand    string          `yaml:"agentCommand"`
	AgentPromptRef  string          `yaml:"agentPromptRef,omitempty"`
	SandboxTemplate string          `yaml:"sandboxTemplate"`
	SandboxClaim    string          `yaml:"sandboxClaim"`
	ContainerName   string          `yaml:"containerName"`
	WorkDir         string          `yaml:"workDir"`
	Steps           []ExecutionStep `yaml:"steps"`
}

// ExecutionStep describes one high-level action for a future controller.
type ExecutionStep struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
}

// BuildExecutionPlan normalizes provider-specific source details into the
// provider-neutral steps a controller needs to create a sandbox and run work.
func BuildExecutionPlan(task *FactoryTask) (*ExecutionPlan, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}

	cloneURL, err := task.Spec.Source.CloneURLOrDefault()
	if err != nil {
		return nil, err
	}
	changeBranch, targetBranch, remoteName, commitMessage, authorName, authorEmail, authTokenEnv, authUsername := changeRequestDefaults(task)

	claimName := task.Spec.Sandbox.ClaimName
	if claimName == "" {
		claimName = fmt.Sprintf("%s-claim", task.Metadata.Name)
	}
	containerName := task.Spec.Sandbox.ContainerName
	if containerName == "" {
		containerName = "dev"
	}
	workDir := "/workspace/repo"
	agentCommand := task.Spec.Agent.Command
	if agentCommand == "" {
		agentCommand = "ai-factory-agent openai-compatible"
	}

	plan := &ExecutionPlan{
		TaskName:        task.Metadata.Name,
		Provider:        task.Spec.Source.Provider,
		Repository:      task.Spec.Source.Repository,
		CloneURL:        cloneURL,
		BaseRef:         task.Spec.Source.BaseRef,
		ChangeBranch:    changeBranch,
		TargetBranch:    targetBranch,
		GitAuthTokenEnv: authTokenEnv,
		GitAuthUsername: authUsername,
		AgentName:       task.Spec.Agent.Name,
		AgentCommand:    agentCommand,
		AgentPromptRef:  task.Spec.Agent.PromptRef,
		SandboxTemplate: task.Spec.Sandbox.TemplateRef,
		SandboxClaim:    claimName,
		ContainerName:   containerName,
		WorkDir:         workDir,
		Steps: []ExecutionStep{
			{
				Name:    "clean workspace",
				Command: []string{"/bin/sh", "-lc", fmt.Sprintf("rm -rf %s && mkdir -p %s", shellQuote(workDir), shellQuote("/workspace"))},
			},
			{
				Name:    "clone repository",
				Command: []string{"/bin/sh", "-lc", fmt.Sprintf("git clone %s %s", shellQuote(cloneURL), shellQuote(workDir))},
			},
			{
				Name:    "checkout base ref",
				Command: []string{"git", "-C", workDir, "checkout", task.Spec.Source.BaseRef},
			},
		},
	}

	if task.Spec.ChangeRequest.Enabled {
		host, err := cloneHost(cloneURL)
		if err != nil {
			return nil, err
		}
		plan.Steps = append([]ExecutionStep{
			{
				Name:    "configure git credentials",
				Command: []string{"/bin/sh", "-lc", configureGitCredentialsScript(host, authTokenEnv, authUsername)},
			},
			{
				Name:    "configure git proxy",
				Command: []string{"/bin/sh", "-lc", configureGitProxyScript()},
			},
		}, plan.Steps...)
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    "create change branch",
			Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch},
		})
	}

	if strings.TrimSpace(task.Spec.Work.Instructions) != "" {
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    "run coding agent",
			Command: []string{"/bin/sh", "-lc", runAgentScript(workDir, task.Spec.Work.Instructions, task.Spec.Agent.PromptRef, agentCommand)},
		})
	}

	for i, command := range task.Spec.Work.Commands {
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    fmt.Sprintf("run command %d", i+1),
			Command: []string{"/bin/sh", "-lc", fmt.Sprintf("cd %s && export PATH=/usr/local/go/bin:$PATH && %s", shellQuote(workDir), command)},
		})
	}

	if task.Spec.ChangeRequest.Enabled {
		plan.Steps = append(plan.Steps,
			ExecutionStep{
				Name:    "commit changes",
				Command: []string{"/bin/sh", "-lc", commitChangesScript(workDir, commitMessage, authorName, authorEmail)},
			},
			ExecutionStep{
				Name:    "push change branch",
				Command: []string{"/bin/sh", "-lc", pushChangeBranchScript(workDir, remoteName, changeBranch, targetBranch)},
			},
		)
	}

	return plan, nil
}

func changeRequestDefaults(task *FactoryTask) (string, string, string, string, string, string, string, string) {
	spec := task.Spec.ChangeRequest
	targetBranch := spec.TargetBranch
	if targetBranch == "" {
		targetBranch = task.Spec.Source.BaseRef
	}
	branchName := spec.BranchName
	if branchName == "" {
		prefix := spec.BranchPrefix
		if prefix == "" {
			prefix = "factory-task"
		}
		branchName = fmt.Sprintf("%s/%s", strings.Trim(prefix, "/"), dnsLabel(task.Metadata.Name))
	}
	remoteName := spec.RemoteName
	if remoteName == "" {
		remoteName = "origin"
	}
	commitMessage := spec.CommitMessage
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("Apply FactoryTask %s", task.Metadata.Name)
	}
	authorName := spec.AuthorName
	if authorName == "" {
		authorName = "ai-factory"
	}
	authorEmail := spec.AuthorEmail
	if authorEmail == "" {
		authorEmail = "ai-factory@example.invalid"
	}
	authTokenEnv := spec.AuthTokenEnv
	if authTokenEnv == "" {
		switch task.Spec.Source.Provider {
		case ProviderGitLab:
			authTokenEnv = "GITLAB_TOKEN"
		default:
			authTokenEnv = "GITHUB_TOKEN"
		}
	}
	authUsername := spec.AuthUsername
	if authUsername == "" {
		switch task.Spec.Source.Provider {
		case ProviderGitLab:
			authUsername = "oauth2"
		default:
			authUsername = "x-access-token"
		}
	}
	return branchName, targetBranch, remoteName, commitMessage, authorName, authorEmail, authTokenEnv, authUsername
}

func commitChangesScript(workDir, commitMessage, authorName, authorEmail string) string {
	return fmt.Sprintf("cd %s && rm -f .ai-factory/agent-prompt.md .ai-factory/task-instructions.md && find . -type d -name '__pycache__' -prune -exec rm -rf {} + && find . -type f \\( -name '*.pyc' -o -name '*.pyo' \\) -delete && git add -A && if git diff --cached --quiet; then echo 'No changes to commit'; else git -c user.name=%s -c user.email=%s commit -m %s; fi", shellQuote(workDir), shellQuote(authorName), shellQuote(authorEmail), shellQuote(commitMessage))
}

func pushChangeBranchScript(workDir, remoteName, branchName, targetBranch string) string {
	return fmt.Sprintf("cd %s && if [ \"$(git rev-parse HEAD)\" = \"$(git rev-parse %s)\" ]; then echo 'No change branch push needed'; else git push --force-with-lease -u %s %s; fi", shellQuote(workDir), shellQuote(targetBranch), shellQuote(remoteName), shellQuote(branchName))
}

func runAgentScript(workDir, instructions, promptRef, agentCommand string) string {
	encodedInstructions := base64.StdEncoding.EncodeToString([]byte(instructions))
	return fmt.Sprintf(`set -eu
cd %s
mkdir -p .ai-factory
printf %%s %s | base64 -d > .ai-factory/task-instructions.md
PROMPT_INPUT=.ai-factory/agent-prompt.md
: > "$PROMPT_INPUT"
if [ -n %s ]; then
  if [ ! -f %s ]; then
    printf 'agent promptRef not found: %%s\n' %s >&2
    exit 1
  fi
  cat %s >> "$PROMPT_INPUT"
  printf '\n\n' >> "$PROMPT_INPUT"
fi
cat >> "$PROMPT_INPUT" <<'EOF'
## ai-factory execution guidance

Work in a plan-first, small-step style:
- Restate a concise implementation plan before changing files.
- Prefer focused edits to one or two related files at a time.
- Avoid broad repository scans unless the task truly requires them.
- Keep generated shell scripts short and deterministic.
- Run focused validation first, then the configured final validation command.
- Changes made through Shell tools persist in the checkout. Once implementation and focused checks pass, stop exploring and return the required final response.
- Generated scripts run with the repository root as the current directory but may be stored under /tmp. Use pwd or git rev-parse --show-toplevel, never dirname "$0", to locate the repository.
- If the task is too large, implement the smallest useful slice and explain the remaining follow-up in comments or commit text.

## Sandbox tool constraints

- Known shell/core tools include bash, sh, awk, cat, cp, find, grep, head, mkdir, mv, rm, sed, sort, tail, tar, touch, tr, unzip, wc, and xargs.
- Known development tools include git, go, make, node, npm, python3, pip3, rg, jq, and curl.
- Python includes the PyYAML module (import yaml), but there is no yaml or yq command.
- Do not run python3 -m py_compile or compileall because they leave __pycache__ or .pyc build artifacts. Use compile(source, filename, "exec") or repository tests instead.
- Do not assume commands outside this list are installed, and do not install packages during a repair. Rewrite the step with available tools instead.

## FactoryTask instructions

EOF
cat .ai-factory/task-instructions.md >> "$PROMPT_INPUT"
/bin/sh -lc %s < "$PROMPT_INPUT"`,
		shellQuote(workDir),
		shellQuote(encodedInstructions),
		shellQuote(promptRef),
		shellQuote(promptRef),
		shellQuote(promptRef),
		shellQuote(promptRef),
		shellQuote(agentCommand),
	)
}

func configureGitCredentialsScript(host, tokenEnv, username string) string {
	return fmt.Sprintf(`set -eu
TOKEN_VALUE=$(printenv %s || true)
if [ -z "$TOKEN_VALUE" ]; then
  echo "%s is required in the sandbox environment for git clone/push" >&2
  exit 1
fi
mkdir -p "$HOME"
HELPER="$HOME/.git-credential-ai-factory"
cat > "$HELPER" <<'EOF'
#!/bin/sh
case "$1" in
get)
  printf 'username=%%s\n' %s
  printf 'password=%%s\n' "$(printenv %s)"
  ;;
esac
EOF
chmod 700 "$HELPER"
git config --global %s "$HELPER"
# Also configure credentials for proxy host if AI_FACTORY_GIT_PROXY is set
PROXY_URL="${AI_FACTORY_GIT_PROXY:-}"
if [ -n "$PROXY_URL" ]; then
  PROXY_HOST=$(echo "$PROXY_URL" | sed 's|https\?://||;s|/.*||')
  git config --global "credential.https://${PROXY_HOST}.helper" "$HELPER"
fi`,
		shellQuote(tokenEnv),
		tokenEnv,
		shellQuote(username),
		tokenEnv,
		shellQuote(fmt.Sprintf("credential.https://%s.helper", host)),
	)
}

func configureGitProxyScript() string {
	return `set -eu
PROXY_URL="${AI_FACTORY_GIT_PROXY:-}"
if [ -n "$PROXY_URL" ]; then
  # URL-rewriting proxy (e.g. https://ghfast.top) — rewrite GitHub URLs to go through proxy
  git config --global url."${PROXY_URL}/https://github.com/".insteadOf "https://github.com/"
  echo "git proxy configured (URL rewrite): $PROXY_URL -> github.com"
else
  echo "no git proxy configured (AI_FACTORY_GIT_PROXY not set)"
fi`
}

func cloneHost(cloneURL string) (string, error) {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("clone URL must be absolute to configure git credentials: %q", cloneURL)
	}
	return u.Host, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
