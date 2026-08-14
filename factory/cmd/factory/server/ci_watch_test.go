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
	"errors"
	"strings"
	"testing"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

// ciWaitResult carries what waitForCIEvent returned out of its goroutine.
type ciWaitResult struct {
	status  ciCheckStatus
	summary string
}

func ciWatchOptsForTest() ciWatchOptions {
	return ciWatchOptions{
		maxRetries:       3,
		maxWait:          3 * time.Second,
		settleInterval:   100 * time.Millisecond, // confirm window T
		maxToolRounds:    3,
		allowTestChanges: true,
		logSnippetLines:  20,
	}
}

func runWaitForCIEvent(t *testing.T, fake *fakeCIClient, key string) chan ciWaitResult {
	t.Helper()
	w := registerWaiter(key)
	t.Cleanup(func() { unregisterWaiter(key) })
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "github-owner-repo-42", Namespace: "default"},
	}
	opts := ciWatchOptsForTest()
	done := make(chan ciWaitResult, 1)
	go func() {
		status, summary := waitForCIEvent(context.Background(), task, fake, "owner", "repo", 42, w, opts, time.Now().Add(opts.maxWait))
		done <- ciWaitResult{status: status, summary: summary}
	}()
	return done
}

// TestWaitForCIEventGreenAfterBootstrap proves the newly-registered waiter
// evaluates an already-finished suite set: the one-time bootstrap snapshot must
// converge through the confirm window to green without any webhook event.
func TestWaitForCIEventGreenAfterBootstrap(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{
		headSHA: "abc123",
		suites: []CheckSuite{
			{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: "abc123"},
			{ID: 2, Status: "completed", Conclusion: "neutral", HeadSHA: "abc123"},
		},
	}
	done := runWaitForCIEvent(t, fake, key)
	select {
	case r := <-done:
		if r.status != ciCheckGreen {
			t.Fatalf("waitForCIEvent status = %v (summary %q), want ciCheckGreen", r.status, r.summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForCIEvent did not converge to green within the confirm window")
	}
}

// TestWaitForCIEventEmptySuitesIsNotGreen guards the "no check suites yet" race
// (lazy registration, right after a force-push): an empty list must NEVER be
// judged green, even after a wake.
func TestWaitForCIEventEmptySuitesIsNotGreen(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{headSHA: "abc123"} // no suites at all
	done := runWaitForCIEvent(t, fake, key)

	// A wake with still-zero suites must not conclude anything.
	notifyWaiter(key)
	select {
	case r := <-done:
		t.Fatalf("waitForCIEvent returned %v (summary %q) for an empty suite list; zero suites must not be green", r.status, r.summary)
	case <-time.After(300 * time.Millisecond):
		// Still waiting — correct: not converged.
	}

	fake.suites = []CheckSuite{{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: "abc123"}}
	notifyWaiter(key)
	select {
	case r := <-done:
		if r.status != ciCheckGreen {
			t.Fatalf("after suites appear, status = %v (summary %q), want ciCheckGreen", r.status, r.summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not go green after suites appeared")
	}
}

// TestWaitForCIEventPendingUntilComplete proves a long CI is never misjudged:
// while any suite is in_progress the loop keeps waiting (no time window runs),
// and green only arrives after all suites complete and the confirm window
// elapses.
func TestWaitForCIEventPendingUntilComplete(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{
		headSHA: "abc123",
		suites:  []CheckSuite{{ID: 1, Status: "in_progress", Conclusion: "", HeadSHA: "abc123"}},
	}
	done := runWaitForCIEvent(t, fake, key)

	// Wake repeatedly while still pending; the loop must keep waiting (this is
	// the old bug: a settle timer would wrongly fire and judge Pending=fail).
	for i := 0; i < 3; i++ {
		notifyWaiter(key)
		select {
		case r := <-done:
			t.Fatalf("waitForCIEvent returned %v (summary %q) while suites were still in_progress", r.status, r.summary)
		case <-time.After(150 * time.Millisecond):
		}
	}

	// Now the long job finishes; the next wake starts the confirm window.
	fake.suites = []CheckSuite{{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: "abc123"}}
	notifyWaiter(key)
	select {
	case r := <-done:
		if r.status != ciCheckGreen {
			t.Fatalf("status = %v (summary %q), want ciCheckGreen", r.status, r.summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not go green after the suite completed")
	}
}

// TestWaitForCIEventFailsFastOnCompletedFailure proves a completed failing
// suite settles red immediately, without waiting for the remaining in_progress
// suites (a failed suite is terminal).
func TestWaitForCIEventFailsFastOnCompletedFailure(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{
		headSHA: "abc123",
		suites: []CheckSuite{
			{ID: 1, Status: "completed", Conclusion: "failure", HeadSHA: "abc123"},
			{ID: 2, Status: "in_progress", Conclusion: ""},
		},
	}
	done := runWaitForCIEvent(t, fake, key)
	select {
	case r := <-done:
		if r.status != ciCheckRed {
			t.Fatalf("status = %v (summary %q), want ciCheckRed", r.status, r.summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed failure did not settle red immediately")
	}
}

// TestWatchAndRepairCICommentsPRStartAndEnd proves watchAndRepairCI posts the
// start comment once on the first failure and the aggregate result comment on
// exit: first round fails and is repaired, second round converges green.
func TestWatchAndRepairCICommentsPRStartAndEnd(t *testing.T) {
	withTaskExists(t, true)
	fake := &fakeCIClient{
		headSHA: "abc123",
		suites:  []CheckSuite{{ID: 1, Status: "completed", Conclusion: "failure", HeadSHA: "abc123"}},
	}
	repairRounds := 0
	repair := func([]CheckRunAnnotation, []JobLogSnippet) error {
		repairRounds++
		// A successful repair makes CI green on the next evaluation.
		fake.suites = []CheckSuite{{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: "abc123"}}
		return nil
	}
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "github-owner-repo-42", Namespace: "default"},
	}
	var out strings.Builder
	outcome, summary := watchAndRepairCI(&out, task, "https://github.com/owner/repo/pull/42", fake, repair, ciWatchOptsForTest())
	if outcome != ciWatchGreen {
		t.Fatalf("outcome = %v (summary %q), want ciWatchGreen", outcome, summary)
	}
	if repairRounds != 1 {
		t.Fatalf("repair rounds = %d, want 1", repairRounds)
	}
	if len(fake.comments) != 2 {
		t.Fatalf("expected 2 PR comments (start + end), got %d: %q", len(fake.comments), fake.comments)
	}
	if !strings.Contains(fake.comments[0], "starting automated repair") {
		t.Errorf("start comment = %q, want an announce", fake.comments[0])
	}
	if !strings.Contains(fake.comments[1], "green after 1 repair round") {
		t.Errorf("end comment = %q, want a green result", fake.comments[1])
	}
}

// TestWatchAndRepairCIContinuesAfterRepairFailure proves a repair pass that
// fails does not abort the watch: watchAndRepairCI keeps trying until
// maxRetries is exhausted, then reports ciWatchFailed (rather than dying on
// the first repair error).
func TestWatchAndRepairCIContinuesAfterRepairFailure(t *testing.T) {
	withTaskExists(t, true)
	fake := &fakeCIClient{
		headSHA: "abc123",
		suites:  []CheckSuite{{ID: 1, Status: "completed", Conclusion: "failure", HeadSHA: "abc123"}},
	}
	repairRounds := 0
	repair := func([]CheckRunAnnotation, []JobLogSnippet) error {
		repairRounds++
		return errors.New("simulated repair exec failure")
	}
	task := &taskpkg.FactoryTask{
		Metadata: taskpkg.ObjectMeta{Name: "github-owner-repo-42", Namespace: "default"},
	}
	var out strings.Builder
	outcome, summary := watchAndRepairCI(&out, task, "https://github.com/owner/repo/pull/42", fake, repair, ciWatchOptsForTest())
	if outcome != ciWatchFailed {
		t.Fatalf("outcome = %v, want ciWatchFailed", outcome)
	}
	if repairRounds != 3 {
		t.Fatalf("repair attempts = %d, want 3 (maxRetries); a failing repair must not abort early", repairRounds)
	}
	if !strings.Contains(summary, "3 repair attempts") {
		t.Errorf("summary = %q, want the budget-exhausted message", summary)
	}
	// start comment + final failure comment (roundsRepaired is 0 because no
	// repair pass succeeded).
	if len(fake.comments) != 2 {
		t.Fatalf("expected 2 PR comments, got %d: %q", len(fake.comments), fake.comments)
	}
	if !strings.Contains(fake.comments[1], "could not be made green") {
		t.Errorf("final comment = %q, want a failure result", fake.comments[1])
	}
}
// TestWaitForCIEventIgnoresStaleSuites proves the head_sha filter: after a
// repair force-push the PR head moves, and the API may still return the
// previous commit's failing suites. Those stale suites must NOT re-trigger red
// or block green — only suites whose HeadSHA equals the current PR head count.
func TestWaitForCIEventIgnoresStaleSuites(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{
		headSHA: "abc123", // current PR head
		suites: []CheckSuite{
			// Stale: this suite belongs to the previous (already repaired)
			// commit and still shows failure — must be ignored.
			{ID: 1, Status: "completed", Conclusion: "failure", HeadSHA: "olddeadbeef"},
			// Current-head suite, still running: not converged yet.
			{ID: 2, Status: "in_progress", Conclusion: "", HeadSHA: "abc123"},
		},
	}
	done := runWaitForCIEvent(t, fake, key)
	// The stale failure must not settle red; and with the live suite
	// in_progress we keep waiting.
	select {
	case r := <-done:
		t.Fatalf("waitForCIEvent returned %v (summary %q): stale-head failure must not judge red", r.status, r.summary)
	case <-time.After(400 * time.Millisecond):
		// Correct: still waiting on the in_progress current-head suite.
	}

	// Now the current-head suite completes successfully: green.
	fake.suites = []CheckSuite{
		{ID: 1, Status: "completed", Conclusion: "failure", HeadSHA: "olddeadbeef"},
		{ID: 2, Status: "completed", Conclusion: "success", HeadSHA: "abc123"},
	}
	notifyWaiter(key)
	select {
	case r := <-done:
		if r.status != ciCheckGreen {
			t.Fatalf("status = %v (summary %q), want ciCheckGreen", r.status, r.summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not go green: stale suite must not block green once current head converges")
	}
}

// TestWaitForCIEventAllSuitesStaleIsNotConverged proves that when EVERY suite
// comes back with a stale head_sha (the new commit's suites have not been
// registered yet), the list reads as not-converged and the loop must keep
// waiting — it must not judge green (vacuous all-completed) from an empty
// filtered set.
func TestWaitForCIEventAllSuitesStaleIsNotConverged(t *testing.T) {
	withTaskExists(t, true)
	const key = "owner/repo/factory-task/github-owner-repo-42"
	fake := &fakeCIClient{
		headSHA: "abc123",
		// All suites stale: new head has no suites registered yet.
		suites: []CheckSuite{
			{ID: 1, Status: "completed", Conclusion: "success", HeadSHA: "olddeadbeef"},
		},
	}
	done := runWaitForCIEvent(t, fake, key)
	notifyWaiter(key)
	select {
	case r := <-done:
		t.Fatalf("waitForCIEvent returned %v (summary %q) with only stale suites; empty filtered set must not be green", r.status, r.summary)
	case <-time.After(400 * time.Millisecond):
		// Correct: not converged.
	}
}
