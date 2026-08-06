# Fork PR Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the self-hosted ai-factory service process issues from an upstream public repo (no write access) by cloning the operator's fork, pushing the change branch to that fork, and opening the PR against the upstream repo — with the fork owner auto-detected from the `GITHUB_TOKEN`.

**Architecture:** Add a single `ChangeRequestSpec.ForkOwner` field. When set (GitHub only), the execution plan bases the change branch on `upstream/<targetBranch>` instead of the fork's local `baseRef`, the PR `head` becomes `forkOwner:branch`, and the existing-PR lookup uses the fork owner. The server **auto-selects the branch**: if the event target repo's owner equals the fork owner (the token owner), it uses the existing direct flow (no fork); otherwise it injects `ForkOwner` + the fork clone URL into the generated FactoryTask. `Source.Repository` stays the upstream/event repo (PR target + issue reporting). A `--repository` allow-list is added to the `server` command to enforce the documented security boundary. Fork owner is resolved from the token (`GET /user`, cached) unless `--fork-owner` overrides it.

**Tech Stack:** Go (factory module), gopkg.in/yaml.v2, Kubernetes CRD (yaml), `sync` for cache.

## Global Constraints

- GitHub-only fork mode: `forkOwner` is rejected when `source.provider == gitlab`.
- `Source.Repository` always remains the **upstream** repo (used for PR target and issue comments/labels).
- The fork clone URL is **derived**, never stored as a separate field — no `forkCloneURL`.
- One token (`GITHUB_TOKEN` / `AI_FACTORY_GITHUB_TOKEN`) drives both sandbox git push and PR creation.
- The fork must already exist; GitHub forks are same-named by default (`<forkOwner>/<repoName>`).
- Follow existing file conventions: Apache 2.0 header, `shellQuote()` for shell interpolation, `dnsLabel`/`dnsName` for names.
- Do not push to or merge the fork's `main`; base the change branch directly on `upstream/<targetBranch>`.
- `--repository` stays unset (empty allow-list = no filtering) in `scripts/dev.sh`; the server flag exists but is not wired into dev.sh. Fork owner is fully auto-detected from the token.

## Verification Workflow

Local testing uses `scripts/watch.sh` (hot-reload on `factory/**/*.go`, restarts `scripts/dev.sh` → `go run ./factory/cmd/factory server`). No changes to `watch.sh`/`dev.sh`/`package.sh` are required — the new flags are optional and auto-detection works without them. After all tests pass locally, `scripts/package.sh` builds and exports the artifacts (it auto-includes the modified `components/factory-task/crd.yaml` and the compiled Go code).

---

### Task 1: Add `forkOwner` field and validation

**Files:**
- Modify: `factory/pkg/task/task.go` (`ChangeRequestSpec` struct ~line 107-121; `FactoryTaskSpec.validate()` ~line 185-215)
- Modify: `components/factory-task/crd.yaml` (`changeRequest` properties ~line 121-149)
- Test: `factory/pkg/task/task_test.go`

**Interfaces:**
- Produces: `ChangeRequestSpec.ForkOwner string` (yaml `forkOwner,omitempty`). Validation error strings: `"spec.changeRequest.forkOwner is only supported for the github provider"` and `"spec.changeRequest.forkOwner must be a GitHub owner name"`. Later tasks read `task.Spec.ChangeRequest.ForkOwner`.

- [ ] **Step 1: Write the failing tests**

Add to `factory/pkg/task/task_test.go`:

```go
func TestParseRejectsForkOwnerWithGitLab(t *testing.T) {
	data := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fork-gitlab
spec:
  source:
    provider: gitlab
    host: gitlab.com
    repository: matrixhub-ai/matrixhub
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: do something
  changeRequest:
    enabled: true
    forkOwner: Verdure-oss
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for forkOwner with gitlab provider, got nil")
	} else if !strings.Contains(err.Error(), "only supported for the github provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRejectsForkOwnerWithSlash(t *testing.T) {
	data := []byte(`apiVersion: factory.ai.gke.io/v1alpha1
kind: FactoryTask
metadata:
  name: fork-slash
spec:
  source:
    provider: github
    repository: matrixhub-ai/matrixhub
    baseRef: main
  agent:
    name: builder
  sandbox:
    templateRef: go-dev
  work:
    instructions: do something
  changeRequest:
    enabled: true
    forkOwner: Bad/Owner
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for forkOwner containing '/', got nil")
	} else if !strings.Contains(err.Error(), "must be a GitHub owner name") {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

Confirm `strings` is imported in `task_test.go` (it already is — the file uses `strings` elsewhere).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./factory/pkg/task/ -run 'TestParseRejectsForkOwner' -v`
Expected: FAIL — `field forkOwner not found in type task.ChangeRequestSpec` (strict YAML decode).

- [ ] **Step 3: Add the field and validation**

In `task.go`, add to `ChangeRequestSpec`:

```go
	AuthTokenEnv  string `yaml:"authTokenEnv,omitempty"`
	AuthUsername  string `yaml:"authUsername,omitempty"`
	ForkOwner     string `yaml:"forkOwner,omitempty"`
```

In `FactoryTaskSpec.validate()`, immediately after `errs = append(errs, s.ChangeRequest.validate()...)`, add:

```go
	if s.ChangeRequest.ForkOwner != "" {
		if s.Source.Provider == ProviderGitLab {
			errs = append(errs, errors.New("spec.changeRequest.forkOwner is only supported for the github provider"))
		}
		if strings.ContainsAny(s.ChangeRequest.ForkOwner, "/: \t") {
			errs = append(errs, errors.New("spec.changeRequest.forkOwner must be a GitHub owner name"))
		}
	}
```

- [ ] **Step 4: Add `forkOwner` to the CRD schema**

In `components/factory-task/crd.yaml`, inside the `changeRequest` properties (after `authUsername`), add:

```yaml
                  authUsername:
                    type: string
                  forkOwner:
                    type: string
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./factory/pkg/task/ -run 'TestParseRejectsForkOwner' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package tests**

Run: `go test ./factory/pkg/task/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add factory/pkg/task/task.go factory/pkg/task/task_test.go components/factory-task/crd.yaml
git commit -m "feat: add forkOwner field and validation for fork PR workflow"
```

---

### Task 2: Derive fork clone URL and inject at webhook time

**Files:**
- Modify: `factory/pkg/task/provider.go` (add `ForkCloneURL` method on `SourceSpec`)
- Modify: `factory/pkg/task/webhook.go` (`IssueWebhookOptions` struct ~line 38-58; `FactoryTaskFromIssueWebhook` ~line 178-192)
- Test: `factory/pkg/task/task_test.go`, `factory/pkg/task/webhook_test.go`

**Interfaces:**
- Consumes: `ChangeRequestSpec.ForkOwner` (Task 1).
- Produces: `SourceSpec.ForkCloneURL(forkOwner string) (string, error)` — returns `https://<host>/<forkOwner>/<repoName>.git`. `IssueWebhookOptions.ForkOwner string`. When `opts.ForkOwner != ""`, `FactoryTaskFromIssueWebhook` sets `task.Spec.ChangeRequest.ForkOwner` and overrides `task.Spec.Source.CloneURL` to the fork URL.

- [ ] **Step 1: Write the failing tests**

Add to `factory/pkg/task/task_test.go`:

```go
func TestSourceForkCloneURL(t *testing.T) {
	tests := []struct {
		name      string
		src       SourceSpec
		forkOwner string
		want      string
		wantErr   bool
	}{
		{
			name:      "github derives fork url",
			src:       SourceSpec{Provider: ProviderGitHub, Repository: "matrixhub-ai/matrixhub"},
			forkOwner: "Verdure-oss",
			want:      "https://github.com/Verdure-oss/matrixhub.git",
		},
		{
			name:      "empty fork owner",
			src:       SourceSpec{Provider: ProviderGitHub, Repository: "matrixhub-ai/matrixhub"},
			forkOwner: "",
			wantErr:   true,
		},
		{
			name:      "malformed repository",
			src:       SourceSpec{Provider: ProviderGitHub, Repository: "norepo"},
			forkOwner: "Verdure-oss",
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.src.ForkCloneURL(tc.forkOwner)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got url %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
```

Add to `factory/pkg/task/webhook_test.go`:

```go
func TestFactoryTaskFromGitHubIssueWebhookWithForkOwner(t *testing.T) {
	payload := []byte(`{
		"action": "labeled",
		"issue": {"number": 7, "title": "Fix bug", "html_url": "https://github.com/matrixhub-ai/matrixhub/issues/7", "labels": [{"name": "ai-factory"}, {"name": "ai-factory-run"}]},
		"repository": {"full_name": "matrixhub-ai/matrixhub", "html_url": "https://github.com/matrixhub-ai/matrixhub", "clone_url": "https://github.com/matrixhub-ai/matrixhub.git", "default_branch": "main"},
		"sender": {"login": "someone"}
	}`)
	opts := IssueWebhookOptions{
		Provider:             ProviderGitHub,
		Repositories:         []string{"matrixhub-ai/matrixhub"},
		RequiredLabels:       []string{"ai-factory-run"},
		ChangeRequestEnabled: true,
		ForkOwner:            "Verdure-oss",
	}
	task, err := FactoryTaskFromIssueWebhook(payload, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Spec.ChangeRequest.ForkOwner != "Verdure-oss" {
		t.Fatalf("got forkOwner %q, want Verdure-oss", task.Spec.ChangeRequest.ForkOwner)
	}
	if want := "https://github.com/Verdure-oss/matrixhub.git"; task.Spec.Source.CloneURL != want {
		t.Fatalf("got cloneURL %q, want %q", task.Spec.Source.CloneURL, want)
	}
	if task.Spec.Source.Repository != "matrixhub-ai/matrixhub" {
		t.Fatalf("repository must stay upstream, got %q", task.Spec.Source.Repository)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./factory/pkg/task/ -run 'TestSourceForkCloneURL|TestFactoryTaskFromGitHubIssueWebhookWithForkOwner' -v`
Expected: FAIL — `ForkCloneURL` undefined, `ForkOwner` field not found.

- [ ] **Step 3: Add `ForkCloneURL` in provider.go**

Add to `factory/pkg/task/provider.go`:

```go
// ForkCloneURL derives the clone URL of a same-named fork given the fork owner.
func (s SourceSpec) ForkCloneURL(forkOwner string) (string, error) {
	if strings.TrimSpace(forkOwner) == "" {
		return "", errors.New("forkOwner is required to derive fork clone URL")
	}
	repo := strings.TrimPrefix(s.Repository, "/")
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("repository must look like owner/repo, got %q", s.Repository)
	}
	host := s.Host
	switch s.Provider {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderGitLab:
		if host == "" {
			host = "gitlab.com"
		}
	default:
		return "", fmt.Errorf("unsupported git provider %q", s.Provider)
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, forkOwner, parts[1]), nil
}
```

Confirm `errors` is imported in `provider.go` (it is not currently — add `"errors"` to its imports).

- [ ] **Step 4: Add `ForkOwner` to options and inject in webhook.go**

Add a field to `IssueWebhookOptions`:

```go
	ChangeRequestAuthTokenEnv string
	ForkOwner                 string
```

In `FactoryTaskFromIssueWebhook`, right after the `if opts.ChangeRequestEnabled { ... }` block (and before `if err := task.Validate(); err != nil`), add:

```go
	if opts.ForkOwner != "" {
		task.Spec.ChangeRequest.ForkOwner = opts.ForkOwner
		forkURL, err := task.Spec.Source.ForkCloneURL(opts.ForkOwner)
		if err != nil {
			return nil, err
		}
		task.Spec.Source.CloneURL = forkURL
	}
```

Note: `ForkCloneURL` is called even if `ChangeRequestEnabled` is false; the injected `ForkOwner` is only acted on by later tasks when change requests are enabled. This is fine — set the clone URL unconditionally when `ForkOwner` is provided.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./factory/pkg/task/ -run 'TestSourceForkCloneURL|TestFactoryTaskFromGitHubIssueWebhookWithForkOwner' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package tests**

Run: `go test ./factory/pkg/task/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add factory/pkg/task/provider.go factory/pkg/task/webhook.go factory/pkg/task/task_test.go factory/pkg/task/webhook_test.go
git commit -m "feat: derive fork clone URL and inject forkOwner at webhook time"
```

---

### Task 3: Base the change branch on upstream in the execution plan

**Files:**
- Modify: `factory/pkg/task/plan.go` (`BuildExecutionPlan` ~line 53-160)
- Test: `factory/pkg/task/task_test.go`

**Interfaces:**
- Consumes: `ChangeRequestSpec.ForkOwner`, `SourceSpec.ForkCloneURL`, `SourceSpec.Repository` (upstream).
- Produces: `ExecutionPlan.Steps` with three extra steps when `ForkOwner` is set: `"add upstream remote"`, `"fetch upstream"`, `"checkout upstream branch"` (replacing `"checkout base ref"` and `"create change branch"`). Helper `upstreamRepoURL(task *FactoryTask) (string, error)`.

- [ ] **Step 1: Write the failing test**

Add to `factory/pkg/task/task_test.go`:

```go
func TestBuildExecutionPlanWithForkOwner(t *testing.T) {
	task := &FactoryTask{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   ObjectMeta{Name: "fork-task"},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{
				Provider:   ProviderGitHub,
				Host:       "github.com",
				Repository: "matrixhub-ai/matrixhub",
				BaseRef:    "main",
				CloneURL:   "https://github.com/Verdure-oss/matrixhub.git",
			},
			Agent:   AgentSpec{Name: "builder"},
			Sandbox: SandboxSpec{TemplateRef: "go-dev"},
			Work:    WorkSpec{Instructions: "fix it"},
			ChangeRequest: ChangeRequestSpec{
				Enabled:   true,
				ForkOwner: "Verdure-oss",
			},
		},
	}
	plan, err := BuildExecutionPlan(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.ChangeBranch != "factory-task/fork-task" {
		t.Fatalf("got changeBranch %q", plan.ChangeBranch)
	}
	var names []string
	for _, step := range plan.Steps {
		names = append(names, step.Name)
	}
	joined := strings.Join(names, ";")
	for _, want := range []string{"add upstream remote", "fetch upstream", "checkout upstream branch"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan steps missing %q: %d:%s", want, len(names), names)
		}
	}
	if strings.Contains(joined, "checkout base ref") || strings.Contains(joined, "create change branch") {
		t.Fatalf("fork mode must not contain base-ref or create-change-branch steps: %s", names)
	}
	// verify the checkout step bases the branch on upstream/main
	for _, step := range plan.Steps {
		if step.Name == "checkout upstream branch" {
			cmd := strings.Join(step.Command, " ")
			if !strings.Contains(cmd, "upstream/main") || !strings.Contains(cmd, plan.ChangeBranch) {
				t.Fatalf("checkout upstream branch command %q must reference upstream/main and %q", cmd, plan.ChangeBranch)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./factory/pkg/task/ -run TestBuildExecutionPlanWithForkOwner -v`
Expected: FAIL — current plan still contains `checkout base ref` / `create change branch` and no upstream steps.

- [ ] **Step 3: Rewrite the step-building section of `BuildExecutionPlan`**

Replace the block from `plan.Steps := []ExecutionStep{` (line ~95) through the `if task.Spec.ChangeRequest.Enabled { ... }` block (ending ~line 130) with:

```go
	useFork := task.Spec.ChangeRequest.ForkOwner != ""

	plan := &ExecutionPlan{
		TaskName:        task.Metadata.Name,
		Provider:        task.Spec.Source.Provider,
		Repository:      task.Spec.Source.Repository,
		CloneURL:        cloneURL,
		BaseRef:         task.Spec.Source.BaseRef,
		ChangeBranch:    changeBranch,
		TargetBranch:    targetBranch,
		GitAuthTokenEnv: authTokenEnv,
		GitAuthUsername: authUsername,
		AgentName:       task.Spec.Agent.Name,
		AgentCommand:    agentCommand,
		AgentPromptRef:  task.Spec.Agent.PromptRef,
		SandboxTemplate: task.Spec.Sandbox.TemplateRef,
		SandboxClaim:    claimName,
		ContainerName:   containerName,
		WorkDir:         workDir,
		Steps: []ExecutionStep{
			{
				Name:    "clean workspace",
				Command: []string{"/bin/sh", "-lc", fmt.Sprintf("rm -rf %s && mkdir -p %s", shellQuote(workDir), shellQuote("/workspace"))},
			},
			{
				Name:    "clone repository",
				Command: []string{"/bin/sh", "-lc", fmt.Sprintf("git -c http.version=HTTP/1.1 clone %s %s", shellQuote(cloneURL), shellQuote(workDir))},
			},
		},
	}

	if !useFork {
		plan.Steps = append(plan.Steps, ExecutionStep{
			Name:    "checkout base ref",
			Command: []string{"git", "-C", workDir, "checkout", task.Spec.Source.BaseRef},
		})
	}

	if task.Spec.ChangeRequest.Enabled {
		host, err := cloneHost(cloneURL)
		if err != nil {
			return nil, err
		}
		plan.Steps = append([]ExecutionStep{
			{
				Name:    "configure git credentials",
				Command: []string{"/bin/sh", "-lc", configureGitCredentialsScript(host, authTokenEnv, authUsername)},
			},
			{
				Name:    "configure git proxy",
				Command: []string{"/bin/sh", "-lc", configureGitProxyScript()},
			},
		}, plan.Steps...)
		if useFork {
			upstreamURL, err := upstreamRepoURL(task)
			if err != nil {
				return nil, err
			}
			plan.Steps = append(plan.Steps, forkBranchSetupSteps(workDir, changeBranch, targetBranch, upstreamURL)...)
		} else {
			plan.Steps = append(plan.Steps, ExecutionStep{
				Name:    "create change branch",
				Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch},
			})
		}
	}
```

Add these helper functions to `plan.go` (after `BuildExecutionPlan`):

```go
func upstreamRepoURL(task *FactoryTask) (string, error) {
	host := task.Spec.Source.Host
	switch task.Spec.Source.Provider {
	case ProviderGitHub:
		if host == "" {
			host = "github.com"
		}
	case ProviderGitLab:
		if host == "" {
			host = "gitlab.com"
		}
	default:
		return "", fmt.Errorf("unsupported git provider %q", task.Spec.Source.Provider)
	}
	return fmt.Sprintf("https://%s/%s.git", host, strings.TrimPrefix(task.Spec.Source.Repository, "/")), nil
}

func forkBranchSetupSteps(workDir, changeBranch, targetBranch, upstreamURL string) []ExecutionStep {
	addUpstream := fmt.Sprintf("git -C %s remote add upstream %s 2>/dev/null || git -C %s remote set-url upstream %s",
		shellQuote(workDir), shellQuote(upstreamURL), shellQuote(workDir), shellQuote(upstreamURL))
	fetchUpstream := fmt.Sprintf("git -C %s fetch upstream %s", shellQuote(workDir), shellQuote(targetBranch))
	return []ExecutionStep{
		{
			Name:    "add upstream remote",
			Command: []string{"/bin/sh", "-lc", addUpstream},
		},
		{
			Name:    "fetch upstream",
			Command: []string{"/bin/sh", "-lc", fetchUpstream},
		},
		{
			Name:    "checkout upstream branch",
			Command: []string{"git", "-C", workDir, "checkout", "-B", changeBranch, fmt.Sprintf("upstream/%s", targetBranch)},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./factory/pkg/task/ -run TestBuildExecutionPlanWithForkOwner -v`
Expected: PASS.

- [ ] **Step 5: Run the full package tests**

Run: `go test ./factory/pkg/task/`
Expected: PASS (existing `TestBuildExecutionPlanWithChangeRequest` must still pass — non-fork path unchanged).

- [ ] **Step 6: Commit**

```bash
git add factory/pkg/task/plan.go factory/pkg/task/task_test.go
git commit -m "feat: base change branch on upstream baseRef in fork mode"
```

---

### Task 4: Use fork owner in PR head and existing-PR lookup

**Files:**
- Modify: `factory/pkg/task/change_request.go` (`buildGitHubPullRequest` ~line 258-291; `findExistingChangeRequest` ~line 156-217)
- Test: `factory/pkg/task/change_request_test.go`

**Interfaces:**
- Consumes: `ChangeRequestSpec.ForkOwner`.
- Produces: For GitHub, when `ForkOwner` is set, the PR request `head` equals `"<forkOwner>:<changeBranch>"` and the existing-PR lookup `head` query param equals `"<forkOwner>:<changeBranch>"`.

- [ ] **Step 1: Write the failing tests**

Add to `factory/pkg/task/change_request_test.go`:

```go
func TestBuildGitHubPullRequestWithForkOwner(t *testing.T) {
	task := &FactoryTask{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   ObjectMeta{Name: "fork-task"},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{Provider: ProviderGitHub, Repository: "matrixhub-ai/matrixhub", BaseRef: "main"},
			ChangeRequest: ChangeRequestSpec{
				Enabled:   true,
				ForkOwner: "Verdure-oss",
				BranchName: "factory-task/fork-task",
			},
		},
	}
	req, err := BuildChangeRequest(task, ChangeRequestOptions{Token: "tok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if want := "Verdure-oss:factory-task/fork-task"; payload["head"] != want {
		t.Fatalf("got head %q, want %q", payload["head"], want)
	}
	if !strings.HasSuffix(req.URL, "/matrixhub-ai/matrixhub/pulls") {
		t.Fatalf("PR must target upstream repo, got URL %q", req.URL)
	}
}
```

Add a second test for the existing-PR lookup that uses a fake HTTP server returning one open PR, asserting the lookup URL contains `head=Verdure-oss%3Afactory-task...`:

```go
func TestCreateChangeRequestFindsExistingPRByForkOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls") {
			query := r.URL.Query()
			if got := query.Get("head"); got != "Verdure-oss:factory-task/fork-task" {
				t.Fatalf("lookup head %q, want Verdure-oss:factory-task/fork-task", got)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"html_url":"https://github.com/matrixhub-ai/matrixhub/pull/11"}]`)
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()

	task := &FactoryTask{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   ObjectMeta{Name: "fork-task"},
		Spec: FactoryTaskSpec{
			Source: SourceSpec{Provider: ProviderGitHub, Repository: "matrixhub-ai/matrixhub", BaseRef: "main"},
			ChangeRequest: ChangeRequestSpec{
				Enabled:    true,
				ForkOwner:  "Verdure-oss",
				BranchName: "factory-task/fork-task",
			},
		},
	}
	result, err := CreateChangeRequest(context.Background(), task, ChangeRequestOptions{
		Token:   "tok",
		APIBase: server.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got URL %q", result.URL)
	}
}
```

Confirm `context`, `net/http`, `net/http/httptest`, `encoding/json`, `fmt` are already imported in `change_request_test.go` (they are — the file already uses `httptest` and `json`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./factory/pkg/task/ -run 'TestBuildGitHubPullRequestWithForkOwner|TestCreateChangeRequestFindsExistingPRByForkOwner' -v`
Expected: FAIL — head is bare `factory-task/fork-task`; lookup uses `matrixhub-ai:...`.

- [ ] **Step 3: Modify `buildGitHubPullRequest`**

At the top of `buildGitHubPullRequest`, after the token check, add:

```go
	if task.Spec.ChangeRequest.ForkOwner != "" {
		head = task.Spec.ChangeRequest.ForkOwner + ":" + head
	}
```

- [ ] **Step 4: Modify `findExistingChangeRequest`**

Inside the `case ProviderGitHub:` branch, after `owner, repo, err := splitRepository(...)`, replace:

```go
		values.Set("head", owner+":"+changeBranch)
```

with:

```go
		headOwner := owner
		if task.Spec.ChangeRequest.ForkOwner != "" {
			headOwner = task.Spec.ChangeRequest.ForkOwner
		}
		values.Set("head", headOwner+":"+changeBranch)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./factory/pkg/task/ -run 'TestBuildGitHubPullRequestWithForkOwner|TestCreateChangeRequestFindsExistingPRByForkOwner' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package tests**

Run: `go test ./factory/pkg/task/`
Expected: PASS (existing `TestBuildGitHubPullRequest`, `TestCreateChangeRequestReturnsExistingGitHubPullRequest` must still pass — non-fork head unchanged).

- [ ] **Step 7: Commit**

```bash
git add factory/pkg/task/change_request.go factory/pkg/task/change_request_test.go
git commit -m "feat: use fork owner in GitHub PR head and lookup"
```

---

### Task 5: Server auto-selects fork vs direct flow, adds `--repository` allow-list

**Files:**
- Modify: `factory/cmd/factory/server/server.go` (`Options` struct ~line 33-50; `init()` flags ~line 63-79; `webhookOptions` ~line 280-298; `issueWebhookHandler` ~line 158-278)
- Modify: `factory/cmd/factory/server/github.go` (add `AuthenticatedLogin` + cached login resolver)
- Test: `factory/cmd/factory/server/github_test.go`

**Interfaces:**
- Consumes: `IssueWebhookOptions.ForkOwner` and `Repositories` (from prior tasks).
- Produces: `Options.ForkOwner string`, `Options.Repositories []string`. `IssueWebhookOptions` returned by `webhookOptions(provider)` carries `Repositories: opts.Repositories` and `ForkOwner` decided by `resolveForkConfig(provider, eventRepository)`. Pure helper `shouldUseFork(eventOwner, forkOwner string) bool`. `GitHubClient.AuthenticatedLogin(ctx) (string, error)` hits `GET /user` and returns `.login`. Package-level `gitHubLoginCacheInstance` caches login by token.

**Fork auto-selection:** `resolveForkConfig` returns `""` (existing direct flow) when the event repo is owned by the fork owner, else the fork owner (fork flow). Fork owner = `--fork-owner` override, else the cached token owner.

- [ ] **Step 1: Write the failing tests**

Add to `factory/cmd/factory/server/github_test.go`:

```go
func TestShouldUseFork(t *testing.T) {
	tests := []struct {
		eventOwner string
		forkOwner  string
		want       bool
	}{
		{"matrixhub-ai", "Verdure-oss", true}, // upstream public repo -> fork
		{"Verdure-oss", "Verdure-oss", false}, // own repo -> direct flow
		{"", "Verdure-oss", false},            // no event owner -> no fork
		{"matrixhub-ai", "", false},           // no fork owner -> no fork
	}
	for _, tc := range tests {
		if got := shouldUseFork(tc.eventOwner, tc.forkOwner); got != tc.want {
			t.Fatalf("shouldUseFork(%q, %q) = %v, want %v", tc.eventOwner, tc.forkOwner, got, tc.want)
		}
	}
}

func TestGitHubClientAuthenticatedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/user") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"login":"Verdure-oss"}`)
	}))
	defer server.Close()

	gh := &GitHubClient{token: "tok", apiBase: strings.TrimRight(server.URL, "/"), client: server.Client()}
	login, err := gh.AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "Verdure-oss" {
		t.Fatalf("got login %q, want Verdure-oss", login)
	}
}

func TestGitHubLoginCacheResolve(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"login":"Verdure-oss"}`)
	}))
	defer server.Close()

	cache := &gitHubLoginCache{byToken: make(map[string]string)}
	gh := &GitHubClient{token: "tok", apiBase: strings.TrimRight(server.URL, "/"), client: server.Client()}
	first, err := cache.resolve(context.Background(), gh)
	if err != nil || first != "Verdure-oss" {
		t.Fatalf("first resolve failed: login=%q err=%v", first, err)
	}
	second, err := cache.resolve(context.Background(), gh)
	if err != nil || second != "Verdure-oss" {
		t.Fatalf("second resolve failed: login=%q err=%v", second, err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 API call, got %d", calls)
	}
}
```

Confirm imports: `context`, `fmt`, `net/http`, `net/http/httptest`, `strings` are already in `github_test.go` (they are).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./factory/cmd/factory/server/ -run 'TestShouldUseFork|TestGitHubClientAuthenticatedLogin|TestGitHubLoginCacheResolve' -v`
Expected: FAIL — `shouldUseFork` undefined, `AuthenticatedLogin` undefined, `gitHubLoginCache` undefined.

- [ ] **Step 3: Add `AuthenticatedLogin` and the cache to github.go**

Add `"sync"` to the imports of `github.go`. Add methods:

```go
// AuthenticatedLogin returns the login of the account owning the configured token.
func (c *GitHubClient) AuthenticatedLogin(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/user", nil)
	if err != nil {
		return "", fmt.Errorf("build /user request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get /user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("get /user: unexpected status %s", resp.Status)
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode /user response: %w", err)
	}
	return payload.Login, nil
}

// gitHubLoginCache caches the authenticated login per token so fork-owner
// detection does not hit the API on every webhook.
type gitHubLoginCache struct {
	mu      sync.Mutex
	byToken map[string]string
}

var gitHubLoginCacheInstance = &gitHubLoginCache{byToken: make(map[string]string)}

func (c *gitHubLoginCache) resolve(ctx context.Context, gh *GitHubClient) (string, error) {
	key := gh.token
	c.mu.Lock()
	if v, ok := c.byToken[key]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	login, err := gh.AuthenticatedLogin(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.byToken[key] = login
	c.mu.Unlock()
	return login, nil
}
```

- [ ] **Step 4: Add options, flags, and injection in server.go**

Add to the `Options` struct:

```go
	EnableChangeRequest bool
	ReportEnabled       bool
	ForkOwner           string
	Repositories        []string
```

Add flags in `init()`:

```go
	Cmd.Flags().StringVar(&opts.ForkOwner, "fork-owner", "", "GitHub owner of the fork used for change requests; defaults to the authenticated token owner")
	Cmd.Flags().StringArrayVar(&opts.Repositories, "repository", nil, "repository allowed to trigger FactoryTasks; can be repeated")
```

Add a resolver helper and the pure decision function (place near `webhookOptions`):

```go
// shouldUseFork reports whether an event targeting eventOwner should use the
// fork flow: only when a fork owner exists and the target repo is not owned by it.
func shouldUseFork(eventOwner, forkOwner string) bool {
	return eventOwner != "" && forkOwner != "" && eventOwner != forkOwner
}

// resolveForkConfig decides the fork owner for an event. It returns "" to keep
// the existing direct flow (used when the event repo is owned by the fork owner).
func resolveForkConfig(provider, eventRepository string) (string, error) {
	if provider != taskpkg.ProviderGitHub {
		return "", nil
	}
	forkOwner := opts.ForkOwner
	if forkOwner == "" {
		gh := NewGitHubClient()
		if !gh.HasToken() {
			return "", nil
		}
		login, err := gitHubLoginCacheInstance.resolve(context.Background(), gh)
		if err != nil {
			return "", fmt.Errorf("detect fork owner from token: %w", err)
		}
		if login == "" {
			return "", errors.New("detect fork owner from token: response missing login")
		}
		forkOwner = login
	}
	if !shouldUseFork(repoOwner(eventRepository), forkOwner) {
		return "", nil
	}
	return forkOwner, nil
}

// repoOwner returns the owner portion of an "owner/repo" string, or "" if invalid.
func repoOwner(repository string) string {
	parts := strings.SplitN(strings.TrimPrefix(repository, "/"), "/", 2)
	if len(parts) == 2 && parts[0] != "" {
		return parts[0]
	}
	return ""
}
```

In `issueWebhookHandler`, the event is already parsed. Replace the task-build call:

```go
		task, err := taskpkg.FactoryTaskFromIssueWebhook(body, webhookOptions(provider))
```

with:

```go
		forkOwner, err := resolveForkConfig(provider, event.Repository)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		opts := webhookOptions(provider)
		opts.ForkOwner = forkOwner
		task, err := taskpkg.FactoryTaskFromIssueWebhook(body, opts)
```

In `webhookOptions`, add `Repositories` (and keep `ForkOwner` unset here — it is set on the returned struct by the handler):

```go
			RequireAllOf:         []string{"ai-factory"},
			Repositories:         opts.Repositories,
			ChangeRequestEnabled: opts.EnableChangeRequest,
		}
```

Add `"errors"` to server.go imports if not already present (it is not — add it).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./factory/cmd/factory/server/ -run 'TestGitHubClientAuthenticatedLogin|TestGitHubLoginCacheResolve' -v`
Expected: PASS.

- [ ] **Step 6: Build the server**

Run: `go build ./factory/cmd/factory/`
Expected: builds cleanly.

- [ ] **Step 7: Run the whole module test suite**

Run: `go test ./factory/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add factory/cmd/factory/server/server.go factory/cmd/factory/server/github.go factory/cmd/factory/server/github_test.go
git commit -m "feat: auto-detect fork owner from token and add repository allow-list to server"
```

---

### Task 6: Update the fork-workflow design doc and self-hosted docs

**Files:**
- Modify: `docs/public-repo-fork-workflow-design.md`
- Modify: `docs/self-hosted-service-changes.md` (if it documents server flags)

**Interfaces:**
- Consumes: converged design decisions from Tasks 1-5.

- [ ] **Step 1: Update the design doc**

In `docs/public-repo-fork-workflow-design.md`:
- Replace the `forkCloneURL` proposal with the `ChangeRequestSpec.ForkOwner` field.
- In the example config, remove `forkCloneURL` and the redundant `cloneURL`, and add `forkOwner: Verdure-oss` under `changeRequest`.
- Update the "需要修改的代码" section to reflect: clone URL derived from `ForkOwner` (no `forkCloneURL` field), upstream branch base in `plan.go`, PR head + lookup in `change_request.go`, `ForkOwner` field + validation in `task.go`, server injection + `--fork-owner` + `--repository` in `server.go`.
- Add a "前置条件" note: the fork must already exist and be same-named.
- Note the auto-detection: when `--fork-owner` is unset, the server resolves the owner from `GITHUB_TOKEN` via `GET /user`.

- [ ] **Step 2: Update deployment docs**

If `docs/self-hosted-service-changes.md` (or `self-hosted-deployment-guide.md`) lists server flags, add `--fork-owner` and `--repository` to the flag tables with one-line descriptions matching the flag help text.

- [ ] **Step 3: Sanity-check the example**

Run: `go run ./factory/cmd/factory task validate examples/factory-task-github.yaml`
Expected: PASS (existing example unchanged).

- [ ] **Step 4: Commit**

```bash
git add docs/public-repo-fork-workflow-design.md docs/self-hosted-service-changes.md
git commit -m "docs: document fork PR workflow with forkOwner and server flags"
```

---

## Self-Review

**Spec coverage:**
- Fork clone (Q1/Q8): Tasks 2 (derivation + webhook injection) + 5 (server auto-detect). ✅
- Push to fork (Q3): Task 3 — clone URL is the fork, so `origin` = fork; push step unchanged. ✅
- PR to upstream (Q4): Tasks 1 + 4 — `Repository` stays upstream, PR head uses fork owner. ✅
- GitHub only (Q6): Task 1 validation rejects `forkOwner` with gitlab. ✅
- No `forkCloneURL` (converged design): Global constraint; Task 2 derives instead. ✅
- `--repository` allow-list: Task 5. ✅
- Cached fork-owner detection: Task 5 `gitHubLoginCache`. ✅
- **Keep existing flow for own repos:** Task 5 `shouldUseFork`/`resolveForkConfig` — event repo owned by the fork owner uses the direct flow; otherwise fork flow. ✅

**Placeholder scan:** No TBD/TODO; every code step includes concrete code. ✅

**Type consistency:** `ChangeRequestSpec.ForkOwner` is defined in Task 1 and consumed identically in Tasks 2, 3, 4. `IssueWebhookOptions.ForkOwner` defined in Task 2, set by Task 5. `Options.Repositories`/`Options.ForkOwner` defined and used within Task 5. `gitHubLoginCache.resolve` signature matches its call site. ✅