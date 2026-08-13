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
	"testing"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

// TestRegisterAndNotifyWaiter proves the waiter registry wiring: a
// waitForCIEvent loop blocked on its select is woken by registerWaiter +
// notifyWaiter and returns the evaluated CI status. The fake checkout is
// already green, so the quiet-window evaluation after the notify must return
// ciCheckGreen.
func TestRegisterAndNotifyWaiter(t *testing.T) {
	withTaskExists(t, true)

	fake := &fakeCIClient{
		headSHA: "abc123",
		runs: []CheckRun{
			{Name: "test", Status: "completed", Conclusion: "success"},
		},
	}
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "github-owner-repo-42", Namespace: "default"},
	}

	const key = "owner/repo/factory-task/github-owner-repo-42"
	w := registerWaiter(key)
	defer unregisterWaiter(key)

	opts := ciWatchOptions{
		maxWait:         5 * time.Second,
		settleInterval:  100 * time.Millisecond,
		logSnippetLines: 20,
	}
	done := make(chan ciCheckStatus, 1)
	go func() {
		status, summary := waitForCIEvent(context.Background(), task, fake, "owner", "repo", 42, w, opts, time.Now().Add(opts.maxWait))
		if status == ciCheckError {
			t.Logf("waitForCIEvent summary: %s", summary)
		}
		done <- status
	}()

	// Let the loop reach its select, then wake it via the registry.
	time.Sleep(50 * time.Millisecond)
	notifyWaiter(key)

	select {
	case status := <-done:
		if status != ciCheckGreen {
			t.Fatalf("waitForCIEvent status = %v, want ciCheckGreen", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForCIEvent did not return after notifyWaiter; select was not woken")
	}
}
