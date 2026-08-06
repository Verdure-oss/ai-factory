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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseValidGitHubTask(t *testing.T) {
	data := []byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: validate-ai-factory
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: validate the factory-runtime-proxy spec
`)

	task, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if task.Spec.Source.Provider != ProviderGitHub {
		t.Fatalf("provider = %q, want %q", task.Spec.Source.Provider, ProviderGitHub)
	}
}

func TestParseValidGitLabTask(t *testing.T) {
	data := []byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: validate-gitlab-project
spec:
  source:
    provider: gitlab
    host: gitlab.example.com
    repository: platform/ai/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
`)

	task, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if task.Spec.Source.Repository != "platform/ai/ai-factory" {
		t.Fatalf("repository = %q", task.Spec.Source.Repository)
	}
}

func TestParseRejectsUnsupportedProvider(t *testing.T) {
	data := []byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: bad-provider
spec:
  source:
    provider: bitbucket
    repository: team/repo
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: do something
`)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("Parse() error = nil, want unsupported provider error")
	}
	if !strings.Contains(err.Error(), "spec.source.provider") {
		t.Fatalf("error = %v, want provider validation error", err)
	}
}

func TestCloneURLOrDefault(t *testing.T) {
	tests := []struct {
		name string
		src  SourceSpec
		want string
	}{
		{
			name: "github default host",
			src: SourceSpec{
				Provider:   ProviderGitHub,
				Repository: "liyuerich/ai-factory",
			},
			want: "https://github.com/liyuerich/ai-factory.git",
		},
		{
			name: "gitlab custom host",
			src: SourceSpec{
				Provider:   ProviderGitLab,
				Host:       "gitlab.example.com",
				Repository: "platform/ai/ai-factory",
			},
			want: "https://gitlab.example.com/platform/ai/ai-factory.git",
		},
		{
			name: "explicit clone url",
			src: SourceSpec{
				Provider:   ProviderGitLab,
				Repository: "platform/ai/ai-factory",
				CloneURL:   "https://mirror.example.com/ai-factory.git",
			},
			want: "https://mirror.example.com/ai-factory.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.src.CloneURLOrDefault()
			if err != nil {
				t.Fatalf("CloneURLOrDefault() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("CloneURLOrDefault() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildExecutionPlan(t *testing.T) {
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: validate-gitlab-project
spec:
  source:
    provider: gitlab
    host: gitlab.example.com
    repository: platform/ai/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	plan, err := BuildExecutionPlan(task)
	if err != nil {
		t.Fatalf("BuildExecutionPlan() error = %v", err)
	}
	if plan.CloneURL != "https://gitlab.example.com/platform/ai/ai-factory.git" {
		t.Fatalf("CloneURL = %q", plan.CloneURL)
	}
	if plan.SandboxClaim != "validate-gitlab-project-claim" {
		t.Fatalf("SandboxClaim = %q", plan.SandboxClaim)
	}
	if plan.AgentCommand != "ai-factory-agent openai-compatible" {
		t.Fatalf("AgentCommand = %q", plan.AgentCommand)
	}
	if got, want := len(plan.Steps), 3; got != want {
		t.Fatalf("len(Steps) = %d, want %d", got, want)
	}
}

func TestBuildExecutionPlanWithChangeRequest(t *testing.T) {
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fix-docs
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
  changeRequest:
    enabled: true
    branchPrefix: ai-factory
    commitMessage: fix docs from task
    title: Fix docs
    body: Generated by ai-factory.
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	plan, err := BuildExecutionPlan(task)
	if err != nil {
		t.Fatalf("BuildExecutionPlan() error = %v", err)
	}
	if plan.ChangeBranch != "ai-factory/fix-docs" {
		t.Fatalf("ChangeBranch = %q", plan.ChangeBranch)
	}
	if plan.TargetBranch != "main" {
		t.Fatalf("TargetBranch = %q", plan.TargetBranch)
	}
	if plan.GitAuthTokenEnv != "GITHUB_TOKEN" {
		t.Fatalf("GitAuthTokenEnv = %q", plan.GitAuthTokenEnv)
	}
	if got, want := len(plan.Steps), 7; got != want {
		t.Fatalf("len(Steps) = %d, want %d", got, want)
	}
	names := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		names = append(names, step.Name)
	}
	wantNames := []string{"configure git credentials", "clone repository", "checkout base ref", "create change branch", "run command 1", "commit changes", "push change branch"}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("step names = %#v", names)
	}
	authCommand := strings.Join(plan.Steps[0].Command, " ")
	if !strings.Contains(authCommand, "GITHUB_TOKEN is required in the sandbox environment for git clone/push") {
		t.Fatalf("auth command = %#v", plan.Steps[0].Command)
	}
	runCommand := strings.Join(plan.Steps[4].Command, " ")
	if !strings.Contains(runCommand, "export PATH=/usr/local/go/bin:$PATH") {
		t.Fatalf("run command should include sandbox toolchain PATH, got %#v", plan.Steps[4].Command)
	}
	commitStep := plan.Steps[5]
	commitCommand := strings.Join(commitStep.Command, " ")
	if !strings.Contains(commitCommand, "rm -f .ai-factory/agent-prompt.md .ai-factory/task-instructions.md") {
		t.Fatalf("commit command should remove runtime prompt files before staging, got %#v", commitStep.Command)
	}
	if !strings.Contains(commitCommand, "find . -type d -name '__pycache__'") || !strings.Contains(commitCommand, "-name '*.pyc'") {
		t.Fatalf("commit command should remove Python build artifacts before staging, got %#v", commitStep.Command)
	}
	if !strings.Contains(commitCommand, "git add -A && if git diff --cached --quiet") {
		t.Fatalf("commit command should stage new files before checking for changes, got %#v", commitStep.Command)
	}
	if !strings.Contains(commitCommand, "git -c user.name='ai-factory' -c user.email='ai-factory@example.invalid' commit -m 'fix docs from task'") {
		t.Fatalf("commit command = %#v", commitStep.Command)
	}
	pushCommand := strings.Join(plan.Steps[6].Command, " ")
	if !strings.Contains(pushCommand, "git push --force-with-lease -u 'origin' 'ai-factory/fix-docs'") {
		t.Fatalf("push command = %#v", plan.Steps[6].Command)
	}
}

func TestBuildExecutionPlanWithGitLabChangeRequestAuthDefaults(t *testing.T) {
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fix-docs
spec:
  source:
    provider: gitlab
    host: gitlab.example.com
    repository: platform/ai/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
  changeRequest:
    enabled: true
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	plan, err := BuildExecutionPlan(task)
	if err != nil {
		t.Fatalf("BuildExecutionPlan() error = %v", err)
	}
	if plan.GitAuthTokenEnv != "GITLAB_TOKEN" {
		t.Fatalf("GitAuthTokenEnv = %q", plan.GitAuthTokenEnv)
	}
	if plan.GitAuthUsername != "oauth2" {
		t.Fatalf("GitAuthUsername = %q", plan.GitAuthUsername)
	}
	authCommand := strings.Join(plan.Steps[0].Command, " ")
	if !strings.Contains(authCommand, "credential.https://gitlab.example.com.helper") {
		t.Fatalf("auth command = %#v", plan.Steps[0].Command)
	}
}

func TestBuildExecutionPlanRunsCodingAgentForInstructions(t *testing.T) {
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fix-docs
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
    promptRef: .agents/builder/agent.md
    command: codex exec --full-auto
  sandbox:
    templateRef: go-dev
  work:
    instructions: |
      Update the README with setup notes.
    commands:
    - go test ./...
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	plan, err := BuildExecutionPlan(task)
	if err != nil {
		t.Fatalf("BuildExecutionPlan() error = %v", err)
	}
	if plan.AgentCommand != "codex exec --full-auto" {
		t.Fatalf("AgentCommand = %q", plan.AgentCommand)
	}
	if got, want := len(plan.Steps), 4; got != want {
		t.Fatalf("len(Steps) = %d, want %d", got, want)
	}
	if plan.Steps[2].Name != "run coding agent" {
		t.Fatalf("step[2].Name = %q", plan.Steps[2].Name)
	}
	command := strings.Join(plan.Steps[2].Command, " ")
	if !strings.Contains(command, ".agents/builder/agent.md") {
		t.Fatalf("agent command = %#v", plan.Steps[2].Command)
	}
	if !strings.Contains(command, "codex exec --full-auto") {
		t.Fatalf("agent command = %#v", plan.Steps[2].Command)
	}
	if !strings.Contains(command, "Work in a plan-first, small-step style") {
		t.Fatalf("agent command should include small-step guidance: %#v", plan.Steps[2].Command)
	}
	if strings.Contains(command, "G"+"EMINI") || strings.Contains(command, "g"+"emini") {
		t.Fatalf("agent command should not inject vendor-specific settings: %#v", plan.Steps[2].Command)
	}
}

func TestRunAgentScriptIncludesProviderNeutralSandboxTools(t *testing.T) {
	for _, agentCommand := range []string{
		"ai-factory-agent openai-compatible",
		"codex exec --full-auto",
	} {
		t.Run(agentCommand, func(t *testing.T) {
			script := runAgentScript("/workspace/repo", "Fix the issue.", "", agentCommand)
			for _, want := range []string{
				"Sandbox tool constraints",
				"Known development tools include git, go, make, node, npm, python3",
				"there is no yaml or yq command",
				"Changes made through Shell tools persist",
				"never dirname \"$0\"",
				"Do not run python3 -m py_compile or compileall",
				"do not install packages during a repair",
			} {
				if !strings.Contains(script, want) {
					t.Fatalf("agent script should include provider-neutral tool guidance %q", want)
				}
			}
		})
	}
}

func TestCommitChangesScriptRemovesPythonBuildArtifacts(t *testing.T) {
	repository := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	git("init", "--quiet")

	if err := os.WriteFile(filepath.Join(repository, "change.txt"), []byte("intended\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheDirectory := filepath.Join(repository, "pkg", "__pycache__")
	if err := os.MkdirAll(cacheDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDirectory, "module.cpython.pyc"), []byte("artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "orphan.pyo"), []byte("artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	promptDirectory := filepath.Join(repository, ".ai-factory")
	if err := os.MkdirAll(promptDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-prompt.md", "task-instructions.md"} {
		if err := os.WriteFile(filepath.Join(promptDirectory, name), []byte("runtime-only"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(
		"/bin/sh",
		"-lc",
		commitChangesScript(repository, "test cleanup", "ai-factory", "ai-factory@example.invalid"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit changes script failed: %v\n%s", err, output)
	}

	if _, err := os.Stat(cacheDirectory); !os.IsNotExist(err) {
		t.Fatalf("Python cache directory still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, "orphan.pyo")); !os.IsNotExist(err) {
		t.Fatalf("Python bytecode artifact still exists: %v", err)
	}
	trackedCommand := exec.Command("git", "-C", repository, "ls-tree", "-r", "--name-only", "HEAD")
	trackedOutput, err := trackedCommand.Output()
	if err != nil {
		t.Fatalf("list committed files: %v", err)
	}
	if got := strings.TrimSpace(string(trackedOutput)); got != "change.txt" {
		t.Fatalf("committed files = %q, want only change.txt", got)
	}
}

func TestParseRejectsConflictingChangeRequestBranchFields(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: bad-change-request
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
  changeRequest:
    enabled: true
    branchName: explicit
    branchPrefix: prefix
`))
	if err == nil {
		t.Fatal("Parse() error = nil, want conflicting branch validation error")
	}
	if !strings.Contains(err.Error(), "spec.changeRequest.branchName") {
		t.Fatalf("error = %v, want changeRequest validation error", err)
	}
}

func TestParseRejectsInvalidChangeRequestAuthTokenEnv(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: bad-change-request-auth
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
  changeRequest:
    enabled: true
    authTokenEnv: 1BAD
`))
	if err == nil {
		t.Fatal("Parse() error = nil, want authTokenEnv validation error")
	}
	if !strings.Contains(err.Error(), "spec.changeRequest.authTokenEnv") {
		t.Fatalf("error = %v, want authTokenEnv validation error", err)
	}
}

func TestParseRejectsInvalidAgentEnv(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: bad-agent-env
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
    env:
    - 1BAD
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
`))
	if err == nil {
		t.Fatal("Parse() error = nil, want agent env validation error")
	}
	if !strings.Contains(err.Error(), "spec.agent.env") {
		t.Fatalf("error = %v, want agent env validation error", err)
	}
}

func TestReconcileBuildsSandboxClaim(t *testing.T) {
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: Validate GitHub Project
  namespace: factory-system
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    commands:
    - go test ./...
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	output, err := Reconcile(task)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if output.SandboxClaim.APIVersion != sandboxClaimAPIVersion {
		t.Fatalf("APIVersion = %q", output.SandboxClaim.APIVersion)
	}
	if output.SandboxClaim.Metadata.Name != "validate-github-project-claim" {
		t.Fatalf("claim name = %q", output.SandboxClaim.Metadata.Name)
	}
	if output.SandboxClaim.Metadata.Namespace != "factory-system" {
		t.Fatalf("namespace = %q", output.SandboxClaim.Metadata.Namespace)
	}
	if got := output.SandboxClaim.Metadata.Labels[taskNameLabel]; got != "validate-github-project" {
		t.Fatalf("task label = %q", got)
	}

	// Verify lifecycle is set for auto-cleanup
	lifecycle, ok := output.SandboxClaim.Spec["lifecycle"].(map[string]interface{})
	if !ok {
		t.Fatalf("lifecycle not set in SandboxClaim spec")
	}
	if lifecycle["shutdownPolicy"] != "Delete" {
		t.Fatalf("shutdownPolicy = %v, want Delete", lifecycle["shutdownPolicy"])
	}
	if lifecycle["ttlSecondsAfterFinished"] != 300 {
		t.Fatalf("ttlSecondsAfterFinished = %v, want 300", lifecycle["ttlSecondsAfterFinished"])
	}

	data, err := output.SandboxClaimYAML()
	if err != nil {
		t.Fatalf("SandboxClaimYAML() error = %v", err)
	}
	if !strings.Contains(string(data), "kind: SandboxClaim") {
		t.Fatalf("SandboxClaimYAML() = %s", data)
	}
}

func TestReconcileInjectsGitAuthTokenEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("CODEX_API_KEY", "codex-token")
	task, err := Parse([]byte(`
apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fix-docs
  namespace: factory-system
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: builder
    env:
    - CODEX_API_KEY
  sandbox:
    templateRef: go-dev
    containerName: dev
  work:
    commands:
    - go test ./...
  changeRequest:
    enabled: true
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	output, err := Reconcile(task)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	envs, ok := output.SandboxClaim.Spec["env"].([]interface{})
	if !ok || len(envs) != 2 {
		t.Fatalf("env = %#v", output.SandboxClaim.Spec["env"])
	}
	want := map[string]string{
		"GITHUB_TOKEN":  "test-token",
		"CODEX_API_KEY": "codex-token",
	}
	for i, rawEnv := range envs {
		env, ok := rawEnv.(map[string]interface{})
		if !ok {
			t.Fatalf("env[%d] = %#v", i, rawEnv)
		}
		name, _ := env["name"].(string)
		if want[name] == "" || env["value"] != want[name] || env["containerName"] != "dev" {
			t.Fatalf("env[%d] = %#v", i, env)
		}
	}
}

func TestParseRejectsForkOwnerWithGitLab(t *testing.T) {
	data := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fork-gitlab
spec:
  source:
    provider: gitlab
    host: gitlab.com
    repository: matrixhub-ai/matrixhub
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: do something
  changeRequest:
    enabled: true
    forkOwner: Verdure-oss
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for forkOwner with gitlab provider, got nil")
	} else if !strings.Contains(err.Error(), "only supported for the github provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRejectsForkOwnerWithSlash(t *testing.T) {
	data := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fork-slash
spec:
  source:
    provider: github
    repository: matrixhub-ai/matrixhub
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: do something
  changeRequest:
    enabled: true
    forkOwner: Bad/Owner
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for forkOwner containing '/', got nil")
	} else if !strings.Contains(err.Error(), "must be a GitHub owner name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStatusMergePatch(t *testing.T) {
	patch, err := StatusMergePatch(StatusPatchOptions{
		Phase:            PhaseRunning,
		Message:          "executing generated plan",
		Reason:           "RunOnceStarted",
		SandboxClaimName: "validate-claim",
		SandboxName:      "validate-sandbox",
		Now:              time.Date(2026, 6, 30, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("StatusMergePatch() error = %v", err)
	}

	var got struct {
		Status FactoryTaskStatus `json:"status"`
	}
	if err := json.Unmarshal(patch, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.Status.Phase != PhaseRunning {
		t.Fatalf("phase = %q", got.Status.Phase)
	}
	if got.Status.StartedAt != "2026-06-30T01:02:03Z" {
		t.Fatalf("startedAt = %q", got.Status.StartedAt)
	}
	if got.Status.CompletedAt != "" {
		t.Fatalf("completedAt = %q, want empty for running phase", got.Status.CompletedAt)
	}
	if len(got.Status.Conditions) != 1 {
		t.Fatalf("conditions length = %d", len(got.Status.Conditions))
	}
	if got.Status.Conditions[0].Reason != "RunOnceStarted" {
		t.Fatalf("condition reason = %q", got.Status.Conditions[0].Reason)
	}
}
