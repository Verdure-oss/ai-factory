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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// CheckRun is a single GitHub Actions check run on a commit.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`      // queued | in_progress | completed
	Conclusion string `json:"conclusion"`  // success | failure | neutral | cancelled | timed_out | skipped
	DetailsURL string `json:"details_url"` // e.g. https://github.com/o/r/actions/runs/{run}/job/{job}
}

// CheckRunAnnotation is a single failure annotation on a check run.
type CheckRunAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Level     string `json:"annotation_level"`
	Message   string `json:"message"`
}

// JobLogSnippet is a cleaned excerpt of a failed Actions job log centered on
// the first error line. Lines is nil when the log could not be fetched or
// contained no recognizable error marker; the caller then degrades to the
// per-file annotations instead.
type JobLogSnippet struct {
	CheckRunName string
	Path         string // the check run's details_url the snippet was fetched from
	Lines        []string
}

// ciClient is the subset of GitHubClient the CI watch/repair path depends on.
// It is defined here so webhook handlers and tests can depend on the
// interface rather than a concrete client.
type ciClient interface {
	PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error)
	ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error)
	ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)
	ActionsJobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error)
}

// parsePullRequestURL extracts owner/repo/number from a GitHub PR URL such as
// "https://github.com/matrixhub-ai/matrixhub/pull/929".
func parsePullRequestURL(rawURL string) (owner, repo string, number int, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("parse pull request URL %q: %w", rawURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("invalid pull request URL %q", rawURL)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid pull request number in %q: %w", rawURL, err)
	}
	return parts[0], parts[1], n, nil
}

// PullRequestHeadSHA returns the head commit SHA of a pull request.
func (c *GitHubClient) PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, owner, repo, number), nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("get pull request %s/%s#%d: %s", owner, repo, number, resp.Status)
	}
	var payload struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Head.SHA == "" {
		return "", fmt.Errorf("pull request %s/%s#%d has no head sha", owner, repo, number)
	}
	return payload.Head.SHA, nil
}

// ListCheckRuns lists all check runs for a commit SHA.
func (c *GitHubClient) ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs", c.apiBase, owner, repo, sha), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list check runs for %s/%s@%s: %s", owner, repo, sha, resp.Status)
	}
	var payload struct {
		CheckRuns []CheckRun `json:"check_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.CheckRuns, nil
}

// ListCheckRunAnnotations lists failure annotations for a check run.
func (c *GitHubClient) ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/check-runs/%d/annotations", c.apiBase, owner, repo, checkRunID), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list annotations for check run %d: %s", checkRunID, resp.Status)
	}
	var annotations []CheckRunAnnotation
	if err := json.NewDecoder(resp.Body).Decode(&annotations); err != nil {
		return nil, err
	}
	return annotations, nil
}

// ciCheckStatus is the terminal or pending state of a set of check runs.
type ciCheckStatus int

const (
	// ciCheckPending means at least one check run is still queued/in-progress.
	ciCheckPending ciCheckStatus = iota
	// ciCheckGreen means all completed check runs are non-failing.
	ciCheckGreen
	// ciCheckRed means at least one completed check run failed.
	ciCheckRed
	// ciCheckError means the check-runs API call failed.
	ciCheckError
)

// evaluateCheckRuns classifies a set of check runs as pending, green, or red.
// success, neutral, and skipped conclusions are non-failing; any other
// completed conclusion (failure, cancelled, timed_out, ...) is failing.
func evaluateCheckRuns(runs []CheckRun) ciCheckStatus {
	for _, r := range runs {
		if r.Status != "completed" {
			return ciCheckPending
		}
	}
	for _, r := range runs {
		switch r.Conclusion {
		case "success", "neutral", "skipped", "":
			continue
		default:
			return ciCheckRed
		}
	}
	return ciCheckGreen
}

// isNonFailingConclusion reports whether a completed check-run conclusion is
// green (not a failure).
func isNonFailingConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped", "":
		return true
	default:
		return false
	}
}

// formatCIFailures renders failure annotations as "- path:line [level]: message" lines.
func formatCIFailures(annotations []CheckRunAnnotation) string {
	var b strings.Builder
	for _, a := range annotations {
		fmt.Fprintf(&b, "- %s:%d [%s]: %s\n", a.Path, a.StartLine, a.Level, a.Message)
	}
	return b.String()
}

// summarizeCheckRuns renders one line per check run for reporting.
func summarizeCheckRuns(runs []CheckRun) string {
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "- %s: %s/%s\n", r.Name, r.Status, r.Conclusion)
	}
	return b.String()
}

// collectFailedAnnotations gathers the annotations of every completed failing
// check run on a commit. Per-run annotation errors are tolerated so a single
// flaky annotation endpoint does not block the whole evaluation.
func collectFailedAnnotations(ctx context.Context, gh ciClient, owner, repo, sha string) ([]CheckRunAnnotation, error) {
	runs, err := gh.ListCheckRuns(ctx, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	var all []CheckRunAnnotation
	for _, r := range runs {
		if r.Status != "completed" || isNonFailingConclusion(r.Conclusion) {
			continue
		}
		anns, err := gh.ListCheckRunAnnotations(ctx, owner, repo, r.ID)
		if err != nil {
			continue // tolerate per-run annotation errors
		}
		all = append(all, anns...)
	}
	return all, nil
}

// jobIDFromDetailsURL extracts the Actions job id from a check run's
// details_url, e.g. https://github.com/o/r/actions/runs/31476849873/job/93732416644.
func jobIDFromDetailsURL(detailsURL string) (int64, bool) {
	u, err := url.Parse(detailsURL)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 1; i-- {
		if parts[i] == "job" {
			id, err := strconv.ParseInt(parts[i+1], 10, 64)
			return id, err == nil
		}
	}
	return 0, false
}

var (
	// ciAnsiPattern strips ANSI CSI escape sequences (colors, wrapping) from
	// job log lines, e.g. "\x1b[1;31m" emitted by GitHub Actions.
	ciAnsiPattern = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")
	// ciFailureMarkers are substrings that mark an actionable error line in a
	// job log. First matching line anchors the snippet window.
	ciFailureMarkers = []string{"##[error]", "FAIL", "Error:", "error:"}
)

// collectFailedJobLogs fetches the Actions job log for each completed failing
// check run with a resolvable details_url job id, strips ANSI escapes, and
// keeps a window of snippetLines lines around the first error marker. Runs
// whose log cannot be fetched or contains no error marker are represented with
// nil Lines so the caller can degrade to per-file annotations.
func collectFailedJobLogs(ctx context.Context, gh ciClient, owner, repo string, runs []CheckRun, snippetLines int) ([]JobLogSnippet, error) {
	if snippetLines <= 0 {
		snippetLines = 20
	}
	var snippets []JobLogSnippet
	for _, r := range runs {
		if r.Status != "completed" || isNonFailingConclusion(r.Conclusion) {
			continue
		}
		s := JobLogSnippet{CheckRunName: r.Name, Path: r.DetailsURL}
		jobID, ok := jobIDFromDetailsURL(r.DetailsURL)
		if !ok {
			continue // not an Actions job URL; never fetched, stays absent
		}
		logBytes, err := gh.ActionsJobLogs(ctx, owner, repo, jobID)
		if err != nil {
			snippets = append(snippets, s) // nil Lines: caller degrades to annotations
			continue
		}
		s.Lines = snippetFromLog(logBytes, snippetLines)
		snippets = append(snippets, s)
	}
	return snippets, nil
}

// snippetFromLog cleans raw Actions job logs (ANSI stripped, CRLF normalized)
// and returns the window of snippetLines lines around the first failure
// marker. It returns nil when no marker is found.
func snippetFromLog(log []byte, snippetLines int) []string {
	clean := make([]string, 0, 64)
	idx := -1
	for _, raw := range strings.Split(string(log), "\n") {
		line := ciAnsiPattern.ReplaceAllString(strings.TrimSuffix(raw, "\r"), "")
		if idx < 0 && isCIFailureLine(line) {
			idx = len(clean)
		}
		clean = append(clean, line)
	}
	if idx < 0 {
		return nil
	}
	start := idx - snippetLines
	if start < 0 {
		start = 0
	}
	end := idx + snippetLines + 1
	if end > len(clean) {
		end = len(clean)
	}
	return clean[start:end]
}

// isCIFailureLine reports whether a line from a cleaned job log contains one
// of the error markers used to anchor a snippet window.
func isCIFailureLine(line string) bool {
	for _, m := range ciFailureMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// buildCIRepairInstructions builds the prompt fed to the coding agent when a
// PR's CI fails: the original task, the PR URL, the exact failure excerpts
// (job-log snippets preferred, then per-file annotations), and strict
// no-exploration constraints so the repair stays focused.
func buildCIRepairInstructions(originalInstructions, prURL string, annotations []CheckRunAnnotation, logSnippets []JobLogSnippet, allowTestChanges bool, snippetLines int) string {
	var b strings.Builder
	b.WriteString("The implementation for this issue was completed and a pull request was created at ")
	b.WriteString(prURL)
	b.WriteString(".\n\nGitHub CI failed. The failures below are EXACT and COMPLETE. Your job is to fix ONLY these failures and nothing else.\n\n")

	if len(logSnippets) > 0 {
		b.WriteString("## CI job log excerpts (around the errors)\n")
		for _, s := range logSnippets {
			fmt.Fprintf(&b, "### %s\n", s.CheckRunName)
			if len(s.Lines) == 0 {
				b.WriteString("(log unavailable)\n")
			} else {
				for _, line := range s.Lines {
					b.WriteString(line)
					b.WriteString("\n")
				}
			}
		}
	} else if len(annotations) > 0 {
		b.WriteString("## CI annotations\n")
		b.WriteString(formatCIFailures(annotations))
	} else {
		b.WriteString("(No per-file annotations or logs were returned; the CI job failed. Inspect the reported files only and fix the failure.)\n")
	}

	b.WriteString("\n## Original task instructions\n")
	b.WriteString(originalInstructions)
	b.WriteString("\n\n## Constraints\n")
	b.WriteString("- Fix ONLY the CI failures listed above. Do NOT undo the existing implementation, do NOT redo the whole task, do NOT refactor unrelated code.\n")
	b.WriteString("- Read ONLY the files implicated by the failure lines above. You already have the full repository context from the task execution; do NOT re-explore.\n")
	b.WriteString("- FORBIDDEN: repository-wide searches (find . , grep -rn across the repo), reading unrelated config files (e.g. .golangci.yml unless the error explicitly names it), and re-planning the task.\n")
	if allowTestChanges {
		b.WriteString("- You MAY modify test files if the failure is in test code (e.g. a mock missing an interface method). The goal is CI green.\n")
	} else {
		b.WriteString("- Do NOT modify test files.\n")
	}
	b.WriteString("- After fixing, run the focused validation that corresponds to the failure (e.g. go build ./... or go test ./... for the affected package) using the same commands the failing job ran, then finish immediately.\n")
	return b.String()
}
