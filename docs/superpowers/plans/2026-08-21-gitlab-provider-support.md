# GitLab Provider 支持 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 ai-factory 完整支持 GitLab，issue 打标签触发、状态标签反馈、取消任务、建 MR、回帖的体验与 GitHub 完全一致，由 `GIT_PROVIDER` 环境变量指定单提供商模式。

**Architecture:** 复用已 provider-neutral 的执行计划、MR 创建、评论逻辑；重写 GitLab webhook 解析把 `update`+`changes.labels` 归一化成 GitHub 的 `labeled`/`unlabeled`；抽象 `IssueReporter` 接口收敛 controller 里的 provider 判断；新建 `GitLabClient`；`GIT_PROVIDER` 必填并只挂载对应 webhook 端点。CI 修复本次不接线，保留现有 GitHub-only 门控。

**Tech Stack:** Go 1.25，标准库 `net/http` / `net/http/httptest` / `encoding/json`，Cobra CLI，Helm chart。

**Spec:** `docs/superpowers/specs/2026-08-21-gitlab-provider-support-design.md`

## Global Constraints

- 每个 `.go` 文件顶部保留现有的 Apache License header（复制同目录已存在文件的完整头部，包括 `// Copyright 2026 Google LLC` 起始的 13 行）。
- 不用 `py_compile` / `compileall`（本任务不涉及 Python，无影响）。
- provider 常量用 `taskpkg.ProviderGitHub` / `taskpkg.ProviderGitLab`（值 `"github"` / `"gitlab"`），不要硬编码字符串。
- 状态标签名沿用现有常量：`ai-factory-running` / `ai-factory-waiting` / `ai-factory-done` / `ai-factory-failed` / `ai-factory-run` / `ai-factory-smoke`。
- 测试用 `net/http/httptest` 起假服务器，禁止真实网络调用（参照 `factory/cmd/factory/server/github_test.go` 的 `fakeGitHubAPI`）。
- 运行测试：`go test ./factory/...`。构建：`go build ./...`。

---

## 文件结构

| 文件 | 责任 |
| --- | --- |
| `factory/pkg/task/webhook.go` | 重写 `parseGitLabIssueWebhook`：解析 `changes.labels` 差集，归一化 Action/TriggerLabel |
| `factory/pkg/task/webhook_test.go` | GitLab labeled/unlabeled/update 归一化的表驱动测试 |
| `factory/cmd/factory/server/gitlab.go` | 新建 `GitLabClient`，实现标签增删 + 评论 + IssueReporter |
| `factory/cmd/factory/server/gitlab_test.go` | GitLabClient 的 httptest 测试 |
| `factory/cmd/factory/server/reporter.go` | 新建 `IssueReporter` 接口 + `newIssueReporter` 工厂 |
| `factory/cmd/factory/server/github.go` | GitHubClient 保持不变（已满足接口，靠 factory 断言） |
| `factory/cmd/factory/server/controller.go` | 标签状态机 4 处改用 `newIssueReporter(provider)` |
| `factory/cmd/factory/server/server.go` | GIT_PROVIDER 校验、端点单挂、cancel 分发 |
| `factory/cmd/factory/server/server_test.go` | GIT_PROVIDER 校验 + 端点挂载测试 |
| `scripts/ai-factory.env.example` | 新增 `GIT_PROVIDER`、`GITLAB_API_BASE` |
| `charts/ai-factory/values.yaml` + `templates/{configmap,deployment}.yaml` | 透传 `GIT_PROVIDER` / `GITLAB_API_BASE` |
| `docs/self-hosted/guide.md` | 3.6 节改为实际支持说明 + 内网拓扑 |

---
## Task 1: GitLab webhook 标签变更归一化

把 GitLab 的 `action=update` + `changes.labels.{previous,current}` 归一化成现有 GitHub 模型：新增触发标签 → `Action="labeled"`；移除触发标签 → `Action="unlabeled"`；并填 `TriggerLabel`。下游 `ShouldTriggerIssue` / `handleIssueCancel` 因此无需改动。

**Files:**
- Modify: `factory/pkg/task/webhook.go`（`gitlabIssueWebhook` 结构体 + `parseGitLabIssueWebhook`，约 352-424 行）
- Test: `factory/pkg/task/webhook_test.go`

**Interfaces:**
- Consumes: 现有 `IssueWebhookEvent`（`Action`、`TriggerLabel`、`Labels` 字段）、常量 `ProviderGitLab`、辅助函数 `hostFromURL`、`gitlabLabels`。
- Produces: 归一化后的 `*IssueWebhookEvent`；触发标签集合的判定沿用现有 `ShouldTriggerIssue`。

- [ ] **Step 1: 写失败测试**

在 `factory/pkg/task/webhook_test.go` 末尾追加。这些 payload 覆盖三种情况：加触发标签、移除触发标签、无关更新。

```go
// GitLab "update" event where ai-factory-run was just added to the label set.
const gitlabLabeledPayload = `{
  "object_kind": "issue",
  "event_type": "issue",
  "user": {"username": "yueli", "name": "Yue Li"},
  "project": {
    "path_with_namespace": "platform/ai/ai-factory",
    "web_url": "https://gitlab.example.com/platform/ai/ai-factory",
    "default_branch": "main",
    "git_http_url": "https://gitlab.example.com/platform/ai/ai-factory.git"
  },
  "object_attributes": {
    "iid": 7, "title": "Run agent task", "description": "Please validate.",
    "url": "https://gitlab.example.com/platform/ai/ai-factory/-/issues/7",
    "action": "update",
    "labels": [{"title": "ai-factory"}, {"title": "ai-factory-run"}]
  },
  "changes": {
    "labels": {
      "previous": [{"title": "ai-factory"}],
      "current": [{"title": "ai-factory"}, {"title": "ai-factory-run"}]
    }
  }
}`

// Same shape, but ai-factory-run was removed (cancellation).
const gitlabUnlabeledPayload = `{
  "object_kind": "issue",
  "object_attributes": {
    "iid": 7, "title": "Run agent task",
    "url": "https://gitlab.example.com/platform/ai/ai-factory/-/issues/7",
    "action": "update",
    "labels": [{"title": "ai-factory"}]
  },
  "project": {"path_with_namespace": "platform/ai/ai-factory", "web_url": "https://gitlab.example.com/platform/ai/ai-factory", "default_branch": "main", "git_http_url": "https://gitlab.example.com/platform/ai/ai-factory.git"},
  "user": {"username": "yueli"},
  "changes": {
    "labels": {
      "previous": [{"title": "ai-factory"}, {"title": "ai-factory-run"}],
      "current": [{"title": "ai-factory"}]
    }
  }
}`

func TestParseGitLabIssueWebhookLabeled(t *testing.T) {
	event, err := ParseIssueWebhook([]byte(gitlabLabeledPayload), ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if event.Action != "labeled" {
		t.Fatalf("Action = %q, want labeled", event.Action)
	}
	if event.TriggerLabel != "ai-factory-run" {
		t.Fatalf("TriggerLabel = %q, want ai-factory-run", event.TriggerLabel)
	}
}

func TestParseGitLabIssueWebhookUnlabeled(t *testing.T) {
	event, err := ParseIssueWebhook([]byte(gitlabUnlabeledPayload), ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if event.Action != "unlabeled" {
		t.Fatalf("Action = %q, want unlabeled", event.Action)
	}
	if event.TriggerLabel != "ai-factory-run" {
		t.Fatalf("TriggerLabel = %q, want ai-factory-run", event.TriggerLabel)
	}
}

func TestParseGitLabIssueWebhookOpenNoChanges(t *testing.T) {
	// The existing gitlabIssuePayload has action "open" and no changes block.
	event, err := ParseIssueWebhook([]byte(gitlabIssuePayload), ProviderGitLab)
	if err != nil {
		t.Fatalf("ParseIssueWebhook() error = %v", err)
	}
	if event.Action != "open" {
		t.Fatalf("Action = %q, want open (fallback to object_attributes.action)", event.Action)
	}
	if event.TriggerLabel != "" {
		t.Fatalf("TriggerLabel = %q, want empty", event.TriggerLabel)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./factory/pkg/task/ -run TestParseGitLabIssueWebhook -v`
Expected: FAIL — `Action` 仍是 `update`（现有解析器直接透传 `object_attributes.action`），`TriggerLabel` 为空。

- [ ] **Step 3: 实现归一化解析**

在 `factory/pkg/task/webhook.go`，给 `gitlabIssueWebhook` 结构体加 `Changes` 字段：

```go
type gitlabIssueWebhook struct {
	ObjectKind       string                 `json:"object_kind"`
	EventType        string                 `json:"event_type"`
	User             gitlabUser             `json:"user"`
	Project          gitlabProject          `json:"project"`
	ObjectAttributes gitlabObjectAttributes `json:"object_attributes"`
	Labels           []gitlabLabel          `json:"labels"`
	Changes          gitlabChanges          `json:"changes"`
}

type gitlabChanges struct {
	Labels gitlabLabelChange `json:"labels"`
}

type gitlabLabelChange struct {
	Previous []gitlabLabel `json:"previous"`
	Current  []gitlabLabel `json:"current"`
}
```

在 `parseGitLabIssueWebhook` 里，在构造返回值前计算差集并覆写 action/triggerLabel。触发标签集合与 `webhookOptions` 里的 `RequiredLabels` 保持一致（`ai-factory-run` / `ai-factory-smoke`）：

```go
	action := raw.ObjectAttributes.Action
	triggerLabel := ""
	// GitLab has no dedicated labeled/unlabeled action: label changes arrive
	// as action "update" with changes.labels.{previous,current}. Normalize to
	// GitHub's model so ShouldTriggerIssue and handleIssueCancel need no change.
	if len(raw.Changes.Labels.Previous) > 0 || len(raw.Changes.Labels.Current) > 0 {
		prev := labelTitleSet(raw.Changes.Labels.Previous)
		curr := labelTitleSet(raw.Changes.Labels.Current)
		if added := firstTriggerLabel(raw.Changes.Labels.Current, prev); added != "" {
			action = "labeled"
			triggerLabel = added
		} else if removed := firstTriggerLabel(raw.Changes.Labels.Previous, curr); removed != "" {
			action = "unlabeled"
			triggerLabel = removed
		}
	}
```

把返回结构体里的 `Action:` 改为 `action`，并新增 `TriggerLabel: triggerLabel`。然后在文件里新增两个 helper（放在 `gitlabLabels` 附近）：

```go
// gitLabTriggerLabels are the labels whose add/remove ai-factory reacts to.
// Must stay in sync with server.webhookOptions RequiredLabels.
var gitLabTriggerLabels = []string{"ai-factory-run", "ai-factory-smoke"}

func labelTitleSet(labels []gitlabLabel) map[string]bool {
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		if t := strings.TrimSpace(l.Title); t != "" {
			set[t] = true
		}
	}
	return set
}

// firstTriggerLabel returns the first trigger label present in labels but
// absent from the exclude set (i.e. newly added or newly removed).
func firstTriggerLabel(labels []gitlabLabel, exclude map[string]bool) string {
	for _, l := range labels {
		t := strings.TrimSpace(l.Title)
		if t == "" || exclude[t] {
			continue
		}
		if matchesAny(t, gitLabTriggerLabels) {
			return t
		}
	}
	return ""
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./factory/pkg/task/ -run TestParseGitLabIssueWebhook -v`
Expected: PASS（3 个子测试全过）

- [ ] **Step 5: 运行全包回归**

Run: `go test ./factory/pkg/task/`
Expected: PASS（确认现有 `TestFactoryTaskFromGitLabIssueWebhook`、`TestShouldTriggerIssueRepositoryFilter` 不回归）

- [ ] **Step 6: 提交**

```bash
git add factory/pkg/task/webhook.go factory/pkg/task/webhook_test.go
git commit -m "feat(gitlab): normalize label-change webhooks to labeled/unlabeled"
```

---

## Task 2: IssueReporter 接口

抽象一个统一接口，让 controller 与 server 里的标签/评论操作按 provider 分发，消除散落的 `if provider != github { return }`。GitHubClient 已实现全部方法，仅需断言。

**Files:**
- Create: `factory/cmd/factory/server/reporter.go`
- Test: `factory/cmd/factory/server/reporter_test.go`

**Interfaces:**
- Consumes: 现有 `GitHubClient`（方法 `SetTaskRunning/SetTaskWaiting/SetTaskDone/SetTaskFailed/SetTaskCancelled/PostComment/HasToken`）、常量 `taskpkg.ProviderGitHub` / `taskpkg.ProviderGitLab`。
- Produces:
  - `type IssueReporter interface { ... }`（方法签名见下）
  - `func newIssueReporter(provider string) IssueReporter` — 返回 `*GitHubClient`、`*GitLabClient`（Task 3 提供），未知 provider 返回 `nil`。

- [ ] **Step 1: 写失败测试**

`factory/cmd/factory/server/reporter_test.go`：

```go
package server

import (
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

func TestNewIssueReporterGitHub(t *testing.T) {
	r := newIssueReporter(taskpkg.ProviderGitHub)
	if r == nil {
		t.Fatal("newIssueReporter(github) = nil")
	}
	if _, ok := r.(*GitHubClient); !ok {
		t.Fatalf("newIssueReporter(github) = %T, want *GitHubClient", r)
	}
}

func TestNewIssueReporterUnknown(t *testing.T) {
	if r := newIssueReporter("bitbucket"); r != nil {
		t.Fatalf("newIssueReporter(unknown) = %T, want nil", r)
	}
}

// Compile-time assertion that GitHubClient satisfies the interface.
var _ IssueReporter = (*GitHubClient)(nil)
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./factory/cmd/factory/server/ -run TestNewIssueReporter -v`
Expected: FAIL — `undefined: IssueReporter` / `undefined: newIssueReporter`。

- [ ] **Step 3: 实现接口与工厂**

`factory/cmd/factory/server/reporter.go`（含 License header）。注意 GitLab 分支暂时返回 `nil`，Task 3 完成后回填：

```go
package server

import (
	"context"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

// IssueReporter is the provider-neutral surface the controller uses to reflect
// task state back onto an issue: status labels, cancellation, and comments.
// GitHubClient and GitLabClient both implement it.
type IssueReporter interface {
	SetTaskRunning(ctx context.Context, repo string, issueNumber int) error
	SetTaskWaiting(ctx context.Context, repo string, issueNumber int) error
	SetTaskDone(ctx context.Context, repo string, issueNumber int) error
	SetTaskFailed(ctx context.Context, repo string, issueNumber int) error
	SetTaskCancelled(ctx context.Context, repo string, issueNumber int, removedTriggerLabel string) error
	PostComment(ctx context.Context, repo string, issueNumber int, body string) error
	HasToken() bool
}

// newIssueReporter returns the reporter for a provider, or nil for an unknown
// provider (callers already tolerate a nil/absent reporter as "skip").
func newIssueReporter(provider string) IssueReporter {
	switch provider {
	case taskpkg.ProviderGitHub:
		return NewGitHubClient()
	case taskpkg.ProviderGitLab:
		return NewGitLabClient()
	default:
		return nil
	}
}
```

> 注意：此时 `NewGitLabClient` 尚未定义，Task 2 单独编译会失败。若按顺序执行，把 `case taskpkg.ProviderGitLab` 分支暂时改成 `return nil` 并留 `// TODO(task3)` 注释以便本任务独立通过测试；Task 3 结束时改回 `NewGitLabClient()`。执行者按序推进时二选一即可，最终状态必须是 `NewGitLabClient()`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./factory/cmd/factory/server/ -run TestNewIssueReporter -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add factory/cmd/factory/server/reporter.go factory/cmd/factory/server/reporter_test.go
git commit -m "feat(server): add IssueReporter interface and provider factory"
```

---
## Task 3: GitLabClient

新建对标 `GitHubClient` 的 GitLab API 客户端：状态标签增删、issue 评论。GitLab 一次 `PUT issue` 即可 `add_labels` + `remove_labels`，且应用标签时自动创建，无需 GitHub 的 EnsureLabel。

**Files:**
- Create: `factory/cmd/factory/server/gitlab.go`
- Test: `factory/cmd/factory/server/gitlab_test.go`
- Modify: `factory/cmd/factory/server/reporter.go`（把 GitLab 分支回填为 `NewGitLabClient()`）

**Interfaces:**
- Consumes: `taskpkg.ReadConfig`（读 `GITLAB_TOKEN` / `GITLAB_API_BASE`）、标签常量 `labelRunning` 等（定义在 `github.go`，同包可直接用）。
- Produces:
  - `type GitLabClient struct { token, apiBase, host string; client *http.Client }`
  - `func NewGitLabClient() *GitLabClient`
  - 满足 `IssueReporter` 的全部方法。
  - `func (c *GitLabClient) SetHostFromRepositoryURL(host string)` — 让 controller 用 task 的 source host 覆盖 API base（自建实例）。

- [ ] **Step 1: 写失败测试**

`factory/cmd/factory/server/gitlab_test.go`。假服务器捕获 PUT 的 query（add/remove labels）和 POST notes：

```go
package server

import (
	"context"
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
			_ = r.ParseForm()
			comments = append(comments, r.FormValue("body"))
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./factory/cmd/factory/server/ -run TestGitLab -v`
Expected: FAIL — `undefined: GitLabClient` / `undefined: NewGitLabClient`。

- [ ] **Step 3: 实现 GitLabClient（结构 + 构造 + 内部 setLabels）**

`factory/cmd/factory/server/gitlab.go`（含 License header）第一部分：

```go
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
```

- [ ] **Step 4: 实现状态机方法 + 评论（同文件追加）**

标签取舍与 `GitHubClient` 的 `SetTask*` 完全对齐（见 `github.go:245-322`）：

```go
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
```

> 注意：`TestGitLabPostComment` 用 `r.FormValue("body")` 读取，而实现发送 JSON body。改测试的断言方式为读取 JSON——把假服务器 notes 分支改为 `json.NewDecoder(r.Body).Decode(&m)` 取 `m["body"]`。执行者在 Step 1 写测试时直接用 JSON 解码版本（下方修正）。

修正后的 notes 分支（Step 1 的 `fakeGitLabAPI` 用这版）：

```go
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			comments = append(comments, m["body"])
			w.WriteHeader(201)
			w.Write([]byte(`{"id":1}`))
```

（记得在 gitlab_test.go 的 import 补 `encoding/json`。）

- [ ] **Step 5: 回填 reporter 工厂**

把 `factory/cmd/factory/server/reporter.go` 里 GitLab 分支改回：

```go
	case taskpkg.ProviderGitLab:
		return NewGitLabClient()
```

并在 gitlab.go 末尾加编译期断言：

```go
var _ IssueReporter = (*GitLabClient)(nil)
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./factory/cmd/factory/server/ -run 'TestGitLab|TestNewIssueReporter' -v`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add factory/cmd/factory/server/gitlab.go factory/cmd/factory/server/gitlab_test.go factory/cmd/factory/server/reporter.go
git commit -m "feat(gitlab): add GitLabClient implementing IssueReporter"
```

---

## Task 4: controller 标签状态机改用 IssueReporter

把 controller.go 里 4 个 GitHub-only 的标签函数改成按 provider 分发，让 GitLab 任务也能反馈状态。取消任务的分发在 Task 5。

**Files:**
- Modify: `factory/cmd/factory/server/controller.go`（`setIssueWaitingLabel` 194-208、`updateTaskLabels` 536-557、`updateIntermediateLabel` 561-582；以及 `server.go` 的 `issueWebhookHandler` 中 `SetTaskRunning` 调用 410-419）
- Test: 依赖现有 server 测试 + 手动构建验证

**Interfaces:**
- Consumes: `newIssueReporter(provider)`（Task 2）、`IssueReporter`（Task 2）。
- Produces: 无新导出符号；行为变更（GitLab 任务不再被静默跳过）。

- [ ] **Step 1: 抽一个共享 helper**

在 controller.go 顶部（`setIssueWaitingLabel` 之前）新增，把"取 reporter + 解析 issueNum + 校验"收敛成一处：

```go
// issueReporterFor returns the reporter and parsed issue number for a task, or
// ok=false when the task cannot be reported on (no provider reporter, no token,
// missing repo/issue). GitLab reporters get their API base filled from the
// task's source host.
func issueReporterFor(task *taskpkg.FactoryTask) (IssueReporter, string, int, bool) {
	r := newIssueReporter(task.Spec.Source.Provider)
	if r == nil || !r.HasToken() {
		return nil, "", 0, false
	}
	if gl, ok := r.(*GitLabClient); ok {
		gl.SetHostFromRepositoryURL(task.Spec.Source.Host)
	}
	repo := task.Spec.Source.Repository
	issueNum := 0
	fmt.Sscanf(task.Spec.Trigger.ID, "%d", &issueNum)
	if repo == "" || issueNum <= 0 {
		return nil, "", 0, false
	}
	return r, repo, issueNum, true
}
```

- [ ] **Step 2: 改写四个标签函数**

`setIssueWaitingLabel`：

```go
func setIssueWaitingLabel(task *taskpkg.FactoryTask) {
	r, repo, issueNum, ok := issueReporterFor(task)
	if !ok {
		return
	}
	_ = r.SetTaskWaiting(context.Background(), repo, issueNum)
}
```

`updateTaskLabels`：

```go
func updateTaskLabels(task *taskpkg.FactoryTask, phase string) {
	r, repo, issueNum, ok := issueReporterFor(task)
	if !ok {
		return
	}
	ctx := context.Background()
	switch phase {
	case taskpkg.PhaseSucceeded:
		_ = r.SetTaskDone(ctx, repo, issueNum)
	case taskpkg.PhaseFailed:
		_ = r.SetTaskFailed(ctx, repo, issueNum)
	}
}
```

`updateIntermediateLabel`：

```go
func updateIntermediateLabel(task *taskpkg.FactoryTask, phase string) {
	r, repo, issueNum, ok := issueReporterFor(task)
	if !ok {
		return
	}
	ctx := context.Background()
	switch phase {
	case taskpkg.PhaseClaimCreated:
		_ = r.SetTaskWaiting(ctx, repo, issueNum)
	case taskpkg.PhaseSandboxReady:
		_ = r.SetTaskRunning(ctx, repo, issueNum)
	}
}
```

- [ ] **Step 3: 改 server.go 里首次创建时的 SetTaskRunning**

`issueWebhookHandler` 中 410-419 现在写死 `provider == taskpkg.ProviderGitHub` + `NewGitHubClient()`。改为：

```go
		if shouldSetLabels {
			if r, repo, issueNum, ok := issueReporterFor(task); ok && task.Spec.Trigger.URL != "" {
				_ = r.SetTaskRunning(req.Context(), repo, issueNum)
			}
		}
```

- [ ] **Step 4: 构建 + 全量测试**

Run: `go build ./... && go test ./factory/cmd/factory/server/`
Expected: PASS（现有 GitHub 标签测试不回归；新分发对 GitHub 行为不变）

- [ ] **Step 5: 提交**

```bash
git add factory/cmd/factory/server/controller.go factory/cmd/factory/server/server.go
git commit -m "feat(gitlab): reflect task status labels via IssueReporter"
```

---
## Task 5: GIT_PROVIDER 必填开关、端点单挂、取消任务分发

`GIT_PROVIDER` 启动时必填校验；只挂载对应 webhook 端点；`handleIssueCancel` 去掉 GitHub-only 限制改用 reporter。

**Files:**
- Modify: `factory/cmd/factory/server/server.go`（`runServer` 97 起、`startWebhookServer` 146-173、`handleIssueCancel` 432-479 的 provider 限制段）
- Test: `factory/cmd/factory/server/server_test.go`

**Interfaces:**
- Consumes: `taskpkg.ReadConfig("GIT_PROVIDER")`、`taskpkg.ProviderGitHub/GitLab`、`issueReporterFor`（Task 4）、`handleIssueCancel` 中 event 已含归一化 `TriggerLabel`（Task 1）。
- Produces:
  - `func resolveGitProvider() (string, error)` — 读取并校验 `GIT_PROVIDER`，非法或缺失返回 error。

- [ ] **Step 1: 写失败测试**

`factory/cmd/factory/server/server_test.go`（若已存在则追加）：

```go
package server

import (
	"os"
	"testing"
)

func TestResolveGitProviderValid(t *testing.T) {
	for _, p := range []string{"github", "gitlab"} {
		t.Setenv("GIT_PROVIDER", p)
		got, err := resolveGitProvider()
		if err != nil {
			t.Fatalf("resolveGitProvider(%q) error = %v", p, err)
		}
		if got != p {
			t.Fatalf("resolveGitProvider(%q) = %q", p, got)
		}
	}
}

func TestResolveGitProviderMissing(t *testing.T) {
	// Ensure the env is empty; ReadConfig also checks mounted files, but in the
	// test environment only the env var applies.
	os.Unsetenv("GIT_PROVIDER")
	if _, err := resolveGitProvider(); err == nil {
		t.Fatal("resolveGitProvider() with unset GIT_PROVIDER: error = nil, want required error")
	}
}

func TestResolveGitProviderInvalid(t *testing.T) {
	t.Setenv("GIT_PROVIDER", "bitbucket")
	if _, err := resolveGitProvider(); err == nil {
		t.Fatal("resolveGitProvider(bitbucket): error = nil, want invalid error")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./factory/cmd/factory/server/ -run TestResolveGitProvider -v`
Expected: FAIL — `undefined: resolveGitProvider`。

- [ ] **Step 3: 实现 resolveGitProvider**

在 `server.go` 新增（放在 `runServer` 上方）：

```go
// resolveGitProvider reads the required GIT_PROVIDER config and validates it.
// A server instance serves exactly one provider (GitHub on a public host,
// GitLab on the internal network); there is no auto-detect fallback.
func resolveGitProvider() (string, error) {
	p := strings.ToLower(strings.TrimSpace(taskpkg.ReadConfig("GIT_PROVIDER")))
	switch p {
	case taskpkg.ProviderGitHub, taskpkg.ProviderGitLab:
		return p, nil
	case "":
		return "", fmt.Errorf("GIT_PROVIDER is required (must be %q or %q)", taskpkg.ProviderGitHub, taskpkg.ProviderGitLab)
	default:
		return "", fmt.Errorf("GIT_PROVIDER %q is invalid (must be %q or %q)", p, taskpkg.ProviderGitHub, taskpkg.ProviderGitLab)
	}
}
```

- [ ] **Step 4: runServer 校验 + 传递 provider**

在 `runServer` 开头（`ctx, cancel := ...` 之前）加：

```go
	provider, err := resolveGitProvider()
	if err != nil {
		return err
	}
	activeProvider = provider
	fmt.Fprintf(cmd.ErrOrStderr(), "ai-factory server provider: %s\n", provider)
```

在文件顶部 `var opts Options` 附近加包级变量（webhook handler 与端点挂载都读它）：

```go
// activeProvider is the single provider this server instance serves, set from
// GIT_PROVIDER at startup.
var activeProvider string
```

- [ ] **Step 5: startWebhookServer 只挂对应端点**

把 `startWebhookServer` 里的 mux 注册改为按 provider 单挂：

```go
	mux := http.NewServeMux()
	switch activeProvider {
	case taskpkg.ProviderGitHub:
		mux.HandleFunc("/webhook/github", func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("X-GitHub-Event") {
			case "check_suite", "check_run":
				ciWebhookHandler(cmd)(w, r)
			default:
				issueWebhookHandler(cmd, taskpkg.ProviderGitHub)(w, r)
			}
		})
	case taskpkg.ProviderGitLab:
		mux.HandleFunc("/webhook/gitlab", issueWebhookHandler(cmd, taskpkg.ProviderGitLab))
	}
	mux.HandleFunc("/healthz", healthHandler)
```

- [ ] **Step 6: handleIssueCancel 去 GitHub-only 限制**

把 432-443 那段 `if event.Provider != taskpkg.ProviderGitHub { ... return }` 整段删除。把 466-473 的 GitHub-only 清理段改为 provider 中立：

```go
	// Clean up status labels and leave a record of the cancellation.
	if r := newIssueReporter(event.Provider); r != nil && r.HasToken() {
		if gl, ok := r.(*GitLabClient); ok {
			gl.SetHostFromRepositoryURL(hostForCancel(event))
		}
		_ = r.SetTaskCancelled(context.Background(), event.Repository, event.IssueNumber, event.TriggerLabel)
	}
```

`handleIssueCancel` 的 event 没有 SourceSpec，GitLab host 从 event 的 `RepositoryHost` 取。新增小 helper：

```go
// hostForCancel returns the host used to build a GitLab API base during
// cancellation. The webhook event carries RepositoryHost parsed from the
// project web_url.
func hostForCancel(event *taskpkg.IssueWebhookEvent) string {
	return event.RepositoryHost
}
```

- [ ] **Step 7: 构建 + 测试**

Run: `go build ./... && go test ./factory/cmd/factory/server/`
Expected: PASS

> 注意：现有测试若直接调 `issueWebhookHandler` 而未设 `activeProvider`，不受影响（handler 参数显式传 provider，`activeProvider` 只影响端点挂载）。如有测试断言 `handleIssueCancel` 对 GitLab 返回 "only supported for GitHub"，需相应更新为验证取消成功。执行时用 grep 确认：`grep -rn "only supported for GitHub" factory/`。

- [ ] **Step 8: 提交**

```bash
git add factory/cmd/factory/server/server.go factory/cmd/factory/server/server_test.go
git commit -m "feat(gitlab): require GIT_PROVIDER, mount one endpoint, dispatch cancel"
```

---

## Task 6: 配置模板与文档

透传 `GIT_PROVIDER` / `GITLAB_API_BASE`，更新 env 示例和 guide。

**Files:**
- Modify: `scripts/ai-factory.env.example`
- Modify: `charts/ai-factory/values.yaml`
- Modify: `charts/ai-factory/templates/configmap.yaml`
- Modify: `docs/self-hosted/guide.md`（3.6 节）

**Interfaces:**
- Consumes: 无代码接口；`ReadConfig("GIT_PROVIDER")` / `ReadConfig("GITLAB_API_BASE")` 已在 Task 3/5 读取。
- Produces: 部署配置项。

- [ ] **Step 1: env 示例**

在 `scripts/ai-factory.env.example` 顶部凭证区之后、模型配置区之前插入：

```
# ============================================================
# 提供商选择（必填）
# ============================================================

# 当前 server 实例服务的代码托管方：github 或 gitlab。必填，不设启动报错。
# 一个实例只服务一个提供商：GitHub 部署在公网、GitLab 部署在内网，分开部署。
GIT_PROVIDER=

# GitLab API 基址（可选，仅 gitlab 模式）。留空则从 issue/project 的 host
# 自动推导为 https://<host>/api/v4，适配自建实例。仅在需要覆盖时填写。
GITLAB_API_BASE=
```

并把现有 `GITLAB_TOKEN=` 的注释从"可选"改为"gitlab 模式必填"。

- [ ] **Step 2: values.yaml**

在 `gitlab:` 段扩展：

```yaml
# GitLab configuration
gitlab:
  # GitLab token (set via --set or in secret). Required when gitProvider=gitlab.
  token: ""
  # GitLab API base override (optional). Empty = derive https://<host>/api/v4
  # from the issue/project host (self-hosted instances).
  apiBase: ""
```

在文件末尾（`reportEnabled` 之后）加顶层：

```yaml
# Git provider this instance serves: "github" or "gitlab". Required.
gitProvider: ""
```

- [ ] **Step 3: configmap.yaml 透传**

在 `configmap.yaml` 的 `data:` 末尾（`gitProxy` 块之后）追加：

```yaml
  {{- if .Values.gitProvider }}
  GIT_PROVIDER: {{ .Values.gitProvider | quote }}
  {{- end }}
  {{- if .Values.gitlab.apiBase }}
  GITLAB_API_BASE: {{ .Values.gitlab.apiBase | quote }}
  {{- end }}
```

- [ ] **Step 4: 校验 chart 渲染**

Run: `helm template charts/ai-factory --set gitProvider=gitlab --set gitlab.apiBase=https://git.internal/api/v4 | grep -E "GIT_PROVIDER|GITLAB_API_BASE"`
Expected: 输出两行含 `GIT_PROVIDER: "gitlab"` 与 `GITLAB_API_BASE: "https://git.internal/api/v4"`。
（若本机无 helm，跳过并在提交信息注明未本地渲染。）

- [ ] **Step 5: 更新 guide.md 3.6 节**

把 `docs/self-hosted/guide.md` 的 "### 3.6 GitLab 集成" 一节（当前是"开发中"占位）整体替换为实际支持说明。内容要点（用户按需精修）：

```markdown
### 3.6 GitLab 集成

GitLab 与 GitHub 分开部署：一个 server 实例只服务一个提供商，由 `GIT_PROVIDER` 环境变量指定（必填，值为 `github` 或 `gitlab`）。

**部署拓扑**：ai-factory 干活时主动连接 GitLab（clone / push / 建 MR / 回帖），因此 GitLab 模式的 server 必须部署在能访问 GitLab 的内网。自建 GitLab 无需公网入口——它只需能把 webhook 推给内网的 ai-factory，并被 ai-factory 反向访问。

**配置步骤**：

1. `ai-factory.env` 设置 `GIT_PROVIDER=gitlab`、`GITLAB_TOKEN=<PAT>`（需 api 权限）。自建实例的 API 基址默认从项目 host 推导为 `https://<host>/api/v4`，如需覆盖设 `GITLAB_API_BASE`。
2. 在目标 GitLab 项目 `Settings → Webhooks` 添加：URL 指向 `http://<ai-factory-内网地址>/webhook/gitlab`，Secret token 填 `WEBHOOK_SECRET`，事件勾选 **Work item events**（即 issue 事件），其余不勾。
3. 给 issue 打 `ai-factory` + `ai-factory-run` 标签触发，与 GitHub 体验一致：running / waiting / done / failed 状态标签、移除触发标签取消任务、自动建 MR、issue 回帖。

**与 GitHub 的差异（已由 ai-factory 内部处理）**：GitLab 加/删标签走 `update` 事件（标签变化在 `changes.labels` 里），ai-factory 自动还原出被加/删的触发标签，用户无感知。

**暂不支持**：GitLab pipeline 的 CI 失败自动修复（GitHub 已支持）；GitLab fork 工作流。
```

- [ ] **Step 6: 提交**

```bash
git add scripts/ai-factory.env.example charts/ai-factory/values.yaml charts/ai-factory/templates/configmap.yaml docs/self-hosted/guide.md
git commit -m "docs(gitlab): document GIT_PROVIDER config and GitLab setup"
```

---

## Task 7: 端到端回归与收尾

全量构建测试，更新 AGENTS.md 架构记录。

**Files:**
- Modify: `AGENTS.md`（Current Architecture 追加一条）

- [ ] **Step 1: 全量测试**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 2: 记录架构决策**

在 `AGENTS.md` 的 "Current Architecture" 列表末尾追加一条：

```markdown
* **GitLab Provider:** A server instance serves exactly one provider, selected by the required `GIT_PROVIDER` env (`github` or `gitlab`); GitHub and GitLab deploy separately (GitHub public, GitLab internal-network). GitLab issue-label changes arrive as `update` events with `changes.labels`; `parseGitLabIssueWebhook` normalizes them into GitHub's `labeled`/`unlabeled` model so trigger and cancel logic is shared. Status-label reporting is dispatched through the `IssueReporter` interface (`GitHubClient` / `GitLabClient`). GitLab CI-failure repair is not yet implemented; the CI watch remains GitHub-gated.
```

- [ ] **Step 3: 提交**

```bash
git add AGENTS.md
git commit -m "docs: record GitLab provider architecture in AGENTS.md"
```

---

## Self-Review 记录

- **Spec coverage:** 触发归一化(T1) / IssueReporter(T2) / GitLabClient(T3) / 标签状态机(T4) / GIT_PROVIDER+端点+取消(T5) / 配置文档(T6) / 收尾(T7)。CI 保留 GitHub 门控 = 非目标，T4/T5 不动 controller.go:386。
- **类型一致性:** `IssueReporter` 方法签名在 T2 定义、T3/T4/T5 消费一致（`issueNumber int`、`SetTaskCancelled(...removedTriggerLabel string)`）。`NewGitLabClient` 在 T3 定义、T2/T4/T5 引用一致。`resolveGitProvider` T5 定义。
- **待校准:** 自建 GitLab 版本未知，`changes.labels.{previous,current}` 结构按主流稳定版实现，留真实 payload 校准点（spec 已记）。



