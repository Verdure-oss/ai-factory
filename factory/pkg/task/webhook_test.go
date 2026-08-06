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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestFactoryTaskFromGitHubIssueWebhook(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(githubIssuePayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		Namespace:          "factory-system",
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		Commands:           []string{"go test ./..."},
		RequiredLabels:     []string{"ai-factory"},
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	if task.Metadata.Name != "github-liyuerich-ai-factory-42" {
		t.Fatalf("metadata.name = %q", task.Metadata.Name)
	}
	if task.Metadata.Namespace != "factory-system" {
		t.Fatalf("metadata.namespace = %q", task.Metadata.Namespace)
	}
	if task.Spec.Source.Provider != ProviderGitHub {
		t.Fatalf("source.provider = %q", task.Spec.Source.Provider)
	}
	if task.Spec.Source.Repository != "liyuerich/ai-factory" {
		t.Fatalf("source.repository = %q", task.Spec.Source.Repository)
	}
	if task.Spec.Source.BaseRef != "main" {
		t.Fatalf("source.baseRef = %q", task.Spec.Source.BaseRef)
	}
	if task.Spec.Trigger.URL != "https://github.com/liyuerich/ai-factory/issues/42" {
		t.Fatalf("trigger.url = %q", task.Spec.Trigger.URL)
	}
	if task.Spec.Reporting.TargetURL != task.Spec.Trigger.URL {
		t.Fatalf("reporting.targetURL = %q", task.Spec.Reporting.TargetURL)
	}
	if !strings.Contains(task.Spec.Work.Instructions, "Add webhook support") {
		t.Fatalf("instructions = %q", task.Spec.Work.Instructions)
	}
	if len(task.Spec.Work.Commands) != 1 || task.Spec.Work.Commands[0] != "go test ./..." {
		t.Fatalf("commands = %#v", task.Spec.Work.Commands)
	}
}

func TestFactoryTaskFromGitHubIssueWebhookWithChangeRequest(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(githubIssuePayload), IssueWebhookOptions{
		Provider:                  ProviderGitHub,
		AgentName:                 "builder",
		AgentCommand:              "codex exec --full-auto",
		AgentEnv:                  []string{"CODEX_API_KEY"},
		SandboxTemplateRef:        "go-dev",
		ChangeRequestEnabled:      true,
		ChangeRequestAuthTokenEnv: "AI_FACTORY_GITHUB_TOKEN",
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	if task.Spec.Agent.Command != "codex exec --full-auto" {
		t.Fatalf("agent.command = %q", task.Spec.Agent.Command)
	}
	if len(task.Spec.Agent.Env) != 1 || task.Spec.Agent.Env[0] != "CODEX_API_KEY" {
		t.Fatalf("agent.env = %#v", task.Spec.Agent.Env)
	}
	if !task.Spec.ChangeRequest.Enabled {
		t.Fatal("changeRequest.enabled = false")
	}
	if task.Spec.ChangeRequest.BranchPrefix != "factory-task" {
		t.Fatalf("branchPrefix = %q", task.Spec.ChangeRequest.BranchPrefix)
	}
	if task.Spec.ChangeRequest.AuthTokenEnv != "AI_FACTORY_GITHUB_TOKEN" {
		t.Fatalf("authTokenEnv = %q", task.Spec.ChangeRequest.AuthTokenEnv)
	}
	if !strings.Contains(task.Spec.ChangeRequest.CommitMessage, "Add webhook support") {
		t.Fatalf("commitMessage = %q", task.Spec.ChangeRequest.CommitMessage)
	}
}

func TestFactoryTaskFromGitHubIssueFormFormatsInstructions(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(githubIssueFormPayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		RequiredLabels:     []string{"ai-factory"},
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	instructions := task.Spec.Work.Instructions
	for _, want := range []string{
		"Task\nAdd a complete issue trigger example.",
		"Acceptance Criteria\n- [ ] Documents labels",
		"Allow creating a pull request\nYes",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "###") {
		t.Fatalf("instructions still contain issue form headings:\n%s", instructions)
	}
	if strings.Contains(instructions, "_No response_") {
		t.Fatalf("instructions still contain no-response placeholder:\n%s", instructions)
	}
}

func TestFactoryTaskFromGitHubIssueEscapedNewlines(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(githubIssueEscapedNewlinePayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		RequiredLabels:     []string{"ai-factory"},
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	instructions := task.Spec.Work.Instructions
	if strings.Contains(instructions, `\n`) {
		t.Fatalf("instructions still contain escaped newlines:\n%s", instructions)
	}
	for _, want := range []string{
		"Goal\nMake ai-factory safe to trigger repeatedly.",
		"Scope\n- Detect an existing source branch.",
		"Acceptance criteria\n- Re-triggering the same issue does not create duplicate PRs.",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instructions)
		}
	}
}

func TestNormalizeLiteralNewlines(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "escaped newlines",
			body: `Goal\nDo the work.\n\nScope\n- Keep it small.`,
			want: "Goal\nDo the work.\n\nScope\n- Keep it small.",
		},
		{
			name: "already formatted",
			body: "Goal\nDo the work.\n\nScope\n- Keep it small.",
			want: "Goal\nDo the work.\n\nScope\n- Keep it small.",
		},
		{
			name: "no escaped newlines",
			body: "Do the work.",
			want: "Do the work.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeLiteralNewlines(tt.body); got != tt.want {
				t.Fatalf("normalizeLiteralNewlines() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFactoryTaskFromGitHubIssueWebhookIgnoredWithoutRequiredLabel(t *testing.T) {
	_, err := FactoryTaskFromIssueWebhook([]byte(githubIssuePayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		RequiredLabels:     []string{"needs-ai"},
	})
	if err == nil {
		t.Fatal("FactoryTaskFromIssueWebhook() error = nil, want ignored error")
	}
	if _, ok := err.(*IgnoredIssueWebhookError); !ok {
		t.Fatalf("error type = %T, want *IgnoredIssueWebhookError", err)
	}
}

func TestFactoryTaskFromIssueWebhookIgnoredForUnsupportedAction(t *testing.T) {
	_, err := FactoryTaskFromIssueWebhook([]byte(githubClosedIssuePayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		RequiredLabels:     []string{"ai-factory"},
	})
	if err == nil {
		t.Fatal("FactoryTaskFromIssueWebhook() error = nil, want ignored error")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("error = %v, want closed action reason", err)
	}
}

// TestFactoryTaskFromGitHubIssueWebhookIgnoresSystemLabelTrigger verifies that
// when the system adds its own label (e.g., ai-factory-running), the webhook
// is ignored even though the issue also has the trigger label (ai-factory-run).
// This prevents duplicate comment/label operations caused by webhook chaining.
func TestFactoryTaskFromGitHubIssueWebhookIgnoresSystemLabelTrigger(t *testing.T) {
	// This payload simulates what GitHub sends when the system adds ai-factory-running:
	// - action: "labeled"
	// - label.name: "ai-factory-running" (the triggering label)
	// - issue.labels: includes BOTH ai-factory-run and ai-factory-running (all current labels)
	payload := `{
  "action": "labeled",
  "label": {"name": "ai-factory-running"},
  "issue": {
    "number": 42,
    "title": "Test issue",
    "body": "Test body",
    "html_url": "https://github.com/test/repo/issues/42",
    "user": {"login": "testuser"},
    "labels": [
      {"name": "ai-factory"},
      {"name": "ai-factory-run"},
      {"name": "ai-factory-running"}
    ]
  },
  "repository": {
    "full_name": "test/repo",
    "html_url": "https://github.com/test/repo",
    "clone_url": "https://github.com/test/repo.git",
    "default_branch": "main"
  },
  "sender": {"login": "testuser"}
}`
	_, err := FactoryTaskFromIssueWebhook([]byte(payload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		TriggerActions:     []string{"labeled"},
		RequiredLabels:     []string{"ai-factory-run", "ai-factory-smoke"},
		RequireAllOf:       []string{"ai-factory"},
	})
	if err == nil {
		t.Fatal("FactoryTaskFromIssueWebhook() error = nil, want ignored error for system label trigger")
	}
	if _, ok := err.(*IgnoredIssueWebhookError); !ok {
		t.Fatalf("error type = %T, want *IgnoredIssueWebhookError", err)
	}
	if !strings.Contains(err.Error(), "required label") {
		t.Fatalf("error = %v, want required label reason", err)
	}
}

// TestFactoryTaskFromGitHubIssueWebhookAcceptsUserLabelTrigger verifies that
// when the user adds a trigger label (e.g., ai-factory-run), the webhook is processed.
func TestFactoryTaskFromGitHubIssueWebhookAcceptsUserLabelTrigger(t *testing.T) {
	payload := `{
  "action": "labeled",
  "label": {"name": "ai-factory-run"},
  "issue": {
    "number": 42,
    "title": "Test issue",
    "body": "Test body",
    "html_url": "https://github.com/test/repo/issues/42",
    "user": {"login": "testuser"},
    "labels": [
      {"name": "ai-factory"},
      {"name": "ai-factory-run"}
    ]
  },
  "repository": {
    "full_name": "test/repo",
    "html_url": "https://github.com/test/repo",
    "clone_url": "https://github.com/test/repo.git",
    "default_branch": "main"
  },
  "sender": {"login": "testuser"}
}`
	task, err := FactoryTaskFromIssueWebhook([]byte(payload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		TriggerActions:     []string{"labeled"},
		RequiredLabels:     []string{"ai-factory-run", "ai-factory-smoke"},
		RequireAllOf:       []string{"ai-factory"},
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v, want nil", err)
	}
	if task.Spec.Trigger.ID != "42" {
		t.Fatalf("Trigger.ID = %q, want 42", task.Spec.Trigger.ID)
	}
}

func TestFactoryTaskFromGitLabIssueWebhook(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(gitlabIssuePayload), IssueWebhookOptions{
		Provider:           ProviderGitLab,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
		RequiredLabels:     []string{"ai-factory"},
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	if task.Metadata.Name != "gitlab-platform-ai-ai-factory-7" {
		t.Fatalf("metadata.name = %q", task.Metadata.Name)
	}
	if task.Spec.Source.Host != "gitlab.example.com" {
		t.Fatalf("source.host = %q", task.Spec.Source.Host)
	}
	if task.Spec.Source.Repository != "platform/ai/ai-factory" {
		t.Fatalf("source.repository = %q", task.Spec.Source.Repository)
	}
	if task.Spec.Trigger.Actor != "yueli" {
		t.Fatalf("trigger.actor = %q", task.Spec.Trigger.Actor)
	}
	if task.Spec.Reporting.Provider != ProviderGitLab {
		t.Fatalf("reporting.provider = %q", task.Spec.Reporting.Provider)
	}
}

func TestShouldTriggerIssueRepositoryFilter(t *testing.T) {
	event, err := ParseIssueWebhook([]byte(gitlabIssuePayload), ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if ok, reason := ShouldTriggerIssue(event, IssueWebhookOptions{Repositories: []string{"platform/ai/ai-factory"}}); !ok {
		t.Fatalf("ShouldTriggerIssue() = false, reason %q", reason)
	}
	if ok, reason := ShouldTriggerIssue(event, IssueWebhookOptions{Repositories: []string{"other/repo"}}); ok || !strings.Contains(reason, "repository") {
		t.Fatalf("ShouldTriggerIssue() = %v, reason %q; want repository ignore", ok, reason)
	}
}

func TestFactoryTaskYAMLRoundTrip(t *testing.T) {
	task, err := FactoryTaskFromIssueWebhook([]byte(githubIssuePayload), IssueWebhookOptions{
		Provider:           ProviderGitHub,
		AgentName:          "builder",
		SandboxTemplateRef: "go-dev",
	})
	if err != nil {
		t.Fatalf("FactoryTaskFromIssueWebhook() error = %v", err)
	}
	data, err := FactoryTaskYAML(task)
	if err != nil {
		t.Fatalf("FactoryTaskYAML() error = %v", err)
	}
	if _, err := Parse(data); err != nil {
		t.Fatalf("Parse(FactoryTaskYAML()) error = %v\n%s", err, data)
	}
}

func TestVerifyGitHubWebhookSignature(t *testing.T) {
	body := []byte(githubIssuePayload)
	secret := "top-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifyGitHubWebhookSignature(secret, body, signature); err != nil {
		t.Fatalf("VerifyGitHubWebhookSignature() error = %v", err)
	}
	if err := VerifyGitHubWebhookSignature(secret, body, "sha256=bad"); err == nil {
		t.Fatal("VerifyGitHubWebhookSignature() error = nil, want mismatch")
	}
}

func TestVerifyGitLabWebhookToken(t *testing.T) {
	if err := VerifyGitLabWebhookToken("top-secret", "top-secret"); err != nil {
		t.Fatalf("VerifyGitLabWebhookToken() error = %v", err)
	}
	if err := VerifyGitLabWebhookToken("top-secret", "wrong"); err == nil {
		t.Fatal("VerifyGitLabWebhookToken() error = nil, want mismatch")
	}
}

const githubIssuePayload = `{
  "action": "opened",
  "issue": {
    "number": 42,
    "title": "Add webhook support",
    "body": "Convert issues into FactoryTasks.",
    "html_url": "https://github.com/liyuerich/ai-factory/issues/42",
    "user": {"login": "yueli"},
    "labels": [{"name": "ai-factory"}]
  },
  "repository": {
    "full_name": "liyuerich/ai-factory",
    "html_url": "https://github.com/liyuerich/ai-factory",
    "clone_url": "https://github.com/liyuerich/ai-factory.git",
    "default_branch": "main"
  },
  "sender": {"login": "liyuerich"}
}`

const githubClosedIssuePayload = `{
  "action": "closed",
  "issue": {
    "number": 42,
    "title": "Add webhook support",
    "body": "Convert issues into FactoryTasks.",
    "html_url": "https://github.com/liyuerich/ai-factory/issues/42",
    "user": {"login": "yueli"},
    "labels": [{"name": "ai-factory"}]
  },
  "repository": {
    "full_name": "liyuerich/ai-factory",
    "html_url": "https://github.com/liyuerich/ai-factory",
    "clone_url": "https://github.com/liyuerich/ai-factory.git",
    "default_branch": "main"
  },
  "sender": {"login": "liyuerich"}
}`

const githubIssueFormPayload = `{
  "action": "opened",
  "issue": {
    "number": 43,
    "title": "Add issue trigger docs",
    "body": "### Task\n\nAdd a complete issue trigger example.\n\n### Requirements\n\n_No response_\n\n### Acceptance Criteria\n\n- [ ] Documents labels\n- [ ] Documents secrets\n\n### Allow creating a pull request\n\nYes\n",
    "html_url": "https://github.com/liyuerich/ai-factory/issues/43",
    "user": {"login": "yueli"},
    "labels": [{"name": "ai-factory"}]
  },
  "repository": {
    "full_name": "liyuerich/ai-factory",
    "html_url": "https://github.com/liyuerich/ai-factory",
    "clone_url": "https://github.com/liyuerich/ai-factory.git",
    "default_branch": "main"
  },
  "sender": {"login": "liyuerich"}
}`

const githubIssueEscapedNewlinePayload = `{
  "action": "opened",
  "issue": {
    "number": 44,
    "title": "Make change requests idempotent",
    "body": "Goal\\nMake ai-factory safe to trigger repeatedly.\\n\\nScope\\n- Detect an existing source branch.\\n\\nAcceptance criteria\\n- Re-triggering the same issue does not create duplicate PRs.",
    "html_url": "https://github.com/liyuerich/ai-factory/issues/44",
    "user": {"login": "yueli"},
    "labels": [{"name": "ai-factory"}]
  },
  "repository": {
    "full_name": "liyuerich/ai-factory",
    "html_url": "https://github.com/liyuerich/ai-factory",
    "clone_url": "https://github.com/liyuerich/ai-factory.git",
    "default_branch": "main"
  },
  "sender": {"login": "liyuerich"}
}`

const gitlabIssuePayload = `{
  "object_kind": "issue",
  "event_type": "issue",
  "user": {"username": "yueli", "name": "Yue Li"},
  "project": {
    "path_with_namespace": "platform/ai/ai-factory",
    "web_url": "https://gitlab.example.com/platform/ai/ai-factory",
    "default_branch": "main",
    "git_http_url": "https://gitlab.example.com/platform/ai/ai-factory.git"
  },
  "object_attributes": {
    "iid": 7,
    "title": "Run agent task",
    "description": "Please validate this project.",
    "url": "https://gitlab.example.com/platform/ai/ai-factory/-/issues/7",
    "action": "open",
    "labels": [{"title": "ai-factory"}]
  }
}`

func TestFactoryTaskFromGitHubIssueWebhookWithForkOwner(t *testing.T) {
	payload := []byte(`{
		"action": "labeled",
		"issue": {"number": 7, "title": "Fix bug", "html_url": "https://github.com/matrixhub-ai/matrixhub/issues/7", "labels": [{"name": "ai-factory"}, {"name": "ai-factory-run"}]},
		"repository": {"full_name": "matrixhub-ai/matrixhub", "html_url": "https://github.com/matrixhub-ai/matrixhub", "clone_url": "https://github.com/matrixhub-ai/matrixhub.git", "default_branch": "main"},
		"sender": {"login": "someone"}
	}`)
	opts := IssueWebhookOptions{
		Provider:             ProviderGitHub,
		Repositories:         []string{"matrixhub-ai/matrixhub"},
		RequiredLabels:       []string{"ai-factory-run"},
		ChangeRequestEnabled: true,
		ForkOwner:            "Verdure-oss",
	}
	task, err := FactoryTaskFromIssueWebhook(payload, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Spec.ChangeRequest.ForkOwner != "Verdure-oss" {
		t.Fatalf("got forkOwner %q, want Verdure-oss", task.Spec.ChangeRequest.ForkOwner)
	}
	if want := "https://github.com/Verdure-oss/matrixhub.git"; task.Spec.Source.CloneURL != want {
		t.Fatalf("got cloneURL %q, want %q", task.Spec.Source.CloneURL, want)
	}
	if task.Spec.Source.Repository != "matrixhub-ai/matrixhub" {
		t.Fatalf("repository must stay upstream, got %q", task.Spec.Source.Repository)
	}
}
