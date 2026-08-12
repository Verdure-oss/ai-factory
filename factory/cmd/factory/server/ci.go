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
	"strconv"
	"strings"
)

// CheckRun is a single GitHub Actions check run on a commit.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | neutral | cancelled | timed_out | skipped
}

// CheckRunAnnotation is a single failure annotation on a check run.
type CheckRunAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	Level     string `json:"annotation_level"`
	Message   string `json:"message"`
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

// buildCIRepairInstructions builds the prompt fed to the coding agent when a
// PR's CI fails. It tells the agent the original task, the PR, and the exact
// failures, and directs it to fix ONLY the CI failures using tools.
func buildCIRepairInstructions(originalInstructions, prURL string, annotations []CheckRunAnnotation) string {
	var b strings.Builder
	b.WriteString("The implementation for this issue was completed and a pull request was created at ")
	b.WriteString(prURL)
	b.WriteString(".\n\nGitHub CI failed with the following errors. The changes are already applied in this checkout. Read the relevant code and fix the CI failures.\n\n")
	if len(annotations) == 0 {
		b.WriteString("(No per-file annotations were returned; the CI job failed. Inspect the repository and fix the failure.)\n")
	} else {
		b.WriteString(formatCIFailures(annotations))
	}
	b.WriteString("\nOriginal task instructions:\n")
	b.WriteString(originalInstructions)
	b.WriteString("\n\nConstraints:\n")
	b.WriteString("- Fix ONLY the CI failures. Do NOT undo the existing implementation or redo the whole task.\n")
	b.WriteString("- Use Shell/Read/Grep tools to read the relevant files and apply a focused fix.\n")
	b.WriteString("- After fixing, run focused validation (e.g. go test ./...) to confirm before finishing.\n")
	return b.String()
}
