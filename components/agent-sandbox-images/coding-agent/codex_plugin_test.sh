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

# Focused test for ai-factory-agent codex plugin registration.
# Drives `ai-factory-agent codex` with a fake `codex` on PATH that records every
# invocation, then asserts the marketplace/plugin command sequence and fallbacks.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
AGENT="${SCRIPT_DIR}/ai-factory-agent"

fail() {
    echo "codex-plugin-test: $1" >&2
    exit 1
}

# Echo the 1-based line number of the first line of $CALLS matching the ERE $1,
# or nothing when there is no match. Patterns are anchored by the caller so a
# multi-line prompt argument cannot be mistaken for a command invocation.
first_call_line() {
    grep -n -m1 -E -- "$1" <<<"${CALLS}" | cut -d: -f1 || true
}

# Run `ai-factory-agent codex` with a fake codex that logs argv lines to
# $CALLS_FILE. Set FAKE_PLUGIN_ADD_FAILS=1 (in env) to make `plugin add` exit 1.
# Pre-set AI_FACTORY_SKILL_FILE (to an existing file) to exercise the SKILL.md
# prompt branch; left unset it points at a nonexistent path so the warn-only
# branch runs. Populates globals: RUN_STDOUT, RUN_STATUS, CALLS.
run_agent() {
    local workroot fakebin
    workroot="$(mktemp -d)"
    fakebin="$(mktemp -d)"
    CALLS_FILE="${workroot}/calls.txt"
    : > "${CALLS_FILE}"

    cat > "${fakebin}/codex" <<EOF
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "\$*" >> "${CALLS_FILE}"
if [[ "\${1:-}" == "plugin" && "\${2:-}" == "add" && -n "\${FAKE_PLUGIN_ADD_FAILS:-}" ]]; then
    echo "fake: plugin add failed" >&2
    exit 1
fi
if [[ "\${1:-}" == "exec" ]]; then
    echo '__AI_FACTORY_RESULT__={"pr_url":"http://example/pr/1","branch":"b","summary":"s"}'
fi
exit 0
EOF
    chmod +x "${fakebin}/codex"

    export CODEX_HOME="${workroot}/codexhome"; mkdir -p "${CODEX_HOME}"
    export AI_FACTORY_WORKDIR="${workroot}/repo"; mkdir -p "${AI_FACTORY_WORKDIR}"
    export AI_FACTORY_SKILL_FILE="${AI_FACTORY_SKILL_FILE:-${workroot}/nonexistent-skill.md}"

    set +e
    RUN_STDOUT="$(printf 'fix the bug\n' | \
        PATH="${fakebin}:${PATH}" bash "${AGENT}" codex 2>&1)"
    RUN_STATUS=$?
    set -e
    CALLS="$(cat "${CALLS_FILE}")"
}

[[ -x "${AGENT}" || -f "${AGENT}" ]] || fail "ai-factory-agent not found at ${AGENT}"

# --- Case 1: enabled -> add, upgrade, plugin add, then exec (in that order) ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN OPENAI_API_KEY OPENAI_BASE_URL
    run_agent
    grep -q 'plugin marketplace add Verdure-oss/ai-factory-codex-plugins --ref main' <<<"${CALLS}" \
        || fail "case1: marketplace add not called with source and ref: ${CALLS}"
    grep -q 'plugin marketplace upgrade' <<<"${CALLS}" || fail "case1: marketplace upgrade not called"
    grep -q 'plugin add issue-fix@ai-factory' <<<"${CALLS}" || fail "case1: plugin add not called"
    # order: marketplace add < upgrade < plugin add < exec (numeric, anchored)
    add_line="$(first_call_line '^plugin marketplace add ')"
    upgrade_line="$(first_call_line '^plugin marketplace upgrade')"
    install_line="$(first_call_line '^plugin add ')"
    exec_line="$(first_call_line '^exec')"
    [[ -n "${add_line}" && -n "${upgrade_line}" && -n "${install_line}" && -n "${exec_line}" ]] \
        || fail "case1: missing invocation (add=${add_line:-none} upgrade=${upgrade_line:-none} install=${install_line:-none} exec=${exec_line:-none}): ${CALLS}"
    (( add_line < upgrade_line && upgrade_line < install_line && install_line < exec_line )) \
        || fail "case1: commands out of order (add=${add_line} upgrade=${upgrade_line} install=${install_line} exec=${exec_line}): ${CALLS}"
    echo "case1 OK"
) || exit 1

# --- Case 2: custom name/marketplace/ref are honoured ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="acme/plugins"
    export AI_FACTORY_CODEX_PLUGIN_NAME="my-flow"
    export AI_FACTORY_CODEX_MARKETPLACE_NAME="acme"
    export AI_FACTORY_CODEX_PLUGIN_REF="dev"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q 'plugin marketplace add acme/plugins --ref dev' <<<"${CALLS}" || fail "case2: custom source/ref ignored"
    grep -q 'plugin add my-flow@acme' <<<"${CALLS}" || fail "case2: custom plugin/marketplace ignored"
    echo "case2 OK"
) || exit 1

# --- Case 3: source empty -> no plugin commands at all ---
(
    unset AI_FACTORY_CODEX_PLUGIN_SOURCE AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q 'plugin ' <<<"${CALLS}" && fail "case3: plugin commands ran with empty source: ${CALLS}"
    grep -q '^exec' <<<"${CALLS}" || fail "case3: exec did not run"
    echo "case3 OK"
) || exit 1

# --- Case 4: SKIP flag -> no plugin commands, exec still runs ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export AI_FACTORY_CODEX_SKIP_PLUGIN=1
    run_agent
    grep -q 'plugin ' <<<"${CALLS}" && fail "case4: SKIP_PLUGIN ignored: ${CALLS}"
    grep -q '^exec' <<<"${CALLS}" || fail "case4: exec did not run"
    echo "case4 OK"
) || exit 1

# --- Case 5: plugin add fails -> exec still runs (fallback, non-fatal) ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export FAKE_PLUGIN_ADD_FAILS=1
    unset AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q '^exec' <<<"${CALLS}" || fail "case5: exec skipped after plugin add failure: ${CALLS}"
    grep -q 'plugin registration failed' <<<"${RUN_STDOUT}" || fail "case5: no warning emitted"
    echo "case5 OK"
) || exit 1

# --- Case 6: config.toml root keys stay above every [table] ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export OPENAI_API_KEY="sk-test"
    export OPENAI_BASE_URL="https://gw.example.com/v1"
    export OPENAI_MODEL="m1"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN AI_FACTORY_CODEX_SKIP_CONFIG
    run_agent
    cfg="${CODEX_HOME}/config.toml"
    [[ -f "${cfg}" ]] || fail "case6: config.toml not generated"
    first_table="$(grep -n '^\[' "${cfg}" | head -1 | cut -d: -f1)"
    model_line="$(grep -n '^model = ' "${cfg}" | head -1 | cut -d: -f1)"
    provider_line="$(grep -n '^model_provider = ' "${cfg}" | head -1 | cut -d: -f1)"
    [[ -n "${model_line}" && -n "${provider_line}" && -n "${first_table}" ]] \
        || fail "case6: expected model/model_provider/table lines in ${cfg}"
    (( model_line < first_table && provider_line < first_table )) \
        || fail "case6: root keys must precede the first [table] in ${cfg}"
    grep -q 'sk-test' "${cfg}" && fail "case6: API key leaked into config.toml"
    echo "case6 OK"
) || exit 1

# --- Case 7: plugin ready -> prompt tells codex to use the installed skill ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN FAKE_PLUGIN_ADD_FAILS
    run_agent
    grep -q 'installed Codex plugin' <<<"${CALLS}" \
        || fail "case7: prompt does not mention the installed plugin: ${CALLS}"
    echo "case7 OK"
) || exit 1

# --- Case 8: plugin registration failed + skill file present -> prompt falls
# --- back to naming the mounted SKILL.md and drops the plugin wording ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export FAKE_PLUGIN_ADD_FAILS=1
    unset AI_FACTORY_CODEX_SKIP_PLUGIN
    skill_dir="$(mktemp -d)"
    export AI_FACTORY_SKILL_FILE="${skill_dir}/SKILL.md"
    printf '# issue-fix\n' > "${AI_FACTORY_SKILL_FILE}"
    run_agent
    grep -q 'installed Codex plugin' <<<"${CALLS}" \
        && fail "case8: plugin prompt used even though registration failed"
    grep -qF "Read and strictly follow the workflow skill at ${AI_FACTORY_SKILL_FILE}" <<<"${CALLS}" \
        || fail "case8: prompt does not fall back to the mounted skill file: ${CALLS}"
    rm -rf "${skill_dir}"
    echo "case8 OK"
) || exit 1

echo "codex-plugin-test: all cases passed"
