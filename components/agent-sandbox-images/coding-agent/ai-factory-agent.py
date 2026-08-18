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

import json
import os
import stat
import subprocess
import sys
import tempfile

from agent_budget import (
    AgentBudgetError,
    ExecutionDeadline,
    PhaseRoundBudget,
    post_chat_completion,
)
from agent_session import (
    build_session_snapshot,
    collect_git_snapshot,
    render_session_snapshot,
    session_file_is_readonly,
)
from repair_loop import RepairCandidate, RepairLoopTerminated, run_repair_loop
from agent_config import (
    AgentConfiguration,
    InvalidAgentConfiguration,
    load_config,
)
from agent_images import (
    build_user_content,
    download_to_data_url,
    extract_image_urls,
)
from script_validation import (
    SCRIPT_HEADER,
    RepairResponseError,
    ScriptValidationError,
    extract_repair_script,
    normalize_model_script,
    validate_shell_script,
)


# Keep child Shell tools and generated scripts independent of the container
# entrypoint and login-shell initialization.
os.environ["PATH"] = f"/usr/local/go/bin:{os.environ.get('PATH', '')}"


def required_env(name):
    value = os.environ.get(name, "").strip()
    if not value:
        print(f"{name} is required for ai-factory-agent openai-compatible", file=sys.stderr)
        sys.exit(2)
    return value


def truncate(value, limit=4000):
    text = str(value)
    if len(text) <= limit:
        return text
    return text[:limit] + f"... <truncated {len(text) - limit} chars>"


def dump_response_diagnostics(payload):
    print("OpenAI-compatible response diagnostics:", file=sys.stderr)
    if not isinstance(payload, dict):
        print(
            f"  payload: {type(payload).__name__} {truncate(payload)}",
            file=sys.stderr,
        )
        return
    print(f"  id: {payload.get('id', '<missing>')}", file=sys.stderr)
    print(f"  object: {payload.get('object', '<missing>')}", file=sys.stderr)
    print(f"  model: {payload.get('model', '<missing>')}", file=sys.stderr)
    print(f"  top-level keys: {sorted(payload.keys())}", file=sys.stderr)
    choices = payload.get("choices")
    if not isinstance(choices, list) or not choices:
        print(f"  choices: {type(choices).__name__} {truncate(choices)}", file=sys.stderr)
        return
    choice = choices[0]
    print(f"  choice[0] keys: {sorted(choice.keys())}", file=sys.stderr)
    print(f"  choice[0].finish_reason: {choice.get('finish_reason', '<missing>')}", file=sys.stderr)
    message = choice.get("message")
    if not isinstance(message, dict):
        print(f"  choice[0].message: {type(message).__name__} {truncate(message)}", file=sys.stderr)
        return
    print(f"  message keys: {sorted(message.keys())}", file=sys.stderr)
    for key in ("role", "content", "reasoning_content", "tool_calls", "function_call", "refusal"):
        if key in message:
            value = message.get(key)
            if isinstance(value, str):
                print(f"  message.{key}: string len={len(value)} preview={truncate(value, 800)!r}", file=sys.stderr)
            else:
                print(f"  message.{key}: {type(value).__name__} {truncate(value, 1200)}", file=sys.stderr)
    usage = payload.get("usage")
    if usage is not None:
        print(f"  usage: {truncate(usage, 1200)}", file=sys.stderr)


def redact(text):
    secret_names = (
        "OPENAI_API_KEY",
        "CODEX_API_KEY",
        "GITHUB_TOKEN",
        "GITLAB_TOKEN",
        "AI_FACTORY_GITHUB_TOKEN",
    )
    redacted = str(text)
    for name in secret_names:
        value = os.environ.get(name)
        if value:
            redacted = redacted.replace(value, f"<redacted:{name}>")
    return redacted


def subprocess_output(value):
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return str(value or "")


def run_shell_tool(arguments, deadline):
    try:
        args = json.loads(arguments or "{}")
    except json.JSONDecodeError as exc:
        return f"invalid Shell tool arguments JSON: {exc}"
    command = args.get("command", "")
    if not isinstance(command, str) or not command.strip():
        return "invalid Shell tool arguments: command must be a non-empty string"
    print(f"--- TOOL: Shell {command}", file=sys.stderr)
    try:
        timeout = deadline.request_timeout("tool-exploration", 120)
    except AgentBudgetError as exc:
        return str(exc)
    try:
        completed = subprocess.run(
            ["/bin/bash", "-lc", command],
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as exc:
        output = subprocess_output(exc.stdout) + subprocess_output(exc.stderr)
        if deadline.remaining_seconds() <= 0:
            error = deadline.exhausted_error(
                "tool-exploration",
                diagnostics=truncate(redact(output), 12000),
            )
            return f"{error}\n{error.diagnostics}"
        return (
            "ShellToolTimeout: phase=tool-exploration; "
            f"request_timeout_seconds={timeout:.3f}\n"
            f"{truncate(redact(output), 12000)}"
        )
    output = [
        f"exit_code={completed.returncode}",
        "--- stdout ---",
        completed.stdout,
        "--- stderr ---",
        completed.stderr,
    ]
    return truncate(redact("\n".join(output)), 12000)


def run_tool_calls(tool_calls, deadline):
    tool_messages = []
    for tool_call in tool_calls:
        function = tool_call.get("function") or {}
        name = function.get("name")
        arguments = function.get("arguments")
        if name != "Shell":
            content = f"unsupported tool: {name}"
        else:
            content = run_shell_tool(arguments, deadline)
        tool_messages.append(
            {
                "role": "tool",
                "tool_call_id": tool_call.get("id", ""),
                "content": content,
            }
        )
    return tool_messages


def write_script(script):
    with tempfile.NamedTemporaryFile("w", prefix="ai-factory-agent-", suffix=".sh", delete=False) as handle:
        handle.write(SCRIPT_HEADER)
        handle.write(script)
        handle.write("\n")
        script_path = handle.name
    os.chmod(script_path, os.stat(script_path).st_mode | stat.S_IXUSR)
    return script_path


def run_generated_script(script, model, label, deadline):
    try:
        script = normalize_model_script(script)
        script = validate_shell_script(script)
    except ScriptValidationError as exc:
        message = f"OpenAI-compatible {label} script validation failed: {exc}"
        print(message, file=sys.stderr)
        return subprocess.CompletedProcess(
            args=[],
            returncode=1,
            stdout="",
            stderr=message + "\n",
        )
    script_path = write_script(script)
    print(f"--- RUN: OpenAI-compatible {label} script ({model})")
    phase = "repair-script-execution" if label.startswith("repair") else "generated-script-execution"
    try:
        timeout = deadline.request_timeout(phase, deadline.total_timeout_seconds)
    except AgentBudgetError as exc:
        try:
            os.unlink(script_path)
        except OSError:
            pass
        return subprocess.CompletedProcess(
            args=["/bin/bash", script_path],
            returncode=124,
            stdout="",
            stderr=f"{exc}\n",
        )
    try:
        try:
            completed = subprocess.run(
                ["/bin/bash", script_path],
                check=False,
                capture_output=True,
                text=True,
                timeout=timeout,
            )
        except subprocess.TimeoutExpired as exc:
            stdout = subprocess_output(exc.stdout)
            stderr = subprocess_output(exc.stderr)
            output = stdout + stderr
            error = deadline.exhausted_error(
                phase,
                diagnostics=truncate(redact(output), 12000),
            )
            completed = subprocess.CompletedProcess(
                args=["/bin/bash", script_path],
                returncode=124,
                stdout=stdout,
                stderr=f"{error}\n{error.diagnostics}\n",
            )
    finally:
        try:
            os.unlink(script_path)
        except OSError:
            pass
    if completed.stdout:
        print(redact(completed.stdout), end="")
    if completed.stderr:
        print(redact(completed.stderr), end="", file=sys.stderr)
    return completed


try:
    config: AgentConfiguration = load_config()
except InvalidAgentConfiguration as exc:
    print(f"InvalidAgentConfiguration: {exc}", file=sys.stderr)
    sys.exit(2)

if "--check-config" in sys.argv:
    print("OpenAI-compatible agent configuration is valid.", file=sys.stderr)
    sys.exit(0)

api_key = config.api_key
base_url = config.base_url
model = config.model
temperature = config.temperature
max_tokens = config.max_tokens
max_tool_rounds = config.max_tool_rounds
max_final_script_rounds = config.max_final_script_rounds
max_repair_rounds = config.max_repair_rounds
total_timeout_seconds = config.total_timeout_seconds
exploration_request_timeout_seconds = config.exploration_request_timeout_seconds
final_request_timeout_seconds = config.final_request_timeout_seconds
repair_request_timeout_seconds = config.repair_request_timeout_seconds
vision_enabled = config.vision_enabled
execution_deadline = ExecutionDeadline(total_timeout_seconds)
with open(required_env("AI_FACTORY_PROMPT_FILE"), "r", encoding="utf-8") as prompt_handle:
    prompt = prompt_handle.read()

session_file = os.environ.get("AI_FACTORY_SESSION_FILE", "").strip()
session_messages = []
if session_file:
    try:
        with open(session_file, "r", encoding="utf-8") as session_handle:
            loaded = json.load(session_handle)
        if isinstance(loaded, dict):
            # New snapshot format (main task collapsed to the essentials a
            # repair round needs); render it into starting messages.
            session_messages = render_session_snapshot(loaded)
        elif isinstance(loaded, list) and loaded and isinstance(loaded[0], dict):
            session_messages = loaded
    except (OSError, ValueError, TypeError):
        # Missing/corrupt session file: proceed with a fresh conversation.
        session_messages = []

if not prompt.strip():
    print("FactoryTask prompt on stdin is empty", file=sys.stderr)
    sys.exit(2)

# Extract image URLs from the prompt and attach them as image_url content
# blocks so a multimodal model can read issue screenshots. When vision is
# disabled (OPENAI_VISION_ENABLED=false) the user content stays a plain string
# with image URLs embedded as text, matching the original text-only path.
image_urls = extract_image_urls(prompt) if vision_enabled else []
user_content = build_user_content(prompt, image_urls)

system_prompt = """You are running inside an ai-factory sandbox.
Return only a POSIX shell script. Do not wrap it in Markdown.
Your response must start with a shell command or a shebang.
Do not spend tokens explaining your plan. The generated shell script can inspect the repository at runtime with commands such as find, rg, sed, and test commands already used by this repository.
Shell tool changes persist in the same checkout. If tool calls already completed the implementation, stop calling tools and return a concise script that validates the existing changes. Otherwise, the final script must modify the checked-out repository to satisfy the task.
Generated scripts are stored at a temporary path outside the repository but run with the repository root as their current working directory. Use `pwd` or `git rev-parse --show-toplevel`; never derive the repository root from `dirname "$0"`.
Use small, focused edits. Prefer reading only directly relevant files. Avoid broad repository dumps unless the task is explicitly about repository-wide behavior.
If the task is complex, make a compact implementation plan inside the shell script and execute it in small functions instead of asking for many exploratory tool calls.
Run local checks when practical.
Do not run `python3 -m py_compile` or `compileall`, because they leave bytecode build artifacts in the repository. Use `compile(source, filename, "exec")` or the repository's tests instead.
Do not assume optional CLIs such as yq are installed. For YAML syntax checks, prefer python3 with the yaml module.
Do not change go.mod or go.sum only to work around the local Go toolchain version; the sandbox is expected to provide the repository's declared Go version.
Do not print secrets. Do not commit, push, or open pull requests.
ai-factory will run validation, commit, push, and create the change request after you exit.
"""
messages = list(session_messages)
if not messages:
    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_content},
    ]
else:
    if messages[0].get("role") != "system":
        messages.insert(0, {"role": "system", "content": system_prompt})
    # The current prompt is the task this process must do: for a repair round it
    # carries the CI failure evidence. It must never be dropped just because a
    # session snapshot was inherited — the agent would be left with the main
    # task's memory and no idea what to fix now (observed: repair "fixes"
    # nothing, or re-applies a stale creation step).
    messages.append({"role": "user", "content": user_content})
tools = [
    {
        "type": "function",
        "function": {
            "name": "Shell",
            "description": "Run a shell command in the checked-out repository and return stdout, stderr, and exit code.",
            "parameters": {
                "type": "object",
                "properties": {
                    "command": {
                        "type": "string",
                        "description": "The shell command to run.",
                    }
                },
                "required": ["command"],
            },
        },
    }
]

def request_model(request, phase, request_timeout_seconds, exit_on_error=True):
    try:
        return post_chat_completion(
            request,
            base_url,
            api_key,
            phase,
            request_timeout_seconds,
            execution_deadline,
        )
    except AgentBudgetError as exc:
        if not exit_on_error:
            raise
        print(str(exc), file=sys.stderr)
        if exc.diagnostics:
            print("--- provider diagnostics start ---", file=sys.stderr)
            print(redact(exc.diagnostics), file=sys.stderr)
            print("--- provider diagnostics end ---", file=sys.stderr)
        sys.exit(1)


def parse_model_message(payload, phase):
    try:
        choice = payload["choices"][0]
        message = choice["message"]
        raw_content = message.get("content")
        if raw_content is not None and not isinstance(raw_content, str):
            raise TypeError(
                f"message.content must be a string or null, got {type(raw_content).__name__}"
            )
        content = raw_content or ""
        tool_calls = message.get("tool_calls") or []
        if not isinstance(tool_calls, list):
            raise TypeError(
                f"message.tool_calls must be a list, got {type(tool_calls).__name__}"
            )
    except (AttributeError, KeyError, IndexError, TypeError) as exc:
        print(
            "InvalidModelResponse: "
            f"phase={phase}; response did not contain choices[0].message: {exc}",
            file=sys.stderr,
        )
        dump_response_diagnostics(payload)
        print(redact(json.dumps(payload, ensure_ascii=False, sort_keys=True)), file=sys.stderr)
        sys.exit(1)
    return choice, message, content, tool_calls


def persist_session():
    if not session_file:
        return
    # Repair rounds run with AI_FACTORY_SESSION_READONLY=1: they load the main
    # task's dumped snapshot but must not write their own session back, or the
    # shared file accumulates round-over-round and overflows the API input
    # window by the third repair. The main task (var unset) dumps once.
    if session_file_is_readonly():
        return
    try:
        # The main task dumps a compact snapshot instead of the raw transcript:
        # task instructions, the files it changed (git diff HEAD), and its final
        # script. The full exploration history (~40 tool rounds of raw output)
        # is deliberately dropped — it bloats every repair request and lets a
        # repair agent replay a stale edit instead of fixing the next CI error.
        snapshot = build_session_snapshot(messages)
        files, change_stat = collect_git_snapshot()
        snapshot["changed_files"] = files
        snapshot["changed_stat"] = change_stat
        raw = json.dumps(snapshot, ensure_ascii=False, sort_keys=True)
        with open(session_file, "w", encoding="utf-8") as session_handle:
            session_handle.write(redact(raw))
    except (OSError, TypeError):
        pass


try:
    script = ""
    payload = {}
    invalid_provider_responses = []
    tool_budget = PhaseRoundBudget(
        "tool-exploration",
        max_tool_rounds,
        "ToolRoundsExhausted",
    )
    # Bounded single retry: if the first request fails and the prompt has images,
    # embed any images that download in the sandbox as base64 data URLs and retry
    # once, in case the provider cannot reach the original image URL.
    base64_fallback_tried = False
    while not script:
        try:
            tool_round = tool_budget.next_round()
        except AgentBudgetError as exc:
            print(
                f"{exc}; switching to phase=final-script",
                file=sys.stderr,
            )
            break

        request = {
            "model": model,
            "messages": messages,
            "temperature": temperature,
            "max_tokens": max_tokens,
            "tools": tools,
            "tool_choice": "auto",
        }
        try:
            payload = request_model(
                request,
                "tool-exploration",
                exploration_request_timeout_seconds,
                exit_on_error=False,
            )
        except AgentBudgetError:
            if image_urls and not base64_fallback_tried:
                base64_fallback_tried = True
                embedded = {url: download_to_data_url(url) for url in image_urls}
                if any(embedded.values()):
                    messages[1] = {
                        "role": "user",
                        "content": build_user_content(prompt, image_urls, embedded),
                    }
                    print(
                        "OpenAI-compatible request failed with image URLs; "
                        "retrying with base64-embedded images",
                        file=sys.stderr,
                    )
                else:
                    print(
                        "OpenAI-compatible request failed with image URLs; "
                        "could not download images, retrying original request",
                        file=sys.stderr,
                    )
            payload = request_model(
                request,
                "tool-exploration",
                exploration_request_timeout_seconds,
            )
        choice, message, content, tool_calls = parse_model_message(
            payload,
            "tool-exploration",
        )
        dump_response_diagnostics(payload)
        finish_reason = choice.get("finish_reason", "")

        if tool_calls:
            assistant_message = {
                "role": "assistant",
                "content": message.get("content"),
                "tool_calls": tool_calls,
            }
            if "reasoning_content" in message:
                assistant_message["reasoning_content"] = message.get("reasoning_content")
            messages.append(assistant_message)
            messages.extend(run_tool_calls(tool_calls, execution_deadline))
            print(
                "OpenAI-compatible tool exploration round completed: "
                f"used_rounds={tool_round}; limit={max_tool_rounds}",
                file=sys.stderr,
            )
            continue

        if content.strip() and finish_reason != "length":
            script = content
            break

        invalid_provider_responses.append(
            (
                "tool-exploration",
                redact(json.dumps(payload, ensure_ascii=False, sort_keys=True)),
            )
        )
        if finish_reason == "length":
            print(
                "ModelOutputTruncated: phase=tool-exploration; switching to "
                "phase=final-script",
                file=sys.stderr,
            )
        else:
            print(
                "EmptyModelResponse: phase=tool-exploration; switching to "
                "phase=final-script",
                file=sys.stderr,
            )
        break

    if not script:
        messages.append(
            {
                "role": "user",
                "content": (
                    "Tool exploration is finished. Do not call more tools. "
                    "Changes already made by Shell tools persist. Return only a "
                    "concise final POSIX shell script now, without Markdown. The "
                    "script runs from the repository root but is stored under a "
                    "temporary path, so do not locate the repository from $0."
                ),
            }
        )
        final_budget = PhaseRoundBudget(
            "final-script",
            max_final_script_rounds,
            "FinalScriptRoundsExhausted",
        )
        while not script:
            try:
                final_attempt = final_budget.next_round()
            except AgentBudgetError as exc:
                print(str(exc), file=sys.stderr)
                for response_index, phase_response in enumerate(
                    invalid_provider_responses,
                    start=1,
                ):
                    response_phase, provider_response = phase_response
                    print(
                        "--- provider response "
                        f"{response_index} phase={response_phase} start ---",
                        file=sys.stderr,
                    )
                    print(provider_response, file=sys.stderr)
                    print(
                        "--- provider response "
                        f"{response_index} phase={response_phase} end ---",
                        file=sys.stderr,
                    )
                sys.exit(1)

            payload = request_model(
                {
                    "model": model,
                    "messages": messages,
                    "tool_choice": "none",
                    "temperature": temperature,
                    "max_tokens": max_tokens,
                },
                "final-script",
                final_request_timeout_seconds,
            )
            choice, _message, content, tool_calls = parse_model_message(
                payload,
                "final-script",
            )
            dump_response_diagnostics(payload)
            finish_reason = choice.get("finish_reason", "")

            if not tool_calls and content.strip() and finish_reason != "length":
                script = content
                break

            invalid_provider_responses.append(
                (
                    "final-script",
                    redact(json.dumps(payload, ensure_ascii=False, sort_keys=True)),
                )
            )
            if tool_calls:
                attempted_tools = truncate(
                    json.dumps(tool_calls, ensure_ascii=False),
                    2000,
                )
                retry_detail = (
                    "Your last response still tried to call tools, but no more "
                    "tools are available. Do not call Shell. "
                    f"Attempted tool calls: {attempted_tools}"
                )
            elif finish_reason == "length":
                retry_detail = (
                    "Your last response was truncated by the token limit. Return "
                    "a shorter script."
                )
            else:
                retry_detail = (
                    "Your last response did not contain a script. Do not return "
                    "only analysis or reasoning."
                )
            print(
                "InvalidFinalScriptResponse: phase=final-script; "
                f"used_rounds={final_attempt}; limit={max_final_script_rounds}; "
                f"finish_reason={finish_reason!r}; tool_calls={len(tool_calls)}; "
                f"content_length={len(content)}",
                file=sys.stderr,
            )
            messages.append(
                {
                    "role": "user",
                    "content": (
                        f"{retry_detail} Do not include analysis or comments. "
                        "Return only a concise POSIX shell script."
                    ),
                }
            )

    completed = run_generated_script(
        script,
        model,
        "generated",
        execution_deadline,
    )


    def request_repair(round_number, round_limit, repair_prompt):
        print(
            "OpenAI-compatible generated script failed; requesting repair script "
            f"({round_number}/{round_limit})",
            file=sys.stderr,
        )
        repair_payload = request_model(
            {
                "model": model,
                "messages": [
                    {"role": "system", "content": system_prompt},
                    # Use the multimodal user content so a repair round still sees
                    # issue images; falls back to the plain prompt when vision is
                    # disabled or there are no images.
                    {"role": "user", "content": user_content},
                    {"role": "user", "content": repair_prompt},
                ],
                "tool_choice": "none",
                "temperature": temperature,
                "max_tokens": max_tokens,
            },
            "repair-script",
            repair_request_timeout_seconds,
            exit_on_error=False,
        )
        dump_response_diagnostics(repair_payload)
        try:
            repair_script = extract_repair_script(repair_payload)
        except RepairResponseError as exc:
            return RepairCandidate(
                error=str(exc),
                diagnostics=json.dumps(repair_payload, ensure_ascii=False, sort_keys=True),
            )
        return RepairCandidate(script=repair_script)


    try:
        completed = run_repair_loop(
            script,
            completed,
            max_repair_rounds,
            request_repair,
            lambda repair_script, label: run_generated_script(
                repair_script,
                model,
                label,
                execution_deadline,
            ),
            redact=redact,
        )
    except RepairLoopTerminated as exc:
        print(exc.diagnostics, file=sys.stderr)
        sys.exit(exc.returncode)

    sys.exit(completed.returncode)
finally:
    persist_session()
