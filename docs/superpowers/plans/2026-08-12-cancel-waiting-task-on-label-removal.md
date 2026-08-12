# 取消 waiting 任务（移除触发标签）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GitHub issue 处于 waiting 状态时，用户移除触发标签（`ai-factory-run`/`ai-factory-smoke`），后台任务被取消（删 SandboxClaim + FactoryTask + 清标签 + 评论）。

**Architecture:** webhook handler 增加 `unlabeled` 分支，识别被移除的触发标签后走 `handleIssueCancel`：用 `FactoryTaskName()` 推导任务名 → `getFactoryTaskPhase` 判 waiting → 删关联 SandboxClaim（label selector）→ 删 FactoryTask → `SetTaskCancelled` 清标签并评论。controller 侧加两道竞态防护：`reportTaskResult` 开头检查 task 存在（失败路径静默），`executeTask` 进入 running 前检查 task 存在（成功路径中止）。

**Tech Stack:** Go, cobra, kubectl exec 包装, GitHub REST API（现有 GitHubClient）。

## Global Constraints

- 仅 GitHub。GitLab 的 unlabeled 事件忽略（标签流程本就只支持 GitHub）。
- 仅取消 waiting：phase ∈ `{Pending, ClaimCreated}`。running/terminal 忽略。
- 取消是尽力而为：竞态窗口由两道防护收敛到"进入 running 前检查"。
- 删除操作幂等：一律 `--ignore-not-found`。
- 评论文案固定为：`ai-factory 已取消 #<n>：触发标签 <label> 被移除（任务尚未开始执行）`。
- 不新增第三方依赖。

---

### Task 1: task 包导出 `FactoryTaskName`（命名单一来源）

**Files:**
- Modify: `factory/pkg/task/webhook.go:137`（`FactoryTaskFromIssueWebhook` 中命名处）
- Test: `factory/pkg/task/webhook_test.go`

**Interfaces:**
- Produces: `func FactoryTaskName(provider, repository string, issueNumber int) string` — 返回确定性任务名，与 `FactoryTaskFromIssueWebhook` 现有命名规则一致。

- [ ] **Step 1: Write the failing test**

在 `webhook_test.go` 末尾追加：

```go
func TestFactoryTaskName(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		repository string
		num        int
		want       string
	}{
		{"github", "github", "liyuerich/ai-factory", 42, "github-liyuerich-ai-factory-42"},
		{"gitlab", "gitlab", "platform/ai-factory", 7, "gitlab-platform-ai-ai-factory-7"},
		{"uppercase normalized", "github", "Owner/Repo", 1, "github-owner-repo-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FactoryTaskName(tc.provider, tc.repository, tc.num); got != tc.want {
				t.Fatalf("FactoryTaskName(%q, %q, %d) = %q, want %q", tc.provider, tc.repository, tc.num, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/pkg/task/ -run TestFactoryTaskName -v
```

Expected: FAIL with `undefined: FactoryTaskName`.

- [ ] **Step 3: Write minimal implementation**

在 `webhook.go` 的 `FactoryTaskFromIssueWebhook` 上方（`dnsName` 函数附近）新增：

```go
// FactoryTaskName returns the deterministic FactoryTask name for an issue.
// Must stay in sync with FactoryTaskFromIssueWebhook so cancellation can
// derive the same name from a webhook event.
func FactoryTaskName(provider, repository string, issueNumber int) string {
	return dnsName(fmt.Sprintf("%s-%s-%d", provider, repository, issueNumber))
}
```

把 `webhook.go:137` 的 `Name: dnsName(fmt.Sprintf("%s-%s-%d", event.Provider, event.Repository, event.IssueNumber)),` 改为：

```go
Name:      FactoryTaskName(event.Provider, event.Repository, event.IssueNumber),
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/pkg/task/ -run TestFactoryTaskName -v
```

Expected: PASS（3 个子测试全过）。

- [ ] **Step 5: Run full task package tests**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/pkg/task/
```

Expected: PASS（确认重构未破坏现有 webhook 测试）。

- [ ] **Step 6: Commit**

```bash
cd /root/ai-factory/ai-factory && git add factory/pkg/task/webhook.go factory/pkg/task/webhook_test.go && git commit -m "refactor: export FactoryTaskName for issue task naming"
```

---

### Task 2: `GitHubClient.SetTaskCancelled` + fakeGitHubAPI 支持评论

**Files:**
- Modify: `factory/cmd/factory/server/github.go`
- Modify: `factory/cmd/factory/server/github_test.go`

**Interfaces:**
- Consumes: 现有 `RemoveLabel`、`PostComment`、`EnsureLabel`。
- Produces: `func (c *GitHubClient) SetTaskCancelled(ctx context.Context, repo string, issueNumber int, removedTriggerLabel string) error` — 移除 `ai-factory-running`/`ai-factory-waiting`，发表评论。
- Produces: `fakeGitHubAPI` 返回值改为 `(*httptest.Server, *[]labelRequest, *[]string)`（第三项记录评论 body）。

- [ ] **Step 1: Extend fakeGitHubAPI to record comments**

修改 `github_test.go:21` 的函数签名和实现，返回评论记录：

```go
func fakeGitHubAPI(t *testing.T, labelsOnIssue []string) (*httptest.Server, *[]labelRequest, *[]string) {
	t.Helper()
	var ops []labelRequest
	var comments []string
	currentLabels := make(map[string]bool)
	for _, l := range labelsOnIssue {
		currentLabels[l] = true
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// POST /repos/{owner}/{repo}/labels — EnsureLabel
		if r.Method == "POST" && strings.HasSuffix(path, "/labels") && !strings.Contains(path, "/issues/") {
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			json.Unmarshal(body, &payload)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(payload)
			return
		}

		// POST /repos/{owner}/{repo}/issues/{num}/labels — AddLabel
		if r.Method == "POST" && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/labels") {
			body, _ := io.ReadAll(r.Body)
			var labels []string
			json.Unmarshal(body, &labels)
			for _, l := range labels {
				currentLabels[l] = true
				ops = append(ops, labelRequest{method: "POST", label: l})
			}
			w.WriteHeader(200)
			json.NewEncoder(w).Encode(labels)
			return
		}

		// DELETE /repos/{owner}/{repo}/issues/{num}/labels/{label} — RemoveLabel
		if r.Method == "DELETE" && strings.Contains(path, "/labels/") {
			parts := strings.Split(path, "/labels/")
			if len(parts) == 2 {
				label := parts[1]
				delete(currentLabels, label)
				ops = append(ops, labelRequest{method: "DELETE", label: label})
			}
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
			return
		}

		// POST /repos/{owner}/{repo}/issues/{num}/comments — PostComment
		if r.Method == "POST" && strings.HasSuffix(path, "/comments") {
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err == nil {
				comments = append(comments, payload["body"])
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(payload)
			return
		}

		w.WriteHeader(404)
	}))

	return server, &ops, &comments
}
```

- [ ] **Step 2: Update the 5 existing call sites**

把 `github_test.go` 中以下 5 处 `server, ops := fakeGitHubAPI(...)` 改为 `server, ops, _ := fakeGitHubAPI(...)`（行号 90、114、138、165、191）：

```go
server, ops, _ := fakeGitHubAPI(t, []string{"ai-factory-run", "ai-factory-running"})
```

- [ ] **Step 3: Write the failing test for SetTaskCancelled**

在 `github_test.go` 末尾追加：

```go
func TestSetTaskCancelled(t *testing.T) {
	server, ops, comments := fakeGitHubAPI(t, []string{"ai-factory-run", "ai-factory-waiting"})
	defer server.Close()

	gh := &GitHubClient{
		token:   "fake-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	err := gh.SetTaskCancelled(context.Background(), "owner/repo", 42, "ai-factory-run")
	if err != nil {
		t.Fatalf("SetTaskCancelled failed: %v", err)
	}

	labels := labelsAfterOps(*ops)
	if labels["ai-factory-waiting"] {
		t.Error("expected ai-factory-waiting label to be removed")
	}
	if labels["ai-factory-running"] {
		t.Error("expected ai-factory-running label to be removed")
	}
	if len(*comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(*comments))
	}
	want := "ai-factory 已取消 #42：触发标签 ai-factory-run 被移除（任务尚未开始执行）"
	if (*comments)[0] != want {
		t.Fatalf("comment = %q, want %q", (*comments)[0], want)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run TestSetTaskCancelled -v
```

Expected: FAIL with `undefined: (*GitHubClient).SetTaskCancelled`.

- [ ] **Step 5: Write minimal implementation**

在 `github.go` 的 `SetTaskFailed` 之后追加：

```go
// SetTaskCancelled removes the running/waiting labels and posts a comment
// explaining that the task was cancelled (trigger label removed before it ran).
func (c *GitHubClient) SetTaskCancelled(ctx context.Context, repo string, issueNumber int, removedTriggerLabel string) error {
	if !c.HasToken() {
		return nil
	}
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelRunning)
	_ = c.RemoveLabel(ctx, repo, issueNumber, labelWaiting)
	return c.PostComment(ctx, repo, issueNumber, fmt.Sprintf(
		"ai-factory 已取消 #%d：触发标签 %s 被移除（任务尚未开始执行）", issueNumber, removedTriggerLabel))
}
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run TestSetTaskCancelled -v
```

Expected: PASS。

- [ ] **Step 7: Run all server tests**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/
```

Expected: PASS（确认 fakeGitHubAPI 签名改造未破坏现有测试）。

- [ ] **Step 8: Commit**

```bash
cd /root/ai-factory/ai-factory && git add factory/cmd/factory/server/github.go factory/cmd/factory/server/github_test.go && git commit -m "feat: add SetTaskCancelled to clean labels and comment on cancel"
```

---

### Task 3: server.go unlabeled 分支 + `handleIssueCancel` + 纯函数

**Files:**
- Modify: `factory/cmd/factory/server/server.go`
- Create: `factory/cmd/factory/server/cancel_test.go`

**Interfaces:**
- Consumes: `taskpkg.FactoryTaskName`（Task 1）、`getFactoryTaskPhase`、`runKubectl`、`NewGitHubClient`、`SetTaskCancelled`（Task 2）、`labelRun`/`labelSmoke` 常量。
- Produces: `handleIssueCancel(w http.ResponseWriter, cmd *cobra.Command, event *taskpkg.IssueWebhookEvent)` — 编排取消流程。
- Produces: `isTriggerLabel(label string) bool`、`isWaitingPhase(phase string) bool`、`cancelCommentBody(issueNumber int, removedTriggerLabel string) string` — 纯函数，可单测。

- [ ] **Step 1: Write the failing pure-function tests**

创建 `factory/cmd/factory/server/cancel_test.go`：

```go
package server

import (
	"testing"

	taskpkg "github.com/ai-on-gke/ai-factory/factory/pkg/task"
)

func TestIsTriggerLabel(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"ai-factory-run", true},
		{"ai-factory-smoke", true},
		{"ai-factory-waiting", false},
		{"ai-factory-running", false},
		{"ai-factory", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isTriggerLabel(tc.label); got != tc.want {
			t.Errorf("isTriggerLabel(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

func TestIsWaitingPhase(t *testing.T) {
	cases := []struct {
		phase string
		want  bool
	}{
		{taskpkg.PhasePending, true},
		{taskpkg.PhaseClaimCreated, true},
		{taskpkg.PhaseSandboxReady, false},
		{taskpkg.PhaseRunning, false},
		{taskpkg.PhaseSucceeded, false},
		{taskpkg.PhaseFailed, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isWaitingPhase(tc.phase); got != tc.want {
			t.Errorf("isWaitingPhase(%q) = %v, want %v", tc.phase, got, tc.want)
		}
	}
}

func TestCancelCommentBody(t *testing.T) {
	got := cancelCommentBody(42, "ai-factory-run")
	want := "ai-factory 已取消 #42：触发标签 ai-factory-run 被移除（任务尚未开始执行）"
	if got != want {
		t.Fatalf("cancelCommentBody() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestIsTriggerLabel|TestIsWaitingPhase|TestCancelCommentBody' -v
```

Expected: FAIL with `undefined: isTriggerLabel`（等）。

- [ ] **Step 3: Write the pure functions**

在 `server.go` 的 `isTerminalPhase` 函数附近追加：

```go
// isTriggerLabel reports whether a label is a trigger label whose removal
// should cancel a waiting task.
func isTriggerLabel(label string) bool {
	return label == labelRun || label == labelSmoke
}

// isWaitingPhase reports whether a FactoryTask is still waiting for a sandbox
// (either queued behind the concurrency gate or waiting for its claim).
func isWaitingPhase(phase string) bool {
	return phase == taskpkg.PhasePending || phase == taskpkg.PhaseClaimCreated
}

// cancelCommentBody builds the GitHub comment posted when a task is cancelled.
func cancelCommentBody(issueNumber int, removedTriggerLabel string) string {
	return fmt.Sprintf("ai-factory 已取消 #%d：触发标签 %s 被移除（任务尚未开始执行）", issueNumber, removedTriggerLabel)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /root/ai-factory/ai-factory && go test ./factory/cmd/factory/server/ -run 'TestIsTriggerLabel|TestIsWaitingPhase|TestCancelCommentBody' -v
```

Expected: PASS。

- [ ] **Step 5: Add the unlabeled interception in issueWebhookHandler**

在 `server.go` 的 `issueWebhookHandler` 中，`ParseIssueWebhook` 成功之后、smoke 检查（`for _, label := range event.Labels`）之前插入：

```go
		// Handle cancellation: removing a trigger label (ai-factory-run / ai-factory-smoke)
		// from a waiting issue cancels its background task. Must intercept before the
		// create flow, which would ignore unlabeled events.
		if event.Action == "unlabeled" && isTriggerLabel(event.TriggerLabel) {
			handleIssueCancel(w, cmd, event)
			return
		}
```

- [ ] **Step 6: Write handleIssueCancel**

在 `server.go` 的 `issueWebhookHandler` 之后追加：

```go
// handleIssueCancel cancels a waiting FactoryTask when its trigger label is removed.
// The claim label value below relies on the task name being already DNS-safe:
// SandboxClaim labels use dnsLabel(task.Metadata.Name), which is a no-op on
// dnsName output, so the raw name is a valid label selector value.
func handleIssueCancel(w http.ResponseWriter, cmd *cobra.Command, event *taskpkg.IssueWebhookEvent) {
	ns := opts.Namespace
	if ns == "" {
		ns = "default"
	}
	name := taskpkg.FactoryTaskName(event.Provider, event.Repository, event.IssueNumber)
	phase := getFactoryTaskPhase(ns, name)
	if !isWaitingPhase(phase) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"ignored":true,"reason":"task not waiting (phase=%q)"}`+"\n", phase)
		fmt.Fprintf(cmd.ErrOrStderr(), "webhook: cancel ignored for %s issue #%d: task %s phase=%q\n",
			event.Provider, event.IssueNumber, name, phase)
		return
	}

	// Release any warm pool pod tied up by the waiting task's claim.
	_ = runKubectl(nil, "delete", "sandboxclaim", "-l", "factory.ai.gke.io/task="+name, "-n", ns, "--ignore-not-found")
	// Remove the task so the controller stops reconciling it.
	if err := runKubectl(nil, "delete", "factorytask", name, "-n", ns, "--ignore-not-found"); err != nil {
		http.Error(w, fmt.Sprintf("delete FactoryTask: %v", err), http.StatusInternalServerError)
		return
	}

	// Clean up GitHub labels and leave a record of the cancellation.
	if event.Provider == taskpkg.ProviderGitHub {
		gh := NewGitHubClient()
		if gh.HasToken() {
			_ = gh.SetTaskCancelled(context.Background(), event.Repository, event.IssueNumber, event.TriggerLabel)
		}
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "webhook: %s issue #%d cancelled: FactoryTask %s/%s removed (label %s removed)\n",
		event.Provider, event.IssueNumber, ns, name, event.TriggerLabel)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"cancelled":true,"task":"%s","namespace":"%s"}`+"\n", name, ns)
}
```

- [ ] **Step 7: Build and run all server tests**

```bash
cd /root/ai-factory/ai-factory && go build ./factory/cmd/factory/... && go test ./factory/cmd/factory/server/
```

Expected: BUILD OK + PASS（`server.go` 已 import `context`、`fmt`、`net/http`、`taskpkg`，无需新 import）。

- [ ] **Step 8: Commit**

```bash
cd /root/ai-factory/ai-factory && git add factory/cmd/factory/server/server.go factory/cmd/factory/server/cancel_test.go && git commit -m "feat: cancel waiting tasks when trigger label is removed"
```

---

### Task 4: controller 竞态防护（reportTaskResult + executeTask）

**Files:**
- Modify: `factory/cmd/factory/server/controller.go`

**Interfaces:**
- Consumes: `factoryTaskExists(namespace, name string) bool`（已存在，controller.go:674）、`namespaceForTask`（已存在）。
- Produces: 无新导出；修改 `reportTaskResult`（controller.go:456）与 `executeTask`（controller.go:219）行为。

- [ ] **Step 1: Guard reportTaskResult on task existence**

在 `reportTaskResult`（controller.go:456）函数体最开头插入：

```go
	// If the FactoryTask was deleted (e.g., cancelled by removing the trigger
	// label), skip all reporting — the cancellation path already cleaned up labels
	// and posted its own comment. Prevents a waiting goroutine whose claim was
	// deleted mid-flight from misreporting failure.
	if !factoryTaskExists(namespaceForTask(task), task.Metadata.Name) {
		return
	}
```

- [ ] **Step 2: Guard executeTask before entering Running**

在 `executeTask`（controller.go:219）中，`waitForSandboxClaimReady` 成功返回之后、`kubectlOutput("get", "sandboxclaim", ...)`（当前第 276 行）之前插入：

```go
	// Cancellation guard: the task may have been deleted (trigger label removed)
	// while we waited for the sandbox to become ready. Abort quietly instead of
	// running the plan — the cancellation path already cleaned up labels.
	if !factoryTaskExists(namespace, task.Metadata.Name) {
		fmt.Fprintf(out, "--- CANCELLED: task %s deleted while waiting for sandbox\n", task.Metadata.Name)
		return nil
	}
```

- [ ] **Step 3: Build and run all tests**

```bash
cd /root/ai-factory/ai-factory && go build ./... && go test ./factory/...
```

Expected: BUILD OK + PASS（两道防护均返回前置、不改变正常路径行为）。

- [ ] **Step 4: Commit**

```bash
cd /root/ai-factory/ai-factory && git add factory/cmd/factory/server/controller.go && git commit -m "feat: guard task reporting and start against mid-flight cancellation"
```

---

### Task 5: 全量验证

- [ ] **Step 1: Run the full test suite**

```bash
cd /root/ai-factory/ai-factory && go test ./... && go vet ./...
```

Expected: ALL PASS + vet clean。

- [ ] **Step 2: Manual smoke checklist（需集群，可选）**

验证 `handleIssueCancel` 的 kubectl 编排（单测不覆盖集群调用）：

1. 触发一个 issue（打 `ai-factory` + `ai-factory-run`），使任务进入 waiting（并发闸门满或等待 sandbox）。
2. 移除 `ai-factory-run` 标签。
3. 预期：`kubectl get factorytask -n ai-factory` 中该任务消失；`kubectl get sandboxclaim -n ai-factory` 中关联 claim 消失；issue 上 `ai-factory-running`/`ai-factory-waiting` 被移除；出现取消评论。
4. 反向验证：任务已进入 Running 后移除标签 → 任务继续跑完，不受影响。

- [ ] **Step 3: Update design doc status**

在 `docs/superpowers/specs/2026-08-12-cancel-waiting-task-by-label-removal-design.md` 的状态行追加 `（已实现，见 plans/2026-08-12-cancel-waiting-task-on-label-removal.md）`。

- [ ] **Step 4: Final commit**

```bash
cd /root/ai-factory/ai-factory && git add -A && git commit -m "docs: mark cancel-waiting-task design as implemented"
```