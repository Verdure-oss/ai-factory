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

package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
)

// CommentReporter writes task results back to issue systems.
type CommentReporter interface {
	Comment(ctx context.Context, targetURL, body string) error
}

// CommentReportOptions configures one issue comment write.
type CommentReportOptions struct {
	Provider   string
	TargetURL  string
	Body       string
	Token      string
	HTTPClient *http.Client
	APIBase    string
}

// CommentRequest describes the HTTP request needed to create an issue comment.
type CommentRequest struct {
	Provider string
	Method   string
	URL      string
	Headers  map[string]string
	Body     []byte
}

// PostIssueComment posts an issue comment for a GitHub or GitLab target URL.
func PostIssueComment(ctx context.Context, opts CommentReportOptions) error {
	req, err := BuildCommentRequest(opts)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("create comment request: %w", err)
	}
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post %s issue comment: %w", req.Provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post %s issue comment: unexpected status %s", req.Provider, resp.Status)
	}
	return nil
}

// BuildCommentRequest converts provider-neutral report options into an HTTP request.
func BuildCommentRequest(opts CommentReportOptions) (*CommentRequest, error) {
	provider := strings.TrimSpace(opts.Provider)
	targetURL := strings.TrimSpace(opts.TargetURL)
	body := strings.TrimSpace(opts.Body)
	if targetURL == "" {
		return nil, errors.New("reporting targetURL is required")
	}
	if body == "" {
		return nil, errors.New("comment body is required")
	}
	if provider == "" {
		inferred, err := InferReportingProvider(targetURL)
		if err != nil {
			return nil, err
		}
		provider = inferred
	}

	switch provider {
	case ProviderGitHub:
		return buildGitHubCommentRequest(opts, body)
	case ProviderGitLab:
		return buildGitLabCommentRequest(opts, body)
	default:
		return nil, fmt.Errorf("unsupported reporting provider %q", provider)
	}
}

// InferReportingProvider maps a known issue URL host to a reporting provider.
func InferReportingProvider(targetURL string) (string, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("parse reporting targetURL: %w", err)
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "github.com":
		return ProviderGitHub, nil
	case strings.Contains(host, "gitlab"):
		return ProviderGitLab, nil
	default:
		return "", fmt.Errorf("spec.reporting.provider is required for target host %q", parsed.Host)
	}
}

func buildGitHubCommentRequest(opts CommentReportOptions, body string) (*CommentRequest, error) {
	owner, repo, issue, err := parseGitHubIssueURL(opts.TargetURL)
	if err != nil {
		return nil, err
	}
	token := opts.Token
	if token == "" {
		token = ReadConfig("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is required to comment on GitHub issues")
	}
	apiBase := strings.TrimRight(opts.APIBase, "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return nil, fmt.Errorf("encode GitHub comment: %w", err)
	}
	return &CommentRequest{
		Provider: ProviderGitHub,
		Method:   http.MethodPost,
		URL:      fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", apiBase, url.PathEscape(owner), url.PathEscape(repo), issue),
		Headers: map[string]string{
			"Accept":        "application/vnd.github+json",
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		},
		Body: payload,
	}, nil
}

func buildGitLabCommentRequest(opts CommentReportOptions, body string) (*CommentRequest, error) {
	target, projectPath, issue, err := parseGitLabIssueURL(opts.TargetURL)
	if err != nil {
		return nil, err
	}
	token := opts.Token
	if token == "" {
		token = ReadConfig("GITLAB_TOKEN")
	}
	if token == "" {
		return nil, errors.New("GITLAB_TOKEN is required to comment on GitLab issues")
	}
	apiBase := strings.TrimRight(opts.APIBase, "/")
	if apiBase == "" {
		apiBase = fmt.Sprintf("%s://%s/api/v4", target.Scheme, target.Host)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return nil, fmt.Errorf("encode GitLab comment: %w", err)
	}
	return &CommentRequest{
		Provider: ProviderGitLab,
		Method:   http.MethodPost,
		URL:      fmt.Sprintf("%s/projects/%s/issues/%d/notes", apiBase, url.PathEscape(projectPath), issue),
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"PRIVATE-TOKEN": token,
		},
		Body: payload,
	}, nil
}

func parseGitHubIssueURL(targetURL string) (string, string, int, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("parse GitHub issue URL: %w", err)
	}
	parts := pathParts(parsed.Path)
	if len(parts) != 4 || parts[2] != "issues" {
		return "", "", 0, fmt.Errorf("GitHub issue URL must look like https://github.com/{owner}/{repo}/issues/{number}")
	}
	issue, err := strconv.Atoi(parts[3])
	if err != nil || issue <= 0 {
		return "", "", 0, fmt.Errorf("GitHub issue number must be positive")
	}
	return parts[0], parts[1], issue, nil
}

func parseGitLabIssueURL(targetURL string) (*url.URL, string, int, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, "", 0, fmt.Errorf("parse GitLab issue URL: %w", err)
	}
	parts := pathParts(parsed.Path)
	marker := -1
	for i := range parts {
		if parts[i] == "-" {
			marker = i
			break
		}
	}
	if marker <= 0 || marker+2 >= len(parts) || parts[marker+1] != "issues" {
		return nil, "", 0, fmt.Errorf("GitLab issue URL must look like https://gitlab.example.com/{namespace}/{project}/-/issues/{iid}")
	}
	issue, err := strconv.Atoi(parts[marker+2])
	if err != nil || issue <= 0 {
		return nil, "", 0, fmt.Errorf("GitLab issue iid must be positive")
	}
	return parsed, strings.Join(parts[:marker], "/"), issue, nil
}

func pathParts(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}
