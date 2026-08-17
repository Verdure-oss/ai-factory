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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// CheckSuiteRef is the suite a check run belongs to (nested in the run object).
type CheckSuiteRef struct {
	ID int64 `json:"id"`
}

// CheckRun is a single GitHub Actions check run on a commit.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`      // queued | in_progress | completed
	Conclusion string `json:"conclusion"`  // success | failure | neutral | cancelled | timed_out | skipped
	DetailsURL string `json:"details_url"` // e.g. https://github.com/o/r/actions/runs/{run}/job/{job}
	// CheckSuite is the suite this run belongs to. Used to tell a real
	// run (which turns its suite in_progress/completed) apart from a "ghost"
	// suite that GitHub leaves queued forever with no run behind it.
	CheckSuite CheckSuiteRef `json:"check_suite"`
}

// CheckSuite is a single GitHub Actions check suite on a commit. A suite
// aggregates a set of check runs and completes when its runs finish; its
// conclusion is the aggregate verdict. check-suites is the authoritative
// convergence signal: a suite may register lazily (reusable workflows, etc.)
// well after the first runs appear, so the verdict must wait for every suite
// to be completed before judging green.
type CheckSuite struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | neutral | cancelled | skipped | timed_out | action_required | ...
	// HeadSHA is the commit SHA this suite belongs to. Suites are fetched
	// per-commit, but a repair force-push changes the PR head: after a push
	// the API may briefly still return the previous commit's suites, so the
	// verdict must only consider suites whose HeadSHA equals the current PR
	// head. Stale-head suites (e.g. a still-failing old commit) must never
	// re-trigger red or block green.
	HeadSHA string `json:"head_sha"`
}

// CheckRunAnnotation is a single failure annotation on a check run.
type CheckRunAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Level     string `json:"annotation_level"`
	Message   string `json:"message"`
}

// JobLogSnippet is a cleaned excerpt of a failed Actions job log containing
// every failure line found (with small context when they are sparse). Lines is
// nil when the log could not be fetched or contained no recognizable error
// marker; the caller then degrades to the per-file annotations instead.
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
	ListCheckSuites(ctx context.Context, owner, repo, sha string) ([]CheckSuite, error)
	ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)
	ActionsJobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error)
	CommentOnIssue(ctx context.Context, owner, repo string, number int, body string) error
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

// githubPerPage is the page size used for paginated list endpoints. GitHub
// caps list endpoints at 100 per page; many workflows (matrices, reusable
// workflows) easily exceed the default 30, so we always request 100.
const githubPerPage = 100

// withPerPage appends ?per_page=N (or &per_page=N when the URL already has
// query parameters) to a raw GitHub API URL.
func withPerPage(rawURL string, perPage int) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sper_page=%d", rawURL, sep, perPage)
}

// nextLink extracts the rel="next" URL from a GitHub Link header, or "" when
// there is no next page.
func nextLink(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		seg := strings.Split(strings.TrimSpace(part), ";")
		if len(seg) < 2 {
			continue
		}
		target := strings.TrimSpace(seg[0])
		rel := ""
		for _, param := range seg[1:] {
			param = strings.TrimSpace(param)
			if rest, ok := strings.CutPrefix(param, `rel=`); ok {
				rel = strings.Trim(rest, `"`)
			}
		}
		if rel == "next" {
			return strings.Trim(target, "<>")
		}
	}
	return ""
}

// listGitHubPages fetches every page of a paginated GitHub list endpoint,
// following Link rel="next" until exhausted, and calls fn with the raw JSON
// body of each page in order. Abstracting pagination here keeps the list
// methods truncation-free: a commit with more runs than one page must not
// silently drop failing runs from the verdict or the repair evidence.
func listGitHubPages(ctx context.Context, c *GitHubClient, rawURL string, perPage int, fn func([]byte) error) error {
	next := withPerPage(rawURL, perPage)
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		c.setHeaders(req)
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("list %s: %s", next, resp.Status)
		}
		if readErr != nil {
			return readErr
		}
		if err := fn(body); err != nil {
			return err
		}
		next = nextLink(resp.Header.Get("Link"))
	}
	return nil
}

// ListCheckRuns lists every check run for a commit SHA, following pagination.
func (c *GitHubClient) ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error) {
	var all []CheckRun
	err := listGitHubPages(ctx, c, fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs", c.apiBase, url.PathEscape(owner), url.PathEscape(repo), sha), githubPerPage, func(body []byte) error {
		var payload struct {
			CheckRuns []CheckRun `json:"check_runs"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		all = append(all, payload.CheckRuns...)
		return nil
	})
	return all, err
}

// ListCheckSuites lists every check suite for a commit SHA, following
// pagination. Suites are the convergence signal: a commit can have many
// suites (one per workflow), and late-registering suites are exactly what the
// short confirm window exists to catch.
func (c *GitHubClient) ListCheckSuites(ctx context.Context, owner, repo, sha string) ([]CheckSuite, error) {
	var all []CheckSuite
	err := listGitHubPages(ctx, c, fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-suites", c.apiBase, url.PathEscape(owner), url.PathEscape(repo), sha), githubPerPage, func(body []byte) error {
		var payload struct {
			CheckSuites []CheckSuite `json:"check_suites"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		all = append(all, payload.CheckSuites...)
		return nil
	})
	return all, err
}

// ListCheckRunAnnotations lists failure annotations for a check run, following
// pagination.
func (c *GitHubClient) ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error) {
	var all []CheckRunAnnotation
	err := listGitHubPages(ctx, c, fmt.Sprintf("%s/repos/%s/%s/check-runs/%d/annotations", c.apiBase, url.PathEscape(owner), url.PathEscape(repo), checkRunID), githubPerPage, func(body []byte) error {
		var annotations []CheckRunAnnotation
		if err := json.Unmarshal(body, &annotations); err != nil {
			return err
		}
		all = append(all, annotations...)
		return nil
	})
	return all, err
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

// allSuitesCompleted reports whether every listed check suite has finished.
// An empty list is intentionally NOT converged: immediately after a force-push
// (and during lazy registration windows) the suite list can be empty, and
// judging that "all suites completed" would falsely pass green before CI
// started. The caller must keep waiting until at least one suite exists.
func allSuitesCompleted(suites []CheckSuite) bool {
	if len(suites) == 0 {
		return false
	}
	for _, s := range suites {
		if s.Status != "completed" {
			return false
		}
	}
	return true
}

// evaluateCheckSuites classifies a set of check suites as pending, green, or
// red. A suite completes only when its runs finish, so this is the authority
// for the convergence verdict — it cannot be fooled by lazy check-run
// registration the way evaluateCheckRuns can.
func evaluateCheckSuites(suites []CheckSuite) ciCheckStatus {
	if !allSuitesCompleted(suites) {
		return ciCheckPending
	}
	for _, s := range suites {
		if !isNonFailingConclusion(s.Conclusion) {
			return ciCheckRed
		}
	}
	return ciCheckGreen
}

// fastRedSuites reports whether any completed suite has a non-failing-missing
// conclusion. A failed suite is terminal — GitHub never reverts a suite
// conclusion — so one failed suite settles the verdict red without waiting for
// the remaining suites or the confirm window. This preserves the old
// immediate-red fast path while staying suite-authoritative.
func fastRedSuites(suites []CheckSuite) bool {
	for _, s := range suites {
		if s.Status == "completed" && !isNonFailingConclusion(s.Conclusion) {
			return true
		}
	}
	return false
}

// suiteIDs returns the set of suite ids in a list, used to detect late-
// registering suites across evaluations.
func suiteIDs(suites []CheckSuite) map[int64]bool {
	ids := make(map[int64]bool, len(suites))
	for _, s := range suites {
		ids[s.ID] = true
	}
	return ids
}

// sameSuiteIDs reports whether two suite id sets are equal.
func sameSuiteIDs(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

// summarizeCheckSuites renders one line per check suite for reporting.
func summarizeCheckSuites(suites []CheckSuite) string {
	var b strings.Builder
	for _, s := range suites {
		fmt.Fprintf(&b, "- suite %d: %s/%s\n", s.ID, s.Status, s.Conclusion)
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
	// job log. snippetFromLog collects every matching line.
	ciFailureMarkers = []string{"##[error]", "FAIL", "Error:", "error:"}
)

// collectFailedJobLogs fetches the Actions job log for each completed failing
// check run with a resolvable details_url job id, strips ANSI escapes, and
// extracts every failure line via snippetFromLog. Runs whose log cannot be
// fetched or contains no error marker are represented with nil Lines so the
// caller can degrade to per-file annotations.
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
// and returns the actionable failure lines plus, when failures are few, a
// small context window around each. Unlike the old first-marker-only behavior,
// it surfaces EVERY failure line: a job that reports multiple problems in one
// run (e.g. a typecheck error AND a missing license header) shows them all to
// the repair agent in a single pass instead of one at a time. It returns nil
// when no failure marker is found, which lets the caller degrade to
// per-file annotations.
func snippetFromLog(log []byte, snippetLines int) []string {
	if snippetLines <= 0 {
		snippetLines = 20
	}
	clean := make([]string, 0, 64)
	var failIdx []int
	for _, raw := range strings.Split(string(log), "\n") {
		line := ciAnsiPattern.ReplaceAllString(strings.TrimSuffix(raw, "\r"), "")
		if isCIFailureLine(line) {
			failIdx = append(failIdx, len(clean))
		}
		clean = append(clean, line)
	}
	if len(failIdx) == 0 {
		return nil
	}

	// Window of context lines around each failure. Failures that come densely
	// (a whole lint report) carry their own path:line, so no context needed;
	// sparse failures (a lone "Error:" line) get surrounding lines so the
	// agent can make sense of them.
	window := 0
	if len(failIdx) < 3 {
		window = 3
	}
	out := make([]string, 0, len(failIdx)*(window*2+1))
	emit := make(map[int]bool, len(failIdx)*(window*2+1))
	add := func(i int) {
		if i < 0 || i >= len(clean) || emit[i] {
			return
		}
		emit[i] = true
		out = append(out, clean[i])
	}
	for _, idx := range failIdx {
		for i := idx - window; i <= idx+window; i++ {
			add(i)
		}
	}
	return out
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

// goheaderImplicated reports whether the collected failure evidence points at
// the goheader linter (a missing repository license header). When it does,
// buildCIRepairInstructions grants the narrow permission to read one existing
// .go file (or .golangci.yml) so the repair agent can copy the real,
// repo-specific header instead of guessing or falling back to inventing one.
func goheaderImplicated(logSnippets []JobLogSnippet, annotations []CheckRunAnnotation) bool {
	for _, s := range logSnippets {
		for _, line := range s.Lines {
			if strings.Contains(line, "goheader") || strings.Contains(line, "Missed header") {
				return true
			}
		}
	}
	for _, a := range annotations {
		if strings.Contains(a.Message, "goheader") || strings.Contains(a.Message, "Missed header") {
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

	b.WriteString("\n## Original task instructions (background context only)\n")
	b.WriteString("The instructions below are the ORIGINAL task the implementation was built from. They are background context about intent only. The CI failures listed above are the current authority and OVERRIDE the original instructions: if the original instructions tell you NOT to modify files, NOT to run validation or fixes, or to keep intentionally broken code, ignore those parts — your only goal is to make the listed CI failures pass. Rewriting an existing file to its exact original content is NOT a valid action and will not clear a failure; change the failing code so the checks pass.\n\n")
	b.WriteString(originalInstructions)
	b.WriteString("\n\n## Constraints\n")
	b.WriteString("- Fix ONLY the CI failures listed above. Do NOT undo the existing implementation, do NOT redo the whole task, do NOT refactor unrelated code.\n")
	b.WriteString("- Read ONLY the files implicated by the failure lines above. You already have the full repository context from the task execution; do NOT re-explore.\n")
	b.WriteString("- FORBIDDEN: repository-wide searches (find . , grep -rn across the repo), reading unrelated config files (e.g. .golangci.yml unless the error explicitly names it), and re-planning the task.\n")
	if goheaderImplicated(logSnippets, annotations) {
		b.WriteString("- The goheader linter fails because this .go file is missing the repository's license header. The header is repo-specific and NOT included above. It IS expressly permitted to (a) read ONE existing .go file from the repository (any package that already passes lint) and copy its leading comment/license block verbatim to the TOP of the failing file, and (b) read .golangci.yml ONLY to locate the exact goheader template if copying an existing header does not satisfy it. Do NOT invent a header; copy the real one.\n")
	}
	if allowTestChanges {
		b.WriteString("- You MAY modify test files if the failure is in test code (e.g. a mock missing an interface method). The goal is CI green.\n")
	} else {
		b.WriteString("- Do NOT modify test files.\n")
	}
	b.WriteString("- Validate with a NON-NETWORK check only. The sandbox is offline: a `go` command that needs to download a missing Go toolchain or a module would hang on the network and fail, so prefer `gofmt -l <files>` or reading the fixed source yourself to confirm the change. If a targeted `go` command starts with a toolchain download it will fail fast (GOTOOLCHAIN=local); that is expected offline behavior, not a reason to keep fixing. The real validation runs again in CI; your job is only to make the code correct. After your fix, finish immediately.\n")
	return b.String()
}
