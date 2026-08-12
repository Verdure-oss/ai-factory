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

"""Testable, offline configuration loader for the OpenAI-compatible agent."""

import math
import os
from urllib.parse import urlparse


class InvalidAgentConfiguration(ValueError):
    """A stable error raised when agent configuration cannot be validated."""


class AgentConfiguration:
    """Validated runtime configuration for one OpenAI-compatible agent run."""

    def __init__(
        self,
        api_key,
        base_url,
        model,
        temperature,
        max_tokens,
        max_tool_rounds,
        max_final_script_rounds,
        max_repair_rounds,
        total_timeout_seconds,
        exploration_request_timeout_seconds,
        final_request_timeout_seconds,
        repair_request_timeout_seconds,
        vision_enabled,
    ):
        self.api_key = api_key
        self.base_url = base_url
        self.model = model
        self.temperature = temperature
        self.max_tokens = max_tokens
        self.max_tool_rounds = max_tool_rounds
        self.max_final_script_rounds = max_final_script_rounds
        self.max_repair_rounds = max_repair_rounds
        self.total_timeout_seconds = total_timeout_seconds
        self.exploration_request_timeout_seconds = exploration_request_timeout_seconds
        self.final_request_timeout_seconds = final_request_timeout_seconds
        self.repair_request_timeout_seconds = repair_request_timeout_seconds
        self.vision_enabled = vision_enabled


DEFAULTS = {
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
    "OPENAI_VISION_ENABLED": "true",
}


def _required_env(name, environ):
    value = environ.get(name, "").strip()
    if not value:
        raise InvalidAgentConfiguration(f"{name} is required")
    return value


def _parse_float(name, raw):
    try:
        value = float(raw)
    except ValueError as exc:
        raise InvalidAgentConfiguration(f"{name} must be a finite number: {exc}")
    if not math.isfinite(value):
        raise InvalidAgentConfiguration(f"{name} must be finite, got {raw}")
    return value


def _parse_int(name, raw, minimum=None):
    try:
        value = int(raw)
    except ValueError as exc:
        raise InvalidAgentConfiguration(f"{name} must be a valid integer: {exc}")
    if minimum is not None and value < minimum:
        raise InvalidAgentConfiguration(
            f"{name} must be at least {minimum}, got {value}"
        )
    return value


def _parse_positive_float(name, raw):
    value = _parse_float(name, raw)
    if value <= 0:
        raise InvalidAgentConfiguration(
            f"{name} must be greater than zero, got {value}"
        )
    return value


def _parse_bool(name, raw):
    value = raw.strip().lower()
    if value in ("true", "1", "yes", "on"):
        return True
    if value in ("false", "0", "no", "off"):
        return False
    raise InvalidAgentConfiguration(
        f"{name} must be a boolean (true/false), got {raw}"
    )


def _validate_base_url(name, value):
    parsed = urlparse(value)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise InvalidAgentConfiguration(
            f"{name} must be an absolute HTTP or HTTPS URL, got {value}"
        )
    return value.rstrip("/")


def load_config(environ=None):
    """Load and validate the complete OpenAI-compatible agent configuration.

    Uses os.environ unless an optional ``environ`` mapping is supplied.  The
    loader performs no network I/O, does not read a prompt, and does not
    execute any generated scripts.
    """
    if environ is None:
        environ = os.environ

    api_key = _required_env("OPENAI_API_KEY", environ)
    base_url = _validate_base_url(
        "OPENAI_BASE_URL", environ.get("OPENAI_BASE_URL", DEFAULTS["OPENAI_BASE_URL"]).strip()
    )
    model = environ.get("OPENAI_MODEL", DEFAULTS["OPENAI_MODEL"]).strip()
    if not model:
        raise InvalidAgentConfiguration("OPENAI_MODEL must be non-empty")

    temperature = _parse_float(
        "OPENAI_TEMPERATURE",
        environ.get("OPENAI_TEMPERATURE", DEFAULTS["OPENAI_TEMPERATURE"]).strip(),
    )
    max_tokens = _parse_int(
        "OPENAI_MAX_TOKENS",
        environ.get("OPENAI_MAX_TOKENS", DEFAULTS["OPENAI_MAX_TOKENS"]).strip(),
        minimum=1,
    )
    max_tool_rounds = _parse_int(
        "OPENAI_MAX_TOOL_ROUNDS",
        environ.get("OPENAI_MAX_TOOL_ROUNDS", DEFAULTS["OPENAI_MAX_TOOL_ROUNDS"]).strip(),
        minimum=0,
    )
    max_final_script_rounds = _parse_int(
        "OPENAI_MAX_FINAL_SCRIPT_ROUNDS",
        environ.get(
            "OPENAI_MAX_FINAL_SCRIPT_ROUNDS",
            DEFAULTS["OPENAI_MAX_FINAL_SCRIPT_ROUNDS"],
        ).strip(),
        minimum=0,
    )
    max_repair_rounds = _parse_int(
        "OPENAI_MAX_REPAIR_ROUNDS",
        environ.get("OPENAI_MAX_REPAIR_ROUNDS", DEFAULTS["OPENAI_MAX_REPAIR_ROUNDS"]).strip(),
        minimum=0,
    )
    total_timeout_seconds = _parse_positive_float(
        "OPENAI_TOTAL_TIMEOUT_SECONDS",
        environ.get(
            "OPENAI_TOTAL_TIMEOUT_SECONDS", DEFAULTS["OPENAI_TOTAL_TIMEOUT_SECONDS"]
        ).strip(),
    )
    exploration_request_timeout_seconds = _parse_positive_float(
        "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS",
        environ.get(
            "OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS",
            DEFAULTS["OPENAI_EXPLORATION_REQUEST_TIMEOUT_SECONDS"],
        ).strip(),
    )
    final_request_timeout_seconds = _parse_positive_float(
        "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS",
        environ.get(
            "OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS",
            DEFAULTS["OPENAI_FINAL_REQUEST_TIMEOUT_SECONDS"],
        ).strip(),
    )
    repair_request_timeout_seconds = _parse_positive_float(
        "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS",
        environ.get(
            "OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS",
            DEFAULTS["OPENAI_REPAIR_REQUEST_TIMEOUT_SECONDS"],
        ).strip(),
    )
    vision_enabled = _parse_bool(
        "OPENAI_VISION_ENABLED",
        environ.get(
            "OPENAI_VISION_ENABLED", DEFAULTS["OPENAI_VISION_ENABLED"]
        ).strip(),
    )

    return AgentConfiguration(
        api_key=api_key,
        base_url=base_url,
        model=model,
        temperature=temperature,
        max_tokens=max_tokens,
        max_tool_rounds=max_tool_rounds,
        max_final_script_rounds=max_final_script_rounds,
        max_repair_rounds=max_repair_rounds,
        total_timeout_seconds=total_timeout_seconds,
        exploration_request_timeout_seconds=exploration_request_timeout_seconds,
        final_request_timeout_seconds=final_request_timeout_seconds,
        repair_request_timeout_seconds=repair_request_timeout_seconds,
        vision_enabled=vision_enabled,
    )
