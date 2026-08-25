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
        status = payload.pop("_status", 200)
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path != "/image.png":
            self.send_response(404)
            self.end_headers()
            return
        body = b"fake-png-bytes"
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
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


class AgentIntegrationTest(unittest.TestCase):
    def run_agent(self, server, prompt="Make the requested focused change.", **overrides):
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

    def test_successful_script_response_remains_compatible(self):
        with completion_server([completion("printf 'agent-success\\n'")]) as server:
            completed = self.run_agent(server)

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("agent-success", completed.stdout)
        self.assertEqual(len(server.requests), 1)
        self.assertEqual(server.requests[0]["tool_choice"], "auto")

    def test_tool_text_response_is_replaced_by_final_script_request(self):
        responses = [
            completion("The fix is complete. Here's the summary."),
            completion("printf 'final-script-success\\n'"),
        ]
        with completion_server(responses) as server:
            completed = self.run_agent(server)

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("final-script-success", completed.stdout)
        self.assertEqual(len(server.requests), 2)
        self.assertEqual(
            [request["tool_choice"] for request in server.requests],
            ["auto", "none"],
        )

    def test_image_fallback_is_used_by_repair_request(self):
        with completion_server(
            [
                {"_status": 400, "error": "image URL rejected"},
                completion("exit 1"),
                completion("printf 'repair-image-success\\n'"),
            ]
        ) as server:
            image_url = f"http://127.0.0.1:{server.server_port}/image.png"
            completed = self.run_agent(
                server,
                prompt=f"Fix it.\\n\\n![screenshot]({image_url})",
                OPENAI_MAX_REPAIR_ROUNDS="1",
            )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("repair-image-success", completed.stdout)
        self.assertGreaterEqual(len(server.requests), 3)
        for request in (server.requests[1], server.requests[2]):
            content = request["messages"][1]["content"]
            image_blocks = [
                block for block in content if block["type"] == "image_url"
            ]
            self.assertEqual(len(image_blocks), 1)
            self.assertTrue(
                image_blocks[0]["image_url"]["url"].startswith(
                    "data:image/png;base64,"
                )
            )


        prompt = (
            "Fix the login page.\n\n"
            '![screenshot](https://example.com/a.png)\n'
            '<img src="https://github.com/user-attachments/assets/abc123" />\n\n'
            "Make the fix."
        )
        with completion_server([completion("printf 'vision-success\\n'")]) as server:
            completed = self.run_agent(server, prompt=prompt)

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("vision-success", completed.stdout)
        user_message = server.requests[0]["messages"][1]
        self.assertEqual(user_message["role"], "user")
        content = user_message["content"]
        self.assertIsInstance(content, list)
        self.assertEqual(content[0]["type"], "text")
        image_urls = [
            block["image_url"]["url"] for block in content if block["type"] == "image_url"
        ]
        self.assertEqual(
            image_urls,
            [
                "https://example.com/a.png",
                "https://github.com/user-attachments/assets/abc123",
            ],
        )

    def test_prompt_without_images_keeps_plain_string_content(self):
        with completion_server([completion("printf 'plain-success\\n'")]) as server:
            completed = self.run_agent(server, prompt="No images here.")

        self.assertEqual(completed.returncode, 0, completed.stderr)
        user_message = server.requests[0]["messages"][1]
        self.assertIsInstance(user_message["content"], str)
        self.assertEqual(user_message["content"], "No images here.")

    def test_vision_disabled_keeps_image_urls_as_plain_text(self):
        prompt = "Fix it.\n\n![screenshot](https://example.com/a.png)"
        with completion_server([completion("printf 'no-vision-success\\n'")]) as server:
            completed = self.run_agent(
                server, prompt=prompt, OPENAI_VISION_ENABLED="false"
            )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        user_message = server.requests[0]["messages"][1]
        self.assertIsInstance(user_message["content"], str)
        self.assertIn("https://example.com/a.png", user_message["content"])

    def test_repair_round_retains_image_content_blocks(self):
        prompt = "Fix it.\n\n![screenshot](https://example.com/a.png)"
        responses = [
            completion("exit 1"),                 # generated script fails -> repair
            completion("printf 'repair-ok\\n'"),  # repair script succeeds
        ]
        with completion_server(responses) as server:
            completed = self.run_agent(
                server,
                prompt=prompt,
                OPENAI_MAX_REPAIR_ROUNDS="1",
            )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("repair-ok", completed.stdout)
        self.assertGreaterEqual(len(server.requests), 2)
        # The repair request (second request) must still carry the image blocks
        # so the model can see the issue screenshot while repairing.
        repair_request = server.requests[1]
        content = repair_request["messages"][1]["content"]
        self.assertIsInstance(content, list)
        image_urls = [
            block["image_url"]["url"]
            for block in content
            if block["type"] == "image_url"
        ]
        self.assertIn("https://example.com/a.png", image_urls)

    def test_standalone_bash_fence_is_unwrapped_and_runs_from_repo_root(self):
        script = "\n".join(
            [
                "```bash",
                'test "$(pwd)" != "$(cd -- "$(dirname -- "$0")" && pwd)"',
                "printf 'fenced-success\\n'",
                "```",
            ]
        )
        with completion_server([completion(script)]) as server:
            completed = self.run_agent(server)

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("fenced-success", completed.stdout)

    def test_fence_with_surrounding_prose_is_not_executed(self):
        response = "Here is the script:\n```bash\nprintf unsafe\n```"
        with completion_server([completion(response)]) as server:
            completed = self.run_agent(server)

        self.assertEqual(completed.returncode, 1)
        self.assertIn("one standalone bash, sh, or shell block", completed.stderr)
        self.assertNotIn("unsafe", completed.stdout)

    def test_tool_limit_switches_to_bounded_final_phase(self):
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
            completion(
                None,
                finish_reason="tool_calls",
                tool_calls=tool_calls,
                reasoning_content="inspect only the relevant file",
            ),
            completion("printf 'final-success\\n'"),
        ]
        with completion_server(responses) as server:
            completed = self.run_agent(
                server,
                OPENAI_MAX_TOOL_ROUNDS="1",
                OPENAI_MAX_FINAL_SCRIPT_ROUNDS="1",
            )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("ToolRoundsExhausted: phase=tool-exploration", completed.stderr)
        self.assertEqual([request["tool_choice"] for request in server.requests], ["auto", "none"])
        assistant_messages = [
            message
            for message in server.requests[1]["messages"]
            if message.get("role") == "assistant"
        ]
        self.assertEqual(
            assistant_messages[0]["reasoning_content"],
            "inspect only the relevant file",
        )

    def test_empty_final_responses_stop_and_preserve_provider_diagnostics(self):
        responses = [
            completion(None, finish_reason=None, reasoning_content="exploration-only"),
            completion(None, finish_reason=None, reasoning_content="final-reasoning-one"),
            completion(None, finish_reason=None, reasoning_content="final-reasoning-two"),
        ]
        with completion_server(responses) as server:
            completed = self.run_agent(
                server,
                OPENAI_MAX_TOOL_ROUNDS="1",
                OPENAI_MAX_FINAL_SCRIPT_ROUNDS="2",
            )

        self.assertEqual(completed.returncode, 1)
        self.assertIn(
            "FinalScriptRoundsExhausted: phase=final-script; used_rounds=2; limit=2",
            completed.stderr,
        )
        self.assertIn("final-reasoning-one", completed.stderr)
        self.assertIn("final-reasoning-two", completed.stderr)
        self.assertEqual(len(server.requests), 3)

    def test_inherited_session_keeps_current_prompt(self):
        # A repair round inherits the main task's session snapshot AND must still
        # receive its own prompt (the CI failure evidence). This was a real bug:
        # user_content was only appended when the session was empty, so a repair
        # round inherited the main task's memory with no idea what to fix now.
        snapshot = {
            "task_instructions": "create a broken file",
            "changed_files": ["internal/ci-repro/ci_repro.go"],
            "changed_stat": "1 file changed, 1 insertion(+)",
            "final_script": "cat > internal/ci-repro/ci_repro.go <<EOF ...",
        }
        with completion_server([completion("#!/bin/sh\necho repair-success\\n")]) as server:
            with tempfile.TemporaryDirectory() as temp_dir:
                session_path = Path(temp_dir) / "session.json"
                session_path.write_text(json.dumps(snapshot), encoding="utf-8")
                completed = self.run_agent(
                    server,
                    prompt="FIX THE CI FAILURES: goheader and typecheck on internal/ci-repro/ci_repro.go",
                    AI_FACTORY_SESSION_FILE=str(session_path),
                    AI_FACTORY_SESSION_READONLY="1",
                )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        messages = server.requests[0]["messages"]
        self.assertEqual(messages[0]["role"], "system")
        rendered = "".join(m.get("content", "") for m in messages)
        # Inherited memory is present ...
        self.assertIn("create a broken file", rendered)
        # ... and so is the current repair prompt (the regression this test pins).
        self.assertIn("FIX THE CI FAILURES: goheader and typecheck", rendered)
        self.assertEqual(messages[-1]["role"], "user")
        self.assertIn("FIX THE CI FAILURES: goheader and typecheck", messages[-1]["content"])


if __name__ == "__main__":
    unittest.main()
