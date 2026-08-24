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

package server

import (
	"os"
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

func TestResolveGitProviderValid(t *testing.T) {
	for _, p := range []string{"github", "gitlab"} {
		t.Setenv("GIT_PROVIDER", p)
		got, err := resolveGitProvider()
		if err != nil {
			t.Fatalf("resolveGitProvider(%q) error = %v", p, err)
		}
		if got != p {
			t.Fatalf("resolveGitProvider(%q) = %q", p, got)
		}
	}
}

func TestWebhookOptionsAcceptIssueCreationActions(t *testing.T) {
	options := webhookOptions("gitlab")
	for _, action := range []string{"open", "opened", "labeled"} {
		found := false
		for _, configured := range options.TriggerActions {
			if configured == action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("TriggerActions = %#v, missing %q", options.TriggerActions, action)
		}
	}
}

func TestWebhookOptionsAcceptGitLabIssueCreatedWithTriggerLabels(t *testing.T) {
	payload := []byte(`{
		"object_kind": "issue",
		"project": {
			"path_with_namespace": "group/project",
			"web_url": "https://gitlab.example.com/group/project"
		},
		"object_attributes": {
			"iid": 4,
			"action": "open",
			"url": "https://gitlab.example.com/group/project/-/issues/4",
			"labels": [
				{"title": "ai-factory"},
				{"title": "ai-factory-run"}
			]
		}
	}`)
	event, err := taskpkg.ParseIssueWebhook(payload, taskpkg.ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if ok, reason := taskpkg.ShouldTriggerIssue(event, webhookOptions(taskpkg.ProviderGitLab)); !ok {
		t.Fatalf("ShouldTriggerIssue() = false, reason %q", reason)
	}
}

func TestWebhookOptionsIgnoreGitLabIssueCreatedWithoutTriggerLabels(t *testing.T) {
	payload := []byte(`{
		"object_kind": "issue",
		"project": {
			"path_with_namespace": "group/project",
			"web_url": "https://gitlab.example.com/group/project"
		},
		"object_attributes": {
			"iid": 4,
			"action": "open",
			"url": "https://gitlab.example.com/group/project/-/issues/4",
			"labels": [{"title": "ai-factory"}]
		}
	}`)
	event, err := taskpkg.ParseIssueWebhook(payload, taskpkg.ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if ok, reason := taskpkg.ShouldTriggerIssue(event, webhookOptions(taskpkg.ProviderGitLab)); ok {
		t.Fatalf("ShouldTriggerIssue() = true, reason %q; want ignored event", reason)
	}
}

func TestResolveGitProviderMissing(t *testing.T) {
	// Ensure the env is empty; ReadConfig also checks mounted files, but in the
	// test environment only the env var applies.
	os.Unsetenv("GIT_PROVIDER")
	if _, err := resolveGitProvider(); err == nil {
		t.Fatal("resolveGitProvider() with unset GIT_PROVIDER: error = nil, want required error")
	}
}

func TestResolveGitProviderInvalid(t *testing.T) {
	t.Setenv("GIT_PROVIDER", "bitbucket")
	if _, err := resolveGitProvider(); err == nil {
		t.Fatal("resolveGitProvider(bitbucket): error = nil, want invalid error")
	}
}
