# CI Feedback Repair (Webhook Event-Driven) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a coding-agent PR's CI fails, ai-factory-server (driven by GitHub `check_suite`/`check_run` webhook events — no polling) fetches the failed job logs, runs a targeted repair inside the reused sandbox that inherits the main task's agent session, force-pushes the fix to the same PR, and only declares the task done once CI is green.

**Architecture:** Replace the reverted polling trigger with an event-driven waiter registry. `/webhook/github` routes `check_suite`/`check_run` events to a CI handler that resets a quiet-window timer on the matching waiter. When the quiet window (60s) elapses without new events, the server makes one check-runs API evaluation: green → success; red → fetch job logs from `details_url`, build a repair prompt embedding those logs + annotations, `kubectl exec` `BuildCIRepairScript` (agent inherits `AI_FACTORY_SESSION_FILE` dumped by the main run) into the reused sandbox, then force-push back. Hard `maxWait` timeout fails the task if no events ever arrive.

**Tech Stack:** Go (server: cobra server cmd, github client, kubernetes exec via kubectl), Python (coding-agent `ai-factory-agent.py` session dump/load), Helm chart + shell scripts (config wiring), GitHub REST API (check-runs, annotations, job logs).

## Global Constraints

- No polling of GitHub: the only `ListCheckRuns`/`PullRequestHeadSHA` calls happen after the quiet window elapses (one evaluation), never on a timer.
- Webhook `/webhook/github` already exists (`server.go:149`) and verifies `X-Hub-Signature-256` via `verifyWebhook`; reuse it, don't create a new endpoint.
- `kubectl exec` cannot attach env vars; repair-round config (e.g. max tool rounds) must be injected by `export` inside the generated script, not via exec flags.
- No new files may be added to target repos; the fix lives entirely server-side + in the coding-agent image.
- Python agent file convention: `OPENAI_*` config read from env via `agent_config.py`; `redact()` must mask `OPENAI_API_KEY`/`GITHUB_TOKEN`/`WEBHOOK_SECRET` before dumping session files.
- Sandbox repo is at `/workspace/repo`; session files live in `/tmp` (never inside the repo, so `commitChangesScript`'s `git add -A` cannot pick them up).
- `git commit` messages end with the `Co-Authored-By: Claude <noreply@anthropic.com>` trailer.
- Tests: `go test ./...` must stay green at each task end; Python agent has an existing `repair_prompt_test.py`/`agent_integration_test.py` harness (no `py_compile`, no bytecode artifacts).

---

### Task 1: Restore `CIFeedbackFailed` failure reason

**Files:**
- Modify: `factory/pkg/task/failure.go`

**Interfaces:**
- Produces: `taskpkg.CIFeedbackFailed FailureReason` — used by Tasks 6/7 to classify CI-watch failure.

- [ ] **Step 1: Read the existing failure-reason registry**

Read `factory/pkg/task/failure.go` to see how reasons are declared and where `FailureReasonList()` is.

- [ ] **Step 2: Add the constant after `RepairRoundsExhausted` + register it**

```go
	RepairRoundsExhausted FailureReason = "RepairRoundsExhausted"
	// CIFeedbackFailed means GitHub CI did not pass after the repair budget
	// was exhausted (or no CI events were observed before the wait deadline).
	CIFeedbackFailed FailureReason = "CIFeedbackFailed"
```

Find the `FailureReasonList()` function and append `CIFeedbackFailed` to the returned slice.

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 4: Commit**

```bash
git add factory/pkg/task/failure.go
git commit -m "feat: add CIFeedbackFailed failure reason

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Add `ActionsJobLogs` to `GitHubClient`

**Files:**
- Modify: `factory/cmd/factory/server/github.go`

**Interfaces:**
- Produces: `func (c *GitHubClient) ActionsJobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error)` — returns the raw job log bytes (ANSI intact); used by Task 3's `collectFailedJobLogs`.
- Consumes: existing `c.apiBase`, `c.client`, `c.setHeaders(req)` helpers already in `github.go`.

- [ ] **Step 1: Write the failing test in `factory/cmd/factory/server/github_test.go`**

Add to the existing test file:

```go
func TestActionsJobLogsRoundTrip(t *testing.T) {
	c := NewGitHubClient()
	if !c.HasToken() {
		t.Skip("no token; requires network to GitHub")
	}
	// PR #929 lint check-run 93732416644 belongs to a public repo; its job
	// logs are readable with a public_repo token.
	logs, err := c.ActionsJobLogs(context.Background(), "matrixhub-ai", "matrixhub", 93732416644)
	if err != nil {
		t.Fatalf("ActionsJobLogs: %v", err)
	}
	if !bytes.Contains(logs, []byte("generator_test.go")) {
		t.Fatalf("expected job log to mention generator_test.go, got %d bytes", len(logs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./factory/cmd/factory/server/ -run TestActionsJobLogsRoundTrip -v`
Expected: FAIL — `c.ActionsJobLogs undefined`.

- [ ] **Step 3: Implement `ActionsJobLogs`**

Find where `PullRequestHeadSHA` is in `ci.go`? No — it lives in the reverted `ci.go`; this repo's `github.go` has no CI methods yet. Add near the label methods in `github.go`:

```go
// ActionsJobLogs returns the full log of a GitHub Actions job. The CheckRuns
// API's details_url points at runs/{run}/job/{job}; job logs themselves are
// served from a 303 redirect to a short-lived signed blob URL which the
// default http client follows automatically.
func (c *GitHubClient) ActionsJobLogs(ctx context.Context, owner, repo string, jobID int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d/logs", c.apiBase, url.PathEscape(owner), url.PathEscape(repo), jobID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get actions job %d logs for %s/%s: %s", jobID, owner, repo, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
```

Make sure `net/url` and `io` are in the imports of `github.go` (add if missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./factory/cmd/factory/server/ -run TestActionsJobLogsRoundTrip -v`
Expected: PASS (logger confirms the log contains the generator_test.go error line). If the token lacks network access or job logs age out, skip is acceptable.

- [ ] **Step 5: Commit**

```bash
git add factory/cmd/factory/server/github.go factory/cmd/factory/server/github_test.go
git commit -m "feat: GitHubClient.ActionsJobLogs fetches full Actions job logs

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: Restore + enhance CI data model and evaluation (recreate `ci.go`)

**Files:**
- Create: `factory/cmd/factory/server/ci.go`
- Modify: `factory/cmd/factory/server/github.go` (may need nothing — CI helpers go in `ci.go`)

**Interfaces:**
- Produces (all used by later tasks):
  - `type CheckRun struct { ID int64; Name, Status, Conclusion string }`
  - `type CheckRunAnnotation struct { Path string; StartLine int; Level, Message string }`
  - `func parsePullRequestURL(rawURL string) (owner, repo string, number int, err error)`
  - `func (c *GitHubClient) PullRequestHeadSHA(ctx, owner, repo string, number int) (string, error)`
  - `func (c *GitHubClient) ListCheckRuns(ctx, owner, repo, sha string) ([]CheckRun, error)`
  - `func (c *GitHubClient) ListCheckRunAnnotations(ctx, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error)`
  - `type ciCheckStatus int` + consts `ciCheckPending/Green/Red/Error`
  - `func evaluateCheckRuns(runs []CheckRun) ciCheckStatus`
  - `func isNonFailingConclusion(conclusion string) bool`
  - `func formatCIFailures(annotations []CheckRunAnnotation) string`
  - `func summarizeCheckRuns(runs []CheckRun) string`
  - `func collectFailedAnnotations(ctx context.Context, gh ciClient, owner, repo, sha string) ([]CheckRunAnnotation, error)` (context-first, matching `collectFailedJobLogs`; the reverted version had no ctx — see Step 2)
  - `func collectFailedJobLogs(ctx context.Context, gh ciClient, owner, repo string, runs []CheckRun, snippetLines int) ([]JobLogSnippet, error)` (context-first, matching Task 6's call site)
  - `type JobLogSnippet struct { CheckRunName string; Path string; Lines []string }`
  - `func buildCIRepairInstructions(originalInstructions, prURL string, annotations []CheckRunAnnotation, logSnippets []JobLogSnippet, allowTestChanges bool, snippetLines int) string`
  - `type ciClient interface { PullRequestHeadSHA(ctx, owner, repo, number string); ListCheckRuns(ctx, owner, repo, sha); ListCheckRunAnnotations(ctx, owner, repo, checkRunID); ActionsJobLogs(ctx, owner, repo, jobID); }` — defined here so Task 6 functions and the webhook tests can depend on it (methods reference the concrete `*GitHubClient` from Task 2).

- [ ] **Step 1: Recreate the base file from the reverted commit**

The full set of GitHub-CI methods + evaluation + parse logic existed in `git show 30b5590:factory/cmd/factory/server/ci.go`. Restore that file as `ci.go` (check-run structs, parse, client methods, evaluate, format, summarize). Use the version’s `PollRequest`-free form — the reverted `ci.go` had `PullRequestHeadSHA`, `ListCheckRuns`, `ListCheckRunAnnotations`, `evaluateCheckRuns`, `isNonFailingConclusion`, `formatCIFailures`, `summarizeCheckRuns`. If `git show` output is unavailable, rewrite by hand per the interfaces above.

- [ ] **Step 2: Extend `collectFailedAnnotations` with a context + error signature**

```go
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
```

- [ ] **Step 3: Implement `collectFailedJobLogs` — parse details_url → job id → fetch+clean+snip**

The event/check-run shape that reaches us: each failed `CheckRun` object returned by `ListCheckRuns` carries `details_url` (e.g. `https://github.com/o/r/actions/runs/{run}/job/{job}`). Add a `CheckRun.DetailsURL string \`json:"details_url"\`` field to the struct in Step 1 and have `ListCheckRuns` decode it. Then:

```go
// CheckRun (Step 1) gains: DetailsURL string `json:"details_url"`
func jobIDFromDetailsURL(detailsURL string) (int64, bool) {
	// e.g. https://github.com/o/r/actions/runs/31476849873/job/93732416644
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
```

`collectFailedJobLogs` behavior: for each failing completed run with a resolvable `DetailsURL` job id, `gh.ActionsJobLogs(ctx, owner, repo, jobID)`; strip ANSI (`regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")`), then find the first line matching `##[error]|FAIL|Error:|error:` and keep a window of `snippetLines` lines around it (default 20). If the log fetch fails or the window is empty, append `lines: nil` for that run (caller degrades to annotations). Return `[]JobLogSnippet` with one entry per failing run.

- [ ] **Step 4: Implement the enhanced `buildCIRepairInstructions`**

Key changes vs the reverted version: embed job-log snippets (not just annotations), add the allow-test-changes toggle, and add strict no-exploration constraints.

```go
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
```

- [ ] **Step 5: Add unit tests for the pure helpers**

In `factory/cmd/factory/server/ci_test.go`: `TestJobIDFromDetailsURL` (valid run/job URL → id), `TestBuildCIRepairInstructionsEmptyAnnotations` (no annotations/logs → still builds a prompt), `TestBuildCIRepairInstructionsIncludesLogSnippet` (snippet text appears verbatim). Write tests first, watch them fail on the missing functions, then implement, then pass.

- [ ] **Step 6: Run tests**

Run: `go test ./factory/cmd/factory/server/ -run 'TestJobIDFromDetailsURL|TestBuildCIRepairInstructions' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add factory/cmd/factory/server/ci.go factory/cmd/factory/server/ci_test.go
git commit -m "feat: recreate CI data model with job-log snippets and strict repair prompt

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: Agent session dump/load (`AI_FACTORY_SESSION_FILE`)

**Files:**
- Modify: `components/agent-sandbox-images/coding-agent/ai-factory-agent.py`
- Test: `components/agent-sandbox-images/coding-agent/agent_session_roundtrip_test.py` (new)

**Interfaces:**
- Produces: env var `AI_FACTORY_SESSION_FILE` — when set: (a) at startup, if the file exists, it is parsed as a JSON array of messages and used as the initial `messages` list; (b) at process exit (success or failure), the final `messages` are `redact()`-ed and dumped back as JSON. Not setting it must not change behavior (backward compatible).
- Consumes: existing `messages` list, `redact()` helper, `os.environ`.

- [ ] **Step 1: Write the failing test**

Create `agent_session_roundtrip_test.py` mirroring the existing test conventions in `agent_integration_test.py` (subprocess-based; set `AI_FACTORY_SESSION_FILE` to a temp path, feed a trivial prompt, assert the file after exit is non-empty JSON with a trailing user message; and that loading that file on a second run works). Look at `agent_integration_test.py` for the harness pattern first.

- [ ] **Step 2: Run test to verify it fails**

Run: `python3 components/agent-sandbox-images/coding-agent/agent_session_roundtrip_test.py`
Expected: FAIL (session file logic absent).

- [ ] **Step 3: Implement session load**

Right after `prompt = prompt_handle.read()` (line ~290):

```python
import json

session_file = os.environ.get("AI_FACTORY_SESSION_FILE", "").strip()
session_messages = []
if session_file:
    try:
        with open(session_file, "r", encoding="utf-8") as session_handle:
            loaded = json.load(session_handle)
        if isinstance(loaded, list) and loaded and isinstance(loaded[0], dict):
            session_messages = loaded
    except (OSError, ValueError, TypeError):
        # Missing/corrupt session file: proceed with a fresh conversation.
        session_messages = []
```

Then where `messages = [` is built (`messages` list after system_prompt + user_content, line ~326), prepend `session_messages` if non-empty:

```python
messages = list(session_messages)
if not messages:
    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_content},
    ]
elif messages and messages[0].get("role") != "system":
    messages.insert(0, {"role": "system", "content": system_prompt})
```

Wait — the session dump should contain the system prompt too, so loading is lossless. Decide: when dumping, save the *entire* `messages` list including system/user. When loading, only prepend `system_prompt` if the loaded head role != system. Either way, the repair instructions must still be pushed: the repair prompt is appended as a further user message after load (Task 5 wires that via `runAgentScript`'s instructions).

- [ ] **Step 4: Implement session dump**

Before `sys.exit(...)` final calls, add a `persist_session()` helper:

```python
def persist_session():
    if not session_file:
        return
    try:
        with open(session_file, "w", encoding="utf-8") as session_handle:
            json.dump(redact(json.dumps(messages, ensure_ascii=False, sort_keys=True)), session_handle, ensure_ascii=False)
    except (OSError, TypeError):
        pass
```

Verify what `redact` accepts: it takes a string and returns a string (`redact(str)` in `dump_response_diagnostics`). Then adjust — better to redact the whole JSON text:

```python
    try:
        raw = json.dumps(messages, ensure_ascii=False, sort_keys=True)
        with open(session_file, "w", encoding="utf-8") as session_handle:
            session_handle.write(redact(raw))
    except (OSError, TypeError):
        pass
```

Call `persist_session()` immediately before every `sys.exit(...)` and before the final `sys.exit(completed.returncode)`. Use a `try/finally` around the whole agent body that calls it once (cleanest — wrap after `script = ""` setup: `try: ... finally: persist_session()`).

- [ ] **Step 5: Run test to verify it passes**

Run: `python3 components/agent-sandbox-images/coding-agent/agent_session_roundtrip_test.py`
Expected: PASS.

- [ ] **Step 6: Run the whole agent test suite**

Run: `python3 components/agent-sandbox-images/coding-agent/agent_integration_test.py && python3 components/agent-sandbox-images/coding-agent/repair_loop_test.py`
Expected: PASS — backward compatibility holds when `AI_FACTORY_SESSION_FILE` unset.

- [ ] **Step 7: Commit**

```bash
git add components/agent-sandbox-images/coding-agent/ai-factory-agent.py components/agent-sandbox-images/coding-agent/agent_session_roundtrip_test.py
git commit -m "feat: coding-agent session dump/load via AI_FACTORY_SESSION_FILE

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: Restore `BuildCIRepairScript` (restore from revert, wire session + tool-round override)

**Files:**
- Modify: `factory/pkg/task/plan.go`

**Interfaces:**
- Produces: `func BuildCIRepairScript(task *FactoryTask, repairInstructions string, opts CIRepairOptions) (string, error)` (note: the reverted version had no `opts`; we add it) where
  ```go
  type CIRepairOptions struct {
      SessionFile    string // /tmp/ai-factory-session.json; "" = no session
      MaxToolRounds  int    // override OPENAI_MAX_TOOL_ROUNDS in the repair agent; <=0 = inherit
  }
  ```
- Consumes: `runAgentScript`, `commitChangesScript`, `pushChangeBranchScript`, `changeRequestDefaults` (all already in `plan.go`).
- Produced for Task 7: a script string with a `set -eu` header that sets `export AI_FACTORY_SESSION_FILE` (if non-empty) and `export OPENAI_MAX_TOOL_ROUNDS` (if >0) before running the agent, then commits, then force-pushes to the change branch.

- [ ] **Step 1: Restore the base `BuildCIRepairScript`**

Revert-the-revert: take the `BuildCIRepairScript` from `git show 30b5590:factory/pkg/task/plan.go` (lines ~400-430) — the version with `workDir := "/workspace/repo"`, agent command default, and the `set -eu` + three-part script. Port it into `plan.go` (after `pushChangeBranchScript`).

- [ ] **Step 2: Extend the signature with `opts CIRepairOptions`**

Replace the plain `func BuildCIRepairScript(task *FactoryTask, repairInstructions string) (string, error)` with the options-carrying version. Add the `CIRepairOptions` type near it.

- [ ] **Step 3: Inject session + tool-round env into the script head**

```go
envSetup := ""
if opts.SessionFile != "" {
	envSetup += fmt.Sprintf("export AI_FACTORY_SESSION_FILE=%s\n", shellQuote(opts.SessionFile))
}
if opts.MaxToolRounds > 0 {
	envSetup += fmt.Sprintf("export OPENAI_MAX_TOOL_ROUNDS=%d\n", opts.MaxToolRounds)
}
script := fmt.Sprintf("set -eu\n%s%s\n%s\n%s",
	envSetup,
	runAgentScript(workDir, repairInstructions, task.Spec.Agent.PromptRef, agentCommand),
	commitChangesScript(workDir, commitMessage, authorName, authorEmail),
	pushChangeBranchScript(workDir, remoteName, changeBranch, task.Spec.Source.BaseRef),
)
```

- [ ] **Step 4: Unit test the env prefix only**

In `factory/pkg/task/plan_test.go` (check if a plan_test.go exists; if not create it) add `TestBuildCIRepairScriptEnvInjection`: call with a task fixture and `CIRepairOptions{SessionFile: "/tmp/s.json", MaxToolRounds: 3}`; assert the returned string contains `export AI_FACTORY_SESSION_FILE=` and `export OPENAI_MAX_TOOL_ROUNDS=3` and, with `CIRepairOptions{}`, does not.

- [ ] **Step 5: Build + test**

Run: `go build ./... && go test ./factory/pkg/task/ -run TestBuildCIRepairScriptEnvInjection -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add factory/pkg/task/plan.go factory/pkg/task/plan_test.go
git commit -m "feat: BuildCIRepairScript with session + repair tool-round env injection

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: Waiter registry + event-driven watch loop (controller.go)

**Files:**
- Modify: `factory/cmd/factory/server/controller.go`

**Interfaces:**
- Produces:
  - `type ciClient interface { PullRequestHeadSHA(...); ListCheckRuns(...); ListCheckRunAnnotations(...); ActionsJobLogs(...) }` (defined in `ci.go` alongside the data model — Task 6 relies on it)
  - `type ciRepairRunner func(annotations []CheckRunAnnotation, logSnippets []JobLogSnippet) error` (changed from the reverted `func(annotations []CheckRunAnnotation) error` — carries job logs)
  - `type ciWatchOptions struct { maxRetries int; maxWait, settleInterval time.Duration; maxToolRounds int; allowTestChanges bool; logSnippetLines int }` (note: no `pollInterval`)
  - `func resolveCIWatchOptions() ciWatchOptions` — reads CI_WATCH_* via ReadConfig like the reverted one, minus retry interval, plus the two new keys.
  - `type ciWaiter struct { notify chan struct{}; once sync.Once }`
  - `var ciRegistry = struct { sync.Mutex; m map[string]*ciWaiter }{}` keyed by `"owner/repo/branch"`
  - `func registerWaiter(key string) *ciWaiter` / `func (w *ciWaiter) unregister(key string)`
  - `func notifyWaiter(key string)` — non-blocking send.
  - `func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string)` — event-driven loop.
  - consts `ciWatchGreen/ciWatchFailed`
  - `func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string, opts ciWatchOptions) ciRepairRunner`
- Consumes: Task 3's `ci.go` helpers (including the `ciClient` interface); Task 5's `taskpkg.BuildCIRepairScript`; `taskExists`/`namespaceForTask`/`changeRequestBranches` vars already in controller.go.

- [ ] **Step 1: Confirm the `ciClient` interface from Task 3 covers `ActionsJobLogs`**

The `ciClient` interface was defined in Task 3's `ci.go` with five methods (PullRequestHeadSHA, ListCheckRuns, ListCheckRunAnnotations, ActionsJobLogs). Verify it exists before continuing; the concrete `*GitHubClient` satisfies it (Task 2). If the implementer of Task 3 named a method differently, reconcile here before proceeding.

- [ ] **Step 2: Implement the waiter registry**

```go
// ciWaiter lets a webhook handler signal a blocked watch loop.
type ciWaiter struct {
	notify chan struct{}
}

var (
	ciRegistryMu sync.Mutex
	ciRegistry   = map[string]*ciWaiter{}
)

func registerWaiter(key string) *ciWaiter {
	ciRegistryMu.Lock()
	defer ciRegistryMu.Unlock()
	w := &ciWaiter{notify: make(chan struct{}, 1)}
	ciRegistry[key] = w
	return w
}

func unregisterWaiter(key string) {
	ciRegistryMu.Lock()
	defer ciRegistryMu.Unlock()
	delete(ciRegistry, key)
}

// notifyWaiter wakes the waiter for key without blocking.
func notifyWaiter(key string) {
	ciRegistryMu.Lock()
	w := ciRegistry[key]
	ciRegistryMu.Unlock()
	if w == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}
```

- [ ] **Step 3: Implement the event-driven `watchAndRepairCI`**

```go
func watchAndRepairCI(out io.Writer, task *taskpkg.FactoryTask, prURL string, gh ciClient, repair ciRepairRunner, opts ciWatchOptions) (ciWatchOutcome, string) {
	owner, repo, number, err := parsePullRequestURL(prURL)
	if err != nil {
		return ciWatchFailed, fmt.Sprintf("parse PR URL %q: %v", prURL, err)
	}
	// branch name is deterministic and force-push-stable; matches webhook head_branch.
	changeBranch, _ := changeRequestBranches(task)
	key := fmt.Sprintf("%s/%s/%s", owner, repo, changeBranch)
	w := registerWaiter(key)
	defer unregisterWaiter(key)
	ctx := context.Background()
	deadline := time.Now().Add(opts.maxWait)
	var lastSummary string
	for attempt := 0; attempt < opts.maxRetries; attempt++ {
		fmt.Fprintf(out, "--- CI watch attempt %d/%d waiting for events on %s\n", attempt+1, opts.maxRetries, key)
		status, summary := waitForCIEvent(ctx, task, gh, owner, repo, number, w, opts, deadline)
		lastSummary = summary
		switch status {
		case ciCheckGreen:
			fmt.Fprintf(out, "--- CI GREEN\n")
			return ciWatchGreen, summary
		case ciCheckRed:
			headSHA, shaErr := gh.PullRequestHeadSHA(ctx, owner, repo, number)
			if shaErr != nil {
				return ciWatchFailed, fmt.Sprintf("get PR head sha: %v", shaErr)
			}
			annotations, annErr := collectFailedAnnotations(ctx, gh, owner, repo, headSHA)
			if annErr != nil {
				return ciWatchFailed, fmt.Sprintf("collect annotations: %v", annErr)
			}
			runs, _ := gh.ListCheckRuns(ctx, owner, repo, headSHA)
			logSnippets, _ := collectFailedJobLogs(ctx, gh, owner, repo, runs, opts.logSnippetLines)
			fmt.Fprintf(out, "--- CI FAILED (attempt %d/%d); repairing\n%s", attempt+1, opts.maxRetries, formatCIFailures(annotations))
			if err := repair(annotations, logSnippets); err != nil {
				return ciWatchFailed, fmt.Sprintf("repair failed: %v", err)
			}
		case ciCheckError:
			return ciWatchFailed, fmt.Sprintf("check-runs API error: %s", summary)
		default: // ciCheckPending with deadline hit
			return ciWatchFailed, fmt.Sprintf("CI still pending after %s", opts.maxWait)
		}
	}
	return ciWatchFailed, fmt.Sprintf("CI still failing after %d repair attempts:\n%s", opts.maxRetries, lastSummary)
}
```

Notes: `latestHeadSHA` is a small helper calling `gh.PullRequestHeadSHA`. `collectFailedAnnotations`/`collectFailedJobLogs` re-list runs on the *current* PR head so the force-pushed head is always what gets evaluated (matching the reverted design where each attempt re-fetches head SHA).

- [ ] **Step 4: Implement `waitForCIEvent` (quiet-window loop)**

```go
func waitForCIEvent(ctx context.Context, task *taskpkg.FactoryTask, gh ciClient, owner, repo string, number int, w *ciWaiter, opts ciWatchOptions, deadline time.Time) (ciCheckStatus, string) {
	var settle *time.Timer
	var settleC <-chan time.Time
	sha, err := gh.PullRequestHeadSHA(ctx, owner, repo, number)
	if err != nil {
		return ciCheckError, fmt.Sprintf("get PR head sha: %v", err)
	}
	for {
		// evaluate immediately once — the CI may already be done before we registered
		runs, err := gh.ListCheckRuns(ctx, owner, repo, sha)
		if err != nil {
			return ciCheckError, fmt.Sprintf("list check runs: %v", err)
		}
		if status := evaluateCheckRuns(runs); status == ciCheckRed {
			return ciCheckRed, summarizeCheckRuns(runs)
		}
		select {
		case <-w.notify:
			// an event arrived: restart the quiet window unless already settled
			if settleC == nil {
				settle = time.NewTimer(opts.settleInterval)
				settleC = settle.C
			} else {
				if !settle.Stop() {
					select {
					case <-settle.C:
					default:
					}
				}
				settle.Reset(opts.settleInterval)
			}
		case <-settleC:
			// quiet window elapsed with no new events: evaluate once
			// re-fetch head in case a repair force-push happened between attempts
			refreshed, err := gh.PullRequestHeadSHA(ctx, owner, repo, number)
			if err != nil {
				return ciCheckError, fmt.Sprintf("refresh PR head sha: %v", err)
			}
			runs, err := gh.ListCheckRuns(ctx, owner, repo, refreshed)
			if err != nil {
				return ciCheckError, fmt.Sprintf("list check runs: %v", err)
			}
			if !taskExists(namespaceForTask(task), task.Metadata.Name) {
				return ciCheckPending, "task cancelled while waiting for CI"
			}
			return evaluateCheckRuns(runs), summarizeCheckRuns(runs)
		case <-time.After(time.Until(deadline)):
			return ciCheckPending, fmt.Sprintf("CI events not observed before %s", opts.maxWait)
		}
	}
}
```

`waitForCIEvent` takes `task` so the event-driven loop can detect cancellation (task deleted → `taskExists` false) at the observation point without adding any extra polling.

- [ ] **Step 5: Implement `resolveCIWatchOptions` (no poll interval)**

```go
func resolveCIWatchOptions() ciWatchOptions {
	o := ciWatchOptions{
		maxRetries:       opts.CIWatchMaxRetries,
		maxWait:          opts.CIWatchMaxWait,
		settleInterval:   opts.CIWatchSettleInterval,
		allowTestChanges: true, // PR #929 classes of failures need test edits
		logSnippetLines:  20,
		maxToolRounds:    3, // inherited session makes full re-exploration wasteful
	}
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_RETRIES"); v != "" { if n, err := strconv.Atoi(v); err == nil && n > 0 { o.maxRetries = n } }
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_WAIT"); v != "" { if d, err := time.ParseDuration(v); err == nil && d > 0 { o.maxWait = d } }
	if v := taskpkg.ReadConfig("CI_WATCH_SETTLE_INTERVAL"); v != "" { if d, err := time.ParseDuration(v); err == nil && d >= 0 { o.settleInterval = d } }
	if v := taskpkg.ReadConfig("CI_WATCH_MAX_TOOL_ROUNDS"); v != "" { if n, err := strconv.Atoi(v); err == nil && n > 0 { o.maxToolRounds = n } }
	if v := taskpkg.ReadConfig("CI_WATCH_LOG_SNIPPET_LINES"); v != "" { if n, err := strconv.Atoi(v); err == nil && n > 0 { o.logSnippetLines = n } }
	return o
}
```

- [ ] **Step 6: Implement `ciRepairRunnerFor` (restore, wired to opts)**

```go
func ciRepairRunnerFor(task *taskpkg.FactoryTask, namespace, sandboxName, prURL string, opts ciWatchOptions) ciRepairRunner {
	containerName := task.Spec.Sandbox.ContainerName
	if containerName == "" {
		containerName = "dev"
	}
	return func(annotations []CheckRunAnnotation, logSnippets []JobLogSnippet) error {
		instructions := buildCIRepairInstructions(task.Spec.Work.Instructions, prURL, annotations, logSnippets, opts.allowTestChanges, opts.logSnippetLines)
		script, err := taskpkg.BuildCIRepairScript(task, instructions, taskpkg.CIRepairOptions{
			SessionFile:   "/tmp/ai-factory-session.json",
			MaxToolRounds: opts.maxToolRounds,
		})
		if err != nil {
			return err
		}
		return runKubectl(nil, "exec", "-n", namespace, sandboxName, "-c", containerName, "--", "/bin/sh", "-lc", script)
	}
}
```

- [ ] **Step 7: Unit test the registry**

In `controller_test.go` (or a new `ci_watch_test.go`): test `registerWaiter` + `notifyWaiter` powers a `waitForCIEvent` return. Provide a fake `ciClient` implementing the full interface. Simulate: register, notify, assert the green path returns after one evaluation on a fake checkout. Keep it minimal — a single test that `notifyWaiter` wakes the `select`.

- [ ] **Step 8: Build + test**

Run: `go build ./... && go test ./factory/cmd/factory/server/ -run TestRegisterAndNotifyWaiter -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add factory/cmd/factory/server/controller.go factory/cmd/factory/server/ci.go factory/cmd/factory/server/ci_watch_test.go
git commit -m "feat: webhook-driven CI watch loop with quiet-window evaluation

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: Webhook event routing + executeTask wiring + server options

**Files:**
- Modify: `factory/cmd/factory/server/server.go`
- Modify: `factory/cmd/factory/server/controller.go` (executeTask CI section)

**Interfaces:**
- Consumes: Task 3 `ci.go` types (CheckRun/CheckRunAnnotation), Task 6 `watchAndRepairCI`, `resolveCIWatchOptions`, `ciRepairRunnerFor`, `ciCheckStatus`, `ciWatchGreen/ciWatchFailed`, `notifyWaiter`, `registerWaiter`.
- Produces:
  - `func ciWebhookHandler(cmd *cobra.Command) http.HandlerFunc` in `server.go` — parses `X-GitHub-Event`, validates signature, extracts repo+branch from payload, calls `notifyWaiter`.
  - `func parseCheckSuiteEvent(body []byte) (owner, repo, branch, headSHA string, err error)`
  - `func parseCheckRunEvent(body []byte) (owner, repo, branch, headSHA string, err error)`
  - server `Options` gains: `CIWatchEnabled bool`, `CIWatchMaxRetries int`, `CIWatchMaxWait time.Duration`, `CIWatchSettleInterval time.Duration`, plus flags `--ci-watch*`.
  - executeTask wiring in `controller.go` (restore from revert, adjust signatures).

- [ ] **Step 1: Add `Options` fields + flags in `server.go`**

```go
	CIWatchEnabled        bool
	CIWatchMaxRetries     int
	CIWatchMaxWait        time.Duration
	CIWatchSettleInterval time.Duration
```

Flags (reuse the reverted flag block, minus `--ci-watch-interval`; add no new flag — tool-rounds and snippet-lines ride configmap/env only):

```go
	Cmd.Flags().BoolVar(&opts.CIWatchEnabled, "ci-watch", true, "wait for GitHub CI on created PRs and repair failures (CI_WATCH_ENABLED overrides)")
	Cmd.Flags().IntVar(&opts.CIWatchMaxRetries, "ci-watch-max-retries", 3, "max CI repair cycles before failing the task (CI_WATCH_MAX_RETRIES overrides)")
	Cmd.Flags().DurationVar(&opts.CIWatchMaxWait, "ci-watch-max-wait", 30*time.Minute, "max time to wait for CI per cycle (CI_WATCH_MAX_WAIT overrides)")
	Cmd.Flags().DurationVar(&opts.CIWatchSettleInterval, "ci-watch-settle-interval", 60*time.Second, "quiet window to wait for late-registering check runs after an event (CI_WATCH_SETTLE_INTERVAL overrides)")
```

- [ ] **Step 2: Route `/webhook/github` by event type**

In `startWebhookServer` (`server.go`), change the github route from `issueWebhookHandler(cmd, ...)` to a dispatcher:

```go
mux.HandleFunc("/webhook/github", func(w http.ResponseWriter, r *http.Request) {
	eventType := r.Header.Get("X-GitHub-Event")
	switch eventType {
	case "check_suite", "check_run":
		ciWebhookHandler(cmd)(w, r)
	default:
		issueWebhookHandler(cmd, taskpkg.ProviderGitHub)(w, r)
	}
})
```

- [ ] **Step 3: Implement the CI webhook handler + payload parsers**

```go
func ciWebhookHandler(cmd *cobra.Command) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := readBody(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := verifyWebhook(taskpkg.ProviderGitHub, body, req); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		eventType := req.Header.Get("X-GitHub-Event")
		var owner, repo, branch string
		if eventType == "check_suite" {
			owner, repo, branch, _, err = parseCheckSuiteEvent(body)
		} else {
			owner, repo, branch, _, err = parseCheckRunEvent(body)
		}
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "ci webhook: parse %s event: %v\n", eventType, err)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "ci webhook: %s event for %s/%s@%s\n", eventType, owner, repo, branch)
		key := fmt.Sprintf("%s/%s/%s", owner, repo, branch)
		notifyWaiter(key)
		w.WriteHeader(http.StatusNoContent)
	}
}
```

Parsers (only `action` values from the GitHub event matter; ignore the rest):

```go
// check_suite event: {"action":"completed","check_suite":{"head_branch":"...","head_sha":"..."},"repository":{"full_name":"o/r"}}
func parseCheckSuiteEvent(body []byte) (owner, repo, branch, headSHA string, err error) {
	var ev struct {
		Action     string `json:"action"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
		} `json:"check_suite"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", "", "", "", err
	}
	// Only completed suites produce a meaningful signal.
	if ev.Action != "completed" {
		return "", "", "", "", fmt.Errorf("ignoring action %q", ev.Action)
	}
	parts := strings.SplitN(ev.Repository.FullName, "/", 2)
	if len(parts) != 2 {
		return "", "", "", "", fmt.Errorf("repository.full_name %q", ev.Repository.FullName)
	}
	return parts[0], parts[1], ev.CheckSuite.HeadBranch, ev.CheckSuite.HeadSHA, nil
}

// check_run event: {"action":"completed","check_run":{"head_sha":"...","status":"completed"},"check_suite":{"head_branch":"..."},"repository":{"full_name":"o/r"}}
func parseCheckRunEvent(body []byte) (owner, repo, branch, headSHA string, err error) {
	var ev struct {
		Action   string `json:"action"`
		CheckRun struct {
			HeadSHA string `json:"head_sha"`
			Status  string `json:"status"`
		} `json:"check_run"`
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return "", "", "", "", err
	}
	// Only completed runs are a signal; in-progress/queued ones just restart the
	// quiet window, which is exactly what we want — but a completed run is the
	// terminal state. Accept any action (each event restarts the window).
	parts := strings.SplitN(ev.Repository.FullName, "/", 2)
	if len(parts) != 2 {
		return "", "", "", "", fmt.Errorf("repository.full_name %q", ev.Repository.FullName)
	}
	return parts[0], parts[1], ev.CheckSuite.HeadBranch, ev.CheckRun.HeadSHA, nil
}
```

Note: `notifyWaiter` restarts the window for *any* event action; the red/green decision is made at window expiry via `evaluateCheckRuns`. If `branch == ""` (some events omit head_branch), fall back to `headSHA`-keyed notify? Keep it simple: if branch empty, skip notify and log (fork edge case documented in spec).

- [ ] **Step 4: Wire `executeTask` in `controller.go`**

Restore the CI section (from the reverted `controller.go`), adapted to the new signatures, placed right after `validateChangeRequestResult` succeeds (before the success status patch):

```go
	if resultURL != "" && reportCIWatchEnabled() && task.Spec.Source.Provider == taskpkg.ProviderGitHub {
		fmt.Fprintf(out, "--- CI gathering check results for %s\n", resultURL)
		watchOpts := resolveCIWatchOptions()
		outcome, summary := watchAndRepairCI(out, task, resultURL, NewGitHubClient(), ciRepairRunnerFor(task, namespace, sandboxName, resultURL, watchOpts), watchOpts)
		if outcome != ciWatchGreen {
			failure := taskpkg.FailureClassification{
				Reason:     taskpkg.CIFeedbackFailed,
				Friendly:   "GitHub CI did not pass after repair attempts",
				RawMessage: summary,
			}
			_ = patchTaskStatus(namespace, task.Metadata.Name, taskpkg.StatusPatchOptions{
				Phase:            taskpkg.PhaseFailed,
				Reason:           "CIFeedbackFailed",
				Message:          "GitHub CI failed after repair attempts: " + summary,
				SandboxClaimName: claim,
				SandboxName:      sandboxName,
				FailureReason:    failure,
			})
			reportTaskResult(out, task, taskpkg.PhaseFailed, "GitHub CI failed after repair attempts: "+summary)
			return fmt.Errorf("ci feedback failed: %s", summary)
		}
	}
```

Also restore `reportCIWatchEnabled()` helper into `controller.go` (exactly as reverted:

```go
func reportCIWatchEnabled() bool {
	if v := taskpkg.ReadConfig("CI_WATCH_ENABLED"); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return opts.CIWatchEnabled
}
```

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 6: Run full Go test suite**

Run: `go test ./...`
Expected: PASS (some network tests may skip when no token/network).

- [ ] **Step 7: Commit**

```bash
git add factory/cmd/factory/server/server.go factory/cmd/factory/server/controller.go factory/cmd/factory/server/ci.go
git commit -m "feat: route check_suite/check_run webhooks to CI watch and wire executeTask

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 8: Config wiring (chart, values, scripts, env)

**Files:**
- Modify: `charts/ai-factory/templates/configmap.yaml`
- Modify: `charts/ai-factory/values.yaml`
- Modify: `scripts/update-config.sh`
- Modify: `scripts/upgrade.sh`
- Modify: `scripts/ai-factory.env` (gitignored; local dev)

**Interfaces:**
- Consumes: Task 6 `resolveCIWatchOptions` reads these keys.
- Produces: config keys `CI_WATCH_ENABLED`, `CI_WATCH_MAX_RETRIES`, `CI_WATCH_MAX_WAIT`, `CI_WATCH_SETTLE_INTERVAL`, `CI_WATCH_MAX_TOOL_ROUNDS`, `CI_WATCH_LOG_SNIPPET_LINES` (no `CI_WATCH_RETRY_INTERVAL`).

- [ ] **Step 1: ConfigMap**

Re-add to `charts/ai-factory/templates/configmap.yaml` (from the revert diff, replacing the removed block, minus `CI_WATCH_RETRY_INTERVAL`, plus the two new keys):

```yaml
  CI_WATCH_ENABLED: {{ .Values.server.ciWatchEnabled | quote }}
  CI_WATCH_MAX_RETRIES: {{ .Values.server.ciWatchMaxRetries | quote }}
  CI_WATCH_MAX_WAIT: {{ .Values.server.ciWatchMaxWait | quote }}
  CI_WATCH_SETTLE_INTERVAL: {{ .Values.server.ciWatchSettleInterval | quote }}
  CI_WATCH_MAX_TOOL_ROUNDS: {{ .Values.server.ciWatchMaxToolRounds | quote }}
  CI_WATCH_LOG_SNIPPET_LINES: {{ .Values.server.ciWatchLogSnippetLines | quote }}
```

- [ ] **Step 2: values.yaml**

Under `server:`, restore ciWatch block (from the revert diff) minus `pollInterval`, plus two:

```yaml
  # CI feedback loop: after a PR is created, watch GitHub CI webhook events
  # and repair failures in the reused sandbox. No polling.
  ciWatchEnabled: true
  ciWatchMaxRetries: 3
  ciWatchMaxWait: 30m
  ciWatchSettleInterval: 60s
  ciWatchMaxToolRounds: 3
  ciWatchLogSnippetLines: 20
```

- [ ] **Step 3: `scripts/update-config.sh`**

Restore the CI_WATCH_* keys in the `CONFIG_KEYS` array (from the revert diff) minus `CI_WATCH_RETRY_INTERVAL`, plus:

```bash
  "CI_WATCH_ENABLED" "CI_WATCH_MAX_RETRIES" "CI_WATCH_MAX_WAIT" "CI_WATCH_SETTLE_INTERVAL" "CI_WATCH_MAX_TOOL_ROUNDS" "CI_WATCH_LOG_SNIPPET_LINES"
```

- [ ] **Step 4: `scripts/upgrade.sh`**

Restore the `--set server.ciWatch*` lines (from the revert diff, minus `--set server.ciWatchPollInterval`), plus:

```bash
[ -n "${CI_WATCH_MAX_TOOL_ROUNDS:-}" ] && HELM_ARGS+=(--set server.ciWatchMaxToolRounds="${CI_WATCH_MAX_TOOL_ROUNDS}")
[ -n "${CI_WATCH_LOG_SNIPPET_LINES:-}" ] && HELM_ARGS+=(--set server.ciWatchLogSnippetLines="${CI_WATCH_LOG_SNIPPET_LINES}")
```

- [ ] **Step 5: `scripts/ai-factory.env`**

Add the six keys (gitignored, so commit not needed, but keep consistent):

```bash
# CI feedback loop: PR 创建后订阅 GitHub check_suite/check_run 事件,失败时读日志并用原 sandbox 修复.
# 纯事件驱动,服务端不轮询 GitHub.
CI_WATCH_ENABLED=true
CI_WATCH_MAX_RETRIES=3
CI_WATCH_MAX_WAIT=30m
CI_WATCH_SETTLE_INTERVAL=60s
CI_WATCH_MAX_TOOL_ROUNDS=3
CI_WATCH_LOG_SNIPPET_LINES=20
```

- [ ] **Step 6: Verify chart renders**

Run: `helm template charts/ai-factory >/dev/null`
Expected: exits 0 (no chart errors).

- [ ] **Step 7: Commit**

```bash
git add charts/ai-factory/templates/configmap.yaml charts/ai-factory/values.yaml scripts/update-config.sh scripts/upgrade.sh
git commit -m "chore: wire CI watch event config through helm chart and update scripts

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 9: Deploy, enable webhook events, and end-to-end validation

**Files:**
- No code files. Runbook.

**Interfaces:**
- Consumes: everything from Tasks 1-8.

- [ ] **Step 1: Build & package**

Run: `./scripts/package.sh` from repo root (per project convention — if the script name differs, use the documented build step in `docs/general/guide.md`). Expected: images built into `dist/`.

- [ ] **Step 2: Upgrade the cluster**

Run: `./scripts/upgrade.sh`
Expected: server + controller restart with new image; rollout status green.

- [ ] **Step 3: Enable webhook events on the target repo**

In GitHub → target repo → Settings → Webhooks → the ai-factory webhook → "Let me select individual events" → add checkboxes for **Check suites** and **Check runs** (also fine to do via the repo's settings page manually — current `public_repo` token cannot create webhooks via API, so manual). Save. Confirm "Recent Deliveries" shows the new event type.

- [ ] **Step 4: Trigger a task whose PR will fail CI**

Open a new issue in the target repo with the ai-factory-run label. Expected: FactoryTask creates PR; GitHub runs CI; task waits (issue label stays `ai-factory-running`).

- [ ] **Step 5: Verify event delivery + watch path**

Run: `kubectl logs -f deployment/ai-factory-server -n ai-factory | grep -E 'ci webhook|CI watch|CI FAILED|CI GREEN'`
Expected: seeing `ci webhook: check_run event for o/r@branch`, then `CI watch attempt`, and (if the PR fails) `CI FAILED`.

- [ ] **Step 6: Force a failure if the test PR passes**

If the PR is green, temporarily add a deliberately failing change (e.g. a compile error) to the issue instructions and re-trigger. Expected: PR red → repair output in logs → new commit force-pushed → re-watch.

- [ ] **Step 7: Verify repair succeeded / failure path**

Run: `kubectl logs -f deployment/ai-factory-server -n ai-factory | tail -100`
Expected: either `--- CI GREEN` and task Succeeded, or `CIFeedbackFailed` task + issue comment explaining no CI events / failed after retries. Confirm no `CI pending after`-style timeout for an actually-green CI (meaning events were received).

- [ ] **Step 8: (If needed) inspect the session file inside a repaired sandbox**

While the first repair runs: `kubectl exec -n ai-factory deploy/... ` on the go-dev pod and `cat /tmp/ai-factory-session.json` — confirm the main task's messages are present and the file is redacted (no secrets).

---

## Self-Review

**Spec coverage:**
- Trigger: webhook events (`check_suite`/`check_run`) — Task 7. ✓
- Wait semantics: sync + hold sandbox — Task 6/7 (`executeTask` blocks). ✓
- Green verdict: quiet window (60s) — Task 6 `waitForCIEvent`. ✓
- Hard timeout — Task 6 `time.After(deadline)`. ✓
- Full job logs → prompt — Task 2/3 (`ActionsJobLogs`, `collectFailedJobLogs`, `buildCIRepairInstructions`). ✓
- Session inheritance (main-only) — Task 4 (`AI_FACTORY_SESSION_FILE`) + Task 5 (script wiring) + Task 6 (runner passes `/tmp/ai-factory-session.json`). ✓
- Independent repair tool rounds — Task 6 (`ciWatchMaxToolRounds`) + Task 5 (env injection). ✓
- Config: no `CI_WATCH_RETRY_INTERVAL`; add `CI_WATCH_MAX_TOOL_ROUNDS` + `CI_WATCH_LOG_SNIPPET_LINES` — Task 8. ✓
- Restore `CIFeedbackFailed` — Task 1. ✓
- Targeted repair prompt (no full-repo exploration; may touch test code) — Task 3 Step 4. ✓
- Fork edge case (empty head_branch) — documented in Task 7, degraded gracefully. ✓

**Placeholder scan:** All steps carry real code or exact commands; no TBD/TODO lines. The one soft spot is Task 3 Step 1 (restore ci.go from `git show 30b5590`), which lists the exact methods to restore — implementer has a concrete source of truth. ✓

**Type consistency:** `ciRepairRunner` is `func(annotations, logSnippets)` everywhere (Task 3 defines `JobLogSnippet`, Task 6 runner + watchAndRepairCI use it). `ciClient` extended with `ActionsJobLogs` in Task 2/6. `BuildCIRepairScript` signature `(task, instructions, CIRepairOptions)` consistent between Task 5 and Task 6/7. `ciWatchOptions` fields consistent. ✓