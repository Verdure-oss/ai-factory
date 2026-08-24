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
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

func TestNewIssueReporterGitHub(t *testing.T) {
	r := newIssueReporter(taskpkg.ProviderGitHub)
	if r == nil {
		t.Fatal("newIssueReporter(github) = nil")
	}
	if _, ok := r.(*GitHubClient); !ok {
		t.Fatalf("newIssueReporter(github) = %T, want *GitHubClient", r)
	}
}

func TestNewIssueReporterUnknown(t *testing.T) {
	if r := newIssueReporter("bitbucket"); r != nil {
		t.Fatalf("newIssueReporter(unknown) = %T, want nil", r)
	}
}

// Compile-time assertion that GitHubClient satisfies the interface.
var _ IssueReporter = (*GitHubClient)(nil)
