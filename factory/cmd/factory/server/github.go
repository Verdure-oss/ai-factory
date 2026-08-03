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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"

	// Label constants
	labelRunning = "ai-factory-running"
	labelWaiting = "ai-factory-waiting"
	labelDone    = "ai-factory-done"
	labelFailed  = "ai-factory-failed"
	labelRun     = "ai-factory-run"
	labelSmoke   = "ai-factory-smoke"
)

// GitHubClient handles GitHub API calls for label and comment management.
type GitHubClient struct {
	token   string
	apiBase string
	client  *http.Client
}

// NewGitHubClient creates a new GitHub API client.
// Credentials are read via taskpkg.ReadConfig to support hot-reload from
// mounted Secret files (with env var fallback for local development).
func NewGitHubClient() *GitHubClient {
	token := taskpkg.ReadConfig("GITHUB_TOKEN")
	if token == "" {
		token = taskpkg.ReadConfig("AI_FACTORY_GITHUB_TOKEN")
	}
	apiBase := taskpkg.ReadConfig("GITHUB_API_BASE")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	return &GitHubClient{
		token:   token,
		apiBase: strings.TrimRight(apiBase, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// HasToken returns true if a GitHub token is configured.
func (c *GitHubClient) HasToken() bool {
	return c.token != ""
}

// EnsureLabel creates a label if it doesn't exist.
func (c *GitHubClient) EnsureLabel(ctx context.Context, repo, name, color, description string) error {
	if !c.HasToken() {
		return nil
	}
	payload, err := json.Marshal(map[string]string{
		"name":        name,
		"color":       color,
		"description": description,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/labels", c.apiBase, repo),
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 422 means label already exists, that's OK
	if resp.StatusCode == 422 {
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("create label %s: %s", name, resp.Status)
	}
	return nil
}

// AddLabel adds a label to an issue.
func (c *GitHubClient) AddLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	if !c.HasToken() {
		return nil
	}
	payload, err := json.Marshal([]string{label})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/issues/%d/labels", c.apiBase, repo, issueNumber),
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("add label %s: %s", label, resp.Status)
	}
	return nil
}

// RemoveLabel removes a label from an issue.
func (c *GitHubClient) RemoveLabel(ctx context.Context, repo string, issueNumber int, label string) error {
	if !c.HasToken() {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", c.apiBase, repo, issueNumber, label),
		nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 404 means label wasn't on the issue, that's OK
	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("remove label %s: %s", label, resp.Status)
	}
	return nil
}

// PostComment posts a comment on an issue.
func (c *GitHubClient) PostComment(ctx context.Context, repo string, issueNumber int, body string) error {
	if !c.HasToken() {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.apiBase, repo, issueNumber),
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("post comment: %s", resp.Status)
	}
	return nil
}

// SetTaskRunning adds the running label and removes done/failed/waiting labels.
func (c *GitHubClient) SetTaskRunning(ctx context.Context, repo string, issueNumber int) error {
	if !c.HasToken() {
		return nil
	}
	// Ensure labels exist
	_ = c.EnsureLabel(ctx, repo, labelRunning, "1D76DB", "ai-factory is processing this issue")
	_ = c.EnsureLabel(ctx, repo, labelDone, "0E8A16", "ai-factory completed this issue")
	_ = c.EnsureLabel(ctx, repo, labelFailed, "B60205", "ai-factory failed this issue")

	// Add running, remove done/failed/waiting
	if err := c.AddLabel(ctx, repo, issueNumber, labelRunning); err != nil {
		return err
	}
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelDone)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelFailed)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelWaiting)
	return nil
}

// SetTaskWaiting adds the waiting label and removes the running label.
// Used when a task is queued waiting for a sandbox pod from the warm pool.
func (c *GitHubClient) SetTaskWaiting(ctx context.Context, repo string, issueNumber int) error {
	if !c.HasToken() {
		return nil
	}
	_ = c.EnsureLabel(ctx, repo, labelWaiting, "F9C809", "ai-factory is waiting for a sandbox pod")

	if err := c.AddLabel(ctx, repo, issueNumber, labelWaiting); err != nil {
		return err
	}
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRunning)
	return nil
}

// SetTaskDone adds the done label and removes running/waiting/failed/run labels.
func (c *GitHubClient) SetTaskDone(ctx context.Context, repo string, issueNumber int) error {
	if !c.HasToken() {
		return nil
	}
	if err := c.AddLabel(ctx, repo, issueNumber, labelDone); err != nil {
		return err
	}
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRunning)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelWaiting)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelFailed)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRun)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelSmoke)
	return nil
}

// SetTaskFailed adds the failed label and removes running/waiting/done/run/smoke labels.
// Removing trigger labels (run/smoke) allows users to re-trigger by re-adding them.
func (c *GitHubClient) SetTaskFailed(ctx context.Context, repo string, issueNumber int) error {
	if !c.HasToken() {
		return nil
	}
	if err := c.AddLabel(ctx, repo, issueNumber, labelFailed); err != nil {
		return err
	}
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRunning)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelWaiting)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelDone)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRun)   // Remove trigger label to allow re-run
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelSmoke) // Remove trigger label to allow re-run
	return nil
}

func (c *GitHubClient) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
}

// ParseRepository extracts owner/repo from a full name like "owner/repo".
func ParseRepository(fullName string) (string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid repository format: %s", fullName)
	}
	return fullName, nil
}
