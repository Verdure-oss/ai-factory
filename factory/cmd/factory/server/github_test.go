package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// labelRequest records what label was added or removed.
type labelRequest struct {
	method string // "POST" = add, "DELETE" = remove
	label  string
}

// fakeGitHubAPI records all label operations and verifies correctness.
func fakeGitHubAPI(t *testing.T, labelsOnIssue []string) (*httptest.Server, *[]labelRequest) {
	t.Helper()
	var ops []labelRequest
	currentLabels := make(map[string]bool)
	for _, l := range labelsOnIssue {
		currentLabels[l] = true
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// POST /repos/{owner}/{repo}/labels — EnsureLabel
		if r.Method == "POST" && strings.HasSuffix(path, "/labels") && !strings.Contains(path, "/issues/") {
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			json.Unmarshal(body, &payload)
			// Label created (no-op for our tracking)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(payload)
			return
		}

		// POST /repos/{owner}/{repo}/issues/{num}/labels — AddLabel
		if r.Method == "POST" && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/labels") {
			body, _ := io.ReadAll(r.Body)
			var labels []string
			json.Unmarshal(body, &labels)
			for _, l := range labels {
				currentLabels[l] = true
				ops = append(ops, labelRequest{method: "POST", label: l})
			}
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(labels)
			return
		}

		// DELETE /repos/{owner}/{repo}/issues/{num}/labels/{label} — RemoveLabel
		if r.Method == "DELETE" && strings.Contains(path, "/labels/") {
			parts := strings.Split(path, "/labels/")
			if len(parts) == 2 {
				label := parts[1]
				delete(currentLabels, label)
				ops = append(ops, labelRequest{method: "DELETE", label: label})
			}
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
			return
		}

		w.WriteHeader(404)
	}))

	return server, &ops
}

func labelsAfterOps(ops []labelRequest) map[string]bool {
	labels := make(map[string]bool)
	for _, op := range ops {
		switch op.method {
		case "POST":
			labels[op.label] = true
		case "DELETE":
			delete(labels, op.label)
		}
	}
	return labels
}

func TestSetTaskWaiting(t *testing.T) {
	server, ops := fakeGitHubAPI(t, []string{"ai-factory-run", "ai-factory-running"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	err := gh.SetTaskWaiting(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("SetTaskWaiting failed: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if !labels["ai-factory-waiting"] {
		t.Error("expected ai-factory-waiting label to be present")
	}
	if labels["ai-factory-running"] {
		t.Error("expected ai-factory-running label to be removed")
	}
}

func TestSetTaskRunningRemovesWaiting(t *testing.T) {
	server, ops := fakeGitHubAPI(t, []string{"ai-factory-waiting"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	err := gh.SetTaskRunning(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("SetTaskRunning failed: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if !labels["ai-factory-running"] {
		t.Error("expected ai-factory-running label to be present")
	}
	if labels["ai-factory-waiting"] {
		t.Error("expected ai-factory-waiting label to be removed")
	}
}

func TestSetTaskDoneRemovesWaiting(t *testing.T) {
	server, ops := fakeGitHubAPI(t, []string{"ai-factory-waiting"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	err := gh.SetTaskDone(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("SetTaskDone failed: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if !labels["ai-factory-done"] {
		t.Error("expected ai-factory-done label to be present")
	}
	if labels["ai-factory-waiting"] {
		t.Error("expected ai-factory-waiting label to be removed")
	}
	if labels["ai-factory-running"] {
		t.Error("expected ai-factory-running label to be removed")
	}
}

func TestSetTaskFailedRemovesWaiting(t *testing.T) {
	server, ops := fakeGitHubAPI(t, []string{"ai-factory-waiting"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	err := gh.SetTaskFailed(context.Background(), "owner/repo", 42)
	if err != nil {
		t.Fatalf("SetTaskFailed failed: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if !labels["ai-factory-failed"] {
		t.Error("expected ai-factory-failed label to be present")
	}
	if labels["ai-factory-waiting"] {
		t.Error("expected ai-factory-waiting label to be removed")
	}
}

// TestFullLabelTransitionFlow simulates the complete label lifecycle
// as it would happen during a real task execution with warm pool waiting.
func TestFullLabelTransitionFlow(t *testing.T) {
	server, ops := fakeGitHubAPI(t, []string{"ai-factory"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	ctx := context.Background()
	repo := "owner/repo"
	issue := 42

	// Step 1: Webhook creates task → SetTaskRunning
	t.Log("Step 1: Webhook → SetTaskRunning (task accepted)")
	if err := gh.SetTaskRunning(ctx, repo, issue); err != nil {
		t.Fatalf("SetTaskRunning: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if !labels["ai-factory-running"] {
		t.Error("after webhook: expected ai-factory-running")
	}

	// Step 2: Controller enters ClaimCreated → SetTaskWaiting
	t.Log("Step 2: Controller ClaimCreated → SetTaskWaiting (waiting for pod)")
	if err := gh.SetTaskWaiting(ctx, repo, issue); err != nil {
		t.Fatalf("SetTaskWaiting: %v", err)
	}

	labels = labelsAfterOps(*ops)
	if !labels["ai-factory-waiting"] {
		t.Error("after ClaimCreated: expected ai-factory-waiting")
	}
	if labels["ai-factory-running"] {
		t.Error("after ClaimCreated: ai-factory-running should be removed")
	}

	// Step 3: SandboxClaim ready → SetTaskRunning
	t.Log("Step 3: SandboxReady → SetTaskRunning (executing)")
	if err := gh.SetTaskRunning(ctx, repo, issue); err != nil {
		t.Fatalf("SetTaskRunning: %v", err)
	}

	labels = labelsAfterOps(*ops)
	if !labels["ai-factory-running"] {
		t.Error("after SandboxReady: expected ai-factory-running")
	}
	if labels["ai-factory-waiting"] {
		t.Error("after SandboxReady: ai-factory-waiting should be removed")
	}

	// Step 4: Task completes → SetTaskDone
	t.Log("Step 4: Task succeeded → SetTaskDone")
	if err := gh.SetTaskDone(ctx, repo, issue); err != nil {
		t.Fatalf("SetTaskDone: %v", err)
	}

	labels = labelsAfterOps(*ops)
	if !labels["ai-factory-done"] {
		t.Error("after success: expected ai-factory-done")
	}
	if labels["ai-factory-running"] {
		t.Error("after success: ai-factory-running should be removed")
	}

	t.Logf("Full transition test passed. Total API operations: %d", len(*ops))
	for i, op := range *ops {
		t.Logf("  [%d] %s %s", i, op.method, op.label)
	}
}

func TestShouldUseFork(t *testing.T) {
	tests := []struct {
		eventOwner string
		forkOwner  string
		want       bool
	}{
		{"matrixhub-ai", "Verdure-oss", true}, // upstream public repo -> fork
		{"Verdure-oss", "Verdure-oss", false}, // own repo -> direct flow
		{"", "Verdure-oss", false},            // no event owner -> no fork
		{"matrixhub-ai", "", false},           // no fork owner -> no fork
	}
	for _, tc := range tests {
		if got := shouldUseFork(tc.eventOwner, tc.forkOwner); got != tc.want {
			t.Fatalf("shouldUseFork(%q, %q) = %v, want %v", tc.eventOwner, tc.forkOwner, got, tc.want)
		}
	}
}

func TestGitHubClientAuthenticatedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/user") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"login":"Verdure-oss"}`)
	}))
	defer server.Close()

	gh := &GitHubClient{token: "tok", apiBase: strings.TrimRight(server.URL, "/"), client: server.Client()}
	login, err := gh.AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "Verdure-oss" {
		t.Fatalf("got login %q, want Verdure-oss", login)
	}
}

func TestGitHubLoginCacheResolve(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"login":"Verdure-oss"}`)
	}))
	defer server.Close()

	cache := &gitHubLoginCache{byToken: make(map[string]string)}
	gh := &GitHubClient{token: "tok", apiBase: strings.TrimRight(server.URL, "/"), client: server.Client()}
	first, err := cache.resolve(context.Background(), gh)
	if err != nil || first != "Verdure-oss" {
		t.Fatalf("first resolve failed: login=%q err=%v", first, err)
	}
	second, err := cache.resolve(context.Background(), gh)
	if err != nil || second != "Verdure-oss" {
		t.Fatalf("second resolve failed: login=%q err=%v", second, err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 API call, got %d", calls)
	}
}

func TestIsAlreadyExistsError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"AlreadyExists from kubectl", fmt.Errorf("kubectl apply: Error from server (AlreadyExists): factorytask \"github-test-31\" already exists"), true},
		{"wrapped AlreadyExists", fmt.Errorf("some prefix: AlreadyExists: some suffix"), true},
		{"unrelated error", fmt.Errorf("connection refused"), false},
		{"empty error", fmt.Errorf(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAlreadyExistsError(tt.err); got != tt.want {
				t.Errorf("isAlreadyExistsError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
