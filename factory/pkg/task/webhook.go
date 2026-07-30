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
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	defaultIssueAgentName       = "builder"
	defaultIssueSandboxTemplate = "go-dev"
)

// IssueWebhookOptions configures how issue webhooks become FactoryTasks.
type IssueWebhookOptions struct {
	Provider                  string
	Namespace                 string
	AgentName                 string
	AgentCommand              string
	SmokeAgentCommand         string   // agent command for smoke mode (ai-factory-smoke label)
	AgentEnv                  []string
	PromptRef                 string
	SandboxTemplateRef        string
	ContainerName             string
	ReportingMode             string
	Commands                  []string // validation commands for run mode
	SmokeCommands             []string // commands for smoke mode
	TriggerActions            []string
	RequiredLabels            []string
	Repositories              []string
	ChangeRequestEnabled      bool
	ChangeRequestPushOnly     bool
	ChangeRequestAuthTokenEnv string
}

// IssueWebhookEvent is the provider-neutral issue data extracted from a webhook.
type IssueWebhookEvent struct {
	Provider       string
	Action         string
	IssueID        string
	IssueNumber    int
	IssueTitle     string
	IssueBody      string
	IssueURL       string
	Actor          string
	Repository     string
	RepositoryHost string
	DefaultBranch  string
	CloneURL       string
	Labels         []string
}

// IgnoredIssueWebhookError reports a valid issue webhook that did not match trigger rules.
type IgnoredIssueWebhookError struct {
	Reason string
}

func (e *IgnoredIssueWebhookError) Error() string {
	return e.Reason
}

// FactoryTaskFromIssueWebhook converts a GitHub or GitLab issue webhook payload into a FactoryTask.
func FactoryTaskFromIssueWebhook(payload []byte, opts IssueWebhookOptions) (*FactoryTask, error) {
	event, err := ParseIssueWebhook(payload, opts.Provider)
	if err != nil {
		return nil, err
	}
	if ok, reason := ShouldTriggerIssue(event, opts); !ok {
		return nil, &IgnoredIssueWebhookError{Reason: reason}
	}

	agentName := opts.AgentName
	if agentName == "" {
		agentName = defaultIssueAgentName
	}
	sandboxTemplate := opts.SandboxTemplateRef
	if sandboxTemplate == "" {
		sandboxTemplate = defaultIssueSandboxTemplate
	}
	reportingMode := opts.ReportingMode
	if reportingMode == "" {
		reportingMode = "comment"
	}
	branch := event.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	// Determine agent command and commands based on trigger label.
	// Smoke mode uses a no-op agent command and smoke-specific validation commands.
	// Run mode uses the full agent command and validation commands.
	isSmoke := hasAnyLabel(event.Labels, []string{"ai-factory-smoke"})
	agentCommand := opts.AgentCommand
	agentEnv := opts.AgentEnv
	commands := opts.Commands
	if isSmoke {
		if opts.SmokeAgentCommand != "" {
			agentCommand = opts.SmokeAgentCommand
		}
		agentEnv = nil // smoke mode does not need LLM-related env vars
		commands = opts.SmokeCommands
	}

	task := &FactoryTask{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: ObjectMeta{
			Name:      dnsName(fmt.Sprintf("%s-%s-%d", event.Provider, event.Repository, event.IssueNumber)),
			Namespace: opts.Namespace,
			Labels: map[string]string{
				"factory.ai.gke.io/provider": event.Provider,
				"factory.ai.gke.io/trigger":  "issue",
			},
		},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{
				Provider:   event.Provider,
				Host:       event.RepositoryHost,
				Repository: event.Repository,
				BaseRef:    branch,
				CloneURL:   event.CloneURL,
			},
			Trigger: TriggerSpec{
				Type:  "issue",
				ID:    event.IssueID,
				URL:   event.IssueURL,
				Actor: event.Actor,
			},
			Agent: AgentSpec{
				Name:      agentName,
				PromptRef: opts.PromptRef,
				Command:   agentCommand,
				Env:       agentEnv,
			},
			Sandbox: SandboxSpec{
				TemplateRef:   sandboxTemplate,
				ContainerName: opts.ContainerName,
			},
			Work: WorkSpec{
				Instructions: issueInstructions(event),
				Commands:     commands,
			},
			Reporting: ReportingSpec{
				Provider:  event.Provider,
				Mode:      reportingMode,
				TargetURL: event.IssueURL,
			},
		},
	}
	if opts.ChangeRequestEnabled {
		task.Spec.ChangeRequest = ChangeRequestSpec{
			Enabled:       true,
			PushOnly:      opts.ChangeRequestPushOnly,
			BranchPrefix:  "factory-task",
			CommitMessage: fmt.Sprintf("Apply issue #%d: %s", event.IssueNumber, event.IssueTitle),
			Title:         event.IssueTitle,
			Body:          fmt.Sprintf("Generated by ai-factory for %s.", event.IssueURL),
			AuthTokenEnv:  opts.ChangeRequestAuthTokenEnv,
		}
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	return task, nil
}

// ShouldTriggerIssue evaluates whether a parsed issue event should create a FactoryTask.
func ShouldTriggerIssue(event *IssueWebhookEvent, opts IssueWebhookOptions) (bool, string) {
	if event.Action != "" && !matchesAny(event.Action, triggerActions(opts.TriggerActions)) {
		return false, fmt.Sprintf("ignored issue action %q", event.Action)
	}
	if len(opts.Repositories) > 0 && !matchesAny(event.Repository, opts.Repositories) {
		return false, fmt.Sprintf("ignored repository %q", event.Repository)
	}
	if len(opts.RequiredLabels) > 0 && !hasAnyLabel(event.Labels, opts.RequiredLabels) {
		return false, fmt.Sprintf("ignored issue without required label %q", strings.Join(opts.RequiredLabels, ","))
	}
	return true, ""
}

// ParseIssueWebhook extracts provider-neutral issue fields from a webhook payload.
func ParseIssueWebhook(payload []byte, provider string) (*IssueWebhookEvent, error) {
	switch provider {
	case ProviderGitHub:
		return parseGitHubIssueWebhook(payload)
	case ProviderGitLab:
		return parseGitLabIssueWebhook(payload)
	case "":
		return nil, errors.New("webhook provider is required")
	default:
		return nil, fmt.Errorf("unsupported webhook provider %q", provider)
	}
}

// FactoryTaskYAML renders a FactoryTask as YAML.
func FactoryTaskYAML(task *FactoryTask) ([]byte, error) {
	data, err := yaml.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("marshal FactoryTask: %w", err)
	}
	return data, nil
}

// VerifyGitHubWebhookSignature verifies an X-Hub-Signature-256 header.
func VerifyGitHubWebhookSignature(secret string, body []byte, signature string) error {
	if secret == "" {
		return nil
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return errors.New("missing GitHub X-Hub-Signature-256 header")
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return fmt.Errorf("decode GitHub signature: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return errors.New("GitHub webhook signature mismatch")
	}
	return nil
}

// VerifyGitLabWebhookToken verifies an X-Gitlab-Token header.
func VerifyGitLabWebhookToken(secret string, token string) error {
	if secret == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(token)) != 1 {
		return errors.New("GitLab webhook token mismatch")
	}
	return nil
}

type githubIssueWebhook struct {
	Action     string `json:"action"`
	Issue      githubIssue
	Repository githubRepository
	Sender     githubUser
	Label      githubLabel `json:"label"`
}

type githubIssue struct {
	Number  int           `json:"number"`
	Title   string        `json:"title"`
	Body    string        `json:"body"`
	HTMLURL string        `json:"html_url"`
	User    githubUser    `json:"user"`
	Labels  []githubLabel `json:"labels"`
}

type githubRepository struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubLabel struct {
	Name string `json:"name"`
}

func parseGitHubIssueWebhook(payload []byte) (*IssueWebhookEvent, error) {
	var raw githubIssueWebhook
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode GitHub issue webhook: %w", err)
	}
	if raw.Issue.Number <= 0 {
		return nil, errors.New("GitHub issue webhook missing issue.number")
	}
	if raw.Repository.FullName == "" {
		return nil, errors.New("GitHub issue webhook missing repository.full_name")
	}
	actor := raw.Sender.Login
	if actor == "" {
		actor = raw.Issue.User.Login
	}
	return &IssueWebhookEvent{
		Provider:       ProviderGitHub,
		Action:         raw.Action,
		IssueID:        strconv.Itoa(raw.Issue.Number),
		IssueNumber:    raw.Issue.Number,
		IssueTitle:     raw.Issue.Title,
		IssueBody:      raw.Issue.Body,
		IssueURL:       raw.Issue.HTMLURL,
		Actor:          actor,
		Repository:     raw.Repository.FullName,
		RepositoryHost: hostFromURL(raw.Repository.HTMLURL, "github.com"),
		DefaultBranch:  raw.Repository.DefaultBranch,
		CloneURL:       raw.Repository.CloneURL,
		Labels:         githubLabels(raw),
	}, nil
}

type gitlabIssueWebhook struct {
	ObjectKind       string                 `json:"object_kind"`
	EventType        string                 `json:"event_type"`
	User             gitlabUser             `json:"user"`
	Project          gitlabProject          `json:"project"`
	ObjectAttributes gitlabObjectAttributes `json:"object_attributes"`
	Labels           []gitlabLabel          `json:"labels"`
}

type gitlabUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type gitlabProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
	GitHTTPURL        string `json:"git_http_url"`
	HTTPURL           string `json:"http_url"`
}

type gitlabObjectAttributes struct {
	IID         int           `json:"iid"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	URL         string        `json:"url"`
	Action      string        `json:"action"`
	Labels      []gitlabLabel `json:"labels"`
}

type gitlabLabel struct {
	Title string `json:"title"`
}

func parseGitLabIssueWebhook(payload []byte) (*IssueWebhookEvent, error) {
	var raw gitlabIssueWebhook
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode GitLab issue webhook: %w", err)
	}
	if raw.ObjectKind != "" && raw.ObjectKind != "issue" {
		return nil, fmt.Errorf("GitLab object_kind must be issue, got %q", raw.ObjectKind)
	}
	if raw.ObjectAttributes.IID <= 0 {
		return nil, errors.New("GitLab issue webhook missing object_attributes.iid")
	}
	if raw.Project.PathWithNamespace == "" {
		return nil, errors.New("GitLab issue webhook missing project.path_with_namespace")
	}
	actor := raw.User.Username
	if actor == "" {
		actor = raw.User.Name
	}
	cloneURL := raw.Project.GitHTTPURL
	if cloneURL == "" {
		cloneURL = raw.Project.HTTPURL
	}
	return &IssueWebhookEvent{
		Provider:       ProviderGitLab,
		Action:         raw.ObjectAttributes.Action,
		IssueID:        strconv.Itoa(raw.ObjectAttributes.IID),
		IssueNumber:    raw.ObjectAttributes.IID,
		IssueTitle:     raw.ObjectAttributes.Title,
		IssueBody:      raw.ObjectAttributes.Description,
		IssueURL:       raw.ObjectAttributes.URL,
		Actor:          actor,
		Repository:     raw.Project.PathWithNamespace,
		RepositoryHost: hostFromURL(raw.Project.WebURL, ""),
		DefaultBranch:  raw.Project.DefaultBranch,
		CloneURL:       cloneURL,
		Labels:         gitlabLabels(raw),
	}, nil
}

func issueInstructions(event *IssueWebhookEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Work on %s issue #%d: %s\n\n", event.Provider, event.IssueNumber, strings.TrimSpace(event.IssueTitle))
	if body := formatIssueBody(event.IssueBody); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	if event.IssueURL != "" {
		fmt.Fprintf(&b, "Issue URL: %s", event.IssueURL)
	}
	return strings.TrimSpace(b.String())
}

func formatIssueBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	body = normalizeLiteralNewlines(body)
	sections := parseGitHubIssueFormBody(body)
	if len(sections) == 0 {
		return body
	}
	labels := []string{
		"Task",
		"Requirements",
		"Acceptance Criteria",
		"Goal",
		"Files or areas to change",
		"Acceptance criteria",
		"Allow creating a pull request",
	}
	var b strings.Builder
	for _, label := range labels {
		value := strings.TrimSpace(sections[label])
		if value == "" || value == "_No response_" {
			continue
		}
		fmt.Fprintf(&b, "%s\n%s\n\n", label, value)
	}
	return strings.TrimSpace(b.String())
}

func normalizeLiteralNewlines(body string) string {
	if strings.Count(body, `\n`) == 0 {
		return body
	}
	if strings.Count(body, "\n") > strings.Count(body, `\n`) {
		return body
	}
	return strings.ReplaceAll(body, `\n`, "\n")
}

func parseGitHubIssueFormBody(body string) map[string]string {
	sections := map[string]string{}
	current := ""
	var value strings.Builder
	flush := func() {
		if current == "" {
			return
		}
		sections[current] = strings.TrimSpace(value.String())
		value.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			continue
		}
		if current == "" {
			continue
		}
		value.WriteString(line)
		value.WriteByte('\n')
	}
	flush()
	return sections
}

func triggerActions(actions []string) []string {
	if len(actions) > 0 {
		return actions
	}
	return []string{"open", "opened", "reopen", "reopened", "labeled"}
}

func matchesAny(value string, allowed []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if value == strings.ToLower(strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}

func hasAnyLabel(labels []string, required []string) bool {
	for _, label := range labels {
		if matchesAny(label, required) {
			return true
		}
	}
	return false
}

func githubLabels(raw githubIssueWebhook) []string {
	labels := make([]string, 0, len(raw.Issue.Labels)+1)
	for _, label := range raw.Issue.Labels {
		if strings.TrimSpace(label.Name) != "" {
			labels = append(labels, label.Name)
		}
	}
	if strings.TrimSpace(raw.Label.Name) != "" {
		labels = append(labels, raw.Label.Name)
	}
	return labels
}

func gitlabLabels(raw gitlabIssueWebhook) []string {
	labels := make([]string, 0, len(raw.Labels)+len(raw.ObjectAttributes.Labels))
	for _, label := range raw.Labels {
		if strings.TrimSpace(label.Title) != "" {
			labels = append(labels, label.Title)
		}
	}
	for _, label := range raw.ObjectAttributes.Labels {
		if strings.TrimSpace(label.Title) != "" {
			labels = append(labels, label.Title)
		}
	}
	return labels
}

func hostFromURL(rawURL, fallback string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return fallback
	}
	return parsed.Hostname()
}

func dnsName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "factory-task"
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		return "factory-task"
	}
	return out
}
