# CI 反馈修复闭环 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** coding-agent 推 PR 后，ai-factory-server 轮询 GitHub CI；CI 失败时读失败注解，复用原 sandbox 用带工具的 coding-agent 定向修复，push --force 更新同一 PR，直到 CI 变绿或达到重试上限。

**Architecture:** 在 `executeTask` 创建 PR 成功后插入 CI 观察循环。新增 `ci.go`（GitHub CI 客户端 + 纯判定函数）与 `plan.go` 的 `BuildCIRepairScript`（复用 `runAgentScript` + commit + push 拼成单一修复脚本）。修复 runner 通过 `kubectl exec` 进原 sandbox，用修复指令重跑 coding-agent（带工具），再让 server 重新 commit + push。观察期占并发槽位（方案 A）。配置走 ReadConfig + configmap 通道（同 MAX_CONCURRENT_TASKS）。

**Tech Stack:** Go（factory 模块），net/http，httptest（测试），Kubernetes kubectl exec。

## Global Constraints

- **GitHub-only**：CI 观察仅对 `provider == github` 且 `changeRequest.Enabled` 且创建了 PR 的任务生效；GitLab 或 PushOnly 跳过。
- **不往目标仓库加任何文件**：修复只读 CI 注解 + 改仓库代码，不新增仓库文件。
- **agent 永不 commit/push**：commit + push 由 server 在 agent 之后执行（沿用现有约定）。
- **修复是定向的**：prompt 明确"只修 CI 失败，别推翻已有实现"，避免 agent 重做整个任务。
- **方案 A**：观察期间占并发槽位，不做槽位释放。
- 复用现有约定：Apache 2.0 头、`shellQuote()` 插值、`dnsLabel`/`dnsName` 命名、`ReadConfig` 配置读取。
- 失败的 failure reason 用已有 `FailureReasonList` 机制注册（新增 `CIFeedbackFailed`）。
- 上报（评论/标签）沿用现有 `reportTaskResult` / `patchTaskStatus` / GitHubClient 机制。

## Verification Workflow

本地 `go test ./...` 通过。`scripts/watch.sh` 热重载 `factory/**/*.go`。集群验证：触发一个会破坏 CI 的 issue（如改接口漏 mock），确认 server 日志出现 `--- CI FAILED; repairing`，PR 被更新，最终 CI 绿或任务标 `CIFeedbackFailed`。

---

### Task 1: GitHub CI 客户端 + PR URL 解析

**Files:**
- Create: `factory/cmd/factory/server/ci.go`
- Test: `factory/cmd/factory/server/ci_test.go`

**Interfaces:**
- Produces `type CheckRun struct`（`Name`、`Status`、`Conclusion`、`ID int64`）、`type CheckRunAnnotation struct`（`Path`、`StartLine int`、`Level`、`Message`）。
- Produces `func parsePullRequestURL(rawURL string) (owner, repo string, number int, err error)`。
- Produces `func (c *GitHubClient) PullRequestHeadSHA(ctx, owner, repo string, number int) (string, error)`。
- Produces `func (c *GitHubClient) ListCheckRuns(ctx, owner, repo, sha string) ([]CheckRun, error)`。
- Produces `func (c *GitHubClient) ListCheckRunAnnotations(ctx, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)`。

- [ ] **Step 1: 写失败测试**

`factory/cmd/factory/server/ci_test.go`，用 httptest：

```go
func TestParsePullRequestURL(t *testing.T) {
	cases := []struct {
		in      string
		owner   string
		repo    string
		number  int
		wantErr bool
	}{
		{"https://github.com/matrixhub-ai/matrixhub/pull/929", "matrixhub-ai", "matrixhub", 929, false},
		{"https://github.com/o/r/pulls/12", "o", "r", 12, false},
		{"https://github.com/o/r", "", "", 0, true},
		{"not-a-url", "", "", 0, true},
	}
	for _, c := range cases {
		owner, repo, number, err := parsePullRequestURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error, got owner=%q repo=%q number=%d", c.in, owner, repo, number)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", c.in, err)
		}
		if owner != c.owner || repo != c.repo || number != c.number {
			t.Errorf("%q: got %q/%q/%d, want %q/%q/%d", c.in, owner, repo, number, c.owner, c.repo, c.number)
		}
	}
}

func TestListCheckRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/abc123/check-runs" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"check_runs":[
			{"id":1,"name":"go-unit-test","status":"completed","conclusion":"failure"},
			{"id":2,"name":"lint","status":"completed","conclusion":"failure"}
		]}`)
	}))
	defer srv.Close()
	c := &GitHubClient{apiBase: srv.URL, client: srv.Client()}
	runs, err := c.ListCheckRuns(context.Background(), "o", "r", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 || runs[0].Name != "go-unit-test" || runs[0].Conclusion != "failure" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
}
```

再加 `TestListCheckRunAnnotations`（路径 `/repos/o/r/check-runs/7/annotations`，body 为注解数组）和 `TestPullRequestHeadSHA`（路径 `/repos/o/r/pulls/929`，body `{"head":{"sha":"deadbeef"}}`，断言返回 `deadbeef`）。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestParsePullRequestURL|TestListCheckRuns|TestListCheckRunAnnotations|TestPullRequestHeadSHA' -v`
Expected: 编译失败（`parsePullRequestURL` 未定义等）。

- [ ] **Step 3: 实现 `ci.go`**

```go
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
	Conclusion string `json:"conclusion"` // success | failure | neutral | cancelled | timed_out | ...
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
```

注意：文件顶部保留现有仓库的 Apache 2.0 license 头（复制 `github.go` 前 13 行）。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestParsePullRequestURL|TestListCheckRuns|TestListCheckRunAnnotations|TestPullRequestHeadSHA' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add factory/cmd/factory/server/ci.go factory/cmd/factory/server/ci_test.go
git commit -m "feat: github CI client for check-runs and annotations"
```

---

### Task 2: CI 状态判定 + 修复指令构建

**Files:**
- Modify: `factory/cmd/factory/server/ci.go`
- Test: `factory/cmd/factory/server/ci_test.go`

**Interfaces:**
- Consumes: `CheckRun`、`CheckRunAnnotation`（Task 1）。
- Produces `type ciCheckStatus int`（`ciCheckPending`、`ciCheckGreen`、`ciCheckRed`）。
- Produces `func evaluateCheckRuns(runs []CheckRun) ciCheckStatus`。
- Produces `func formatCIFailures(annotations []CheckRunAnnotation) string`。
- Produces `func buildCIRepairInstructions(originalInstructions, prURL string, annotations []CheckRunAnnotation) string`。

- [ ] **Step 1: 写失败测试**

```go
func TestEvaluateCheckRuns(t *testing.T) {
	run := func(status, conclusion string) CheckRun {
		return CheckRun{Name: "x", Status: status, Conclusion: conclusion}
	}
	cases := []struct {
		name string
		runs []CheckRun
		want ciCheckStatus
	}{
		{"all green", []CheckRun{run("completed", "success"), run("completed", "neutral")}, ciCheckGreen},
		{"one red", []CheckRun{run("completed", "success"), run("completed", "failure")}, ciCheckRed},
		{"pending", []CheckRun{run("in_progress", ""), run("completed", "success")}, ciCheckPending},
		{"cancelled counts red", []CheckRun{run("completed", "cancelled")}, ciCheckRed},
		{"skipped ignored", []CheckRun{run("completed", "skipped"), run("completed", "success")}, ciCheckGreen},
		{"empty is green", []CheckRun{}, ciCheckGreen},
	}
	for _, c := range cases {
		if got := evaluateCheckRuns(c.runs); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFormatCIFailures(t *testing.T) {
	got := formatCIFailures([]CheckRunAnnotation{
		{Path: "internal/domain/syncpolicy/generator_test.go", StartLine: 256, Level: "failure", Message: "mock does not implement IRegistryRepo"},
	})
	if !strings.Contains(got, "generator_test.go:256") || !strings.Contains(got, "mock does not implement") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestBuildCIRepairInstructions(t *testing.T) {
	got := buildCIRepairInstructions("implement feature X", "https://github.com/o/r/pull/1", []CheckRunAnnotation{
		{Path: "a.go", StartLine: 3, Level: "failure", Message: "compile error"},
	})
	for _, want := range []string{"implement feature X", "o/r/pull/1", "a.go:3", "compile error", "Fix ONLY the CI failures"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestEvaluateCheckRuns|TestFormatCIFailures|TestBuildCIRepairInstructions' -v`
Expected: 编译失败（函数未定义）。

- [ ] **Step 3: 实现**

追加到 `ci.go`：

```go
// ciCheckStatus is the terminal or pending state of a set of check runs.
type ciCheckStatus int

const (
	// ciCheckPending means at least one check run is still queued/in-progress.
	ciCheckPending ciCheckStatus = iota
	// ciCheckGreen means all completed check runs are non-failing.
	ciCheckGreen
	// ciCheckRed means at least one completed check run failed.
	ciCheckRed
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

// formatCIFailures renders failure annotations as "- path:line [level]: message" lines.
func formatCIFailures(annotations []CheckRunAnnotation) string {
	var b strings.Builder
	for _, a := range annotations {
		fmt.Fprintf(&b, "- %s:%d [%s]: %s\n", a.Path, a.StartLine, a.Level, a.Message)
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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestEvaluateCheckRuns|TestFormatCIFailures|TestBuildCIRepairInstructions' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add factory/cmd/factory/server/ci.go factory/cmd/factory/server/ci_test.go
git commit -m "feat: evaluate check-run status and build CI repair instructions"
```

---

### Task 3: `BuildCIRepairScript`（plan.go 导出修复脚本生成）

**Files:**
- Modify: `factory/pkg/task/plan.go`
- Test: `factory/pkg/task/plan_test.go`

**Interfaces:**
- Consumes: 现有 `runAgentScript`、`commitChangesScript`、`pushChangeBranchScript`、`changeRequestDefaults`。
- Produces `func BuildCIRepairScript(task *FactoryTask, repairInstructions string) (string, error)`：返回一个 `sh -lc` 脚本，先跑 coding-agent（带修复指令），再 commit，再 push --force 更新 change branch。

- [ ] **Step 1: 写失败测试**

在 `factory/pkg/task/plan_test.go`：

```go
func TestBuildCIRepairScript(t *testing.T) {
	task := &FactoryTask{
		Metadata: Meta{Name: "test-task"},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{Provider: ProviderGitHub, Repository: "o/r", BaseRef: "main", CloneURL: "https://example.com/o/r.git"},
			Agent:  AgentSpec{Name: "builder", Command: "ai-factory-agent openai-compatible"},
			ChangeRequest: ChangeRequestSpec{
				Enabled:    true,
				BranchName: "factory-task/test-task",
			},
		},
	}
	script, err := BuildCIRepairScript(task, "fix the mock")
	if err != nil {
		t.Fatalf("BuildCIRepairScript: %v", err)
	}
	for _, want := range []string{"ai-factory-agent openai-compatible", "fix the mock", "git commit", "git push"} {
		if !strings.Contains(script, want) {
			t.Errorf("repair script missing %q:\n%s", want, script)
		}
	}
}
```

（先确认 `plan_test.go` 里已有的 `FactoryTask`/`Meta`/`SourceSpec` 等类型字面量写法，复用现有测试的构造方式。）

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/pkg/task/ -run TestBuildCIRepairScript -v`
Expected: 编译失败（`BuildCIRepairScript` 未定义）。

- [ ] **Step 3: 实现**

追加到 `factory/pkg/task/plan.go`：

```go
// BuildCIRepairScript builds a single shell script that runs the coding agent
// with CI-failure repair instructions against the existing checkout, then
// commits and force-pushes the fix to the change branch (updating the PR).
// The agent never commits or pushes; this script runs them after the agent.
func BuildCIRepairScript(task *FactoryTask, repairInstructions string) (string, error) {
	if task == nil {
		return "", errors.New("FactoryTask is required")
	}
	workDir := "/workspace/repo"
	agentCommand := task.Spec.Agent.Command
	if agentCommand == "" {
		agentCommand = "ai-factory-agent openai-compatible"
	}
	changeBranch, _, remoteName, commitMessage, authorName, authorEmail, _, _ := changeRequestDefaults(task)
	return fmt.Sprintf(`set -eu
%s
%s
%s`,
		runAgentScript(workDir, repairInstructions, task.Spec.Agent.PromptRef, agentCommand),
		commitChangesScript(workDir, commitMessage, authorName, authorEmail),
		pushChangeBranchScript(workDir, remoteName, changeBranch, task.Spec.Source.BaseRef),
	), nil
}
```

确认 `plan.go` 已 `import "errors"`；若没有，加到 import 块。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/pkg/task/ -run TestBuildCIRepairScript -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add factory/pkg/task/plan.go factory/pkg/task/plan_test.go
git commit -m "feat: build CI repair script that runs agent then force-pushes"
```

---

### Task 4: CI 观察 + 修复编排（controller.go）

**Files:**
- Modify: `factory/cmd/factory/server/controller.go`
- Test: `factory/cmd/factory/server/controller_test.go`

**Interfaces:**
- Consumes: Task 1/2/3 的 `ciClient` 方法、`ciCheckStatus`、`buildCIRepairInstructions`、`taskpkg.BuildCIRepairScript`。
- Produces `type ciClient interface`、`type ciRepairRunner func([]CheckRunAnnotation) error`、`type ciWatchOptions struct`、`type ciWatchOutcome int`（`ciWatchGreen`/`ciWatchFailed`）。
- Produces `func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string)`。
- Produces `func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string) ciRepairRunner`。

- [ ] **Step 1: 写失败测试**

在 `factory/cmd/factory/server/controller_test.go`，用 fake `ciClient` 和 fake `repair`：

```go
type fakeCIClient struct {
	headSHA func() (string, error)
	runs    func(sha string) ([]CheckRun, error)
	anns    func(id int64) ([]CheckRunAnnotation, error)
}

func (f *fakeCIClient) PullRequestHeadSHA(_ context.Context, _, _ string, _ int) (string, error) { return f.headSHA() }
func (f *fakeCIClient) ListCheckRuns(_ context.Context, _, _, sha string) ([]CheckRun, error)    { return f.runs(sha) }
func (f *fakeCIClient) ListCheckRunAnnotations(_ context.Context, _, _ string, id int64) ([]CheckRunAnnotation, error) {
	return f.anns(id)
}

func TestWatchAndRepairCI(t *testing.T) {
	green := []CheckRun{{Name: "c1", Status: "completed", Conclusion: "success"}}
	red := []CheckRun{{Name: "c1", Status: "completed", Conclusion: "failure"}}
	// pending first, then green after repair
	attempts := 0
	repairs := 0
	gh := &fakeCIClient{
		headSHA: func() (string, error) { return "sha", nil },
		runs: func(sha string) ([]CheckRun, error) {
			attempts++
			if attempts <= 1 {
				return red, nil
			}
			return green, nil
		},
		anns: func(id int64) ([]CheckRunAnnotation, error) {
			return []CheckRunAnnotation{{Path: "a.go", StartLine: 1, Level: "failure", Message: "err"}}, nil
		},
	}
	repair := func(_ []CheckRunAnnotation) error { repairs++; return nil }
	outcome, _ := watchAndRepairCI(io.Discard, taskForCI(), "https://github.com/o/r/pull/1", gh, repair, ciWatchOptions{maxRetries: 3, maxWait: time.Minute, pollInterval: time.Millisecond})
	if outcome != ciWatchGreen {
		t.Fatalf("want green, got %v", outcome)
	}
	if repairs != 1 {
		t.Fatalf("want 1 repair, got %d", repairs)
	}
}

func TestWatchAndRepairCIExhausted(t *testing.T) {
	gh := &fakeCIClient{
		headSHA: func() (string, error) { return "sha", nil },
		runs:    func(_ string) ([]CheckRun, error) { return redAlways, nil },
		anns:    func(_ int64) ([]CheckRunAnnotation, error) { return nil, nil },
	}
	repair := func(_ []CheckRunAnnotation) error { return nil }
	outcome, summary := watchAndRepairCI(io.Discard, taskForCI(), "https://github.com/o/r/pull/1", gh, repair, ciWatchOptions{maxRetries: 2, maxWait: time.Second, pollInterval: time.Millisecond})
	if outcome != ciWatchFailed {
		t.Fatalf("want failed, got %v", outcome)
	}
	if summary == "" {
		t.Fatal("want non-empty failure summary")
	}
}
```

`taskForCI()` 返回一个 `ProviderGitHub` 的 FactoryTask（复用 controller_test.go 现有构造 helper）。确认现有测试的构造方式后对齐。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestWatchAndRepairCI' -v`
Expected: 编译失败。

- [ ] **Step 3: 实现**

追加到 `controller.go`：

```go
// ciClient abstracts the GitHub CI API for testability.
type ciClient interface {
	PullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error)
	ListCheckRuns(ctx context.Context, owner, repo, sha string) ([]CheckRun, error)
	ListCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)
}

// ciRepairRunner repairs a CI failure inside the reused sandbox and pushes the fix.
type ciRepairRunner func(annotations []CheckRunAnnotation) error

// ciWatchOptions bounds the CI watch loop.
type ciWatchOptions struct {
	maxRetries   int
	maxWait      time.Duration
	pollInterval time.Duration
}

// ciWatchOutcome is the result of the CI watch loop.
type ciWatchOutcome int

const (
	ciWatchGreen  ciWatchOutcome = iota
	ciWatchFailed ciWatchOutcome = 1
)

// watchAndRepairCI polls a PR's CI until green, red, or the budget expires. On
// red it invokes repair (which fixes and force-pushes, updating the PR head SHA)
// and re-polls, up to maxRetries. It returns the outcome and a failure summary.
func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string) {
	owner, repo, number, err := parsePullRequestURL(prURL)
	if err != nil {
		return ciWatchFailed, fmt.Sprintf("parse PR URL %q: %v", prURL, err)
	}
	var lastSummary string
	for attempt := 0; attempt < opts.maxRetries; attempt++ {
		sha, err := gh.PullRequestHeadSHA(context.Background(), owner, repo, number)
		if err != nil {
			return ciWatchFailed, fmt.Sprintf("get PR head sha: %v", err)
		}
		status, summary := pollCheckRuns(gh, owner, repo, sha, opts)
		lastSummary = summary
		switch status {
		case ciCheckGreen:
			fmt.Fprintf(out, "--- CI GREEN (%s)\n", sha)
			return ciWatchGreen, summary
		case ciCheckRed:
			annotations := collectFailedAnnotations(gh, owner, repo, sha)
			fmt.Fprintf(out, "--- CI FAILED on %s (attempt %d/%d); repairing\n%s", sha, attempt+1, opts.maxRetries, formatCIFailures(annotations))
			if err := repair(annotations); err != nil {
				return ciWatchFailed, fmt.Sprintf("repair failed: %v", err)
			}
		default: // ciCheckPending with budget exhausted
			return ciWatchFailed, fmt.Sprintf("CI still pending after %s", opts.maxWait)
		}
	}
	return ciWatchFailed, fmt.Sprintf("CI still failing after %d repair attempts:\n%s", opts.maxRetries, lastSummary)
}

// pollCheckRuns polls check-runs until green/red or the wait budget expires.
func pollCheckRuns(gh ciClient, owner, repo, sha string, opts ciWatchOptions) (ciCheckStatus, string) {
	deadline := time.Now().Add(opts.maxWait)
	for {
		runs, err := gh.ListCheckRuns(context.Background(), owner, repo, sha)
		if err != nil {
			return ciCheckError, fmt.Sprintf("list check runs: %v", err)
		}
		if status := evaluateCheckRuns(runs); status != ciCheckPending {
			return status, summarizeCheckRuns(runs)
		}
		if time.Now().After(deadline) {
			return ciCheckPending, fmt.Sprintf("CI pending after %s", opts.maxWait)
		}
		time.Sleep(opts.pollInterval)
	}
}
```

注意：`ci.go` 的 `ciCheckStatus` 需新增 `ciCheckError ciCheckStatus = 3`（API 调用失败），`watchAndRepairCI` 的 switch 里把 `ciCheckError` 并入 `ciCheckFailed` 分支返回。`collectFailedAnnotations` 遍历失败 job 归并注解：

```go
// collectFailedAnnotations returns all failure annotations across failing check runs.
func collectFailedAnnotations(gh ciClient, owner, repo, sha string) []CheckRunAnnotation {
	runs, err := gh.ListCheckRuns(context.Background(), owner, repo, sha)
	if err != nil {
		return nil
	}
	var all []CheckRunAnnotation
	for _, r := range runs {
		if r.Status != "completed" || isNonFailingConclusion(r.Conclusion) {
			continue
		}
		anns, err := gh.ListCheckRunAnnotations(context.Background(), owner, repo, r.ID)
		if err != nil {
			continue
		}
		all = append(all, anns...)
	}
	return all
}

func isNonFailingConclusion(c string) bool {
	switch c {
	case "success", "neutral", "skipped", "":
		return true
	default:
		return false
	}
}

func summarizeCheckRuns(runs []CheckRun) string {
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "- %s: %s/%s\n", r.Name, r.Status, r.Conclusion)
	}
	return b.String()
}

// ciRepairRunnerFor returns a repair runner that runs BuildCIRepairScript in the
// reused sandbox via kubectl exec.
func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string) ciRepairRunner {
	containerName := task.Spec.Sandbox.ContainerName
	if containerName == "" {
		containerName = "dev"
	}
	return func(annotations []CheckRunAnnotation) error {
		instructions := buildCIRepairInstructions(task.Spec.Work.Instructions, prURL, annotations)
		script, err := taskpkg.BuildCIRepairScript(task, instructions)
		if err != nil {
			return err
		}
		return runKubectl(nil, []string{"exec", "-n", namespace, sandboxName, "-c", containerName, "--", "/bin/sh", "-lc", script})
	}
}
```

在 `executeTask` 中，PR 创建成功后（`createTaskChangeRequest` + `validateChangeRequestResult` 之后、Succeeded 分支之前）插入：

```go
if resultURL != "" && reportCIWatchEnabled() {
	outcome, summary := watchAndRepairCI(out, task, resultURL, newGitHubClient(), ciRepairRunnerFor(task, namespace, sandboxName, resultURL), resolveCIWatchOptions())
	if outcome != ciWatchGreen {
		failure := taskpkg.FailureClassification{
			Reason:    taskpkg.CIFeedbackFailed,
			Friendly:  "GitHub CI did not pass after repair attempts",
			RawMessage: summary,
		}
		_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
			Phase: taskpkg.PhaseFailed, Reason: "CIFeedbackFailed", Message: summary,
			SandboxClaimName: claim, SandboxName: sandboxName, FailureReason: failure,
		})
		reportTaskResult(out, task, taskpkg.PhaseFailed, "GitHub CI failed after repair attempts: "+summary)
		return fmt.Errorf("ci feedback failed: %s", summary)
	}
}
```

其中 `reportCIWatchEnabled()` 和 `resolveCIWatchOptions()` 在 Task 5 实现（本任务先提供 stub 或直接内联 `opts.CIWatchEnabled`）。为让本任务自洽，先用 `opts.CIWatchEnabled && opts.CIWatchMaxRetries > 0` 判断，`resolveCIWatchOptions()` 从 `opts` 构造。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestWatchAndRepairCI' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add factory/cmd/factory/server/controller.go factory/cmd/factory/server/controller_test.go
git commit -m "feat: watch PR CI and repair failures in the reused sandbox"
```

---

### Task 5: 配置接入 + failure reason + 汇报集成

**Files:**
- Modify: `factory/cmd/factory/server/server.go`（opts + flags + `resolveCIWatchOptions`）
- Modify: `factory/cmd/factory/server/controller.go`（`reportCIWatchEnabled`）
- Modify: `factory/pkg/task/failure.go`（新增 `CIFeedbackFailed`）
- Modify: `charts/ai-factory/templates/configmap.yaml`
- Modify: `charts/ai-factory/values.yaml`
- Modify: `scripts/update-config.sh`
- Modify: `scripts/upgrade.sh`
- Modify: `scripts/ai-factory.env`
- Test: `factory/cmd/factory/server/controller_test.go` 或 `server_test.go`

**Interfaces:**
- Consumes: Task 4 的 `ciWatchOptions`、`watchAndRepairCI`。
- Produces `opts.CIWatchEnabled bool`、`opts.CIWatchMaxRetries int`、`opts.CIWatchMaxWait time.Duration`、`opts.CIWatchPollInterval time.Duration`。
- Produces `func resolveCIWatchOptions(cmd *cobra.Command) ciWatchOptions`（flag → ReadConfig → 默认）。
- Produces `taskpkg.CIFeedbackFailed FailureReason`。

- [ ] **Step 1: 写失败测试**

`TestResolveCIWatchOptionsDefaults`：`resolveCIWatchOptions` 在无 flag、无 env 时返回 `{maxRetries: 3, maxWait: 30m, pollInterval: 60s}`。可在 `server_test.go` 或 `controller_test.go`。若 `resolveCIWatchOptions` 依赖 `cmd`，用 `&cobra.Command{}` 并 `executeContext` 后不必真正执行。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run TestResolveCIWatchOptionsDefaults -v`
Expected: 编译失败。

- [ ] **Step 3: 实现**

`server.go` Options 增加字段，`init()` 增加 flags：

```go
opts.CIWatchEnabled, "ci-watch", true, "wait for GitHub CI on created PRs and repair failures"
opts.CIWatchMaxRetries, "ci-watch-max-retries", 3, "max CI repair cycles before failing"
opts.CIWatchMaxWait, "ci-watch-max-wait", 30*time.Minute, "max time to wait for CI per cycle"
opts.CIWatchPollInterval, "ci-watch-interval", 60*time.Second, "CI status poll interval"
```

`resolveCIWatchOptions`（放在 controller.go，同 `resolveMaxConcurrentTasks` 风格）：

```go
func resolveCIWatchOptions(cmd *cobra.Command) ciWatchOptions {
	// Seed from the flag-resolved opts (cobra already applied flag defaults).
	o := ciWatchOptions{maxRetries: opts.CIWatchMaxRetries, maxWait: opts.CIWatchMaxWait, pollInterval: opts.CIWatchPollInterval}
	// ReadConfig overrides for hot-update via scripts/update-config.sh.
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			o.maxRetries = n
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			o.maxWait = d
		}
	}
	if v := taskpkg.ReadConfig("CI_WATCH_RETRY_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			o.pollInterval = d
		}
	}
	return o
}

func reportCIWatchEnabled() bool {
	if v := taskpkg.ReadConfig("CI_WATCH_ENABLED"); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return opts.CIWatchEnabled
}
```

（保留 `opts` 字段用于 flag 直读；`resolveCIWatchOptions` 以 ReadConfig 优先——与 `resolveMaxConcurrentTasks` 的"flag 优先"略有差异，但为让 `update-config.sh` 能热更新，统一用 ReadConfig 优先。若实现者发现与 flag 语义冲突，保持 flag 优先、env 兜底即可，测试按实际决议对齐。）

`failure.go` 新增：

```go
// CIFeedbackFailed means GitHub CI did not pass after the configured repair attempts.
CIFeedbackFailed FailureReason = "CIFeedbackFailed"
```

并把 `CIFeedbackFailed` 加入 `FailureReasonList()`（`failure.go` 末尾）。

`charts/ai-factory/templates/configmap.yaml` 增加：

```yaml
  CI_WATCH_ENABLED: {{ .Values.server.ciWatchEnabled | quote }}
  CI_WATCH_MAX_RETRIES: {{ .Values.server.ciWatchMaxRetries | quote }}
  CI_WATCH_MAX_WAIT: {{ .Values.server.ciWatchMaxWait | quote }}
  CI_WATCH_RETRY_INTERVAL: {{ .Values.server.ciWatchPollInterval | quote }}
```

`charts/ai-factory/values.yaml` 在 `server:` 下增加：

```yaml
  ciWatchEnabled: true
  ciWatchMaxRetries: 3
  ciWatchMaxWait: 30m
  ciWatchPollInterval: 60s
```

`scripts/update-config.sh` 的 `CONFIG_KEYS` 增加：
```bash
"CI_WATCH_ENABLED" "CI_WATCH_MAX_RETRIES" "CI_WATCH_MAX_WAIT" "CI_WATCH_RETRY_INTERVAL"
```

`scripts/upgrade.sh` 增加 `--set`：
```bash
[ -n "${CI_WATCH_ENABLED:-}" ] && HELM_ARGS+=(--set server.ciWatchEnabled="${CI_WATCH_ENABLED}")
[ -n "${CI_WATCH_MAX_RETRIES:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxRetries="${CI_WATCH_MAX_RETRIES}")
[ -n "${CI_WATCH_MAX_WAIT:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxWait="${CI_WATCH_MAX_WAIT}")
[ -n "${CI_WATCH_RETRY_INTERVAL:-}" ] && HELM_ARGS+=(--set server.ciWatchPollInterval="${CI_WATCH_RETRY_INTERVAL}")
```

`scripts/ai-factory.env` 增加：
```
CI_WATCH_ENABLED=true
CI_WATCH_MAX_RETRIES=3
CI_WATCH_MAX_WAIT=30m
CI_WATCH_RETRY_INTERVAL=60s
```

`controller.go` 的 `executeTask` 插入处改用 `reportCIWatchEnabled()` 与 `resolveCIWatchOptions(cmd)`（若 `executeTask` 无 cmd 引用，则 `resolveCIWatchOptions(nil)`）。

- [ ] **Step 4: 运行测试确认通过 + 全量测试**

Run: `cd /root/ai-factory/ai-factory && go build ./... && go test ./...`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add factory/ charts/ scripts/
git commit -m "feat: wire CI watch config through env/configmap and add CIFeedbackFailed"
```

---

## 自审检查

- **Spec 覆盖**：CI 观察（Task 4）、失败注解（Task 1/2/4）、带工具定向修复（Task 3/4）、push 更新同一 PR（Task 3）、方案 A 占槽位（executeTask 内同步，天然占槽）、配置（Task 5）、CIFeedbackFailed（Task 5）、权限（设计文档已确认公开 API 无需 token）。
- **占位符扫描**：无 TBD/TODO；每个代码步骤含实际代码。
- **类型一致性**：`ciCheckStatus` 在 Task 2 定义、Task 4 使用；`ciClient` 方法签名在 Task 1 定义、Task 4 的 fake 实现；`BuildCIRepairScript` 在 Task 3 定义、Task 4 的 `ciRepairRunnerFor` 调用。`parsePullRequestURL` Task 1 → `watchAndRepairCI`（Task 4）一致。
- **未覆盖提醒**：`ci.go` 需新增 `ciCheckError ciCheckStatus = 3`（API 调用失败），`watchAndRepairCI` 的 switch 把 `ciCheckError` 并入失败返回分支；`resolveCIWatchOptions` 以 `opts`（cobra flag 决议值）为种子、ReadConfig 覆盖，实现时确认 `executeTask` 是否有 `cmd` 引用（无则传 `nil`）。