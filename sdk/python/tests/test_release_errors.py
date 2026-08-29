"""Release lookup failures must say what an operator can do about them."""

from __future__ import annotations

import time

import httpx
import pytest

from agentmux_sdk import AgentMuxReleaseError
from agentmux_sdk.release import latest_release


def client_returning(status: int, headers: dict[str, str] | None = None) -> httpx.Client:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(status, headers=headers or {}, json={})

    return httpx.Client(transport=httpx.MockTransport(handler))


def test_rate_limit_reports_the_wait_instead_of_the_exception_type() -> None:
    reset_at = int(time.time()) + 25 * 60
    with client_returning(
        403, {"x-ratelimit-remaining": "0", "x-ratelimit-reset": str(reset_at)}
    ) as client:
        with pytest.raises(AgentMuxReleaseError) as excinfo:
            latest_release(client=client)
    message = str(excinfo.value)
    assert "rate limit" in message
    assert "25 minute" in message
    # The old message leaked only the exception class name, which told the
    # operator nothing actionable.
    assert "HTTPStatusError" not in message


def test_rate_limit_without_a_reset_header_still_explains_itself() -> None:
    with client_returning(429, {"x-ratelimit-remaining": "0"}) as client:
        with pytest.raises(AgentMuxReleaseError) as excinfo:
            latest_release(client=client)
    assert "rate limit" in str(excinfo.value)


def test_missing_release_is_distinguished_from_a_rate_limit() -> None:
    with client_returning(404) as client:
        with pytest.raises(AgentMuxReleaseError) as excinfo:
            latest_release(client=client)
    assert "no published release" in str(excinfo.value)


def test_other_status_codes_report_the_code() -> None:
    with client_returning(500) as client:
        with pytest.raises(AgentMuxReleaseError) as excinfo:
            latest_release(client=client)
    assert "HTTP 500" in str(excinfo.value)


def test_forbidden_without_rate_limit_headers_is_not_called_a_rate_limit() -> None:
    with client_returning(403) as client:
        with pytest.raises(AgentMuxReleaseError) as excinfo:
            latest_release(client=client)
    assert "HTTP 403" in str(excinfo.value)
