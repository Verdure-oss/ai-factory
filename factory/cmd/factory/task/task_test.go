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
	"bytes"
	"encoding/json"
	"fmt"
	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldReconcile(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		retryFailed bool
		want        bool
	}{
		{name: "empty phase", phase: "", want: true},
		{name: "pending phase", phase: taskpkg.PhasePending, want: true},
		{name: "claim created phase", phase: taskpkg.PhaseClaimCreated, want: true},
		{name: "sandbox ready phase", phase: taskpkg.PhaseSandboxReady, want: true},
		{name: "running phase", phase: taskpkg.PhaseRunning, want: false},
		{name: "succeeded phase", phase: taskpkg.PhaseSucceeded, want: false},
		{name: "failed without retry", phase: taskpkg.PhaseFailed, want: false},
		{name: "failed with retry", phase: taskpkg.PhaseFailed, retryFailed: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldReconcile(taskpkg.FactoryTask{
				Status: taskpkg.FactoryTaskStatus{Phase: tt.phase},
			}, tt.retryFailed)
			if got != tt.want {
				t.Fatalf("shouldReconcile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFactoryTaskListJSON(t *testing.T) {
	data := []byte(`{
	  "apiVersion": "factory.ai.gke.io/v1alpha1",
	  "kind": "FactoryTaskList",
	  "items": [
	    {
	      "apiVersion": "factory.ai.gke.io/v1alpha1",
	      "kind": "FactoryTask",
	      "metadata": {
	        "name": "validate-ai-factory",
	        "namespace": "factory-system"
	      },
	      "spec": {
	        "source": {
	          "provider": "github",
	          "repository": "liyuerich/ai-factory",
	          "baseRef": "main"
	        },
	        "agent": {
	          "name": "builder"
	        },
	        "sandbox": {
	          "templateRef": "go-dev"
	        },
	        "work": {
	          "commands": ["go test ./..."]
	        }
	      },
	      "status": {
	        "phase": "Pending"
	      }
	    }
	  ]
	}`)

	var list factoryTaskList
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len(items) = %d", len(list.Items))
	}
	item := list.Items[0]
	if item.Metadata.Name != "validate-ai-factory" {
		t.Fatalf("metadata.name = %q", item.Metadata.Name)
	}
	if item.Spec.Source.BaseRef != "main" {
		t.Fatalf("spec.source.baseRef = %q", item.Spec.Source.BaseRef)
	}
	if item.Status.Phase != taskpkg.PhasePending {
		t.Fatalf("status.phase = %q", item.Status.Phase)
	}
}

func TestReportingProviderFallsBackToSource(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Spec: taskpkg.FactoryTaskSpec{
			Source: taskpkg.SourceSpec{Provider: taskpkg.ProviderGitHub},
		},
	}
	if got := reportingProvider(task); got != taskpkg.ProviderGitHub {
		t.Fatalf("reportingProvider() = %q, want %q", got, taskpkg.ProviderGitHub)
	}

	task.Spec.Reporting.Provider = taskpkg.ProviderGitLab
	if got := reportingProvider(task); got != taskpkg.ProviderGitLab {
		t.Fatalf("reportingProvider() = %q, want %q", got, taskpkg.ProviderGitLab)
	}
}

func TestBuildReportMessage(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{
			Name:      "validate-ai-factory",
			Namespace: "factory-system",
		},
	}
	got := buildReportMessageFromString(task, taskpkg.PhaseSucceeded, "done")
	want := "FactoryTask `factory-system/validate-ai-factory` Succeeded\n\ndone"
	if got != want {
		t.Fatalf("buildReportMessage() = %q, want %q", got, want)
	}
}

func TestBuildReportMessageClassifiesAgentFailures(t *testing.T) {
	task := &taskpkg.FactoryTask{Metadata: taskpkg.ObjectMeta{Name: "validate-ai-factory"}}
	got := buildReportMessageFromString(task, taskpkg.PhaseFailed, "OpenAI-compatible model reached max tool rounds (40)")
	if !strings.Contains(got, "Likely cause: The agent used all available shell tool rounds") {
		t.Fatalf("buildReportMessage() = %q", got)
	}
}

func TestChangeRequestReportMessageIncludesReviewContext(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "github-liyuerich-ai-factory-29"},
		Spec: taskpkg.FactoryTaskSpec{
			Source: taskpkg.SourceSpec{
				Provider: taskpkg.ProviderGitHub,
				BaseRef:  "main",
			},
			Work: taskpkg.WorkSpec{Commands: []string{"go test ./..."}},
			ChangeRequest: taskpkg.ChangeRequestSpec{
				Enabled:      true,
				BranchPrefix: "factory-task",
			},
		},
	}
	got := changeRequestReportMessage(task, "https://github.com/liyuerich/ai-factory/pull/30", true, nil)
	for _, want := range []string{
		"An existing change request is already open",
		"**GitHub pull request:** https://github.com/liyuerich/ai-factory/pull/30",
		"`go test ./...` passed",
		"Source: `factory-task/github-liyuerich-ai-factory-29`",
		"Target: `main`",
		"### Changed files",
		"### Next steps",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("changeRequestReportMessage() missing %q:\n%s", want, got)
		}
	}
}

func TestChangeRequestReportMessageListsChangedFiles(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "fix-docs"},
		Spec: taskpkg.FactoryTaskSpec{
			Source: taskpkg.SourceSpec{
				Provider: taskpkg.ProviderGitLab,
				BaseRef:  "main",
			},
			Work: taskpkg.WorkSpec{Commands: []string{"go test ./..."}},
			ChangeRequest: taskpkg.ChangeRequestSpec{
				Enabled:      true,
				BranchPrefix: "factory-task",
			},
		},
	}
	got := changeRequestReportMessage(task, "https://gitlab.example.com/platform/ai/ai-factory/-/merge_requests/8", false, []string{
		"factory/pkg/task/task.go",
		"factory/cmd/factory/task/task.go",
	})
	for _, want := range []string{
		"A change request was created for this FactoryTask.",
		"**GitLab merge request:** https://gitlab.example.com/platform/ai/ai-factory/-/merge_requests/8",
		"- `factory/pkg/task/task.go`",
		"- `factory/cmd/factory/task/task.go`",
		"`go test ./...` passed",
		"Review the change request",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("changeRequestReportMessage() missing %q:\n%s", want, got)
		}
	}
}

func TestValidateChangeRequestResult(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Spec: taskpkg.FactoryTaskSpec{
			ChangeRequest: taskpkg.ChangeRequestSpec{Enabled: true},
		},
	}
	tests := []struct {
		name          string
		enabled       bool
		taskEnabled   bool
		resultURL     string
		alreadyExists bool
		wantErr       bool
	}{
		{name: "missing result", enabled: true, taskEnabled: true, wantErr: true},
		{name: "created result", enabled: true, taskEnabled: true, resultURL: "https://github.com/example/repo/pull/1"},
		{name: "existing result", enabled: true, taskEnabled: true, alreadyExists: true},
		{name: "change request disabled by controller", enabled: false, taskEnabled: true},
		{name: "change request disabled by task", enabled: true, taskEnabled: false},
	}
	taskDisabled := *task
	taskDisabled.Spec.ChangeRequest.Enabled = false
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := task
			if !tt.taskEnabled {
				candidate = &taskDisabled
			}
			err := validateChangeRequestResult(candidate, tt.enabled, tt.resultURL, tt.alreadyExists)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateChangeRequestResult() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "no change request created") {
				t.Fatalf("error = %q, want no-change diagnostic", err)
			}
		})
	}
}

func TestNoChangeTaskIsNotReportedAsSuccessful(t *testing.T) {
	task := &taskpkg.FactoryTask{
		Spec: taskpkg.FactoryTaskSpec{
			ChangeRequest: taskpkg.ChangeRequestSpec{Enabled: true},
		},
	}
	err := validateChangeRequestResult(task, true, "", false)
	if err == nil {
		t.Fatal("no-change task returned success, want failure")
	}
	failure := taskpkg.ClassifyFailure(err.Error())
	if failure.Reason != taskpkg.NoChangeRequest {
		t.Fatalf("failure reason = %q, want %q", failure.Reason, taskpkg.NoChangeRequest)
	}
	if !strings.Contains(taskpkg.FriendlyFailureMessage(failure), err.Error()) {
		t.Fatal("no-change failure did not preserve the provider diagnostic")
	}
}

func TestIssueWebhookHandlerIgnoresMissingRequiredLabel(t *testing.T) {
	previousOptions := webhookOptions
	previousServeOptions := webhookServeOptions
	defer func() {
		webhookOptions = previousOptions
		webhookServeOptions = previousServeOptions
	}()

	webhookOptions = struct {
		provider                  string
		namespace                 string
		agent                     string
		agentCommand              string
		agentEnv                  []string
		promptRef                 string
		sandboxTemplateRef        string
		containerName             string
		reportingMode             string
		command                   []string
		triggerAction             []string
		requireLabel              []string
		repository                []string
		changeRequest             bool
		changeRequestAuthTokenEnv string
	}{
		namespace:          "factory-system",
		agent:              "builder",
		sandboxTemplateRef: "go-dev",
		requireLabel:       []string{"ai-factory"},
	}
	webhookServeOptions.apply = false

	payload := []byte(`{
	  "action": "opened",
	  "issue": {
	    "number": 42,
	    "title": "Missing label",
	    "body": "Do not run.",
	    "html_url": "https://github.com/liyuerich/ai-factory/issues/42",
	    "user": {"login": "yueli"}
	  },
	  "repository": {
	    "full_name": "liyuerich/ai-factory",
	    "html_url": "https://github.com/liyuerich/ai-factory",
	    "clone_url": "https://github.com/liyuerich/ai-factory.git",
	    "default_branch": "main"
	  },
	  "sender": {"login": "liyuerich"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", bytes.NewReader(payload))
	resp := httptest.NewRecorder()

	issueWebhookHandler(&cobra.Command{}, taskpkg.ProviderGitHub)(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusAccepted, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"ignored":true`) {
		t.Fatalf("body = %s, want ignored response", resp.Body.String())
	}
}

func TestGitLabPipelineTriggerHandler(t *testing.T) {
	oldWebhookOptions := webhookOptions
	oldPipelineOptions := webhookTriggerPipelineOptions
	t.Cleanup(func() {
		webhookOptions = oldWebhookOptions
		webhookTriggerPipelineOptions = oldPipelineOptions
	})
	webhookOptions = struct {
		provider                  string
		namespace                 string
		agent                     string
		agentCommand              string
		agentEnv                  []string
		promptRef                 string
		sandboxTemplateRef        string
		containerName             string
		reportingMode             string
		command                   []string
		triggerAction             []string
		requireLabel              []string
		repository                []string
		changeRequest             bool
		changeRequestPushOnly     bool
		changeRequestAuthTokenEnv string
	}{}
	webhookTriggerPipelineOptions = struct {
		addr     string
		secret   string
		tokenEnv string
		ref      string
		apiBase  string
	}{
		secret:   "webhook-secret",
		tokenEnv: "GITLAB_TOKEN",
		ref:      "main",
	}
	t.Setenv("GITLAB_TOKEN", "pipeline-token")

	var pipelineRequest *http.Request
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pipelineRequest = r
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":17,"status":"created","web_url":"https://gitlab.example.com/platform/app/-/pipelines/17"}`)
	}))
	defer api.Close()
	webhookTriggerPipelineOptions.apiBase = api.URL + "/api/v4"

	payload := []byte(`{
	  "object_kind": "issue",
	  "event_type": "issue",
	  "user": {"username": "yueli"},
	  "project": {
	    "path_with_namespace": "platform/app",
	    "web_url": "https://gitlab.example.com/platform/app",
	    "default_branch": "main",
	    "git_http_url": "https://gitlab.example.com/platform/app.git"
	  },
	  "object_attributes": {
	    "iid": 9,
	    "title": "Add docs",
	    "description": "Document the setup.",
	    "url": "https://gitlab.example.com/platform/app/-/issues/9",
	    "action": "open"
	  },
	  "labels": [{"title": "ai-factory-run"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/gitlab", bytes.NewReader(payload))
	req.Header.Set("X-Gitlab-Token", "webhook-secret")
	resp := httptest.NewRecorder()

	gitLabPipelineTriggerHandler(&cobra.Command{})(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusAccepted, resp.Body.String())
	}
	if pipelineRequest == nil {
		t.Fatal("pipeline API was not called")
	}
	if pipelineRequest.Header.Get("PRIVATE-TOKEN") != "pipeline-token" {
		t.Fatalf("PRIVATE-TOKEN = %q", pipelineRequest.Header.Get("PRIVATE-TOKEN"))
	}
	if pipelineRequest.FormValue("ref") != "main" {
		t.Fatalf("ref = %q, want main", pipelineRequest.FormValue("ref"))
	}
	if pipelineRequest.FormValue("variables[AI_FACTORY_ISSUE_IID]") != "9" {
		t.Fatalf("issue IID = %q, want 9", pipelineRequest.FormValue("variables[AI_FACTORY_ISSUE_IID]"))
	}
	if !strings.Contains(resp.Body.String(), `"pipeline":17`) {
		t.Fatalf("body = %s, want pipeline result", resp.Body.String())
	}
}

func TestPlanDryRunValid(t *testing.T) {
	content := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: dry-run-task
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
    instructions: "Do some work"
`)
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "task.yaml")
	if err := os.WriteFile(p, content, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := planDryRunCmd.RunE(cmd, []string{p}); err != nil {
		t.Fatalf("plan-dry-run failed: %v\noutput:\n%s", err, buf.String())
	}

	out := buf.String()
	for _, want := range []string{
		"taskName: dry-run-task",
		"provider: github",
		"repository: liyuerich/ai-factory",
		"steps:",
		"clone repository",
		"run coding agent",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPlanDryRunInvalid(t *testing.T) {
	content := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: dry-run-invalid
spec:
  source:
    provider: github
    repository: liyuerich/ai-factory
    baseRef: main
  agent:
    name: ""
  sandbox:
    templateRef: go-dev
  work:
    instructions: "Do some work"
`)
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "task.yaml")
	if err := os.WriteFile(p, content, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	err := planDryRunCmd.RunE(cmd, []string{p})
	if err == nil {
		t.Fatalf("expected error for invalid FactoryTask, got output:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "invalid FactoryTask YAML") {
		t.Fatalf("error should mention invalid FactoryTask YAML: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output for invalid YAML, got:\n%s", buf.String())
	}
}

func TestKubectlCommandErrorIncludesOutputTailAndRedactsSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret-token")
	err := kubectlCommandError{
		args:   []string{"exec", "sandbox", "--", "agent"},
		err:    fmt.Errorf("exit status 1"),
		stdout: strings.Repeat("x", 4100) + "stdout clue",
		stderr: "failed with secret-token and finish_reason=length",
	}
	message := err.Error()
	for _, want := range []string{
		"kubectl exec sandbox -- agent: exit status 1",
		"stdout tail:",
		"stdout clue",
		"stderr tail:",
		"finish_reason=length",
		"<redacted:OPENAI_API_KEY>",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "secret-token") {
		t.Fatalf("error message leaked secret:\n%s", message)
	}
}

func TestReportMessagePreservesRawMessage(t *testing.T) {
	task := &taskpkg.FactoryTask{Metadata: taskpkg.ObjectMeta{Name: "validate-ai-factory"}}
	got := buildReportMessageFromString(task, taskpkg.PhaseFailed, "empty repair script")
	if !strings.Contains(got, "Likely cause") {
		t.Fatalf("missing friendly cause: %q", got)
	}
	if !strings.Contains(got, "empty repair script") {
		t.Fatalf("raw message not preserved: %q", got)
	}
}

func TestReportMessageNoClassification(t *testing.T) {
	task := &taskpkg.FactoryTask{Metadata: taskpkg.ObjectMeta{Name: "validate-ai-factory"}}
	got := buildReportMessageFromString(task, taskpkg.PhaseFailed, "unrecognized failure")
	if strings.Contains(got, "Likely cause") {
		t.Fatalf("should not classify unknown failure: %q", got)
	}
	if got != "FactoryTask `default/validate-ai-factory` Failed\n\nunrecognized failure" {
		t.Fatalf("unexpected message: %q", got)
	}
}
