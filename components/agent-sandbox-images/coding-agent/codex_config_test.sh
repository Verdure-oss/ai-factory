#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Focused test for ai-factory-agent codex third-party config.toml generation.
# Drives `ai-factory-agent codex` with a fake `codex` on PATH that records the
# CODEX_HOME/config.toml it would have used, then asserts the generation rules.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
AGENT="${SCRIPT_DIR}/ai-factory-agent"

fail() {
    echo "codex-config-test: $1" >&2
    exit 1
}

# Run `ai-factory-agent codex` in an isolated environment with a fake codex.
# Args: none. Reads env vars set by the caller. Echoes the captured config.toml
# path on success. Populates globals: RUN_STDOUT, RUN_STATUS, CAPTURED_CONFIG.
run_agent() {
    local workroot fakebin
    workroot="$(mktemp -d)"
    fakebin="$(mktemp -d)"

    # Fake codex: dump the config.toml it sees (if any), then print a result line.
    cat > "${fakebin}/codex" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cfg="\${CODEX_HOME:-}/config.toml"
if [[ -f "\${cfg}" ]]; then
    echo "__FAKE_CODEX_CONFIG_BEGIN__"
    cat "\${cfg}"
    echo "__FAKE_CODEX_CONFIG_END__"
else
    echo "__FAKE_CODEX_NO_CONFIG__"
fi
echo '__AI_FACTORY_RESULT__={"pr_url":"http://example/pr/1","branch":"b","summary":"s"}'
EOF
    chmod +x "${fakebin}/codex"

    export CODEX_HOME="${workroot}/codexhome"
    mkdir -p "${CODEX_HOME}"
    export AI_FACTORY_WORKDIR="${workroot}/repo"
    mkdir -p "${AI_FACTORY_WORKDIR}"
    # Skill file intentionally absent; run_codex only warns.
    export AI_FACTORY_SKILL_FILE="${workroot}/nonexistent-skill.md"

    set +e
    RUN_STDOUT="$(printf 'do the task\n' | PATH="${fakebin}:${PATH}" bash "${AGENT}" codex 2>/dev/null)"
    RUN_STATUS=$?
    set -e

    rm -rf "${fakebin}"
    CAPTURED_CONFIG=""
    if grep -q "__FAKE_CODEX_CONFIG_BEGIN__" <<<"${RUN_STDOUT}"; then
        CAPTURED_CONFIG="$(awk '/__FAKE_CODEX_CONFIG_BEGIN__/{f=1;next}/__FAKE_CODEX_CONFIG_END__/{f=0}f' <<<"${RUN_STDOUT}")"
    fi
    rm -rf "${workroot}"
}

[[ -x "${AGENT}" || -f "${AGENT}" ]] || fail "ai-factory-agent not found at ${AGENT}"

# --- Case 1: third-party mode generates config.toml, CODEX_MODEL empty -> OPENAI_MODEL ---
(
    export OPENAI_BASE_URL="https://gw.example.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="qwen3.7-flash"
    unset CODEX_MODEL 2>/dev/null || true
    unset AI_FACTORY_CODEX_SKIP_CONFIG 2>/dev/null || true
    run_agent
    [[ "${RUN_STATUS}" -eq 0 ]] || fail "case1: agent exited ${RUN_STATUS}"
    [[ -n "${CAPTURED_CONFIG}" ]] || fail "case1: expected config.toml to be generated, none seen"
    grep -q 'base_url = "https://gw.example.com/v1"' <<<"${CAPTURED_CONFIG}" || fail "case1: base_url missing/wrong"
    grep -q 'env_key = "OPENAI_API_KEY"' <<<"${CAPTURED_CONFIG}" || fail "case1: env_key missing/wrong"
    grep -q 'wire_api = "responses"' <<<"${CAPTURED_CONFIG}" || fail "case1: wire_api should default to responses"
    grep -q 'model = "qwen3.7-flash"' <<<"${CAPTURED_CONFIG}" || fail "case1: model should fall back to OPENAI_MODEL"
    grep -q 'sk-test-123' <<<"${RUN_STDOUT}" && fail "case1: API key must not be printed"
    echo "case1 PASS: third-party config generated, model=OPENAI_MODEL, key not leaked"
) || exit 1

# --- Case 2: CODEX_MODEL set overrides the config model ---
(
    export OPENAI_BASE_URL="https://gw.example.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="qwen3.7-flash"
    export CODEX_MODEL="deepseek-v4-pro-0813"
    unset AI_FACTORY_CODEX_SKIP_CONFIG 2>/dev/null || true
    run_agent
    [[ -n "${CAPTURED_CONFIG}" ]] || fail "case2: expected config.toml, none seen"
    grep -q 'model = "deepseek-v4-pro-0813"' <<<"${CAPTURED_CONFIG}" || fail "case2: model should use CODEX_MODEL"
    echo "case2 PASS: CODEX_MODEL overrides config model"
) || exit 1

# --- Case 3: official openai.com base_url -> do NOT generate (preserve auth.json mode) ---
(
    export OPENAI_BASE_URL="https://api.openai.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="gpt-4.1"
    unset CODEX_MODEL 2>/dev/null || true
    unset AI_FACTORY_CODEX_SKIP_CONFIG 2>/dev/null || true
    run_agent
    [[ -z "${CAPTURED_CONFIG}" ]] || fail "case3: config.toml must NOT be generated for api.openai.com"
    grep -q "__FAKE_CODEX_NO_CONFIG__" <<<"${RUN_STDOUT}" || fail "case3: expected no-config marker"
    echo "case3 PASS: official endpoint preserves auth.json mode (no config generated)"
) || exit 1

# --- Case 4: skip flag disables generation ---
(
    export OPENAI_BASE_URL="https://gw.example.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="qwen3.7-flash"
    export AI_FACTORY_CODEX_SKIP_CONFIG=1
    unset CODEX_MODEL 2>/dev/null || true
    run_agent
    [[ -z "${CAPTURED_CONFIG}" ]] || fail "case4: skip flag must prevent config generation"
    echo "case4 PASS: AI_FACTORY_CODEX_SKIP_CONFIG=1 skips generation"
) || exit 1

# --- Case 5: operator-provided config.toml (no managed marker) is respected ---
(
    export OPENAI_BASE_URL="https://gw.example.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="qwen3.7-flash"
    unset CODEX_MODEL 2>/dev/null || true
    unset AI_FACTORY_CODEX_SKIP_CONFIG 2>/dev/null || true

    workroot="$(mktemp -d)"
    fakebin="$(mktemp -d)"
    cat > "${fakebin}/codex" <<'EOF'
#!/usr/bin/env bash
cat "${CODEX_HOME}/config.toml"
echo '__AI_FACTORY_RESULT__={"pr_url":"http://example/pr/1","branch":"b","summary":"s"}'
EOF
    chmod +x "${fakebin}/codex"
    export CODEX_HOME="${workroot}/codexhome"
    mkdir -p "${CODEX_HOME}"
    printf 'model = "PREEXISTING_OPERATOR"\n' > "${CODEX_HOME}/config.toml"
    export AI_FACTORY_WORKDIR="${workroot}/repo"; mkdir -p "${AI_FACTORY_WORKDIR}"
    export AI_FACTORY_SKILL_FILE="${workroot}/none.md"

    out="$(printf 'x\n' | PATH="${fakebin}:${PATH}" bash "${AGENT}" codex 2>/dev/null || true)"
    grep -q 'PREEXISTING_OPERATOR' <<<"${out}" || fail "case5: operator-provided config.toml must be respected"
    grep -q 'model_provider = "aifactory"' <<<"${out}" && fail "case5: must not overwrite operator config"
    rm -rf "${fakebin}" "${workroot}"
    echo "case5 PASS: operator-provided config.toml preserved (no marker => untouched)"
) || exit 1

# --- Case 6: our own previously-generated (marked) config is regenerated on warm-pod reuse ---
(
    export OPENAI_BASE_URL="https://gw2.example.com/v1"
    export OPENAI_API_KEY="sk-test-123"
    export OPENAI_MODEL="new-model"
    unset CODEX_MODEL 2>/dev/null || true
    unset AI_FACTORY_CODEX_SKIP_CONFIG 2>/dev/null || true

    workroot="$(mktemp -d)"
    fakebin="$(mktemp -d)"
    cat > "${fakebin}/codex" <<'EOF'
#!/usr/bin/env bash
cat "${CODEX_HOME}/config.toml"
echo '__AI_FACTORY_RESULT__={"pr_url":"http://example/pr/1","branch":"b","summary":"s"}'
EOF
    chmod +x "${fakebin}/codex"
    export CODEX_HOME="${workroot}/codexhome"
    mkdir -p "${CODEX_HOME}"
    # Stale config from a prior run: carries the managed marker on line 1.
    {
        echo "# ai-factory: managed codex config, do not edit"
        echo 'model = "stale-model"'
        echo 'base_url = "https://old.example.com/v1"'
    } > "${CODEX_HOME}/config.toml"
    export AI_FACTORY_WORKDIR="${workroot}/repo"; mkdir -p "${AI_FACTORY_WORKDIR}"
    export AI_FACTORY_SKILL_FILE="${workroot}/none.md"

    out="$(printf 'x\n' | PATH="${fakebin}:${PATH}" bash "${AGENT}" codex 2>/dev/null || true)"
    grep -q 'base_url = "https://gw2.example.com/v1"' <<<"${out}" || fail "case6: marked config should be regenerated with new base_url"
    grep -q 'stale-model' <<<"${out}" && fail "case6: stale marked config must be overwritten"
    rm -rf "${fakebin}" "${workroot}"
    echo "case6 PASS: managed (marked) config regenerated on reuse"
) || exit 1

echo "codex-config-test: PASS"
