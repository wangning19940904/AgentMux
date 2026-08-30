"""Sync and async clients for the AgentMux public integration contract.

Usage::

    from agentmux_sdk import AgentMuxClient

    client = AgentMuxClient(token="...")
    report = client.health()
    for event in client.invoke_stream(agent_id="agent-abc", input="run the tests"):
        if event.type == "output":
            render(event.text)  # full snapshot: replace, do not append
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator, Iterator
from pathlib import Path
from typing import Any

import httpx

from ._sse import aiter_sse_payloads, iter_sse_payloads
from .detect import looks_installed
from .errors import (
    AgentMuxUnreachable,
    error_for_status,
)
from .models import (
    SUPPORTED_CONTRACT_MAJOR,
    AgentInstance,
    Attachment,
    Capabilities,
    Channel,
    ConsoleSession,
    TenantRegistration,
    HealthReport,
    HealthState,
    InvocationEvent,
    InvocationResult,
    IntegrationSnapshot,
    Orchestration,
    OrchestrationTask,
    TenancySelf,
    Trigger,
    contract_major,
    version_key,
)

DEFAULT_BASE_URL = "http://127.0.0.1:8765"
DEFAULT_TIMEOUT = 10.0
# Synchronous invocations wait for a full Agent turn; keep this generous.
DEFAULT_INVOKE_TIMEOUT = 600.0

_LEGACY_PROBE_PATHS = ("/api/v1/agent-instances", "/api/v1/channels")


def _auth_headers(token: str | None) -> dict[str, str]:
    return {"Authorization": f"Bearer {token}"} if token else {}


def _error_message(response: httpx.Response) -> str:
    try:
        payload = response.json()
        if isinstance(payload, dict) and isinstance(payload.get("error"), str):
            return payload["error"]
    except ValueError:
        pass
    return response.text.strip() or response.reason_phrase


def _raise_for_response(response: httpx.Response) -> None:
    if response.status_code >= 400:
        raise error_for_status(response.status_code, _error_message(response))


def _invocation_payload(
    *,
    input: str,
    agent_id: str,
    conversation_id: str | None,
    attachments: list[Attachment] | None,
    output_schema: dict[str, Any] | None,
) -> dict[str, Any]:
    if not agent_id.strip():
        raise ValueError("agent_id is required")
    payload: dict[str, Any] = {"agent_id": agent_id, "input": input}
    if conversation_id:
        payload["conversation_id"] = conversation_id
    if attachments:
        payload["attachments"] = [attachment.to_payload() for attachment in attachments]
    if output_schema:
        payload["output_schema"] = output_schema
    return payload


def _health_from_capabilities(
    capabilities: Capabilities,
    *,
    min_version: str | None,
    console_url: str,
) -> HealthReport:
    major = contract_major(capabilities.contract_version)
    if major is not None and major != SUPPORTED_CONTRACT_MAJOR:
        return HealthReport(
            state=HealthState.INCOMPATIBLE,
            message=(
                f"AgentMux speaks contract {capabilities.contract_version}; "
                f"this SDK supports major {SUPPORTED_CONTRACT_MAJOR}"
            ),
            version=capabilities.version or None,
            contract_version=capabilities.contract_version or None,
            console_url=console_url,
            capabilities=capabilities,
        )
    if min_version and not _meets_min_version(capabilities.version, min_version):
        return HealthReport(
            state=HealthState.INCOMPATIBLE,
            message=f"AgentMux {min_version} or newer is required",
            version=capabilities.version or None,
            contract_version=capabilities.contract_version or None,
            console_url=console_url,
            capabilities=capabilities,
        )
    return HealthReport(
        state=HealthState.READY,
        message="AgentMux is ready",
        version=capabilities.version or None,
        contract_version=capabilities.contract_version or None,
        console_url=console_url,
        capabilities=capabilities,
    )


def _meets_min_version(current: str | None, minimum: str) -> bool:
    current_key = version_key(current)
    minimum_key = version_key(minimum)
    return bool(current_key and minimum_key and current_key >= minimum_key)


def _legacy_health(
    status_payload: Any,
    probe_payloads: list[Any],
    *,
    min_version: str | None,
    console_url: str,
) -> HealthReport:
    """Health evaluation for pre-capabilities servers (contract < 1.0)."""
    version = None
    if isinstance(status_payload, dict):
        version = str(status_payload.get("version") or "").strip() or None
        if status_payload.get("ok") is not True:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message="AgentMux status response is not healthy",
                version=version,
                console_url=console_url,
            )
    for payload in probe_payloads:
        if not isinstance(payload, list):
            return HealthReport(
                state=HealthState.INCOMPATIBLE,
                message="AgentMux list contract mismatch",
                version=version,
                console_url=console_url,
            )
    if min_version and not _meets_min_version(version, min_version):
        return HealthReport(
            state=HealthState.INCOMPATIBLE,
            message=f"AgentMux {min_version} or newer is required",
            version=version,
            console_url=console_url,
        )
    return HealthReport(
        state=HealthState.READY,
        message="AgentMux is ready",
        version=version,
        console_url=console_url,
    )


class AgentMuxClient:
    """Synchronous client. Thread-safe for concurrent requests via httpx."""

    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        *,
        token: str | None = None,
        console_url: str | None = None,
        timeout: float = DEFAULT_TIMEOUT,
        invoke_timeout: float = DEFAULT_INVOKE_TIMEOUT,
        min_version: str | None = None,
        install_locations: tuple[Path, ...] = (),
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.console_url = (console_url or self.base_url).rstrip("/")
        self.min_version = min_version
        self.install_locations = install_locations
        self._invoke_timeout = invoke_timeout
        self._client = httpx.Client(
            base_url=self.base_url,
            headers=_auth_headers(token),
            timeout=timeout,
            transport=transport,
        )
        self.agents = AgentsResource(self)
        self.channels = ChannelsResource(self)
        self.triggers = TriggersResource(self)
        self.orchestrations = OrchestrationsResource(self)
        self.integration = IntegrationResource(self)
        self.console = ConsoleResource(self)
        self.tenancy = TenancyResource(self)

    # -- lifecycle -----------------------------------------------------------

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "AgentMuxClient":
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    # -- plumbing ------------------------------------------------------------

    def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        try:
            response = self._client.request(method, path, **kwargs)
        except httpx.ConnectError as exc:
            raise AgentMuxUnreachable(f"AgentMux is unreachable at {self.base_url}") from exc
        except httpx.TimeoutException as exc:
            raise AgentMuxUnreachable(f"AgentMux timed out at {self.base_url}") from exc
        _raise_for_response(response)
        return response

    # -- discovery -----------------------------------------------------------

    def capabilities(self) -> Capabilities:
        return Capabilities.from_dict(self._request("GET", "/api/v1/capabilities").json())

    def runtimes(self) -> list[str]:
        return list(self._request("GET", "/api/v1/agents").json() or [])

    def platforms(self) -> list[str]:
        return list(self._request("GET", "/api/v1/platforms").json() or [])

    def status(self) -> dict[str, Any]:
        return self._request("GET", "/api/v1/status").json()

    def health(self) -> HealthReport:
        """Aggregate reachability, auth, contract and version into 5 states.

        Never raises for expected conditions: connection failures, bad
        credentials and incompatible servers are reported as states.
        """
        try:
            response = self._client.get("/api/v1/capabilities")
        except (httpx.ConnectError, httpx.TimeoutException):
            return self._offline_report()
        except httpx.HTTPError as exc:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux API check failed: {type(exc).__name__}",
                console_url=self.console_url,
            )
        if response.status_code == 401:
            return HealthReport(
                state=HealthState.UNAUTHORIZED,
                message="AgentMux rejected the bridge token",
                console_url=self.console_url,
            )
        if response.status_code == 404:
            return self._legacy_probe()
        if response.status_code >= 400:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux capabilities returned HTTP {response.status_code}",
                console_url=self.console_url,
            )
        try:
            capabilities = Capabilities.from_dict(response.json())
        except ValueError:
            return HealthReport(
                state=HealthState.INCOMPATIBLE,
                message="AgentMux capabilities response is not valid JSON",
                console_url=self.console_url,
            )
        return _health_from_capabilities(
            capabilities, min_version=self.min_version, console_url=self.console_url
        )

    def _legacy_probe(self) -> HealthReport:
        try:
            status_response = self._client.get("/api/v1/status")
            if status_response.status_code == 401:
                return HealthReport(
                    state=HealthState.UNAUTHORIZED,
                    message="AgentMux rejected the bridge token",
                    console_url=self.console_url,
                )
            status_response.raise_for_status()
            probes = []
            for path in _LEGACY_PROBE_PATHS:
                probe = self._client.get(path)
                probe.raise_for_status()
                probes.append(probe.json())
            return _legacy_health(
                status_response.json(),
                probes,
                min_version=self.min_version,
                console_url=self.console_url,
            )
        except (httpx.ConnectError, httpx.TimeoutException):
            return self._offline_report()
        except (httpx.HTTPError, ValueError) as exc:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux API check failed: {type(exc).__name__}",
                console_url=self.console_url,
            )

    def _offline_report(self) -> HealthReport:
        if looks_installed(self.install_locations):
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message="AgentMux is installed but its API is unreachable",
                console_url=self.console_url,
            )
        return HealthReport(
            state=HealthState.MISSING,
            message="AgentMux is not installed or running",
            console_url=self.console_url,
        )

    # -- invocations ---------------------------------------------------------

    def invoke(
        self,
        *,
        input: str,
        agent_id: str,
        conversation_id: str | None = None,
        attachments: list[Attachment] | None = None,
        output_schema: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> InvocationResult:
        payload = _invocation_payload(
            input=input,
            agent_id=agent_id,
            conversation_id=conversation_id,
            attachments=attachments,
            output_schema=output_schema,
        )
        response = self._request(
            "POST",
            "/api/v1/invocations",
            json=payload,
            timeout=timeout or self._invoke_timeout,
        )
        return InvocationResult.from_dict(response.json())

    def invoke_stream(
        self,
        *,
        input: str,
        agent_id: str,
        conversation_id: str | None = None,
        attachments: list[Attachment] | None = None,
        output_schema: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> Iterator[InvocationEvent]:
        """Stream invocation events over SSE.

        The ``text`` of ``output``/``thinking`` events is a full snapshot;
        replace what you displayed before instead of appending. Keepalive
        comments are filtered out by the SDK.
        """
        payload = _invocation_payload(
            input=input,
            agent_id=agent_id,
            conversation_id=conversation_id,
            attachments=attachments,
            output_schema=output_schema,
        )
        request_timeout = httpx.Timeout(timeout or self._invoke_timeout, connect=10.0)
        try:
            with self._client.stream(
                "POST",
                "/api/v1/invocations/stream",
                json=payload,
                headers={"Accept": "text/event-stream"},
                timeout=request_timeout,
            ) as response:
                if response.status_code >= 400:
                    response.read()
                    _raise_for_response(response)
                for event_payload in iter_sse_payloads(response.iter_text()):
                    yield InvocationEvent.from_dict(event_payload)
        except httpx.ConnectError as exc:
            raise AgentMuxUnreachable(f"AgentMux is unreachable at {self.base_url}") from exc

    # -- messaging & usage ---------------------------------------------------

    def send(
        self,
        *,
        text: str,
        channel_id: str,
        conversation_key: str,
        images: list[str] | None = None,
        files: list[str] | None = None,
    ) -> None:
        payload: dict[str, Any] = {"text": text}
        payload["channel_id"] = channel_id
        payload["conversation_key"] = conversation_key
        if images:
            payload["images"] = images
        if files:
            payload["files"] = files
        self._request("POST", "/api/v1/send", json=payload)

    def usage(
        self,
        *,
        period: str = "daily",
        date_from: str | None = None,
        date_to: str | None = None,
    ) -> dict[str, Any]:
        params: dict[str, str] = {"period": period}
        if date_from:
            params["from"] = date_from
        if date_to:
            params["to"] = date_to
        return self._request("GET", "/api/v1/usage", params=params).json()


class AgentsResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def list(self) -> list[AgentInstance]:
        payload = self._client._request("GET", "/api/v1/agent-instances").json()
        return [AgentInstance.from_dict(item) for item in payload or []]

    def upsert(self, agent: AgentInstance | dict[str, Any]) -> AgentInstance:
        payload = agent.to_payload() if isinstance(agent, AgentInstance) else agent
        response = self._client._request("POST", "/api/v1/agent-instances", json=payload)
        return AgentInstance.from_dict(response.json())

    def delete(self, agent_id: str) -> None:
        self._client._request("DELETE", "/api/v1/agent-instances", params={"id": agent_id})


class ChannelsResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def list(self) -> list[Channel]:
        payload = self._client._request("GET", "/api/v1/channels").json()
        return [Channel.from_dict(item) for item in payload or []]

    def upsert(self, channel: Channel | dict[str, Any]) -> Channel:
        payload = channel.to_payload() if isinstance(channel, Channel) else channel
        response = self._client._request("POST", "/api/v1/channels", json=payload)
        return Channel.from_dict(response.json())

    def delete(self, channel_id: str) -> None:
        self._client._request("DELETE", "/api/v1/channels", params={"id": channel_id})

    def restart(self, channel_id: str) -> None:
        self._client._request("POST", "/api/v1/channels/restart", params={"id": channel_id})


class TriggersResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def list(self) -> list[Trigger]:
        payload = self._client._request("GET", "/api/v1/triggers").json()
        return [Trigger.from_dict(item) for item in payload or []]

    def upsert(self, trigger: Trigger | dict[str, Any]) -> Trigger:
        payload = trigger.to_payload() if isinstance(trigger, Trigger) else trigger
        response = self._client._request("POST", "/api/v1/triggers", json=payload)
        return Trigger.from_dict(response.json())

    def delete(self, trigger_id: str) -> None:
        self._client._request("DELETE", "/api/v1/triggers", params={"id": trigger_id})

    def run(self, trigger_id: str) -> None:
        self._client._request("POST", "/api/v1/triggers/run", params={"id": trigger_id})


class OrchestrationsResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def create(
        self,
        tasks: list[OrchestrationTask | dict[str, Any]],
        *,
        name: str | None = None,
        max_concurrency: int | None = None,
    ) -> Orchestration:
        payload: dict[str, Any] = {
            "tasks": [
                task.to_payload() if isinstance(task, OrchestrationTask) else task
                for task in tasks
            ]
        }
        if name:
            payload["name"] = name
        if max_concurrency:
            payload["max_concurrency"] = max_concurrency
        response = self._client._request("POST", "/api/v1/orchestrations", json=payload)
        return Orchestration.from_dict(response.json())

    def get(self, orchestration_id: str) -> Orchestration:
        response = self._client._request(
            "GET", "/api/v1/orchestrations", params={"id": orchestration_id}
        )
        return Orchestration.from_dict(response.json())

    def list(self, *, active: bool = False, limit: int | None = None) -> list[Orchestration]:
        params: dict[str, str] = {}
        if active:
            params["active"] = "true"
        if limit:
            params["limit"] = str(limit)
        payload = self._client._request("GET", "/api/v1/orchestrations", params=params).json()
        return [Orchestration.from_dict(item) for item in payload or []]

    def cancel(self, orchestration_id: str) -> None:
        self._client._request(
            "POST", "/api/v1/orchestrations/cancel", json={"id": orchestration_id}
        )


class IntegrationResource:
    """High-level BFF view for applications embedding AgentMux capabilities."""

    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def snapshot(self, *, orchestration_limit: int = 8) -> IntegrationSnapshot:
        return IntegrationSnapshot(
            capabilities=self._client.capabilities(),
            identity=self._client.tenancy.self(),
            runtimes=tuple(self._client.runtimes()),
            platforms=tuple(self._client.platforms()),
            agents=tuple(self._client.agents.list()),
            channels=tuple(self._client.channels.list()),
            triggers=tuple(self._client.triggers.list()),
            orchestrations=tuple(
                self._client.orchestrations.list(limit=orchestration_limit)
            ),
        )


class ConsoleResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def create_session(self, *, landing: str | None = None) -> ConsoleSession:
        """Mint a one-time Console entry URL (requires a bearer token).

        The session inherits this client's scope: a tenant token yields a
        Console that shows only that application's resources.
        """
        params = {"landing": landing} if landing else None
        response = self._client._request("POST", "/api/v1/console/sessions", params=params)
        return ConsoleSession.from_dict(response.json())


class TenancyResource:
    def __init__(self, client: AgentMuxClient) -> None:
        self._client = client

    def self(self) -> TenancySelf:
        """Read back this credential's identity and scope."""
        response = self._client._request("GET", "/api/v1/tenancy/self")
        return TenancySelf.from_dict(response.json())

    def register(self, name: str, *, kind: str = "app") -> TenantRegistration:
        response = self._client._request(
            "POST", "/api/v1/tenancy/register", json={"name": name, "kind": kind}
        )
        return TenantRegistration.from_dict(response.json())


class AsyncAgentMuxClient:
    """Async twin of :class:`AgentMuxClient` built on httpx.AsyncClient."""

    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        *,
        token: str | None = None,
        console_url: str | None = None,
        timeout: float = DEFAULT_TIMEOUT,
        invoke_timeout: float = DEFAULT_INVOKE_TIMEOUT,
        min_version: str | None = None,
        install_locations: tuple[Path, ...] = (),
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.console_url = (console_url or self.base_url).rstrip("/")
        self.min_version = min_version
        self.install_locations = install_locations
        self._invoke_timeout = invoke_timeout
        self._client = httpx.AsyncClient(
            base_url=self.base_url,
            headers=_auth_headers(token),
            timeout=timeout,
            transport=transport,
        )
        self.agents = AsyncAgentsResource(self)
        self.channels = AsyncChannelsResource(self)
        self.triggers = AsyncTriggersResource(self)
        self.orchestrations = AsyncOrchestrationsResource(self)
        self.integration = AsyncIntegrationResource(self)
        self.console = AsyncConsoleResource(self)
        self.tenancy = AsyncTenancyResource(self)

    async def aclose(self) -> None:
        await self._client.aclose()

    async def __aenter__(self) -> "AsyncAgentMuxClient":
        return self

    async def __aexit__(self, *exc_info: object) -> None:
        await self.aclose()

    async def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        try:
            response = await self._client.request(method, path, **kwargs)
        except httpx.ConnectError as exc:
            raise AgentMuxUnreachable(f"AgentMux is unreachable at {self.base_url}") from exc
        except httpx.TimeoutException as exc:
            raise AgentMuxUnreachable(f"AgentMux timed out at {self.base_url}") from exc
        _raise_for_response(response)
        return response

    async def capabilities(self) -> Capabilities:
        response = await self._request("GET", "/api/v1/capabilities")
        return Capabilities.from_dict(response.json())

    async def runtimes(self) -> list[str]:
        return list((await self._request("GET", "/api/v1/agents")).json() or [])

    async def platforms(self) -> list[str]:
        return list((await self._request("GET", "/api/v1/platforms")).json() or [])

    async def status(self) -> dict[str, Any]:
        return (await self._request("GET", "/api/v1/status")).json()

    async def health(self) -> HealthReport:
        try:
            response = await self._client.get("/api/v1/capabilities")
        except (httpx.ConnectError, httpx.TimeoutException):
            return self._offline_report()
        except httpx.HTTPError as exc:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux API check failed: {type(exc).__name__}",
                console_url=self.console_url,
            )
        if response.status_code == 401:
            return HealthReport(
                state=HealthState.UNAUTHORIZED,
                message="AgentMux rejected the bridge token",
                console_url=self.console_url,
            )
        if response.status_code == 404:
            return await self._legacy_probe()
        if response.status_code >= 400:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux capabilities returned HTTP {response.status_code}",
                console_url=self.console_url,
            )
        try:
            capabilities = Capabilities.from_dict(response.json())
        except ValueError:
            return HealthReport(
                state=HealthState.INCOMPATIBLE,
                message="AgentMux capabilities response is not valid JSON",
                console_url=self.console_url,
            )
        return _health_from_capabilities(
            capabilities, min_version=self.min_version, console_url=self.console_url
        )

    async def _legacy_probe(self) -> HealthReport:
        try:
            status_response = await self._client.get("/api/v1/status")
            if status_response.status_code == 401:
                return HealthReport(
                    state=HealthState.UNAUTHORIZED,
                    message="AgentMux rejected the bridge token",
                    console_url=self.console_url,
                )
            status_response.raise_for_status()
            probes = []
            for path in _LEGACY_PROBE_PATHS:
                probe = await self._client.get(path)
                probe.raise_for_status()
                probes.append(probe.json())
            return _legacy_health(
                status_response.json(),
                probes,
                min_version=self.min_version,
                console_url=self.console_url,
            )
        except (httpx.ConnectError, httpx.TimeoutException):
            return self._offline_report()
        except (httpx.HTTPError, ValueError) as exc:
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message=f"AgentMux API check failed: {type(exc).__name__}",
                console_url=self.console_url,
            )

    def _offline_report(self) -> HealthReport:
        if looks_installed(self.install_locations):
            return HealthReport(
                state=HealthState.UNREACHABLE,
                message="AgentMux is installed but its API is unreachable",
                console_url=self.console_url,
            )
        return HealthReport(
            state=HealthState.MISSING,
            message="AgentMux is not installed or running",
            console_url=self.console_url,
        )

    async def invoke(
        self,
        *,
        input: str,
        agent_id: str,
        conversation_id: str | None = None,
        attachments: list[Attachment] | None = None,
        output_schema: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> InvocationResult:
        payload = _invocation_payload(
            input=input,
            agent_id=agent_id,
            conversation_id=conversation_id,
            attachments=attachments,
            output_schema=output_schema,
        )
        response = await self._request(
            "POST",
            "/api/v1/invocations",
            json=payload,
            timeout=timeout or self._invoke_timeout,
        )
        return InvocationResult.from_dict(response.json())

    async def invoke_stream(
        self,
        *,
        input: str,
        agent_id: str,
        conversation_id: str | None = None,
        attachments: list[Attachment] | None = None,
        output_schema: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> AsyncIterator[InvocationEvent]:
        payload = _invocation_payload(
            input=input,
            agent_id=agent_id,
            conversation_id=conversation_id,
            attachments=attachments,
            output_schema=output_schema,
        )
        request_timeout = httpx.Timeout(timeout or self._invoke_timeout, connect=10.0)
        try:
            async with self._client.stream(
                "POST",
                "/api/v1/invocations/stream",
                json=payload,
                headers={"Accept": "text/event-stream"},
                timeout=request_timeout,
            ) as response:
                if response.status_code >= 400:
                    await response.aread()
                    _raise_for_response(response)
                async for event_payload in aiter_sse_payloads(response.aiter_text()):
                    yield InvocationEvent.from_dict(event_payload)
        except httpx.ConnectError as exc:
            raise AgentMuxUnreachable(f"AgentMux is unreachable at {self.base_url}") from exc

    async def send(
        self,
        *,
        text: str,
        channel_id: str,
        conversation_key: str,
        images: list[str] | None = None,
        files: list[str] | None = None,
    ) -> None:
        payload: dict[str, Any] = {"text": text}
        payload["channel_id"] = channel_id
        payload["conversation_key"] = conversation_key
        if images:
            payload["images"] = images
        if files:
            payload["files"] = files
        await self._request("POST", "/api/v1/send", json=payload)

    async def usage(
        self,
        *,
        period: str = "daily",
        date_from: str | None = None,
        date_to: str | None = None,
    ) -> dict[str, Any]:
        params: dict[str, str] = {"period": period}
        if date_from:
            params["from"] = date_from
        if date_to:
            params["to"] = date_to
        return (await self._request("GET", "/api/v1/usage", params=params)).json()


class AsyncAgentsResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def list(self) -> list[AgentInstance]:
        payload = (await self._client._request("GET", "/api/v1/agent-instances")).json()
        return [AgentInstance.from_dict(item) for item in payload or []]

    async def upsert(self, agent: AgentInstance | dict[str, Any]) -> AgentInstance:
        payload = agent.to_payload() if isinstance(agent, AgentInstance) else agent
        response = await self._client._request("POST", "/api/v1/agent-instances", json=payload)
        return AgentInstance.from_dict(response.json())

    async def delete(self, agent_id: str) -> None:
        await self._client._request("DELETE", "/api/v1/agent-instances", params={"id": agent_id})


class AsyncChannelsResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def list(self) -> list[Channel]:
        payload = (await self._client._request("GET", "/api/v1/channels")).json()
        return [Channel.from_dict(item) for item in payload or []]

    async def upsert(self, channel: Channel | dict[str, Any]) -> Channel:
        payload = channel.to_payload() if isinstance(channel, Channel) else channel
        response = await self._client._request("POST", "/api/v1/channels", json=payload)
        return Channel.from_dict(response.json())

    async def delete(self, channel_id: str) -> None:
        await self._client._request("DELETE", "/api/v1/channels", params={"id": channel_id})

    async def restart(self, channel_id: str) -> None:
        await self._client._request(
            "POST", "/api/v1/channels/restart", params={"id": channel_id}
        )


class AsyncTriggersResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def list(self) -> list[Trigger]:
        payload = (await self._client._request("GET", "/api/v1/triggers")).json()
        return [Trigger.from_dict(item) for item in payload or []]

    async def upsert(self, trigger: Trigger | dict[str, Any]) -> Trigger:
        payload = trigger.to_payload() if isinstance(trigger, Trigger) else trigger
        response = await self._client._request("POST", "/api/v1/triggers", json=payload)
        return Trigger.from_dict(response.json())

    async def delete(self, trigger_id: str) -> None:
        await self._client._request("DELETE", "/api/v1/triggers", params={"id": trigger_id})

    async def run(self, trigger_id: str) -> None:
        await self._client._request("POST", "/api/v1/triggers/run", params={"id": trigger_id})


class AsyncOrchestrationsResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def create(
        self,
        tasks: list[OrchestrationTask | dict[str, Any]],
        *,
        name: str | None = None,
        max_concurrency: int | None = None,
    ) -> Orchestration:
        payload: dict[str, Any] = {
            "tasks": [
                task.to_payload() if isinstance(task, OrchestrationTask) else task
                for task in tasks
            ]
        }
        if name:
            payload["name"] = name
        if max_concurrency:
            payload["max_concurrency"] = max_concurrency
        response = await self._client._request("POST", "/api/v1/orchestrations", json=payload)
        return Orchestration.from_dict(response.json())

    async def get(self, orchestration_id: str) -> Orchestration:
        response = await self._client._request(
            "GET", "/api/v1/orchestrations", params={"id": orchestration_id}
        )
        return Orchestration.from_dict(response.json())

    async def list(self, *, active: bool = False, limit: int | None = None) -> list[Orchestration]:
        params: dict[str, str] = {}
        if active:
            params["active"] = "true"
        if limit:
            params["limit"] = str(limit)
        payload = (
            await self._client._request("GET", "/api/v1/orchestrations", params=params)
        ).json()
        return [Orchestration.from_dict(item) for item in payload or []]

    async def cancel(self, orchestration_id: str) -> None:
        await self._client._request(
            "POST", "/api/v1/orchestrations/cancel", json={"id": orchestration_id}
        )


class AsyncIntegrationResource:
    """Async high-level BFF view for host application integration pages."""

    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def snapshot(self, *, orchestration_limit: int = 8) -> IntegrationSnapshot:
        capabilities, identity, runtimes, platforms, agents, channels, triggers, orchestrations = await asyncio.gather(
            self._client.capabilities(),
            self._client.tenancy.self(),
            self._client.runtimes(),
            self._client.platforms(),
            self._client.agents.list(),
            self._client.channels.list(),
            self._client.triggers.list(),
            self._client.orchestrations.list(limit=orchestration_limit),
        )
        return IntegrationSnapshot(
            capabilities=capabilities,
            identity=identity,
            runtimes=tuple(runtimes),
            platforms=tuple(platforms),
            agents=tuple(agents),
            channels=tuple(channels),
            triggers=tuple(triggers),
            orchestrations=tuple(orchestrations),
        )


class AsyncConsoleResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def create_session(self, *, landing: str | None = None) -> ConsoleSession:
        params = {"landing": landing} if landing else None
        response = await self._client._request(
            "POST", "/api/v1/console/sessions", params=params
        )
        return ConsoleSession.from_dict(response.json())


class AsyncTenancyResource:
    def __init__(self, client: AsyncAgentMuxClient) -> None:
        self._client = client

    async def self(self) -> TenancySelf:
        response = await self._client._request("GET", "/api/v1/tenancy/self")
        return TenancySelf.from_dict(response.json())

    async def register(self, name: str, *, kind: str = "app") -> TenantRegistration:
        response = await self._client._request(
            "POST", "/api/v1/tenancy/register", json={"name": name, "kind": kind}
        )
        return TenantRegistration.from_dict(response.json())
