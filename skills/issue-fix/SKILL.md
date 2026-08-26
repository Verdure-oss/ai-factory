---
name: issue-fix
description: ai-factory delegated workflow. Use inside an ai-factory sandbox when Codex must take an issue all the way to a pull/merge request itself — understand the issue, make the smallest correct fix, run the repository's checks locally, commit, push, and open the PR/MR. The controller only prepares the checkout and reports the result; every workflow step lives here.
---

# ai-factory issue-fix (delegated workflow)

You are Codex running inside an ai-factory sandbox. The repository is already
cloned and checked out at `AI_FACTORY_WORKDIR` (default `/workspace/repo`), git
auth is already in the environment, and you are authenticated. **You own the
whole workflow**: fix the issue, validate locally, commit, push, and open the
PR/MR. The controller runs no commit/push/PR/CI steps after you — if you do not
push and open the change request, nothing will.

## Inputs (already in the environment)

| Variable | Meaning |
| --- | --- |
| `AI_FACTORY_REPO` | `owner/repo` slug |
| `AI_FACTORY_PROVIDER` | `github` or `gitlab` |
| `AI_FACTORY_BASE_REF` | base branch to target (e.g. `main`) |
| `AI_FACTORY_BRANCH` | the deterministic change branch to create/update |
| `AI_FACTORY_TARGET_BRANCH` | branch the PR/MR merges into |
| `AI_FACTORY_REMOTE` | git remote name (usually `origin`) |
| `AI_FACTORY_ISSUE_URL` | the triggering issue URL |
| `AI_FACTORY_PR_TITLE` | suggested PR/MR title (may be empty) |
| `AI_FACTORY_PR_BODY` | suggested PR/MR body (may be empty) |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` | git + `gh`/`glab` auth (already exported) |

The task instructions (the issue) are the prompt you were given. Provider
command recipes live in `references/github.md` and `references/gitlab.md`.

## Workflow

1. **Understand.** Read the issue and locate the relevant code (`rg`, `find`,
   read files). Reproduce the problem when practical.
2. **Plan.** State a short plan. Classify each requirement as IN scope, OUT of
   scope, or UNCERTAIN. Do the smallest complete change that resolves the issue.
   Follow the repository's own conventions and `AGENTS.md`/contributor rules.
3. **Edit.** Make focused edits. Do not refactor unrelated code.
4. **Validate locally (CI left-shift).** Run the repository's own checks in the
   sandbox before pushing — see "Local CI" below. Fix failures and re-run until
   green or you hit the no-progress cap.
5. **Commit.** Create/switch to `AI_FACTORY_BRANCH` off the base, then commit
   real changes only, with a semantic message (`fix:`, `feat:`, `docs:`…).
6. **Push.** Push the branch to `AI_FACTORY_REMOTE` (see safety rails on force).
7. **Open the PR/MR.** Use `gh` (GitHub) or `glab` (GitLab) with the title/body
   from the environment; link the issue with `Closes #<n>` when possible.
8. **Report the result** (required — see "Result contract").

## Local CI (validate before you push)

Pick check commands in this priority and run them from the repo root:

1. Any validation commands ai-factory injected for this task (if provided in the
   instructions) — these are the most authoritative.
2. Otherwise infer from the repo: `go build ./... && go test ./...`,
   `gofmt -l .`, `npm ci && npm test`, `make test`, or the linters configured in
   the repo. Prefer what the repo's own CI runs.
3. Best-effort: skim `.github/workflows/*.yml` or `.gitlab-ci.yml` for the
   `run:` steps and mirror the cheap, non-network ones.

The sandbox is largely offline for Go (`GOTOOLCHAIN=local`, no proxy); if a
check needs network dependencies it cannot fetch, note it and move on rather
than fighting it. `scripts/local-ci.sh` is a convenience wrapper.

Local validation is an approximation — remote CI can still differ. That is
acceptable: the goal is to catch the common failures (build, unit tests, format,
lint) before opening the PR, not to guarantee remote green.

## Commit, push, open the PR/MR

```sh
cd "${AI_FACTORY_WORKDIR:-/workspace/repo}"
git checkout -B "$AI_FACTORY_BRANCH"
git add -A            # NEVER stage .ai-factory/ (it is git-excluded already)
git -c user.name=ai-factory -c user.email=ai-factory@example.invalid \
    commit -m "fix: <concise summary>"
git push -u "$AI_FACTORY_REMOTE" "$AI_FACTORY_BRANCH"
```

Then open the change request with the provider CLI. Compact inline recipes
(self-contained — work even if the `references/` files are not mounted):

**GitHub** (`gh`, authed via `GITHUB_TOKEN`):
```sh
TITLE="${AI_FACTORY_PR_TITLE:-fix: $(git log -1 --pretty=%s)}"
BODY="${AI_FACTORY_PR_BODY:-Resolves ${AI_FACTORY_ISSUE_URL}}
Closes ${AI_FACTORY_ISSUE_URL}"
PR_URL="$(gh pr create --base "$AI_FACTORY_TARGET_BRANCH" --head "$AI_FACTORY_BRANCH" \
  --title "$TITLE" --body "$BODY" 2>/dev/null)"
[ -z "$PR_URL" ] && PR_URL="$(gh pr view "$AI_FACTORY_BRANCH" --json url --jq .url 2>/dev/null)"
```

**GitLab** (`glab`, authed via `GITLAB_TOKEN`):
```sh
glab mr create --source-branch "$AI_FACTORY_BRANCH" --target-branch "$AI_FACTORY_TARGET_BRANCH" \
  --title "${AI_FACTORY_PR_TITLE:-fix: $(git log -1 --pretty=%s)}" \
  --description "${AI_FACTORY_PR_BODY:-Resolves ${AI_FACTORY_ISSUE_URL}}
Closes ${AI_FACTORY_ISSUE_URL}" --yes >/tmp/glab.out 2>&1 || true
PR_URL="$(grep -Eo 'https?://[^ ]+/-/merge_requests/[0-9]+' /tmp/glab.out | head -n1)"
```

For fuller detail (existing-PR reuse, CI inspection, self-managed hosts) see
[`references/github.md`](references/github.md) and
[`references/gitlab.md`](references/gitlab.md) when present. Target
`AI_FACTORY_TARGET_BRANCH`; use `AI_FACTORY_PR_TITLE`/`AI_FACTORY_PR_BODY` when set.

## Result contract (required)

When finished, record the change-request URL both ways so the controller can
report it:

```sh
mkdir -p .ai-factory
printf '%s\n' "$PR_URL" > .ai-factory/result-url.txt
printf '__AI_FACTORY_RESULT__=%s\n' \
  "{\"pr_url\":\"$PR_URL\",\"branch\":\"$AI_FACTORY_BRANCH\",\"summary\":\"<one sentence>\"}"
```

Print the `__AI_FACTORY_RESULT__=...` line exactly once, as the final line, with
nothing after it.

## Scope rules

- Priority: repository rules > issue intent > your own preferences.
- Make the smallest change that fully resolves the issue; classify each item
  IN/OUT/UNCERTAIN and act only on IN. Declining out-of-scope work is success,
  not failure — note it in the PR body.

## Safety rails

- **Never print or commit secrets.** Tokens are in the environment; keep them there.
- **Never stage `.ai-factory/`** (scaffolding + result files). It is already in
  `.git/info/exclude`; do not `git add` it.
- **Force-push only the deterministic per-issue branch** `AI_FACTORY_BRANCH`, and
  only when updating this same task's existing branch. Never force-push the base
  or target branch; never rewrite others' commits.
- **Never merge** the PR/MR yourself, and never bypass required checks.
- **No-progress cap:** if repeated attempts make no forward progress (same
  failure twice, or no new commit), stop and report the blocker in the result
  summary rather than looping.
- Commit only real diffs; if there is nothing to change, say so in the result.

