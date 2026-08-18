package server

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestNextLink(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			"next page",
			`<https://api.github.com/repos/o/r/commits/abc/check-suites?page=2>; rel="next", <https://api.github.com/repos/o/r/commits/abc/check-suites?page=2>; rel="last"`,
			"https://api.github.com/repos/o/r/commits/abc/check-suites?page=2",
		},
		{"last page", `<https://api.github.com/x>; rel="last"`, ""},
		{"empty header", "", ""},
		{"garbage", "not a link header", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextLink(tt.header); got != tt.want {
				t.Errorf("nextLink(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestAllSuitesCompleted(t *testing.T) {
	empty := allSuitesCompleted(nil)
	if empty {
		t.Error("allSuitesCompleted(nil) = true, want false: an empty suite list must not be converged")
	}
	done := allSuitesCompleted([]CheckSuite{
		{ID: 1, Status: "completed", Conclusion: "success"},
		{ID: 2, Status: "completed", Conclusion: "failure"},
	})
	if !done {
		t.Error("allSuitesCompleted with all completed = false, want true")
	}
	pending := allSuitesCompleted([]CheckSuite{
		{ID: 1, Status: "completed", Conclusion: "success"},
		{ID: 2, Status: "in_progress", Conclusion: ""},
	})
	if pending {
		t.Error("allSuitesCompleted with in_progress = true, want false")
	}
}

func TestJobIDFromDetailsURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		wantID int64
		wantOK bool
	}{
		{"run + job segments", "https://github.com/o/r/actions/runs/31476849873/job/93732416644", 93732416644, true},
		{"job only", "https://github.com/o/r/actions/runs/job/93732416644", 93732416644, true},
		{"empty", "", 0, false},
		{"pull request url", "https://github.com/o/r/pull/5", 0, false},
		{"no scheme", "not-a-url", 0, false},
		{"non-numeric job id", "https://github.com/o/r/actions/runs/123/job/abc", 0, false},
		{"trailing slash after job id", "https://github.com/o/r/actions/runs/31476849873/job/93732416644/", 93732416644, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotOK := jobIDFromDetailsURL(tt.rawURL)
			if gotOK != tt.wantOK || gotID != tt.wantID {
				t.Errorf("jobIDFromDetailsURL(%q) = (%d, %v), want (%d, %v)",
					tt.rawURL, gotID, gotOK, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestBuildCIRepairInstructionsEmptyAnnotations(t *testing.T) {
	inst := buildCIRepairInstructions("fix the widget", "https://github.com/o/r/pull/5", nil, nil, false, 20)
	for _, want := range []string{
		"https://github.com/o/r/pull/5",
		"GitHub CI failed. The failures below are EXACT and COMPLETE",
		"No per-file annotations or logs were returned",
		"## Original task instructions",
		"fix the widget",
		"Do NOT modify test files.",
	} {
		if !strings.Contains(inst, want) {
			t.Errorf("expected instructions to contain %q, got:\n%s", want, inst)
		}
	}
}

func TestBuildCIRepairInstructionsIncludesLogSnippet(t *testing.T) {
	snippets := []JobLogSnippet{{
		CheckRunName: "lint",
		Lines:        []string{"lint: internal/check.go:33: unexpected token", "see lint docs"},
	}}
	inst := buildCIRepairInstructions("orig", "https://github.com/o/r/pull/5", nil, snippets, true, 20)
	for _, want := range []string{
		"## CI job log excerpts (around the errors)",
		"### lint",
		"lint: internal/check.go:33: unexpected token",
		"see lint docs",
		"You MAY modify test files if the failure is in test code",
	} {
		if !strings.Contains(inst, want) {
			t.Errorf("expected instructions to contain %q, got:\n%s", want, inst)
		}
	}
	if strings.Contains(inst, "## CI annotations") {
		t.Error("annotations section should not appear when log snippets are present")
	}
	if strings.Contains(inst, "Do NOT modify test files.") {
		t.Error("expected the allowed test changes line when allowTestChanges is true")
	}
}

func TestCollectFailedJobLogs(t *testing.T) {
	rawLog := "\x1b[31m##[error]generator_test.go:42: test failed\x1b[0m\nFAIL\tgithub.com/ai-on-gke/ai-factory/factory\n"
	gh := &fakeCIClient{
		runs: []CheckRun{
			{ID: 1, Name: "lint", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/o/r/actions/runs/10/job/101"},
			{ID: 2, Name: "test", Status: "completed", Conclusion: "success", DetailsURL: "https://github.com/o/r/actions/runs/10/job/102"},
			{ID: 3, Name: "scheduled", Status: "completed", Conclusion: "failure", DetailsURL: ""},
		},
		logs: map[int64][]byte{101: []byte(rawLog)},
	}
	snippets, err := collectFailedJobLogs(context.Background(), gh, "o", "r", gh.runs, 20)
	if err != nil {
		t.Fatalf("collectFailedJobLogs: %v", err)
	}
	if len(snippets) != 1 {
		t.Fatalf("expected 1 snippet (only the failing run with a job URL), got %d", len(snippets))
	}
	if snippets[0].CheckRunName != "lint" {
		t.Errorf("snippet check run name = %q, want %q", snippets[0].CheckRunName, "lint")
	}
	joined := strings.Join(snippets[0].Lines, "\n")
	if !strings.Contains(joined, "##[error]generator_test.go:42: test failed") {
		t.Errorf("snippet missing the error line (ANSI should be stripped), got:\n%s", joined)
	}
	// Only the failing lint run should trigger a log fetch.
	if len(gh.gotJobLogs) != 1 || gh.gotJobLogs[0] != 101 {
		t.Errorf("ActionsJobLogs called with job IDs %v, want only [101]", gh.gotJobLogs)
	}
}

func TestCollectFailedJobLogsDegradesOnLogError(t *testing.T) {
	gh := &fakeCIClient{
		runs: []CheckRun{
			{ID: 1, Name: "lint", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/o/r/actions/runs/10/job/101"},
		},
		logErr: context.DeadlineExceeded,
	}
	snippets, err := collectFailedJobLogs(context.Background(), gh, "o", "r", gh.runs, 20)
	if err != nil {
		t.Fatalf("collectFailedJobLogs: %v", err)
	}
	if len(snippets) != 1 {
		t.Fatalf("expected 1 snippet degrading to nil lines, got %d", len(snippets))
	}
	if snippets[0].Lines != nil {
		t.Errorf("expected nil Lines when the log fetch fails, got %v", snippets[0].Lines)
	}
}

// fakeCIClient is a test double for ciClient. Fields configure canned
// responses; gotJobLogs records the job IDs passed to ActionsJobLogs and
// comments records every PR comment body passed to CommentOnIssue.
//
// mu guards the canned state (headSHA/suites/runs) because the CI watch loop
// reads it from a goroutine while the test goroutine advances the simulation
// between wakes; use setHeadSHA/setSuites to mutate.
type fakeCIClient struct {
	mu          sync.Mutex
	headSHA     string
	runs        []CheckRun
	suites      []CheckSuite
	annotations map[int64][]CheckRunAnnotation
	logs        map[int64][]byte
	logErr      error
	gotJobLogs  []int64
	comments    []string
}

func (f *fakeCIClient) setHeadSHA(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headSHA = v
}

func (f *fakeCIClient) setSuites(v []CheckSuite) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suites = v
}

func (f *fakeCIClient) PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headSHA, nil
}

func (f *fakeCIClient) ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs, nil
}

func (f *fakeCIClient) ListCheckSuites(ctx context.Context, owner, repo, sha string) ([]CheckSuite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.suites, nil
}

func (f *fakeCIClient) ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error) {
	return f.annotations[checkRunID], nil
}

func (f *fakeCIClient) ActionsJobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error) {
	f.gotJobLogs = append(f.gotJobLogs, jobID)
	if f.logErr != nil {
		return nil, f.logErr
	}
	return f.logs[jobID], nil
}

func (f *fakeCIClient) CommentOnIssue(ctx context.Context, owner, repo string, number int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

// TestSnippetFromLogCollectsAllFailureLines proves snippetFromLog surfaces
// EVERY failure line, not just the first one — a lint log reporting a
// typecheck error AND a missing license header must show both, so the repair
// agent fixes all problems in one pass.
func TestSnippetFromLogCollectsAllFailureLines(t *testing.T) {
	rawLog := "\x1b[31m##[error]internal/ci-repro/ci_repro.go:5:12: cannot use \"oops\" (untyped string constant) as int value (typecheck)\x1b[0m\n" +
		"package ci_repro\n" +
		"##[error]internal/ci-repro/ci_repro.go:1:1: Missed header for check (goheader)\n" +
		"### issues summary\n" +
		"##[error]issues found\n"
	lines := snippetFromLog([]byte(rawLog), 20)
	if lines == nil {
		t.Fatal("snippetFromLog returned nil; expected failure lines")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"cannot use \"oops\"",
		"Missed header for check (goheader)",
		"issues found",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("snippet missing failure line containing %q, got:\n%s", want, joined)
		}
	}
}

// TestSnippetFromLogNoFailureReturnsNil proves the degrade-to-annotations
// fallback: a log with no recognizable failure marker yields nil.
func TestSnippetFromLogNoFailureReturnsNil(t *testing.T) {
	if got := snippetFromLog([]byte("no errors here\njust gofmt output\n"), 20); got != nil {
		t.Errorf("snippetFromLog(clean log) = %v, want nil", got)
	}
}

// TestBuildCIRepairInstructionsGoheaderGuidance proves a goheader failure
// grants the repair agent a narrow permission to read one existing .go file
// (or .golangci.yml) to copy the repo's real license header — otherwise the
// strict no-exploration constraints lock the agent out of fixing the header.
func TestBuildCIRepairInstructionsGoheaderGuidance(t *testing.T) {
	snippets := []JobLogSnippet{{
		CheckRunName: "lint",
		Lines:        []string{"##[error]internal/ci-repro/ci_repro.go:1:1: Missed header for check (goheader)"},
	}}
	inst := buildCIRepairInstructions("orig", "https://github.com/o/r/pull/5", nil, snippets, true, 20)
	if !strings.Contains(inst, "missing the repository's license header") {
		t.Errorf("goheader guidance missing, got:\n%s", inst)
	}
	if !strings.Contains(inst, "copy its leading comment/license block verbatim") {
		t.Errorf("copy-header permission missing, got:\n%s", inst)
	}
	if !strings.Contains(inst, "Do NOT invent a header") {
		t.Errorf("no-invent guard missing, got:\n%s", inst)
	}
	// typecheck-only failures must NOT get the goheader permission.
	inst2 := buildCIRepairInstructions("orig", "https://github.com/o/r/pull/5", nil, []JobLogSnippet{{
		CheckRunName: "lint",
		Lines:        []string{"cannot use \"oops\" (untyped string constant) as int value"},
	}}, true, 20)
	if strings.Contains(inst2, "missing the repository's license header") {
		t.Errorf("goheader guidance wrongly present for a non-goheader failure")
	}
}
