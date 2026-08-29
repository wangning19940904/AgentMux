import json

import httpx
import pytest

from agentmux_sdk import (
    AgentMuxBusy,
    AgentMuxClient,
    AgentMuxNotFound,
    AgentMuxUnauthorized,
    Attachment,
    OrchestrationTask,
)


def make_client(handler, **kwargs) -> AgentMuxClient:
    return AgentMuxClient(transport=httpx.MockTransport(handler), token="secret", **kwargs)


def test_invoke_requires_exactly_one_target() -> None:
    client = make_client(lambda request: httpx.Response(200, json={}))
    with pytest.raises(ValueError):
        client.invoke(input="hi")
    with pytest.raises(ValueError):
        client.invoke(input="hi", agent_id="a", project="p")


def test_invoke_roundtrip() -> None:
    captured: dict = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["path"] = request.url.path
        captured["auth"] = request.headers.get("Authorization")
        captured["body"] = json.loads(request.content)
        return httpx.Response(
            200,
            json={
                "id": "inv-1",
                "agent_id": "agent-abc",
                "conversation_id": "conv-1",
                "session_id": "sess-1",
                "answer": "42",
                "duration_ms": 1200,
            },
        )

    result = make_client(handler).invoke(
        agent_id="agent-abc",
        input="meaning of life",
        conversation_id="conv-1",
        attachments=[Attachment(kind="image", path="/tmp/x.png")],
        output_schema={"type": "object"},
    )
    assert captured["path"] == "/api/v1/invocations"
    assert captured["auth"] == "Bearer secret"
    assert captured["body"]["agent_id"] == "agent-abc"
    assert captured["body"]["attachments"] == [{"kind": "image", "path": "/tmp/x.png"}]
    assert captured["body"]["output_schema"] == {"type": "object"}
    assert result.answer == "42"
    assert result.duration_ms == 1200


def test_invoke_stream_parses_events_and_snapshots() -> None:
    body = (
        'event: started\ndata: {"type": "started", "invocation_id": "inv-1"}\n\n'
        ": keepalive\n\n"
        'event: output\ndata: {"type": "output", "text": "partial"}\n\n'
        'event: output\ndata: {"type": "output", "text": "partial answer"}\n\n'
        "event: completed\ndata: "
        '{"type": "completed", "final": true, "result": {"id": "inv-1", '
        '"conversation_id": "conv-1", "answer": "partial answer", "duration_ms": 5}}\n\n'
    )

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/v1/invocations/stream"
        return httpx.Response(200, content=body, headers={"Content-Type": "text/event-stream"})

    events = list(make_client(handler).invoke_stream(agent_id="agent-abc", input="q"))
    assert [event.type for event in events] == ["started", "output", "output", "completed"]
    # output text is a snapshot: the last one wins, nothing to concatenate
    assert events[2].text == "partial answer"
    assert events[3].final is True
    assert events[3].result is not None
    assert events[3].result.answer == "partial answer"


def test_error_mapping() -> None:
    codes = iter([401, 404, 409])

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(next(codes), json={"error": "nope"})

    client = make_client(handler)
    with pytest.raises(AgentMuxUnauthorized):
        client.status()
    with pytest.raises(AgentMuxNotFound):
        client.status()
    with pytest.raises(AgentMuxBusy) as busy:
        client.status()
    assert busy.value.status_code == 409


def test_resources_roundtrip() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        path, method = request.url.path, request.method
        if path == "/api/v1/agent-instances" and method == "GET":
            return httpx.Response(
                200, json=[{"id": "a1", "name": "A", "runtime_id": "codex", "enabled": True}]
            )
        if path == "/api/v1/channels" and method == "GET":
            return httpx.Response(
                200, json=[{"id": "c1", "name": "C", "type": "feishu", "enabled": True}]
            )
        if path == "/api/v1/console/sessions" and method == "POST":
            assert request.url.params["landing"] == "tenants"
            return httpx.Response(
                200,
                json={
                    "enter_url": "http://127.0.0.1:8765/console/enter?nonce=n",
                    "expires_at": "2026-01-01T00:00:00Z",
                    "session_ttl_seconds": 28800,
                },
            )
        if path == "/api/v1/orchestrations" and method == "POST":
            payload = json.loads(request.content)
            assert payload["tasks"][0] == {"id": "t1", "input": "do it"}
            return httpx.Response(
                202,
                json={"id": "orch-1", "status": "queued", "max_concurrency": 4, "tasks": []},
            )
        if path == "/api/v1/usage":
            assert request.url.params["period"] == "daily"
            return httpx.Response(200, json={"days": []})
        raise AssertionError(f"unexpected {method} {path}")

    client = make_client(handler)
    agents = client.agents.list()
    assert agents[0].runtime_id == "codex"
    channels = client.channels.list()
    assert channels[0].type == "feishu"
    session = client.console.create_session(landing="tenants")
    assert session.enter_url.endswith("nonce=n")
    assert session.session_ttl_seconds == 28800
    orchestration = client.orchestrations.create([OrchestrationTask(id="t1", input="do it")])
    assert orchestration.id == "orch-1"
    assert not orchestration.terminal
    assert client.usage() == {"days": []}


def test_integration_snapshot_composes_tenant_scoped_resources() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/api/v1/capabilities":
            return httpx.Response(
                200,
                json={
                    "ok": True,
                    "product": "agentmux",
                    "version": "v0.1.5",
                    "contract_version": "1.1",
                    "features": ["tenancy", "invocations.stream"],
                    "auth": {"bridge_enabled": True, "scope": "tenant", "tenant": "host"},
                },
            )
        if path == "/api/v1/tenancy/self":
            return httpx.Response(200, json={"admin": False, "tenant": "host", "tenant_id": "ten_host"})
        if path == "/api/v1/agents":
            return httpx.Response(200, json=["codex", "claude"])
        if path == "/api/v1/platforms":
            return httpx.Response(200, json=["feishu", "webhook"])
        if path == "/api/v1/agent-instances":
            return httpx.Response(200, json=[{"id": "a", "name": "Agent", "runtime_id": "codex", "enabled": True}])
        if path == "/api/v1/channels":
            return httpx.Response(200, json=[{"id": "c", "name": "Channel", "type": "feishu", "enabled": True}])
        if path == "/api/v1/triggers":
            return httpx.Response(200, json=[{"id": "t", "name": "Daily", "kind": "cron", "enabled": True}])
        if path == "/api/v1/orchestrations":
            assert request.url.params["limit"] == "3"
            return httpx.Response(200, json=[{"id": "o", "status": "running", "max_concurrency": 2}])
        raise AssertionError(path)

    snapshot = make_client(handler).integration.snapshot(orchestration_limit=3)
    assert snapshot.identity.tenant == "host"
    assert snapshot.capabilities.contract_version == "1.1"
    assert snapshot.runtimes == ("codex", "claude")
    assert snapshot.platforms == ("feishu", "webhook")
    assert snapshot.agents[0].id == "a"
    assert snapshot.channels[0].id == "c"
    assert snapshot.triggers[0].id == "t"
    assert snapshot.orchestrations[0].id == "o"


@pytest.mark.anyio
async def test_async_client_smoke() -> None:
    from agentmux_sdk import AsyncAgentMuxClient

    body = (
        'event: output\ndata: {"type": "output", "text": "hi"}\n\n'
        'event: completed\ndata: {"type": "completed", "final": true}\n\n'
    )

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/invocations/stream":
            return httpx.Response(200, content=body)
        if request.url.path == "/api/v1/status":
            return httpx.Response(200, json={"ok": True, "version": "v0.1.4"})
        raise AssertionError(request.url.path)

    async with AsyncAgentMuxClient(transport=httpx.MockTransport(handler)) as client:
        status = await client.status()
        assert status["ok"] is True
        events = [
            event async for event in client.invoke_stream(agent_id="agent-abc", input="q")
        ]
        assert [event.type for event in events] == ["output", "completed"]


@pytest.fixture
def anyio_backend() -> str:
    return "asyncio"
