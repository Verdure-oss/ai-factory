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
import subprocess
import tempfile
import unittest

from agent_session import (
    build_session_snapshot,
    collect_git_snapshot,
    render_session_snapshot,
    session_file_is_readonly,
)


def sample_transcript():
    """A main-task transcript: task prompt, then 40 rounds of exploration,
    ending with the final script — the shape that used to bloat repair input."""
    messages = [
        {"role": "user", "content": "fix the broken widget"},
        {"role": "assistant", "content": None, "tool_calls": [{"id": "t1"}]},
        {"role": "tool", "tool_call_id": "t1", "content": "cat internal/x.go\n" + "func X()" * 200},
    ]
    for i in range(40):
        messages.append({"role": "assistant", "content": None, "tool_calls": [{"id": "t%d" % i}]})
        messages.append({"role": "tool", "tool_call_id": "t%d" % i, "content": "grep output " * 500})
    messages.append({"role": "assistant", "content": "#!/bin/sh\nsed -i 's/oops/0/' x.go"})
    return messages


class TestBuildSessionSnapshot(unittest.TestCase):
    def test_drops_exploration_transcript(self):
        messages = sample_transcript()
        snapshot = build_session_snapshot(messages)
        raw = json.dumps(snapshot)
        # The 40 rounds of tool output must not leak into the snapshot.
        self.assertLess(len(raw), 8000)
        self.assertNotIn("grep output", raw)
        self.assertNotIn("cat internal/x.go", raw)

    def test_keeps_prompt_and_final_script(self):
        snapshot = build_session_snapshot(sample_transcript())
        self.assertIn("fix the broken widget", snapshot["task_instructions"])
        self.assertIn("sed -i 's/oops/0/'", snapshot["final_script"])

    def test_truncates_oversized_parts(self):
        messages = [
            {"role": "user", "content": "x" * 10000},
            {"role": "assistant", "content": "y" * 2000},
        ]
        snapshot = build_session_snapshot(messages)
        self.assertLess(len(snapshot["task_instructions"]), 4200)
        self.assertLess(len(snapshot["final_script"]), 900)


class TestRenderSessionSnapshot(unittest.TestCase):
    def test_renders_change_context_messages(self):
        snapshot = {
            "task_instructions": "fix the widget",
            "changed_files": ["internal/ci-repro/ci_repro.go"],
            "changed_stat": "1 file changed, 2 insertions(+)",
            "final_script": "sed -i 's/oops/0/' ci_repro.go",
        }
        rendered = render_session_snapshot(snapshot)
        # A snapshot renders as exactly one user message so the agent can append
        # its own prompt without producing consecutive-user noise.
        self.assertEqual(len(rendered), 1)
        self.assertEqual(rendered[0]["role"], "user")
        self.assertIn("fix the widget", rendered[0]["content"])
        self.assertIn("ci_repro.go", rendered[0]["content"])
        self.assertIn("1 file changed", rendered[0]["content"])

    def test_tolerates_missing_fields(self):
        rendered = render_session_snapshot({})
        self.assertEqual(len(rendered), 1)
        self.assertIn("主任务已完成的改动", rendered[0]["content"])


class TestCollectGitSnapshot(unittest.TestCase):
    def test_basic_git(self):
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(["git", "init", "-q", tmp], check=True)
            subprocess.run(["git", "-C", tmp, "config", "user.email", "t@example.com"], check=True)
            subprocess.run(["git", "-C", tmp, "config", "user.name", "t"], check=True)
            with open(os.path.join(tmp, "keep.go"), "w") as handle:
                handle.write("package p\n")
            subprocess.run(["git", "-C", tmp, "add", "."], check=True)
            subprocess.run(["git", "-C", tmp, "commit", "-qm", "base"], check=True)
            with open(os.path.join(tmp, "keep.go"), "a") as handle:
                handle.write("func Added() {}\n")
            os.makedirs(os.path.join(tmp, ".ai-factory"), exist_ok=True)
            with open(os.path.join(tmp, ".ai-factory/agent-prompt.md"), "w") as handle:
                handle.write("noise\n")
            files, change_stat = collect_git_snapshot(cwd=tmp)
            self.assertIn("keep.go", files)
            # Runtime artifacts under .ai-factory/ must not be offered as
            # "changed" evidence to the repair round.
            self.assertNotIn(".ai-factory/agent-prompt.md", files)
            self.assertIn("keep.go", change_stat)
            self.assertIn("1 file changed", change_stat)

    def test_non_repository_degrades(self):
        with tempfile.TemporaryDirectory() as tmp:
            files, change_stat = collect_git_snapshot(cwd=tmp)
            self.assertEqual(files, [])
            self.assertEqual(change_stat, "")


class TestSessionReadOnly(unittest.TestCase):
    def test_var_rules(self):
        old = os.environ.pop("AI_FACTORY_SESSION_READONLY", None)
        try:
            os.environ["AI_FACTORY_SESSION_READONLY"] = ""
            self.assertFalse(session_file_is_readonly())
            os.environ["AI_FACTORY_SESSION_READONLY"] = "1"
            self.assertTrue(session_file_is_readonly())
            os.environ["AI_FACTORY_SESSION_READONLY"] = "false"
            self.assertFalse(session_file_is_readonly())
        finally:
            if old is None:
                os.environ.pop("AI_FACTORY_SESSION_READONLY", None)
            else:
                os.environ["AI_FACTORY_SESSION_READONLY"] = old


if __name__ == "__main__":
    unittest.main()