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
	"bytes"
	"fmt"
	"strings"
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
	"github.com/spf13/cobra"
)

func TestResolveMaxConcurrentTasks(t *testing.T) {
	// newCmd builds a fresh command so Changed() reflects only this subtest's flag sets.
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().IntVar(&opts.MaxConcurrentTasks, "max-concurrent-tasks", 0, "")
		return cmd
	}

	t.Run("flag wins over env", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "7")
		_ = cmd.Flags().Set("max-concurrent-tasks", "3")
		if got := resolveMaxConcurrentTasks(cmd); got != 3 {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want 3", got)
		}
	})

	t.Run("env used when flag unset", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "5")
		if got := resolveMaxConcurrentTasks(cmd); got != 5 {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want 5", got)
		}
	})

	t.Run("default when neither set", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "")
		if got := resolveMaxConcurrentTasks(cmd); got != maxConcurrentTasksDefault {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want %d", got, maxConcurrentTasksDefault)
		}
	})

	t.Run("invalid env falls back to default", func(t *testing.T) {
		cmd := newCmd()
		t.Setenv("MAX_CONCURRENT_TASKS", "abc")
		if got := resolveMaxConcurrentTasks(cmd); got != maxConcurrentTasksDefault {
			t.Fatalf("resolveMaxConcurrentTasks() = %d, want %d", got, maxConcurrentTasksDefault)
		}
	})
}

func TestIsTaskQueued(t *testing.T) {
	cases := []struct {
		name   string
		status taskpkg.FactoryTaskStatus
		want   bool
	}{
		{
			name:   "empty status",
			status: taskpkg.FactoryTaskStatus{},
			want:   false,
		},
		{
			name:   "pending without queued reason",
			status: taskpkg.FactoryTaskStatus{Phase: taskpkg.PhasePending},
			want:   false,
		},
		{
			name: "pending with queued reason",
			status: taskpkg.FactoryTaskStatus{
				Phase: taskpkg.PhasePending,
				Conditions: []taskpkg.Condition{
					{Type: taskpkg.PhasePending, Reason: "Queued"},
				},
			},
			want: true,
		},
		{
			name: "running with stale queued condition",
			status: taskpkg.FactoryTaskStatus{
				Phase: taskpkg.PhaseRunning,
				Conditions: []taskpkg.Condition{
					{Type: taskpkg.PhasePending, Reason: "Queued"},
				},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &taskpkg.FactoryTask{Status: tc.status}
			if got := isTaskQueued(task); got != tc.want {
				t.Fatalf("isTaskQueued() = %v, want %v", got, tc.want)
			}
		})
	}
}

// newGitHubReportTask builds a minimal GitHub-sourced FactoryTask configured
// for comment reporting, as used by the reportTaskResult tests.
func newGitHubReportTask() *taskpkg.FactoryTask {
	return &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{
			Name:      "github-owner-repo-42",
			Namespace: "default",
		},
		Spec: taskpkg.FactoryTaskSpec{
			Source: taskpkg.SourceSpec{
				Provider:   taskpkg.ProviderGitHub,
				Repository: "owner/repo",
			},
			Trigger: taskpkg.TriggerSpec{
				ID: "42",
			},
			Reporting: taskpkg.ReportingSpec{
				Mode:      "comment",
				TargetURL: "https://github.com/owner/repo/issues/42",
			},
		},
	}
}

func newGitLabReportTask() *taskpkg.FactoryTask {
	return &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{
			Name:      "gitlab-group-project-4",
			Namespace: "default",
		},
		Spec: taskpkg.FactoryTaskSpec{
			Source: taskpkg.SourceSpec{
				Provider:   taskpkg.ProviderGitLab,
				Host:       "gitlab.example.test",
				Repository: "group/project",
			},
			Trigger: taskpkg.TriggerSpec{
				ID: "4",
			},
			Reporting: taskpkg.ReportingSpec{
				Provider:  taskpkg.ProviderGitLab,
				Mode:      "comment",
				TargetURL: "https://gitlab.example.test/group/project/-/work_items/4",
			},
		},
	}
}

func TestPostStartedCommentReportsGitHub(t *testing.T) {
	server, _, comments := fakeGitHubAPI(t, nil)
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_BASE", server.URL)
	withReportEnabled(t, true)

	var out bytes.Buffer
	postStartedComment(&out, newGitHubReportTask())

	if len(*comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(*comments))
	}
	if !strings.Contains((*comments)[0], "ai-factory started processing this issue.") {
		t.Fatalf("comment = %q, want started message", (*comments)[0])
	}
}

func TestPostStartedCommentReportsGitLabWorkItem(t *testing.T) {
	server, rec := newFakeGitLabAPI(t)
	defer server.Close()

	t.Setenv("GITLAB_TOKEN", "fake-token")
	t.Setenv("GITLAB_API_BASE", server.URL)
	withReportEnabled(t, true)

	var out bytes.Buffer
	postStartedComment(&out, newGitLabReportTask())

	if len(rec.comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(rec.comments))
	}
	if !strings.Contains(rec.comments[0], "ai-factory started processing this issue.") {
		t.Fatalf("comment = %q, want started message", rec.comments[0])
	}
}

func TestReportTaskResultReportsGitLabWorkItem(t *testing.T) {
	server, rec := newFakeGitLabAPI(t)
	defer server.Close()

	t.Setenv("GITLAB_TOKEN", "fake-token")
	t.Setenv("GITLAB_API_BASE", server.URL)
	withTaskExists(t, true)
	withReportEnabled(t, true)

	var out bytes.Buffer
	reportTaskResult(&out, newGitLabReportTask(), taskpkg.PhaseSucceeded, "FactoryTask completed successfully")

	if len(rec.comments) != 1 {
		t.Fatalf("comments = %d, want 1; output = %q", len(rec.comments), out.String())
	}
	if !strings.Contains(rec.comments[0], "FactoryTask `default/gitlab-group-project-4` Succeeded") {
		t.Fatalf("comment = %q, want succeeded report", rec.comments[0])
	}
}

// withTaskExists overrides the taskExists seam for the duration of the test.
func withTaskExists(t *testing.T, exists bool) {
	t.Helper()
	orig := taskExists
	taskExists = func(namespace, name string) bool { return exists }
	t.Cleanup(func() { taskExists = orig })
}

// withReportEnabled pins opts.ReportEnabled for the duration of the test.
func withReportEnabled(t *testing.T, enabled bool) {
	t.Helper()
	orig := opts.ReportEnabled
	opts.ReportEnabled = enabled
	t.Cleanup(func() { opts.ReportEnabled = orig })
}

func TestReportTaskResultSkipsReportingWhenTaskDeleted(t *testing.T) {
	server, ops, comments := fakeGitHubAPI(t, []string{"ai-factory-running", "ai-factory-waiting"})
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_BASE", server.URL)
	withTaskExists(t, false)
	withReportEnabled(t, true)

	var out bytes.Buffer
	reportTaskResult(&out, newGitHubReportTask(), taskpkg.PhaseFailed, "SandboxClaim ready wait failed: claim deleted")

	if len(*ops) != 0 {
		t.Errorf("deleted task: expected no label ops, got %d: %+v", len(*ops), *ops)
	}
	if len(*comments) != 0 {
		t.Errorf("deleted task: expected no comments, got %d: %q", len(*comments), *comments)
	}
}

func TestReportTaskResultReportsWhenTaskExists(t *testing.T) {
	server, ops, comments := fakeGitHubAPI(t, []string{"ai-factory-running"})
	defer server.Close()

	t.Setenv("GITHUB_TOKEN", "fake-token")
	t.Setenv("GITHUB_API_BASE", server.URL)
	withTaskExists(t, true)
	withReportEnabled(t, true)

	var out bytes.Buffer
	reportTaskResult(&out, newGitHubReportTask(), taskpkg.PhaseFailed, "SandboxClaim ready wait failed: claim deleted")

	if len(*comments) == 0 {
		t.Fatal("existing task: expected at least one report comment, got none")
	}
	if len(*ops) == 0 {
		t.Error("existing task: expected label ops, got none")
	}
}

func TestTaskCancelled(t *testing.T) {
	withTaskExists(t, false)
	var out bytes.Buffer
	if !taskCancelled(&out, "default", "github-owner-repo-42") {
		t.Fatal("taskCancelled() = false when task deleted, want true")
	}
	if !strings.Contains(out.String(), "--- CANCELLED") {
		t.Errorf("expected --- CANCELLED log line when task deleted, got %q", out.String())
	}

	out.Reset()
	withTaskExists(t, true)
	if taskCancelled(&out, "default", "github-owner-repo-42") {
		t.Fatal("taskCancelled() = true when task exists, want false")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output when task exists, got %q", out.String())
	}
}

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"NotFound from kubectl", fmt.Errorf("kubectl get factorytask github-owner-repo-42 -n default: Error from server (NotFound): factorytasks.factory.ai.gke.io \"github-owner-repo-42\" not found: exit status 1"), true},
		{"lowercase not found", fmt.Errorf("sandboxclaims.agent.k8s.io \"claim\" not found"), true},
		{"wrapped NotFound", fmt.Errorf("some prefix: NotFound: some suffix"), true},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"timeout error", fmt.Errorf("context deadline exceeded"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFoundError(tt.err); got != tt.want {
				t.Errorf("isNotFoundError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
