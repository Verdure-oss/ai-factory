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

// fakeGitLabAPI records label add/remove query params and comment bodies.
func fakeGitLabAPI(t *testing.T) (*httptest.Server, *[]url.Values, *[]string) {
	t.Helper()
	var puts []url.Values
	var comments []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/issues/"):
			puts = append(puts, r.URL.Query())
			w.WriteHeader(200)
			w.Write([]byte(`{"iid":7}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			comments = append(comments, m["body"])
			w.WriteHeader(201)
			w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	return server, &puts, &comments
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
