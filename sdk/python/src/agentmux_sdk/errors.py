"""Exception hierarchy for the AgentMux SDK.

Every error raised by the SDK derives from :class:`AgentMuxError` so callers
can catch one type. HTTP-status-specific subclasses map the contract's error
semantics (401 unauthorized, 404 not found, 409 busy/disabled, ...).
"""

from __future__ import annotations


class AgentMuxError(Exception):
    """Base class for all AgentMux SDK errors."""


class AgentMuxUnreachable(AgentMuxError):
    """The AgentMux daemon could not be reached at all."""


class AgentMuxIncompatible(AgentMuxError):
    """The server's contract or binary version is outside the supported range."""


class AgentMuxAPIError(AgentMuxError):
    """An HTTP error response from the AgentMux API."""

    def __init__(self, status_code: int, message: str) -> None:
        super().__init__(f"AgentMux API error {status_code}: {message}")
        self.status_code = status_code
        self.message = message


class AgentMuxUnauthorized(AgentMuxAPIError):
    """Missing or invalid bridge token / console session (HTTP 401)."""

    def __init__(self, message: str = "missing or invalid bridge token") -> None:
        super().__init__(401, message)


class AgentMuxNotFound(AgentMuxAPIError):
    """Target agent/project/resource does not exist (HTTP 404)."""

    def __init__(self, message: str = "not found") -> None:
        super().__init__(404, message)


class AgentMuxBusy(AgentMuxAPIError):
    """Target disabled or the conversation already has an active invocation (HTTP 409)."""

    def __init__(self, message: str = "conversation already has an active invocation") -> None:
        super().__init__(409, message)


def error_for_status(status_code: int, message: str) -> AgentMuxAPIError:
    if status_code == 401:
        return AgentMuxUnauthorized(message)
    if status_code == 404:
        return AgentMuxNotFound(message)
    if status_code == 409:
        return AgentMuxBusy(message)
    return AgentMuxAPIError(status_code, message)
