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

import subprocess
import os
import sys
import unittest
from pathlib import Path

from agent_config import DEFAULTS, InvalidAgentConfiguration, load_config


SCRIPT_DIR = Path(__file__).resolve().parent
AGENT = SCRIPT_DIR / "ai-factory-agent.py"


class TestLoadConfig(unittest.TestCase):
    def base_env(self, **overrides):
        env = {
            "OPENAI_API_KEY": "test-key",
        }
        for name, value in DEFAULTS.items():
            if name != "OPENAI_API_KEY":
                env[name] = value
        env.update(overrides)
        return env

    def test_default_configuration_valid(self):
        config = load_config(self.base_env())
        self.assertEqual(config.api_key, "test-key")
        self.assertEqual(config.base_url, "https://api.openai.com/v1")
        self.assertEqual(config.model, "gpt-4.1")
        self.assertEqual(config.temperature, 1.0)
        self.assertEqual(config.max_tokens, 48000)
        self.assertEqual(config.max_tool_rounds, 40)
        self.assertEqual(config.max_final_script_rounds, 5)
        self.assertEqual(config.max_repair_rounds, 3)
        self.assertEqual(config.total_timeout_seconds, 1800.0)
        self.assertEqual(config.exploration_request_timeout_seconds, 180.0)
        self.assertEqual(config.final_request_timeout_seconds, 90.0)
        self.assertEqual(config.repair_request_timeout_seconds, 90.0)
        self.assertTrue(config.vision_enabled)

    def test_vision_disabled(self):
        config = load_config(self.base_env(OPENAI_VISION_ENABLED="false"))
        self.assertFalse(config.vision_enabled)

    def test_vision_accepts_boolean_variants(self):
        for raw in ("false", "0", "no", "off"):
            config = load_config(self.base_env(OPENAI_VISION_ENABLED=raw))
            self.assertFalse(config.vision_enabled, raw)
        for raw in ("true", "1", "yes", "on"):
            config = load_config(self.base_env(OPENAI_VISION_ENABLED=raw))
            self.assertTrue(config.vision_enabled, raw)

    def test_vision_invalid_value_is_rejected(self):
        with self.assertRaises(InvalidAgentConfiguration):
            load_config(self.base_env(OPENAI_VISION_ENABLED="maybe"))

    def test_api_key_required(self):
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_API_KEY=""))
        self.assertIn("OPENAI_API_KEY", str(ctx.exception))

    def test_api_key_not_printed(self):
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_API_KEY=""))
        self.assertNotIn("test-key", str(ctx.exception))

    def test_model_non_empty(self):
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_MODEL="  "))
        self.assertIn("OPENAI_MODEL", str(ctx.exception))

    def test_base_url_requires_http_scheme(self):
        for invalid in ("ftp://example.com/v1", "example.com/v1", "/v1"):
            with self.subTest(url=invalid):
                with self.assertRaises(InvalidAgentConfiguration) as ctx:
                    load_config(self.base_env(OPENAI_BASE_URL=invalid))
                self.assertIn("OPENAI_BASE_URL", str(ctx.exception))

    def test_base_url_trailing_slash_removed(self):
        config = load_config(self.base_env(OPENAI_BASE_URL="https://example.com/v1/"))
        self.assertEqual(config.base_url, "https://example.com/v1")

    def test_temperature_must_be_finite(self):
        for invalid in ("nan", "inf", "-inf", "not-a-number"):
            with self.subTest(value=invalid):
                with self.assertRaises(InvalidAgentConfiguration) as ctx:
                    load_config(self.base_env(OPENAI_TEMPERATURE=invalid))
                self.assertIn("OPENAI_TEMPERATURE", str(ctx.exception))

    def test_max_tokens_valid_integer_positive(self):
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_MAX_TOKENS="0"))
        self.assertIn("OPENAI_MAX_TOKENS", str(ctx.exception))
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_MAX_TOKENS="abc"))
        self.assertIn("OPENAI_MAX_TOKENS", str(ctx.exception))

    def test_round_limits_valid_integers(self):
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_MAX_TOOL_ROUNDS="-1"))
        self.assertIn("OPENAI_MAX_TOOL_ROUNDS", str(ctx.exception))
        with self.assertRaises(InvalidAgentConfiguration) as ctx:
            load_config(self.base_env(OPENAI_MAX_REPAIR_ROUNDS="-5"))
        self.assertIn("OPENAI_MAX_REPAIR_ROUNDS", str(ctx.exception))

    def test_timeouts_positive_finite(self):
        for name in (
            "OPENAI_TOTAL_TIMEOUT_SECONDS",
            "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS",
            "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS",
            "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS",
        ):
            for invalid in ("0", "-1", "inf", "nan"):
                with self.subTest(name=name, value=invalid):
                    with self.assertRaises(InvalidAgentConfiguration) as ctx:
                        load_config(self.base_env(**{name: invalid}))
                    self.assertIn(name, str(ctx.exception))

    def test_current_kimi_configuration_is_valid(self):
        config = load_config(
            self.base_env(
                OPENAI_BASE_URL="https://api.kimi.com/coding/v1",
                OPENAI_MODEL="kimi-for-coding",
                OPENAI_TEMPERATURE="1",
                OPENAI_MAX_TOKENS="32768",
                OPENAI_MAX_TOOL_ROUNDS="32",
                OPENAI_MAX_FINAL_SCRIPT_ROUNDS="3",
                OPENAI_MAX_REPAIR_ROUNDS="3",
                OPENAI_TOTAL_TIMEOUT_SECONDS="1800",
                OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS="180",
                OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS="120",
                OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS="180",
            )
        )
        self.assertEqual(config.base_url, "https://api.kimi.com/coding/v1")
        self.assertEqual(config.model, "kimi-for-coding")
        self.assertEqual(config.max_tokens, 32768)
        self.assertEqual(config.max_tool_rounds, 32)
        self.assertEqual(config.final_request_timeout_seconds, 120)
        self.assertEqual(config.repair_request_timeout_seconds, 180)


class TestCheckConfig(unittest.TestCase):
    def run_check(self, env):
        child_env = {"PYTHONDONTWRITEBYTECODE": "1"}
        child_env.update(env)
        return subprocess.run(
            [sys.executable, str(AGENT), "--check-config"],
            env=child_env,
            capture_output=True,
            text=True,
        )

    def test_check_config_exits_zero_for_valid_config(self):
        env = {
            "OPENAI_API_KEY": "valid-key",
            "OPENAI_BASE_URL": "https://api.openai.com/v1",
            "OPENAI_MODEL": "gpt-4.1",
            "OPENAI_TEMPERATURE": "1",
            "OPENAI_MAX_TOKENS": "48000",
            "OPENAI_MAX_TOOL_ROUNDS": "40",
            "OPENAI_MAX_FINAL_SCRIPT_ROUNDS": "5",
            "OPENAI_MAX_REPAIR_ROUNDS": "3",
            "OPENAI_TOTAL_TIMEOUT_SECONDS": "1800",
            "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS": "180",
            "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS": "90",
            "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS": "90",
        }
        result = self.run_check(env)
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertIn("configuration is valid", result.stderr)

    def test_check_config_exits_two_without_traceback(self):
        env = {
            "OPENAI_API_KEY": "valid-key",
            "OPENAI_MAX_TOKENS": "not-a-number",
        }
        result = self.run_check(env)
        self.assertEqual(result.returncode, 2)
        self.assertIn("InvalidAgentConfiguration", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertNotIn("valid-key", result.stderr)

    def test_check_config_does_not_expose_api_key(self):
        env = {
            "OPENAI_API_KEY": "super-secret-key-12345",
            "OPENAI_MAX_TOKENS": "abc",
        }
        result = self.run_check(env)
        self.assertEqual(result.returncode, 2)
        combined = result.stdout + result.stderr
        self.assertNotIn("super-secret-key-12345", combined)


LAUNCHER = SCRIPT_DIR / "ai-factory-agent"


class TestLauncherCheckConfig(unittest.TestCase):
    """Launcher-level regression tests for the bash runtime wrapper."""

    def run_launcher_check(self, env):
        child_env = {
            "PATH": os.environ.get("PATH", ""),
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        child_env.update(env)
        return subprocess.run(
            [str(LAUNCHER), "openai-compatible", "--check-config"],
            env=child_env,
            capture_output=True,
            text=True,
        )

    def test_check_config_missing_api_key(self):
        result = self.run_launcher_check({})
        combined = result.stdout + result.stderr
        self.assertEqual(
            result.returncode,
            2,
            f"expected exit status 2, got {result.returncode}: {combined}",
        )
        self.assertIn("InvalidAgentConfiguration", combined)
        self.assertIn("OPENAI_API_KEY", combined)
        self.assertNotIn("Traceback", combined)
        self.assertNotIn("FactoryTask", combined)
        self.assertNotIn("--- RUN:", combined)
        self.assertNotIn("--- TOOL:", combined)


if __name__ == "__main__":
    unittest.main()
