# GitHub recipes (`gh`)

`gh` is pre-installed and authenticated via `GITHUB_TOKEN`. All commands run from
the repository checkout.

## Open a pull request

```sh
# Title/body from the environment, falling back to sensible defaults.
TITLE="${AI_FACTORY_PR_TITLE:-fix: $(git log -1 --pretty=%s)}"
BODY="${AI_FACTORY_PR_BODY:-Resolves ${AI_FACTORY_ISSUE_URL}}"

# Link the issue so it auto-closes on merge (append if not already present).
case "$BODY" in
  *Closes*|*closes*) : ;;
  *) BODY="$BODY

Closes ${AI_FACTORY_ISSUE_URL}" ;;
esac

PR_URL="$(gh pr create \
  -R "$AI_FACTORY_REPO" \
  --base "$AI_FACTORY_TARGET_BRANCH" \
  --head "$AI_FACTORY_BRANCH" \
  --title "$TITLE" \
  --body "$BODY" 2>/dev/null)"

# If a PR for this branch already exists (re-run), reuse its URL.
if [ -z "$PR_URL" ]; then
  PR_URL="$(gh pr view "$AI_FACTORY_BRANCH" -R "$AI_FACTORY_REPO" --json url --jq .url 2>/dev/null)"
fi
```

`PR_URL` is what you write into the result contract. Always pass `-R "$AI_FACTORY_REPO"`:
a git proxy (`AI_FACTORY_GIT_PROXY`) may rewrite the origin remote to a non-github.com
host, and without `-R` gh fails with "none of the git remotes ... point to a known
GitHub host". `-R` names the repo explicitly so gh uses the API directly.

## Inspect CI (optional, only if the task needs it)

```sh
gh pr checks "$AI_FACTORY_BRANCH" -R "$AI_FACTORY_REPO"
gh pr view "$AI_FACTORY_BRANCH" -R "$AI_FACTORY_REPO" --json statusCheckRollup,reviewDecision,mergeable
```

## Phase 2 (not required for v1): react to remote CI / review

If you are asked to babysit the PR to green, poll `gh pr checks`, fix failures,
push again, and re-request reviewers — but **never merge on red/pending checks**
and **never force-push the base branch**. v1 stops after opening the PR.

## Notes

- Do not use `--admin` or any check-bypass flag.
- Do not delete the base or target branch.
