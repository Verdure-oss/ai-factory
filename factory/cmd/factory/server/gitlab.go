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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

const defaultGitLabAPIPath = "/api/v4"

// GitLabClient handles GitLab API calls for label and note management.
// It mirrors GitHubClient but targets the GitLab v4 REST API.
type GitLabClient struct {
	token   string
	apiBase string
	client  *http.Client
}

// NewGitLabClient builds a client from ReadConfig. GITLAB_API_BASE wins; else
// the caller sets the API base from the task's source host via
// SetHostFromRepositoryURL (self-hosted instances have no fixed default host).
func NewGitLabClient() *GitLabClient {
	return &GitLabClient{
		token:   taskpkg.ReadConfig("GITLAB_TOKEN"),
		apiBase: strings.TrimRight(taskpkg.ReadConfig("GITLAB_API_BASE"), "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// HasToken reports whether a GitLab token is configured.
func (c *GitLabClient) HasToken() bool { return c.token != "" }

// SetHostFromRepositoryURL sets the API base to https://<host>/api/v4 when no
// explicit GITLAB_API_BASE override is configured. Host comes from the task's
// source (self-hosted domain).
func (c *GitLabClient) SetHostFromRepositoryURL(host string) {
	if c.apiBase != "" || strings.TrimSpace(host) == "" {
		return
	}
	c.apiBase = fmt.Sprintf("https://%s%s", host, defaultGitLabAPIPath)
}

func (c *GitLabClient) projectPath(repo string) string {
	return url.PathEscape(strings.TrimPrefix(repo, "/"))
}

// setLabels issues one PUT to add and/or remove labels on an issue. GitLab
// creates unknown labels on apply, so no ensure step is needed.
func (c *GitLabClient) setLabels(ctx context.Context, repo string, issueNumber int, add, remove []string) error {
	if !c.HasToken() {
		return nil
	}
	q := url.Values{}
	if len(add) > 0 {
		q.Set("add_labels", strings.Join(add, ","))
	}
	if len(remove) > 0 {
		q.Set("remove_labels", strings.Join(remove, ","))
	}
	endpoint := fmt.Sprintf("%s/projects/%s/issues/%d?%s", c.apiBase, c.projectPath(repo), issueNumber, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
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
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("set gitlab labels on %s#%d: %s", repo, issueNumber, resp.Status)
	}
	return nil
}

func (c *GitLabClient) setHeaders(req *http.Request) {
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/json")
}

// SetTaskRunning adds running, removes waiting/done/failed.
func (c *GitLabClient) SetTaskRunning(ctx context.Context, repo string, issueNumber int) error {
	return c.setLabels(ctx, repo, issueNumber,
		[]string{labelRunning},
		[]string{labelWaiting, labelDone, labelFailed})
}

// SetTaskWaiting adds waiting, removes running.
func (c *GitLabClient) SetTaskWaiting(ctx context.Context, repo string, issueNumber int) error {
	return c.setLabels(ctx, repo, issueNumber,
		[]string{labelWaiting}, []string{labelRunning})
}

// SetTaskDone adds done, removes running/waiting/failed/run/smoke.
func (c *GitLabClient) SetTaskDone(ctx context.Context, repo string, issueNumber int) error {
	return c.setLabels(ctx, repo, issueNumber,
		[]string{labelDone},
		[]string{labelRunning, labelWaiting, labelFailed, labelRun, labelSmoke})
}

// SetTaskFailed adds failed, removes running/waiting/done/run/smoke.
func (c *GitLabClient) SetTaskFailed(ctx context.Context, repo string, issueNumber int) error {
	return c.setLabels(ctx, repo, issueNumber,
		[]string{labelFailed},
		[]string{labelRunning, labelWaiting, labelDone, labelRun, labelSmoke})
}

// SetTaskCancelled removes running/waiting and posts a cancellation note.
func (c *GitLabClient) SetTaskCancelled(ctx context.Context, repo string, issueNumber int, removedTriggerLabel string) error {
	if err := c.setLabels(ctx, repo, issueNumber, nil, []string{labelRunning, labelWaiting}); err != nil {
		return err
	}
	return c.PostComment(ctx, repo, issueNumber, cancelCommentBody(issueNumber, removedTriggerLabel))
}

// PostComment posts a note on a GitLab issue.
func (c *GitLabClient) PostComment(ctx context.Context, repo string, issueNumber int, body string) error {
	if !c.HasToken() {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/projects/%s/issues/%d/notes", c.apiBase, c.projectPath(repo), issueNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("post gitlab note on %s#%d: %s", repo, issueNumber, resp.Status)
	}
	return nil
}

var _ IssueReporter = (*GitLabClient)(nil)
