# Codex Plugin Marketplace 迁移 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `ai-factory-agent codex` 在每个任务开始时从 git marketplace 注册并安装 issue-fix 插件，使 skill 内容 push 后下个任务即生效，无需重建 go-dev 或镜像。

**Architecture:** `run_codex()` 在 `codex exec` 之前新增 `maybe_register_codex_plugin()`，依次执行 `codex plugin marketplace add` → `marketplace upgrade` → `plugin add`；插件源是独立仓库 `Verdure-oss/ai-factory-codex-plugins`（已建好并推送）。插件不可用时回退到现有的单文件 SKILL.md prompt 注入，保证可回退。新增 env 经 Helm ConfigMap 注入 go-dev pod。

**Tech Stack:** Bash（`ai-factory-agent`）、Helm（chart 模板 + values）、K8s ConfigMap/SandboxTemplate、Codex CLI ≥0.121（实测 0.150.1）。测试用 PATH 上的假 `codex` 驱动真实脚本（沿用 `codex_config_test.sh` 的模式）。

**Spec:** `docs/superpowers/specs/2026-08-28-codex-plugin-marketplace-design.md`

## Global Constraints

- Codex CLI 最低 0.121（marketplace 支持）；生产实测版本 0.150.1。
- marketplace catalog 必须放在 `.agents/plugins/marketplace.json`；**`.codex-plugin/marketplace.json` 会被 Codex 拒绝**（实测）。插件清单则用 `.codex-plugin/plugin.json`。
- 插件注册必须在 `configure-git-proxy` **之后**运行：pod 直连 github.com 不稳定，依赖 `AI_FACTORY_GIT_PROXY` 的 insteadOf 改写把 clone 导向 gh-proxy。**不得关闭该改写。**
- `$CODEX_HOME/config.toml` 中根键 `model` / `model_provider` 必须出现在任何 `[table]` 之前（TOML 规则）；`plugin add` 会向该文件追加 `[marketplaces.*]` 和 `[plugins."x@y"]` 表。生成顺序：先 `maybe_write_codex_config`，后插件注册。
- 绝不打印 `OPENAI_API_KEY` / `GITHUB_TOKEN` / `GITLAB_TOKEN`。`base_url`、插件源 URL、版本可打印。
- 插件源仓库已存在：`Verdure-oss/ai-factory-codex-plugins`，marketplace 名 `ai-factory`，插件名 `issue-fix`，本地检出在 `/root/ai-factory/ai-factory-codex-plugins`（与主仓同级，非 submodule）。
- 迁移期保留旧 subPath skill 挂载与 `codex.skillConfigMapName` 作兜底，本计划不删除它们。
- 所有 bash 改动需通过 `bash -n`；`go test ./...` 必须保持通过。

---

## File Structure

| 文件 | 职责 | 动作 |
| --- | --- | --- |
| `components/agent-sandbox-images/coding-agent/ai-factory-agent` | 新增 `maybe_register_codex_plugin()`、env 默认值、`compose_codex_prompt` 适配、usage 文本 | Modify |
| `components/agent-sandbox-images/coding-agent/codex_plugin_test.sh` | 插件注册的 TDD 测试（假 codex 断言命令序列与各分支） | Create |
| `charts/ai-factory/values.yaml` | `codex.plugin.*` 新值 | Modify |
| `charts/ai-factory/templates/configmap.yaml` | 把 `codex.plugin.*` 注入 `ai-factory-config` | Modify |
| `scripts/update-config.sh` | `CONFIG_KEYS` 白名单补插件键 | Modify |
| `scripts/deploy-ghcr.sh` / `scripts/upgrade-ghcr.sh` | 把插件 env 透传成 `--set` | Modify |
| `scripts/ai-factory.env.example` | 记录插件相关 env | Modify |

---

## Task 1: 插件注册函数 + TDD 测试

**Files:**
- Modify: `components/agent-sandbox-images/coding-agent/ai-factory-agent`
- Test: `components/agent-sandbox-images/coding-agent/codex_plugin_test.sh` (create)

**Interfaces:**
- Consumes: 现有 `run_codex()`（:203）、`maybe_write_codex_config()`（:156）、`CODEX_CONFIG_MARKER`（:154）
- Produces:
  - `maybe_register_codex_plugin()` — 无参数，读 env，返回 0；把是否注册成功记入全局 `CODEX_PLUGIN_READY`（`true`/`false`）
  - 全局 `CODEX_PLUGIN_READY`（字符串 `true`/`false`），供 Task 2 的 prompt 组装判断兜底
  - env：`AI_FACTORY_CODEX_PLUGIN_SOURCE`（空=禁用）、`AI_FACTORY_CODEX_PLUGIN_NAME`（默认 `issue-fix`）、`AI_FACTORY_CODEX_MARKETPLACE_NAME`（默认 `ai-factory`）、`AI_FACTORY_CODEX_PLUGIN_REF`（默认 `main`）、`AI_FACTORY_CODEX_SKIP_PLUGIN`（非空=跳过）

- [ ] **Step 1: 写失败测试**

创建 `components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`：

```bash
#!/usr/bin/env bash
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

# Run `ai-factory-agent codex` with a fake codex that logs argv lines to
# $CALLS_FILE. Set FAKE_PLUGIN_ADD_FAILS=1 (in env) to make `plugin add` exit 1.
# Populates globals: RUN_STDOUT, RUN_STATUS, CALLS.
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
    export AI_FACTORY_SKILL_FILE="${workroot}/nonexistent-skill.md"

    set +e
    RUN_STDOUT="$(PATH="${fakebin}:${PATH}" printf 'fix the bug\n' | \
        PATH="${fakebin}:${PATH}" bash "${AGENT}" codex 2>&1)"
    RUN_STATUS=$?
    set -e
    CALLS="$(cat "${CALLS_FILE}")"
}
```

- [ ] **Step 2: 追加 6 个断言用例到测试文件**

```bash
# --- Case 1: enabled -> add, upgrade, plugin add, then exec (in that order) ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN OPENAI_API_KEY OPENAI_BASE_URL
    run_agent
    grep -q 'plugin marketplace add Verdure-oss/ai-factory-codex-plugins --ref main' <<<"${CALLS}" \
        || fail "case1: marketplace add not called with source and ref: ${CALLS}"
    grep -q 'plugin marketplace upgrade' <<<"${CALLS}" || fail "case1: marketplace upgrade not called"
    grep -q 'plugin add issue-fix@ai-factory' <<<"${CALLS}" || fail "case1: plugin add not called"
    # order: marketplace add < upgrade < plugin add < exec
    local_order="$(grep -n -E 'plugin marketplace add|plugin marketplace upgrade|plugin add |^exec' <<<"${CALLS}" | cut -d: -f1 | tr '\n' ' ')"
    [[ "$(sort -n <<<"${local_order// /$'\n'}" | tr '\n' ' ')" == "${local_order}" ]] \
        || fail "case1: commands out of order: ${CALLS}"
    echo "case1 OK"
)

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
)

# --- Case 3: source empty -> no plugin commands at all ---
(
    unset AI_FACTORY_CODEX_PLUGIN_SOURCE AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q 'plugin ' <<<"${CALLS}" && fail "case3: plugin commands ran with empty source: ${CALLS}"
    grep -q '^exec' <<<"${CALLS}" || fail "case3: exec did not run"
    echo "case3 OK"
)

# --- Case 4: SKIP flag -> no plugin commands, exec still runs ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export AI_FACTORY_CODEX_SKIP_PLUGIN=1
    run_agent
    grep -q 'plugin ' <<<"${CALLS}" && fail "case4: SKIP_PLUGIN ignored: ${CALLS}"
    grep -q '^exec' <<<"${CALLS}" || fail "case4: exec did not run"
    echo "case4 OK"
)

# --- Case 5: plugin add fails -> exec still runs (fallback, non-fatal) ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export FAKE_PLUGIN_ADD_FAILS=1
    unset AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q '^exec' <<<"${CALLS}" || fail "case5: exec skipped after plugin add failure: ${CALLS}"
    grep -q 'plugin registration failed' <<<"${RUN_STDOUT}" || fail "case5: no warning emitted"
    echo "case5 OK"
)

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
)

echo "codex-plugin-test: all cases passed"
```

- [ ] **Step 3: 跑测试确认失败（红）**

Run: `bash components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`
Expected: FAIL，`case1: marketplace add not called with source and ref:`（`CALLS` 里只有 `exec ...`，因为注册函数还不存在）

- [ ] **Step 4: 实现 `maybe_register_codex_plugin()`**

在 `ai-factory-agent` 的 `maybe_write_codex_config()` 之后（约 :198 之后）插入：

```bash
# Register and install the workflow plugin from a Codex marketplace so skill
# edits take effect on the next task without rebuilding the image or the warm
# pool pod. `plugin add` re-copies the source even at an unchanged version, so
# running it every task is what makes updates propagate.
#
# Skipped entirely when AI_FACTORY_CODEX_PLUGIN_SOURCE is empty (feature off) or
# AI_FACTORY_CODEX_SKIP_PLUGIN is set. Every step is best-effort: on failure we
# warn and let run_codex fall back to the single-file SKILL.md prompt.
#
# MUST run after any git proxy insteadOf rewrite is in place: direct github.com
# clones from the sandbox are unreliable and the rewrite routes them via the
# proxy mirror.
CODEX_PLUGIN_READY=false

maybe_register_codex_plugin() {
    local source="${AI_FACTORY_CODEX_PLUGIN_SOURCE:-}"
    local name="${AI_FACTORY_CODEX_PLUGIN_NAME:-issue-fix}"
    local marketplace="${AI_FACTORY_CODEX_MARKETPLACE_NAME:-ai-factory}"
    local ref="${AI_FACTORY_CODEX_PLUGIN_REF:-main}"

    if [[ -n "${AI_FACTORY_CODEX_SKIP_PLUGIN:-}" ]]; then
        echo "ai-factory: AI_FACTORY_CODEX_SKIP_PLUGIN set; skipping plugin registration" >&2
        return 0
    fi
    if [[ -z "${source}" ]]; then
        return 0
    fi

    echo "ai-factory: registering codex plugin ${name}@${marketplace} from ${source}#${ref}" >&2

    # Idempotent: re-adding an already-registered source is a no-op.
    codex plugin marketplace add "${source}" --ref "${ref}" >&2 || \
        echo "ai-factory: marketplace add reported an error (may already be registered)" >&2
    # Refresh the git snapshot so a pushed skill edit is picked up.
    codex plugin marketplace upgrade >&2 || \
        echo "ai-factory: marketplace upgrade failed; using the existing snapshot" >&2

    if codex plugin add "${name}@${marketplace}" >&2; then
        CODEX_PLUGIN_READY=true
        echo "ai-factory: codex plugin ${name}@${marketplace} installed" >&2
    else
        echo "ai-factory: codex plugin registration failed; falling back to the SKILL.md prompt" >&2
    fi
}
```

- [ ] **Step 5: 在 `run_codex()` 里调用（顺序关键）**

修改 `run_codex()`，把 `maybe_write_codex_config` 那行（:209）替换为：

```bash
    # Order matters: our managed block writes root keys (model/model_provider)
    # that must precede every [table]; `plugin add` then appends its own
    # [marketplaces.*] / [plugins."x@y"] tables to the same config.toml.
    maybe_write_codex_config
    maybe_register_codex_plugin
```

- [ ] **Step 6: 跑测试确认通过（绿）**

Run: `bash components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`
Expected: 打印 `case1 OK` … `case6 OK`，最后 `codex-plugin-test: all cases passed`

- [ ] **Step 7: 回归 + 语法检查**

Run:
```bash
bash -n components/agent-sandbox-images/coding-agent/ai-factory-agent
bash -n components/agent-sandbox-images/coding-agent/codex_plugin_test.sh
bash components/agent-sandbox-images/coding-agent/codex_config_test.sh
go test ./...
```
Expected: 全部通过（`codex_config_test.sh` 原有 6 个用例仍绿；Go 测试不受影响）

- [ ] **Step 8: Commit**

```bash
chmod +x components/agent-sandbox-images/coding-agent/codex_plugin_test.sh
git add components/agent-sandbox-images/coding-agent/ai-factory-agent \
        components/agent-sandbox-images/coding-agent/codex_plugin_test.sh
git commit -m "feat(agent): install the issue-fix workflow from a Codex marketplace

Register the plugin marketplace and reinstall the plugin at the start of every
codex task, so a skill edit pushed to the plugin repo takes effect on the next
task without rebuilding the image or recreating the warm pool pod. Falls back
to the single-file SKILL.md prompt when registration is off or fails."
```

---

## Task 2: prompt 组装适配插件模式

**Files:**
- Modify: `components/agent-sandbox-images/coding-agent/ai-factory-agent`（`compose_codex_prompt`，:128-138；`run_codex` 调用点 :219；`usage`，:53-67）
- Test: `components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`（追加用例）

**Interfaces:**
- Consumes: Task 1 的全局 `CODEX_PLUGIN_READY`（`true`/`false`）
- Produces: `compose_codex_prompt "<task>" "<skill_file>" "<plugin_ready>"` — 第三参数为 `true` 时输出插件版指引，否则沿用原来的"读取 SKILL.md 文件"指引

- [ ] **Step 1: 写失败测试（追加到 codex_plugin_test.sh，放在 case6 之后、最终 echo 之前）**

```bash
# --- Case 7: plugin ready -> prompt tells codex to use the installed skill ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    unset AI_FACTORY_CODEX_SKIP_PLUGIN FAKE_PLUGIN_ADD_FAILS
    run_agent
    grep -q 'installed Codex plugin' <<<"${CALLS}" \
        || fail "case7: prompt does not mention the installed plugin: ${CALLS}"
    echo "case7 OK"
)

# --- Case 8: plugin unavailable -> prompt falls back to the SKILL.md path ---
(
    export AI_FACTORY_CODEX_PLUGIN_SOURCE="Verdure-oss/ai-factory-codex-plugins"
    export FAKE_PLUGIN_ADD_FAILS=1
    unset AI_FACTORY_CODEX_SKIP_PLUGIN
    run_agent
    grep -q 'installed Codex plugin' <<<"${CALLS}" \
        && fail "case8: plugin prompt used even though registration failed"
    echo "case8 OK"
)
```

- [ ] **Step 2: 跑测试确认失败（红）**

Run: `bash components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`
Expected: FAIL with `case7: prompt does not mention the installed plugin:`

- [ ] **Step 3: 改 `compose_codex_prompt` 接受第三参数**

把 `compose_codex_prompt()`（:128-138）整体替换为：

```bash
# Build the Codex prompt. With the workflow plugin installed, Codex discovers
# the skills itself (front-matter metadata is always in context), so we only
# need to tell it to follow them. Without the plugin we fall back to naming the
# single mounted SKILL.md file.
compose_codex_prompt() {
    local task="$1" skill="$2" plugin_ready="${3:-false}"
    if [[ "${plugin_ready}" == "true" ]]; then
        printf 'Use the workflow skills from the installed Codex plugin as the authoritative playbook for this task: fix the issue, run the relevant checks locally, commit, push, and open a PR/MR yourself, then print the required result line.\n\n'
    elif [[ -f "${skill}" ]]; then
        printf 'Read and strictly follow the workflow skill at %s. It is the authoritative playbook for this task: fix the issue, run the relevant checks locally, commit, push, and open a PR/MR yourself, then print the required result line.\n\n' "${skill}"
    else
        echo "ai-factory: no workflow plugin and no skill file ${skill}; proceeding with task instructions only" >&2
    fi
    printf 'Task context is available in these environment variables: AI_FACTORY_REPO, AI_FACTORY_BASE_REF, AI_FACTORY_BRANCH, AI_FACTORY_ISSUE_URL, AI_FACTORY_PR_TITLE, AI_FACTORY_PR_BODY, AI_FACTORY_REMOTE, AI_FACTORY_PROVIDER. Git auth (GITHUB_TOKEN/GITLAB_TOKEN) is already in the environment.\n\n'
    printf 'When finished, print exactly one final line: __AI_FACTORY_RESULT__={"pr_url":"<url>","branch":"<branch>","summary":"<one sentence>"} and nothing after it.\n\n'
    printf '%s\n' "${task}"
}
```

- [ ] **Step 4: 更新 `run_codex` 的调用点**

把 :219 那行替换为：

```bash
    full_prompt="$(compose_codex_prompt "${prompt_body}" "${CODEX_SKILL_FILE}" "${CODEX_PLUGIN_READY}")"
```

- [ ] **Step 5: 更新 usage 文本**

在 `usage()` 的 "Codex mode environment:" 块内（`AI_FACTORY_SKILL_FILE` 那行之后）插入：

```
  AI_FACTORY_CODEX_PLUGIN_SOURCE  marketplace source (owner/repo, https git URL,
                           or local path). Empty = plugin mode off, use
                           AI_FACTORY_SKILL_FILE instead.
  AI_FACTORY_CODEX_PLUGIN_NAME    default: issue-fix
  AI_FACTORY_CODEX_MARKETPLACE_NAME default: ai-factory
  AI_FACTORY_CODEX_PLUGIN_REF     default: main
  AI_FACTORY_CODEX_SKIP_PLUGIN    set non-empty to skip plugin registration
```

并把 "Codex (delegated) mode ..." 段里 "an externally loaded workflow skill (default /opt/ai-factory/skills/issue-fix/SKILL.md)" 改为 "the workflow skills from an installed Codex plugin (see AI_FACTORY_CODEX_PLUGIN_SOURCE), falling back to a single mounted SKILL.md".

- [ ] **Step 6: 跑测试确认通过（绿）**

Run: `bash components/agent-sandbox-images/coding-agent/codex_plugin_test.sh`
Expected: `case1 OK` … `case8 OK`，`all cases passed`

- [ ] **Step 7: 回归**

Run:
```bash
bash -n components/agent-sandbox-images/coding-agent/ai-factory-agent
bash components/agent-sandbox-images/coding-agent/codex_config_test.sh
go test ./...
```
Expected: 全部通过

- [ ] **Step 8: Commit**

```bash
git add components/agent-sandbox-images/coding-agent/ai-factory-agent \
        components/agent-sandbox-images/coding-agent/codex_plugin_test.sh
git commit -m "feat(agent): let the prompt defer to plugin-provided skills

With the plugin installed Codex discovers the skills from front-matter
metadata, so stop naming a single file path; keep the old wording as the
fallback when the plugin is unavailable."
```

---

## Task 3: Helm 与脚本接线

**Files:**
- Modify: `charts/ai-factory/values.yaml`（`codex:` 段，:97-117）
- Modify: `charts/ai-factory/templates/configmap.yaml`（`CODEX_MODEL` 块之后，:23-26 附近）
- Modify: `scripts/update-config.sh`（`CONFIG_KEYS`，:71 附近）
- Modify: `scripts/deploy-ghcr.sh`（:172 附近的 `HELM_ARGS`）
- Modify: `scripts/upgrade-ghcr.sh`（:93 附近的 `HELM_ARGS`）
- Modify: `scripts/ai-factory.env.example`（codex 段，:49-55 附近）

**Interfaces:**
- Consumes: Task 1 定义的 env 名（`AI_FACTORY_CODEX_PLUGIN_SOURCE` / `_NAME` / `AI_FACTORY_CODEX_MARKETPLACE_NAME` / `_REF`）
- Produces: Helm values `codex.plugin.source`、`codex.plugin.name`、`codex.plugin.marketplace`、`codex.plugin.ref`；对应 ConfigMap 键即上述 env 名

- [ ] **Step 1: 加 values**

在 `charts/ai-factory/values.yaml` 的 `codex:` 段内，`model: ""` 之后追加：

```yaml
  # Codex marketplace plugin carrying the delegated workflow skills. When
  # `source` is set, the agent registers this marketplace and reinstalls the
  # plugin at the start of every task, so pushing a skill edit takes effect on
  # the next task with no image or pod rebuild. Empty = keep using the
  # skillConfigMapName single-file mount.
  plugin:
    # owner/repo, an https git URL, or a local path inside the sandbox.
    source: ""
    # Plugin name as declared in the marketplace catalog.
    name: "issue-fix"
    # Marketplace name (the `name` field in .agents/plugins/marketplace.json).
    marketplace: "ai-factory"
    # Git ref to track.
    ref: "main"
```

- [ ] **Step 2: 注入 ConfigMap**

在 `charts/ai-factory/templates/configmap.yaml` 的 `CODEX_MODEL` 的 `{{- end }}` 之后追加：

```yaml
  {{- if .Values.codex.plugin.source }}
  # Codex marketplace plugin providing the delegated workflow skills. The agent
  # runs `plugin marketplace add/upgrade` + `plugin add` per task, so a pushed
  # skill edit lands on the next task without a rebuild.
  AI_FACTORY_CODEX_PLUGIN_SOURCE: {{ .Values.codex.plugin.source | quote }}
  AI_FACTORY_CODEX_PLUGIN_NAME: {{ .Values.codex.plugin.name | quote }}
  AI_FACTORY_CODEX_MARKETPLACE_NAME: {{ .Values.codex.plugin.marketplace | quote }}
  AI_FACTORY_CODEX_PLUGIN_REF: {{ .Values.codex.plugin.ref | quote }}
  {{- end }}
```

- [ ] **Step 3: 验证 chart 渲染**

Run:
```bash
helm template ai-factory charts/ai-factory --set gitProvider=github \
  --set codex.plugin.source=Verdure-oss/ai-factory-codex-plugins \
  | grep -A4 AI_FACTORY_CODEX_PLUGIN_SOURCE
```
Expected: 输出四个键，`AI_FACTORY_CODEX_PLUGIN_SOURCE: "Verdure-oss/ai-factory-codex-plugins"`、`_NAME: "issue-fix"`、`AI_FACTORY_CODEX_MARKETPLACE_NAME: "ai-factory"`、`_REF: "main"`

再验证默认不渲染：
```bash
helm template ai-factory charts/ai-factory --set gitProvider=github \
  | grep -c AI_FACTORY_CODEX_PLUGIN_SOURCE || true
```
Expected: `0`

- [ ] **Step 4: 补 `update-config.sh` 白名单**

`CONFIG_KEYS` 里 `"CODEX_MODEL" "CODEX_WIRE_API"` 那行之后追加一行：

```bash
             "AI_FACTORY_CODEX_PLUGIN_SOURCE" "AI_FACTORY_CODEX_PLUGIN_NAME"
             "AI_FACTORY_CODEX_MARKETPLACE_NAME" "AI_FACTORY_CODEX_PLUGIN_REF"
```

- [ ] **Step 5: 透传到 deploy/upgrade 的 HELM_ARGS**

`scripts/deploy-ghcr.sh` 在 `codex.model` 那行（:172）之后、`scripts/upgrade-ghcr.sh` 在 `codex.model` 那行（:93）之后，各追加同样四行：

```bash
[[ -n "${AI_FACTORY_CODEX_PLUGIN_SOURCE:-}" ]] && HELM_ARGS+=(--set "codex.plugin.source=${AI_FACTORY_CODEX_PLUGIN_SOURCE}")
[[ -n "${AI_FACTORY_CODEX_PLUGIN_NAME:-}" ]] && HELM_ARGS+=(--set "codex.plugin.name=${AI_FACTORY_CODEX_PLUGIN_NAME}")
[[ -n "${AI_FACTORY_CODEX_MARKETPLACE_NAME:-}" ]] && HELM_ARGS+=(--set "codex.plugin.marketplace=${AI_FACTORY_CODEX_MARKETPLACE_NAME}")
[[ -n "${AI_FACTORY_CODEX_PLUGIN_REF:-}" ]] && HELM_ARGS+=(--set "codex.plugin.ref=${AI_FACTORY_CODEX_PLUGIN_REF}")
```

`deploy-ghcr.sh` 还需在 :111 `CODEX_MODEL=${CODEX_MODEL:-}` 附近追加默认声明，使生成的 env 文件持久化这些键：

```bash
AI_FACTORY_CODEX_PLUGIN_SOURCE=${AI_FACTORY_CODEX_PLUGIN_SOURCE:-}
AI_FACTORY_CODEX_PLUGIN_NAME=${AI_FACTORY_CODEX_PLUGIN_NAME:-}
AI_FACTORY_CODEX_MARKETPLACE_NAME=${AI_FACTORY_CODEX_MARKETPLACE_NAME:-}
AI_FACTORY_CODEX_PLUGIN_REF=${AI_FACTORY_CODEX_PLUGIN_REF:-}
```

- [ ] **Step 6: 更新 env.example**

在 `scripts/ai-factory.env.example` 的 codex 说明段末尾追加：

```
# 委托模式的工作流 skill 可以来自 Codex marketplace 插件（推荐）：设
# AI_FACTORY_CODEX_PLUGIN_SOURCE=Verdure-oss/ai-factory-codex-plugins 后，agent 会在
# 每个任务开始时 marketplace add/upgrade + plugin add，插件仓 push 后下个任务即生效，
# 无需重建镜像或 go-dev pod。可选覆盖：AI_FACTORY_CODEX_PLUGIN_NAME（默认 issue-fix）、
# AI_FACTORY_CODEX_MARKETPLACE_NAME（默认 ai-factory）、AI_FACTORY_CODEX_PLUGIN_REF（默认 main）。
# 设 AI_FACTORY_CODEX_SKIP_PLUGIN=1 可跳过插件、回退到单文件 SKILL.md 挂载。
AI_FACTORY_CODEX_PLUGIN_SOURCE=
```

- [ ] **Step 7: 语法检查**

Run:
```bash
bash -n scripts/update-config.sh scripts/deploy-ghcr.sh scripts/upgrade-ghcr.sh
helm lint charts/ai-factory --set gitProvider=github
```
Expected: 无错误

- [ ] **Step 8: Commit**

```bash
git add charts/ai-factory/values.yaml charts/ai-factory/templates/configmap.yaml \
        scripts/update-config.sh scripts/deploy-ghcr.sh scripts/upgrade-ghcr.sh \
        scripts/ai-factory.env.example
git commit -m "feat(chart): wire the Codex plugin marketplace env through to sandboxes

Add codex.plugin.* values, render them into ai-factory-config, and pass them
from the deploy/upgrade scripts. Also add the four keys to the update-config
allowlist so a hot config refresh does not silently drop them."
```

---

## Task 4: 发版并在集群端到端验证

**Files:**
- Modify: `charts/ai-factory/Chart.yaml`（`version` / `appVersion`，:5-6）
- Modify: `scripts/ai-factory.env`（本地未入库的实际配置；加插件 source）

**Interfaces:**
- Consumes: Task 1-3 的全部改动
- Produces: 新的 chart 版本 `0.1.8` + 带插件注册逻辑的 sandbox 镜像；集群中 go-dev pod 的 env 含 `AI_FACTORY_CODEX_PLUGIN_SOURCE`

- [ ] **Step 1: bump chart 版本**

把 `charts/ai-factory/Chart.yaml` 的 `version: 0.1.7` 与 `appVersion: "0.1.7"` 同时改为 `0.1.8` / `"0.1.8"`。

- [ ] **Step 2: Commit + tag**

```bash
git add charts/ai-factory/Chart.yaml
git commit -m "chore(chart): release 0.1.8 with Codex plugin marketplace support"
git tag v0.1.8
```

- [ ] **Step 3: 打包并推送镜像 + chart**

Run: `bash scripts/deploy-ghcr.sh`（或仓库既有的发布脚本；先 `--help` 确认参数）
Expected: sandbox 镜像与 chart 推到 GHCR，无错误

- [ ] **Step 4: 在 .env 里启用插件并升级集群**

在 `scripts/ai-factory.env` 中加一行：
```
AI_FACTORY_CODEX_PLUGIN_SOURCE=Verdure-oss/ai-factory-codex-plugins
```
Run: `bash scripts/upgrade-ghcr.sh`
Expected: helm upgrade 成功

- [ ] **Step 5: 验证 env 已到 go-dev pod**

Run:
```bash
kubectl get configmap ai-factory-config -n ai-factory \
  -o jsonpath='{.data.AI_FACTORY_CODEX_PLUGIN_SOURCE}'; echo
POD=$(kubectl get pods -n ai-factory -l agents.x-k8s.io/warm-pool-sandbox -o name | head -1 | cut -d/ -f2)
kubectl exec -n ai-factory "$POD" -- printenv AI_FACTORY_CODEX_PLUGIN_SOURCE
```
Expected: 两处都输出 `Verdure-oss/ai-factory-codex-plugins`（若 pod 为空，说明是升级前的旧 pod，删掉让它重建：`kubectl delete pod -n ai-factory "$POD"`）

- [ ] **Step 6: 触发真实 issue，确认插件被注册**

在目标仓库新建一个简单 issue 触发 FactoryTask，然后看 agent 日志：

Run:
```bash
kubectl logs -n ai-factory deployment/ai-factory-server --tail=200 | \
  grep -E "registering codex plugin|codex plugin .* installed|plugin registration failed"
```
Expected: 出现 `ai-factory: registering codex plugin issue-fix@ai-factory from Verdure-oss/ai-factory-codex-plugins#main` 与 `codex plugin issue-fix@ai-factory installed`；任务正常开出 PR

- [ ] **Step 7: 验证"push 即生效、不重建"**

在插件仓 `/root/ai-factory/ai-factory-codex-plugins` 里对 `plugins/issue-fix/skills/issue-fix/SKILL.md` 做一处可观测的小改动（例如在 Workflow 第 1 步末尾加一句
`Always start your plan with the line "PLUGIN-VERSION-CHECK: v2".`），然后：

```bash
cd /root/ai-factory/ai-factory-codex-plugins
git add -A && git commit -m "test: add a marker to verify live plugin updates" && git push origin main
```

**不做任何 pod 重建、不重新部署**，再触发一个 issue，确认新任务的 codex 输出里出现 `PLUGIN-VERSION-CHECK: v2`。
Expected: 出现该标记 → 证明改完 push 下个任务即生效

- [ ] **Step 8: 清理验证标记并 push**

```bash
cd /root/ai-factory/ai-factory-codex-plugins
git revert --no-edit HEAD
git push origin main
```

- [ ] **Step 9: 推送主仓 tag（需确认）**

```bash
git -C /root/ai-factory/ai-factory push origin main
git -C /root/ai-factory/ai-factory push origin v0.1.8
```

---

## 不在本计划范围

- 把单个 `issue-fix` skill 拆成多角色 skill（speccer / planner / builder / reviewer / cleanup）—— 插件机制跑通后单独一轮。
- 删除主仓 `skills/issue-fix/`、`codex.skillConfigMapName`、warm-pool 的 subPath 挂载 —— 兜底期保留，确认稳定后单独收尾。
- `scripted` 模式（`ai-factory-agent openai-compatible`）不受影响，不改。

