package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// gitLabRecorder captures everything the fake GitLab API sees.
type gitLabRecorder struct {
	requests int
	puts     []url.Values
	comments []string
	headers  []http.Header
}

// newFakeGitLabAPI starts the fake GitLab API and returns its recorder. It is
// the single fake-server implementation; fakeGitLabAPI adapts it for the
// label/comment tests that only need those two slices.
func newFakeGitLabAPI(t *testing.T) (*httptest.Server, *gitLabRecorder) {
	t.Helper()
	rec := &gitLabRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.requests++
		rec.headers = append(rec.headers, r.Header.Clone())
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/issues/"):
			rec.puts = append(rec.puts, r.URL.Query())
			w.WriteHeader(200)
			w.Write([]byte(`{"iid":7}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			rec.comments = append(rec.comments, m["body"])
			w.WriteHeader(201)
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	return server, rec
}

// fakeGitLabAPI records label add/remove query params and comment bodies.
func fakeGitLabAPI(t *testing.T) (*httptest.Server, *[]url.Values, *[]string) {
	t.Helper()
	server, rec := newFakeGitLabAPI(t)
	return server, &rec.puts, &rec.comments
}

func newTestGitLabClient(apiBase string, client *http.Client) *GitLabClient {
	return &GitLabClient{token: "glpat-test", apiBase: apiBase, client: client}
}

func TestGitLabSetTaskRunning(t *testing.T) {
	server, puts, _ := fakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())

	if err := c.SetTaskRunning(context.Background(), "platform/ai/ai-factory", 7); err != nil {
		t.Fatalf("SetTaskRunning: %v", err)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(*puts))
	}
	q := (*puts)[0]
	if !strings.Contains(q.Get("add_labels"), "ai-factory-running") {
		t.Errorf("add_labels = %q, want ai-factory-running", q.Get("add_labels"))
	}
	rem := q.Get("remove_labels")
	for _, want := range []string{"ai-factory-waiting", "ai-factory-done", "ai-factory-failed"} {
		if !strings.Contains(rem, want) {
			t.Errorf("remove_labels = %q, want to contain %q", rem, want)
		}
	}
}

func TestGitLabProjectPathIsEncoded(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())
	_ = c.SetTaskDone(context.Background(), "platform/ai/ai-factory", 7)
	// The project path segments must be percent-encoded into a single path
	// element: platform%2Fai%2Fai-factory
	if !strings.Contains(gotPath, "platform%2Fai%2Fai-factory") {
		t.Fatalf("escaped path = %q, want encoded project path", gotPath)
	}
}

func TestGitLabPostComment(t *testing.T) {
	server, _, comments := fakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())
	if err := c.PostComment(context.Background(), "platform/ai/ai-factory", 7, "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if len(*comments) != 1 || (*comments)[0] != "hello" {
		t.Fatalf("comments = %#v, want [hello]", *comments)
	}
}

// labelSet parses a comma-separated label param into a set.
func labelSet(csv string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(csv, ",") {
		if l = strings.TrimSpace(l); l != "" {
			set[l] = true
		}
	}
	return set
}

// assertLabelSet requires the param to hold exactly the wanted labels.
func assertLabelSet(t *testing.T, field, got string, want ...string) {
	t.Helper()
	gotSet := labelSet(got)
	if len(gotSet) != len(want) {
		t.Errorf("%s = %q, want exactly %v", field, got, want)
		return
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("%s = %q, want to contain %q", field, got, w)
		}
	}
}

func TestGitLabSetTaskWaiting(t *testing.T) {
	server, puts, _ := fakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())

	if err := c.SetTaskWaiting(context.Background(), "platform/ai/ai-factory", 7); err != nil {
		t.Fatalf("SetTaskWaiting: %v", err)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(*puts))
	}
	q := (*puts)[0]
	assertLabelSet(t, "add_labels", q.Get("add_labels"), labelWaiting)
	assertLabelSet(t, "remove_labels", q.Get("remove_labels"), labelRunning)
}

func TestGitLabSetTaskFailed(t *testing.T) {
	server, puts, _ := fakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())

	if err := c.SetTaskFailed(context.Background(), "platform/ai/ai-factory", 7); err != nil {
		t.Fatalf("SetTaskFailed: %v", err)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(*puts))
	}
	q := (*puts)[0]
	assertLabelSet(t, "add_labels", q.Get("add_labels"), labelFailed)
	assertLabelSet(t, "remove_labels", q.Get("remove_labels"),
		labelRunning, labelWaiting, labelDone, labelRun, labelSmoke)
}

func TestGitLabSetTaskCancelled(t *testing.T) {
	server, puts, comments := fakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())

	if err := c.SetTaskCancelled(context.Background(), "platform/ai/ai-factory", 7, labelRun); err != nil {
		t.Fatalf("SetTaskCancelled: %v", err)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(*puts))
	}
	q := (*puts)[0]
	if got := q.Get("add_labels"); got != "" {
		t.Errorf("add_labels = %q, want empty on cancel", got)
	}
	assertLabelSet(t, "remove_labels", q.Get("remove_labels"), labelRunning, labelWaiting)
	want := cancelCommentBody(7, labelRun)
	if len(*comments) != 1 || (*comments)[0] != want {
		t.Fatalf("comments = %#v, want [%q]", *comments, want)
	}
}

func TestGitLabSetHostFromRepositoryURL(t *testing.T) {
	tests := []struct {
		name    string
		apiBase string
		host    string
		want    string
	}{
		{
			name:    "explicit api base wins",
			apiBase: "https://gitlab.example.com/api/v4",
			host:    "other.example.com",
			want:    "https://gitlab.example.com/api/v4",
		},
		{name: "empty host is a no-op", apiBase: "", host: "   ", want: ""},
		{
			name:    "derives api base from host",
			apiBase: "",
			host:    "gitlab.corp.example",
			want:    "https://gitlab.corp.example/api/v4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &GitLabClient{apiBase: tc.apiBase}
			c.SetHostFromRepositoryURL(tc.host)
			if c.apiBase != tc.want {
				t.Errorf("apiBase = %q, want %q", c.apiBase, tc.want)
			}
		})
	}
}

func TestGitLabNoTokenSkipsHTTP(t *testing.T) {
	server, rec := newFakeGitLabAPI(t)
	defer server.Close()
	c := &GitLabClient{token: "", apiBase: server.URL, client: server.Client()}

	if c.HasToken() {
		t.Fatal("HasToken() = true for empty token")
	}
	if err := c.SetTaskRunning(context.Background(), "platform/ai/ai-factory", 7); err != nil {
		t.Fatalf("SetTaskRunning: %v", err)
	}
	if err := c.PostComment(context.Background(), "platform/ai/ai-factory", 7, "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if rec.requests != 0 {
		t.Fatalf("fake server got %d requests, want 0 without a token", rec.requests)
	}
}

func TestGitLabSendsPrivateTokenHeader(t *testing.T) {
	server, rec := newFakeGitLabAPI(t)
	defer server.Close()
	c := newTestGitLabClient(server.URL, server.Client())

	if err := c.SetTaskRunning(context.Background(), "platform/ai/ai-factory", 7); err != nil {
		t.Fatalf("SetTaskRunning: %v", err)
	}
	if len(rec.headers) != 1 {
		t.Fatalf("expected 1 request, got %d", len(rec.headers))
	}
	if got := rec.headers[0].Get("PRIVATE-TOKEN"); got != "glpat-test" {
		t.Errorf("PRIVATE-TOKEN = %q, want %q", got, "glpat-test")
	}
}
