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

"""Session snapshot for CI repair rounds.

The main task's dump used to be the full message transcript (every tool round,
reasoning, and raw shell output — tens of thousands of tokens). Repair rounds
then inherited that whole stream: it bloated the model input, and worse, it
carried the main task's exploration state so a repair agent could re-apply a
stale edit (e.g. restoring ``return \"oops\"``) instead of fixing the next CI
error.

This module collapses the transcript into the small snapshot a repair round
actually needs: the original task instructions, which files the main task
changed (with a change summary from ``git diff HEAD``), and the final script.
The exploration transcript is deliberately dropped.
"""

import os
import subprocess

_FINAL_SCRIPT_LIMIT = 800
_TASK_INSTRUCTIONS_LIMIT = 4000
_CHANGE_STAT_LIMIT = 2000


def truncate(value, limit):
    text = str(value)
    if len(text) <= limit:
        return text
    return text[:limit] + f"... <truncated {len(text) - limit} chars>"


def build_session_snapshot(messages):
    """Collapse a completed main-task transcript into a compact snapshot dict."""
    snapshot = {}
    for message in messages:
        if message.get("role") == "user" and message.get("content"):
            snapshot["task_instructions"] = truncate(message["content"], _TASK_INSTRUCTIONS_LIMIT)
            break
    for message in reversed(messages):
        if message.get("role") == "assistant" and message.get("content"):
            snapshot["final_script"] = truncate(message["content"], _FINAL_SCRIPT_LIMIT)
            break
    return snapshot


def collect_git_snapshot(cwd=None):
    """Collect the main task's uncommitted changes via git diff HEAD.

    Returns (changed_files, change_stat). cwd should be the repository checkout
    (the agent runs with the repo root as its working directory); a failure or
    a non-repository cwd degrades to an empty listing rather than aborting.
    """
    try:
        name_only = subprocess.run(
            ["git", "diff", "HEAD", "--name-only"],
            capture_output=True,
            text=True,
            timeout=15,
            cwd=cwd,
        )
        if name_only.returncode != 0:
            return [], ""
        files = [
            line.strip()
            for line in name_only.stdout.splitlines()
            if line.strip() and not line.strip().startswith(".ai-factory/")
        ]
        stats = subprocess.run(
            ["git", "diff", "HEAD", "--stat"],
            capture_output=True,
            text=True,
            timeout=15,
            cwd=cwd,
        )
        change_stat = stats.stdout.strip() if stats.returncode == 0 else ""
        return files, truncate(change_stat, _CHANGE_STAT_LIMIT)
    except (OSError, subprocess.TimeoutExpired):
        return [], ""


def render_session_snapshot(snapshot):
    """Turn a snapshot dict into a single starting user message: the original
    task instructions plus the main task's change summary. One message keeps
    the transcript start compact for providers that dislike consecutive user
    turns; the repair prompt itself is appended after it by the agent."""
    body = []
    if snapshot.get("task_instructions"):
        body.append("# 主任务原指令\n" + snapshot["task_instructions"])
    change_parts = ["## 主任务已完成的改动(当前 checkout 已包含这些变更)"]
    files = snapshot.get("changed_files") or []
    if files:
        change_parts.append("### 变更文件")
        change_parts.extend("- " + f for f in files)
    if snapshot.get("changed_stat"):
        change_parts.append("### 变更统计")
        change_parts.append(snapshot["changed_stat"])
    if snapshot.get("final_script"):
        change_parts.append("### 主任务最终脚本片段")
        change_parts.append(snapshot["final_script"])
    body.append("\n".join(change_parts))
    return [{"role": "user", "content": "\n\n".join(body)}]


def session_file_is_readonly():
    """Repair rounds (AI_FACTORY_SESSION_READONLY=1) never write their session back."""
    return os.environ.get("AI_FACTORY_SESSION_READONLY", "").strip() not in ("", "0", "false")