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

"""Execution budgets and bounded OpenAI-compatible HTTP requests."""

import json
import socket
import sys
import time
import urllib.error
import urllib.request


class AgentBudgetError(RuntimeError):
    """A stable, phase-aware terminal error for a bounded agent operation."""

    def __init__(self, reason, phase, detail, diagnostics=""):
        self.reason = str(reason)
        self.phase = str(phase)
        self.detail = str(detail)
        self.diagnostics = str(diagnostics or "")
        super().__init__(f"{self.reason}: phase={self.phase}; {self.detail}")


class ExecutionDeadline:
    """A monotonic deadline shared by every phase of one agent run."""

    def __init__(self, total_timeout_seconds, clock=time.monotonic):
        total_timeout_seconds = float(total_timeout_seconds)
        if total_timeout_seconds <= 0:
            raise ValueError("total_timeout_seconds must be greater than zero")
        self.total_timeout_seconds = total_timeout_seconds
        self._clock = clock
        self._started_at = clock()

    def elapsed_seconds(self):
        return max(0.0, self._clock() - self._started_at)

    def remaining_seconds(self):
        return max(0.0, self.total_timeout_seconds - self.elapsed_seconds())

    def exhausted_error(self, phase, diagnostics=""):
        return AgentBudgetError(
            "TotalExecutionDeadlineExceeded",
            phase,
            "elapsed_seconds=%.3f; total_timeout_seconds=%.3f"
            % (self.elapsed_seconds(), self.total_timeout_seconds),
            diagnostics=diagnostics,
        )

    def ensure_remaining(self, phase):
        if self.remaining_seconds() <= 0:
            raise self.exhausted_error(phase)

    def request_timeout(self, phase, configured_timeout_seconds):
        configured_timeout_seconds = float(configured_timeout_seconds)
        if configured_timeout_seconds <= 0:
            raise ValueError("configured_timeout_seconds must be greater than zero")
        self.ensure_remaining(phase)
        return min(configured_timeout_seconds, self.remaining_seconds())


class PhaseRoundBudget:
    """Track a stable round limit for one named agent phase."""

    def __init__(self, phase, limit, reason):
        limit = int(limit)
        if limit < 0:
            raise ValueError("phase round limit must not be negative")
        self.phase = str(phase)
        self.limit = limit
        self.reason = str(reason)
        self.used = 0

    def next_round(self):
        if self.used >= self.limit:
            raise self.exhausted_error()
        self.used += 1
        return self.used

    def exhausted_error(self):
        return AgentBudgetError(
            self.reason,
            self.phase,
            f"used_rounds={self.used}; limit={self.limit}",
        )


def _is_timeout_error(error):
    if isinstance(error, (TimeoutError, socket.timeout)):
        return True
    if isinstance(error, urllib.error.URLError):
        return isinstance(error.reason, (TimeoutError, socket.timeout))
    return False


def _retry_delay(attempt):
    return float(2 * attempt)


def _sleep_for_retry(deadline, phase, attempt, sleeper, diagnostics=""):
    delay = _retry_delay(attempt)
    remaining = deadline.remaining_seconds()
    if remaining <= delay:
        raise AgentBudgetError(
            "TotalExecutionDeadlineExceeded",
            phase,
            "remaining_seconds=%.3f; retry_delay_seconds=%.3f; "
            "total_timeout_seconds=%.3f"
            % (remaining, delay, deadline.total_timeout_seconds),
            diagnostics=diagnostics,
        )
    sleeper(delay)


def post_chat_completion(
    request,
    base_url,
    api_key,
    phase,
    request_timeout_seconds,
    deadline,
    max_attempts=2,
    opener=urllib.request.urlopen,
    sleeper=time.sleep,
):
    """Post one bounded chat completion request and return decoded JSON."""
    max_attempts = int(max_attempts)
    if max_attempts <= 0:
        raise ValueError("max_attempts must be greater than zero")

    data = json.dumps(request).encode("utf-8")
    response_body = b""
    attempt_diagnostics = []
    for attempt in range(1, max_attempts + 1):
        timeout = deadline.request_timeout(phase, request_timeout_seconds)
        http_request = urllib.request.Request(
            f"{base_url}/chat/completions",
            data=data,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
                "User-Agent": "ai-factory-agent/1.0",
            },
            method="POST",
        )
        try:
            with opener(http_request, timeout=timeout) as response:
                response_body = response.read()
            break
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            attempt_diagnostics.append(
                f"attempt={attempt}/{max_attempts}; HTTP_status={exc.code}\n{detail}"
            )
            retryable = exc.code in (408, 409, 425, 429, 500, 502, 503, 504)
            if retryable and attempt < max_attempts:
                print(
                    "OpenAI-compatible API request failed: "
                    f"phase={phase}; HTTP {exc.code}; retrying "
                    f"({attempt}/{max_attempts})",
                    file=sys.stderr,
                )
                _sleep_for_retry(
                    deadline,
                    phase,
                    attempt,
                    sleeper,
                    diagnostics="\n\n".join(attempt_diagnostics),
                )
                continue
            raise AgentBudgetError(
                "ModelRequestFailed",
                phase,
                f"attempts={attempt}/{max_attempts}; HTTP_status={exc.code}",
                diagnostics="\n\n".join(attempt_diagnostics),
            ) from exc
        except (urllib.error.URLError, TimeoutError, ConnectionError, socket.timeout) as exc:
            timed_out = _is_timeout_error(exc)
            attempt_diagnostics.append(
                f"attempt={attempt}/{max_attempts}; error={exc}"
            )
            if attempt < max_attempts:
                print(
                    "OpenAI-compatible API request failed: "
                    f"phase={phase}; {exc}; retrying ({attempt}/{max_attempts})",
                    file=sys.stderr,
                )
                _sleep_for_retry(
                    deadline,
                    phase,
                    attempt,
                    sleeper,
                    diagnostics="\n\n".join(attempt_diagnostics),
                )
                continue
            reason = "ModelRequestTimeout" if timed_out else "ModelRequestFailed"
            raise AgentBudgetError(
                reason,
                phase,
                "attempts=%d/%d; request_timeout_seconds=%.3f; "
                "elapsed_seconds=%.3f; total_timeout_seconds=%.3f; last_error=%s"
                % (
                    attempt,
                    max_attempts,
                    float(request_timeout_seconds),
                    deadline.elapsed_seconds(),
                    deadline.total_timeout_seconds,
                    exc,
                ),
                diagnostics="\n\n".join(attempt_diagnostics),
            ) from exc

    try:
        return json.loads(response_body)
    except json.JSONDecodeError as exc:
        provider_response = response_body.decode("utf-8", errors="replace")
        raise AgentBudgetError(
            "InvalidModelResponse",
            phase,
            f"response was not valid JSON: {exc}",
            diagnostics=provider_response,
        ) from exc
