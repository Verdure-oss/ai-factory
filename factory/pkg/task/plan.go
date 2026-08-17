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
	"errors"
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

// CISessionFile is the sandbox-local path where the coding agent persists its
// session when AI_FACTORY_SESSION_FILE is set. The main task writes a compact
// snapshot (task instructions, changed files, change stat, final script) that
// CI repair rounds load as their starting context; repair rounds are read-only.
// The main task agent writes it; the CI repair agent loads it (BuildCIRepairScript
// exports the same path) so the repair round inherits the main task's codebase
// knowledge without re-exploring. /tmp is used so the session never pollutes
// the repository checkout; both runs share the same sandbox container, so the
// file survives between the two kubectl exec calls.
const CISessionFile = "/tmp/ai-factory-session.json"

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

	useFork := task.Spec.ChangeRequest.ForkOwner != ""

	// Use a shallow clone (--depth 1) to keep the initial transfer small. Full
	// history is not needed for the change-request workflow, and a shallow clone
	// avoids timeouts when cloning through a slow/throttled git proxy. In
	// non-fork mode the clone targets the base ref branch directly so the
	// subsequent base-ref checkout has the branch available.
	cloneArgs := "--depth 1"
	if !useFork && strings.TrimSpace(task.Spec.Source.BaseRef) != "" {
		cloneArgs += " --branch " + shellQuote(task.Spec.Source.BaseRef)
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
				Command: []string{"/bin/sh", "-lc", fmt.Sprintf("git -c http.version=HTTP/1.1 clone %s %s %s", cloneArgs, shellQuote(cloneURL), shellQuote(workDir))},
			},
		},
	}

	if !useFork {
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    "checkout base ref",
			Command: []string{"git", "-C", workDir, "checkout", task.Spec.Source.BaseRef},
		})
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
		if useFork {
			upstreamURL, err := upstreamRepoURL(task)
			if err != nil {
				return nil, err
			}
			plan.Steps = append(plan.Steps, forkBranchSetupSteps(workDir, changeBranch, targetBranch, upstreamURL)...)
		} else {
			plan.Steps = append(plan.Steps, ExecutionStep{
				Name:    "create change branch",
				Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch},
			})
		}
	}

	if strings.TrimSpace(task.Spec.Work.Instructions) != "" {
		// Dedicated agent step. The session file env var lets the agent load a
		// prior session (CI repair round) and, more importantly, dump its own
		// conversation at the end so a later repair round inherits context. It
		// is set unconditionally — the agent is already backward compatible and
		// simply skips dump/load when the var is unset.
		agentScript := fmt.Sprintf("export AI_FACTORY_SESSION_FILE=%s\n%s", shellQuote(CISessionFile),
			runAgentScript(workDir, task.Spec.Work.Instructions, task.Spec.Agent.PromptRef, agentCommand))
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    "run coding agent",
			Command: []string{"/bin/sh", "-lc", agentScript},
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

func upstreamRepoURL(task *FactoryTask) (string, error) {
	host := task.Spec.Source.Host
	switch task.Spec.Source.Provider {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderGitLab:
		if host == "" {
			host = "gitlab.com"
		}
	default:
		return "", fmt.Errorf("unsupported git provider %q", task.Spec.Source.Provider)
	}
	return fmt.Sprintf("https://%s/%s.git", host, strings.TrimPrefix(task.Spec.Source.Repository, "/")), nil
}

func forkBranchSetupSteps(workDir, changeBranch, targetBranch, upstreamURL string) []ExecutionStep {
	addUpstream := fmt.Sprintf("git -C %s remote add upstream %s 2>/dev/null || git -C %s remote set-url upstream %s",
		shellQuote(workDir), shellQuote(upstreamURL), shellQuote(workDir), shellQuote(upstreamURL))
	fetchUpstream := fmt.Sprintf("git -C %s fetch --depth 1 upstream %s", shellQuote(workDir), shellQuote(targetBranch))
	return []ExecutionStep{
		{
			Name:    "add upstream remote",
			Command: []string{"/bin/sh", "-lc", addUpstream},
		},
		{
			Name:    "fetch upstream",
			Command: []string{"/bin/sh", "-lc", fetchUpstream},
		},
		{
			Name:    "checkout upstream branch",
			Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch, fmt.Sprintf("upstream/%s", targetBranch)},
		},
	}
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
	// Use a plain --force push, not --force-with-lease. The change branch is
	// always regenerated from the latest base ref (never edited incrementally
	// or merged with remote history), and the branch name is deterministic per
	// issue (factory-task/<repo>-<issue>). In that model the lease check of
	// --force-with-lease becomes unreliable: on a fresh shallow clone that only
	// fetches the base ref, the remote-tracking ref for the change branch is
	// absent, so a re-run reports a bogus "stale info" rejection. --force
	// handles both the first push (creates the branch) and re-runs (overwrites
	// it, updating the same PR) without requiring a lease.
	return fmt.Sprintf("cd %s && if [ \"$(git rev-parse HEAD)\" = \"$(git rev-parse %s)\" ]; then echo 'No change branch push needed'; else git -c http.version=HTTP/1.1 push --force -u %s %s; fi", shellQuote(workDir), shellQuote(targetBranch), shellQuote(remoteName), shellQuote(branchName))
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
- **DO NOT run git commit, git push, git add, or any git commands that modify repository state.** You can use git status, git diff, git log, git branch for reading, but NEVER commit or push. The system will handle commit and push after validation passes.

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
  git config --global url."${PROXY_URL}/https://github.com/".insteadOf "https://github.com/"
  echo "git proxy configured: $PROXY_URL -> github.com"
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

// CIRepairOptions controls the CI-failure repair script's environment overrides.
type CIRepairOptions struct {
	SessionFile   string // /tmp/ai-factory-session.json; "" = no session
	MaxToolRounds int    // override OPENAI_MAX_TOOL_ROUNDS in the repair agent; <=0 = inherit
}

// BuildCIRepairScript builds a single shell script that runs the coding agent
// with CI-failure repair instructions against the existing checkout, then
// commits and force-pushes the fix to the change branch (updating the PR).
// The agent never commits or pushes; this script runs them after the agent.
func BuildCIRepairScript(task *FactoryTask, repairInstructions string, opts CIRepairOptions) (string, error) {
	if task == nil {
		return "", errors.New("FactoryTask is required")
	}
	workDir := "/workspace/repo"
	agentCommand := task.Spec.Agent.Command
	if agentCommand == "" {
		agentCommand = "ai-factory-agent openai-compatible"
	}
	changeBranch, _, remoteName, commitMessage, authorName, authorEmail, _, _ := changeRequestDefaults(task)
	envSetup := ""
	if opts.SessionFile != "" {
		envSetup += fmt.Sprintf("export AI_FACTORY_SESSION_FILE=%s\n", shellQuote(opts.SessionFile))
		// Repair rounds load the main task's snapshot but must not write their
		// own session back to it: every pass would append the previous pass,
		// and the shared file overflows the model input window by the third
		// repair (HTTP 400 "Range of input length should be [1, ...]" — the
		// observed "third repair always fails, nothing to commit").
		envSetup += "export AI_FACTORY_SESSION_READONLY=1\n"
	}
	if opts.MaxToolRounds > 0 {
		envSetup += fmt.Sprintf("export OPENAI_MAX_TOOL_ROUNDS=%d\n", opts.MaxToolRounds)
	}
	// The repair sandbox is offline: a `go` command against a module that
	// requires a newer toolchain would try to download it from proxy.golang.org
	// and hang on a network timeout. Pin the local toolchain so any such
	// attempt fails fast instead of burning the repair budget; the repair
	// prompt already forbids go-based validation for the same reason.
	envSetup += "export GOTOOLCHAIN=local\n"
	script := fmt.Sprintf("set -eu\n%s%s\n%s\n%s",
		envSetup,
		runAgentScript(workDir, repairInstructions, task.Spec.Agent.PromptRef, agentCommand),
		commitChangesScript(workDir, commitMessage, authorName, authorEmail),
		pushChangeBranchScript(workDir, remoteName, changeBranch, task.Spec.Source.BaseRef),
	)
	return script, nil
}
