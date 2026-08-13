package server

import (
	"context"
	"strings"
	"testing"
)

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
// responses; gotJobLogs records the job IDs passed to ActionsJobLogs.
type fakeCIClient struct {
	headSHA     string
	runs        []CheckRun
	annotations map[int64][]CheckRunAnnotation
	logs        map[int64][]byte
	logErr      error
	gotJobLogs  []int64
}

func (f *fakeCIClient) PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	return f.headSHA, nil
}

func (f *fakeCIClient) ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error) {
	return f.runs, nil
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
