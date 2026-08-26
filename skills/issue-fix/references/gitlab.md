# GitLab recipes (`glab`)

`glab` is pre-installed and authenticated via `GITLAB_TOKEN`. All commands run
from the repository checkout. Set the host once if the project is self-managed:

```sh
# Only needed for self-managed GitLab; skip for gitlab.com.
# export GITLAB_HOST="$(printf '%s' "$AI_FACTORY_ISSUE_URL" | sed -E 's#^https?://([^/]+)/.*#\1#')"
```

## Open a merge request

```sh
TITLE="${AI_FACTORY_PR_TITLE:-fix: $(git log -1 --pretty=%s)}"
BODY="${AI_FACTORY_PR_BODY:-Resolves ${AI_FACTORY_ISSUE_URL}}"

# GitLab closes issues from MRs with "Closes #<iid>".
case "$BODY" in
  *Closes*|*closes*) : ;;
  *) BODY="$BODY

Closes ${AI_FACTORY_ISSUE_URL}" ;;
esac

glab mr create \
  --source-branch "$AI_FACTORY_BRANCH" \
  --target-branch "$AI_FACTORY_TARGET_BRANCH" \
  --title "$TITLE" \
  --description "$BODY" \
  --yes >/tmp/glab-mr.out 2>&1 || true

# Extract the MR URL (glab prints it; also queryable).
PR_URL="$(grep -Eo 'https?://[^ ]+/-/merge_requests/[0-9]+' /tmp/glab-mr.out | head -n1)"
if [ -z "$PR_URL" ]; then
  PR_URL="$(glab mr view "$AI_FACTORY_BRANCH" -F json 2>/dev/null | \
    sed -n 's/.*"web_url" *: *"\([^"]*\)".*/\1/p' | head -n1)"
fi
```

`PR_URL` is what you write into the result contract.

## Notes

- `glab` asset/flag names vary by version; if `--yes` is unsupported, drop it and
  provide all required flags so the command stays non-interactive.
- Never merge the MR yourself; never force-push the target branch.
