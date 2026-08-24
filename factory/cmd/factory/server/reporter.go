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
	"context"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

// IssueReporter is the provider-neutral surface the controller uses to reflect
// task state back onto an issue: status labels, cancellation, and comments.
// GitHubClient and GitLabClient both implement it.
type IssueReporter interface {
	SetTaskRunning(ctx context.Context, repo string, issueNumber int) error
	SetTaskWaiting(ctx context.Context, repo string, issueNumber int) error
	SetTaskDone(ctx context.Context, repo string, issueNumber int) error
	SetTaskFailed(ctx context.Context, repo string, issueNumber int) error
	SetTaskCancelled(ctx context.Context, repo string, issueNumber int, removedTriggerLabel string) error
	PostComment(ctx context.Context, repo string, issueNumber int, body string) error
	HasToken() bool
}

// newIssueReporter returns the reporter for a provider, or nil for an unknown
// provider (callers already tolerate a nil/absent reporter as "skip").
func newIssueReporter(provider string) IssueReporter {
	switch provider {
	case taskpkg.ProviderGitHub:
		return NewGitHubClient()
	case taskpkg.ProviderGitLab:
		return NewGitLabClient()
	default:
		return nil
	}
}
