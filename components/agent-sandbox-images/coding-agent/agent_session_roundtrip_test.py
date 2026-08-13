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

import contextlib
import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent
AGENT = SCRIPT_DIR / "ai-factory-agent.py"


class CompletionHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length))
        self.server.requests.append(request)
        payload = self.server.responses.pop(0)
        body = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        return


@contextlib.contextmanager
def completion_server(responses):
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), CompletionHandler)
    server.responses = list(responses)
    server.requests = []
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


def completion(content, finish_reason="stop", tool_calls=None, reasoning_content=None):
    message = {"role": "assistant", "content": content}
    if tool_calls is not None:
        message["tool_calls"] = tool_calls
    if reasoning_content is not None:
        message["reasoning_content"] = reasoning_content
    return {
        "id": "test-response",
        "object": "chat.completion",
        "model": "test-model",
        "choices": [
            {
                "index": 0,
                "finish_reason": finish_reason,
                "message": message,
            }
        ],
    }


class AgentSessionRoundtripTest(unittest.TestCase):
    def run_agent(
        self,
        server,
        session_file,
        prompt="Make the requested focused change.",
        **overrides,
    ):
        with tempfile.TemporaryDirectory() as temp_dir:
            prompt_path = Path(temp_dir) / "prompt.txt"
            prompt_path.write_text(prompt, encoding="utf-8")
            env = os.environ.copy()
            for name in (
                "HTTP_PROXY",
                "HTTPS_PROXY",
                "ALL_PROXY",
                "http_proxy",
                "https_proxy",
                "all_proxy",
            ):
                env.pop(name, None)
            env.update(
                {
                    "NO_PROXY": "127.0.0.1,localhost",
                    "OPENAI_API_KEY": "test-key",
                    "OPENAI_BASE_URL": f"http://127.0.0.1:{server.server_port}/v1",
                    "OPENAI_MODEL": "test-model",
                    "OPENAI_TEMPERATURE": "1",
                    "OPENAI_MAX_TOKENS": "48000",
                    "OPENAI_MAX_TOOL_ROUNDS": "2",
                    "OPENAI_MAX_FINAL_SCRIPT_ROUNDS": "2",
                    "OPENAI_MAX_REPAIR_ROUNDS": "0",
                    "OPENAI_TOTAL_TIMEOUT_SECONDS": "10",
                    "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS": "2",
                    "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS": "2",
                    "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS": "2",
                    "AI_FACTORY_PROMPT_FILE": str(prompt_path),
                    "AI_FACTORY_SESSION_FILE": str(session_file),
                    "PYTHONDONTWRITEBYTECODE": "1",
                    "PYTHONPATH": str(SCRIPT_DIR),
                }
            )
            env.update(overrides)
            return subprocess.run(
                [sys.executable, str(AGENT)],
                cwd=temp_dir,
                env=env,
                check=False,
                capture_output=True,
                text=True,
                timeout=15,
            )

    def test_session_dump_written_on_normal_exit(self):
        tool_calls = [
            {
                "id": "call-1",
                "type": "function",
                "function": {
                    "name": "Shell",
                    "arguments": json.dumps({"command": "printf inspected"}),
                },
            }
        ]
        responses = [
            completion(None, finish_reason="tool_calls", tool_calls=tool_calls),
            completion(None, finish_reason=None, reasoning_content="exploration-empty"),
            completion("printf 'session-success\\n'"),
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            with completion_server(responses) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    prompt="Original issue prompt.",
                )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertTrue(session_path.exists())
            raw = session_path.read_text(encoding="utf-8")
            self.assertTrue(raw.strip())
            session = json.loads(raw)
            self.assertIsInstance(session, list)
            self.assertEqual(session[0]["role"], "system")
            self.assertEqual(session[1]["role"], "user")
            self.assertEqual(session[1]["content"], "Original issue prompt.")
            # A tool round was explored; its assistant message and tool result
            # are captured, and the run ends with a trailing user message.
            self.assertEqual(session[2]["role"], "assistant")
            self.assertEqual(session[2]["tool_calls"], tool_calls)
            self.assertEqual(session[3]["role"], "tool")
            self.assertEqual(session[-1]["role"], "user")
            self.assertIn("Tool exploration is finished", session[-1]["content"])
            # The dump is exactly the final conversation that was last sent to
            # the provider (the winning script itself is never appended to
            # messages, matching the pre-existing agent flow).
            self.assertEqual(session, server.requests[-1]["messages"])

    def test_session_dump_redacts_secrets(self):
        prompt = "Use the token test-key-imaginary to call the API.\n"
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            with completion_server([completion("printf 'redact-success\\n'")]) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    prompt=prompt,
                    OPENAI_API_KEY="test-key-imaginary",
                )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            raw = session_path.read_text(encoding="utf-8")
            self.assertNotIn("test-key-imaginary", raw)
            self.assertIn("<redacted:OPENAI_API_KEY>", raw)

    def test_session_dump_written_on_failure_with_trailing_user_message(self):
        responses = [
            completion(None, finish_reason=None, reasoning_content="exploration-empty"),
            completion(None, finish_reason=None, reasoning_content="final-empty"),
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            with completion_server(responses) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    OPENAI_MAX_TOOL_ROUNDS="1",
                    OPENAI_MAX_FINAL_SCRIPT_ROUNDS="1",
                )

            self.assertEqual(completed.returncode, 1)
            self.assertTrue(session_path.exists())
            raw = session_path.read_text(encoding="utf-8")
            self.assertTrue(raw.strip())
            session = json.loads(raw)
            self.assertIsInstance(session, list)
            self.assertEqual(session[0]["role"], "system")
            self.assertEqual(session[-1]["role"], "user")

    def test_session_load_resumes_from_dumped_file(self):
        tool_calls = [
            {
                "id": "call-1",
                "type": "function",
                "function": {
                    "name": "Shell",
                    "arguments": json.dumps({"command": "printf inspected"}),
                },
            }
        ]
        responses = [
            completion(None, finish_reason="tool_calls", tool_calls=tool_calls),
            completion("printf 'resumed-final\\n'"),
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            # First run: a tool exploration round then a final script, which
            # builds a multi-turn conversation and dumps it.
            with completion_server(responses) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    prompt="Investigate and fix the bug.",
                    OPENAI_MAX_TOOL_ROUNDS="1",
                    OPENAI_MAX_FINAL_SCRIPT_ROUNDS="2",
                )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            session = json.loads(session_path.read_text(encoding="utf-8"))
            self.assertGreaterEqual(len(session), 4)

            # Second run: the dumped conversation becomes the initial messages.
            # The agent must not append the new prompt itself; the caller
            # (repair flow) appends repair instructions afterward.
            with completion_server([completion("printf 'resume-ok\\n'")]) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    prompt="Different repair instructions.",
                )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(server.requests[0]["messages"], session)

    def test_session_load_prepends_system_prompt_when_head_is_not_system(self):
        session = [
            {"role": "user", "content": "Previous conversation context."},
            {"role": "assistant", "content": "printf 'prior\\n'"},
        ]
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            session_path.write_text(json.dumps(session), encoding="utf-8")
            with completion_server([completion("printf 'prepended-system\\n'")]) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    prompt="Fresh prompt that must not be included.",
                )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            messages = server.requests[0]["messages"]
            self.assertEqual(messages[0]["role"], "system")
            self.assertIn(
                "You are running inside an ai-factory sandbox",
                messages[0]["content"],
            )
            self.assertEqual(messages[1:], session)

    def test_session_file_unset_is_backward_compatible(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            session_path = Path(temp_dir) / "session.json"
            with completion_server([completion("printf 'no-session\\n'")]) as server:
                completed = self.run_agent(
                    server,
                    session_file=session_path,
                    AI_FACTORY_SESSION_FILE="",
                )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("no-session", completed.stdout)
            self.assertFalse(session_path.exists())


if __name__ == "__main__":
    unittest.main()