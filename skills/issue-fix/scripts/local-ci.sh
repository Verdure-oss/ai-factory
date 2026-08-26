#!/usr/bin/env bash
# Best-effort local CI runner for the ai-factory issue-fix skill.
#
# Runs cheap, non-network checks that mirror common CI so Codex can validate a
# fix before pushing. This is an approximation, not a guarantee of remote green.
# Extend/override freely — this script is part of the editable skill, not the
# engine. Exits non-zero if any attempted check fails.

set -u
cd "${AI_FACTORY_WORKDIR:-/workspace/repo}" 2>/dev/null || cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

status=0
run() {
  echo "--- local-ci: $*"
  if ! "$@"; then
    echo "--- local-ci: FAILED: $*" >&2
    status=1
  fi
}

# Go
if [ -f go.mod ]; then
  command -v gofmt >/dev/null 2>&1 && run sh -c 'test -z "$(gofmt -l .)" || { gofmt -l .; false; }'
  command -v go >/dev/null 2>&1 && run go build ./...
  command -v go >/dev/null 2>&1 && run go test ./...
fi

# Node
if [ -f package.json ]; then
  if command -v npm >/dev/null 2>&1; then
    [ -f package-lock.json ] && run npm ci --no-audit --no-fund
    npm run --silent 2>/dev/null | grep -q '^  test$' && run npm test --silent
  fi
fi

# Python
if [ -f pyproject.toml ] || [ -f setup.cfg ] || ls ./*_test.py >/dev/null 2>&1; then
  command -v pytest >/dev/null 2>&1 && run pytest -q
fi

# Make
if [ -f Makefile ] && grep -qE '^test:' Makefile; then
  command -v make >/dev/null 2>&1 && run make test
fi

if [ "$status" -eq 0 ]; then
  echo "--- local-ci: all attempted checks passed"
else
  echo "--- local-ci: one or more checks failed" >&2
fi
exit "$status"
