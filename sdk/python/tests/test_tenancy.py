"""Tenancy: self-registration, scope reporting and ownership fields."""

from __future__ import annotations

import httpx
from agentmux_sdk import (
    AgentMuxClient,
    Capabilities,
)
from agentmux_sdk.models import AgentInstance, Channel


def transport(handler) -> httpx.MockTransport:
    return httpx.MockTransport(handler)


def test_register_creates_an_empty_tenant_without_an_existing_token() -> None:
    seen: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/v1/tenancy/register"
        assert "authorization" not in {key.lower() for key in request.headers}
        import json

        seen.update(json.loads(request.content))
        return httpx.Response(
            200,
            json={
                "tenant": {
                    "id": "ten_abc",
                    "name": "homebook",
                    "kind": "web",
                    "status": "active",
                },
                "token": "amxt_secret",
                "prefix": "amxt_secret1",
            },
        )

    with AgentMuxClient("http://agentmux.test", transport=transport(handler)) as client:
        result = client.tenancy.register("homebook", kind="web")
    assert seen == {"name": "homebook", "kind": "web"}
    assert result.token == "amxt_secret"
    assert result.tenant.name == "homebook"
    assert result.tenant.active
def test_capabilities_reports_tenant_scope() -> None:
    scoped = Capabilities.from_dict(
        {
            "ok": True,
            "product": "agentmux",
            "version": "0.1.4",
            "contract_version": "1.1",
            "features": ["send", "invocations", "tenancy"],
            "auth": {
                "bridge_enabled": True,
                "scope": "tenant",
                "tenant": "homebook",
                "tenant_id": "ten_abc",
            },
        }
    )
    assert scoped.tenant_scoped
    assert scoped.tenant == "homebook"
    assert scoped.supports("tenancy")

    admin = Capabilities.from_dict(
        {"ok": True, "auth": {"bridge_enabled": True, "scope": "admin"}}
    )
    assert not admin.tenant_scoped

    # A pre-1.1 server omits the scope entirely; it is an unscoped admin.
    legacy = Capabilities.from_dict({"ok": True, "auth": {"bridge_enabled": True}})
    assert legacy.scope == "admin"
    assert not legacy.tenant_scoped


def test_tenancy_self_reports_identity() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/v1/tenancy/self"
        return httpx.Response(
            200,
            json={"admin": False, "tenant": "rookie", "tenant_id": "ten_r", "status": "active"},
        )

    with AgentMuxClient(
        "http://agentmux.test", token="amxt_x", transport=transport(handler)
    ) as client:
        identity = client.tenancy.self()
    assert not identity.admin
    assert identity.tenant == "rookie"


def test_ownership_fields_round_trip_and_are_not_echoed_on_writes() -> None:
    agent = AgentInstance.from_dict(
        {
            "id": "agent-1",
            "name": "one",
            "runtime_id": "codex",
            "enabled": True,
            "owner_tenant_id": "ten_abc",
            "owner_tenant_name": "homebook",
            "visibility": "public",
        }
    )
    assert agent.owner_tenant_name == "homebook"
    assert agent.visibility == "public"
    # Ownership is server-assigned, so it must not be sent back on an update.
    payload = agent.to_payload()
    assert "owner_tenant_id" not in payload
    assert "owner_tenant_name" not in payload

    channel = Channel.from_dict(
        {
            "id": "chan-1",
            "name": "one",
            "type": "webhook",
            "enabled": True,
            "owner_tenant_id": "ten_abc",
            "visibility": "private",
        }
    )
    assert channel.owner_tenant_id == "ten_abc"
    assert "owner_tenant_id" not in channel.to_payload()


def test_unknown_server_fields_are_preserved_for_forward_compatibility() -> None:
    agent = AgentInstance.from_dict(
        {
            "id": "agent-1",
            "name": "one",
            "runtime_id": "codex",
            "enabled": True,
            "some_future_field": 42,
        }
    )
    assert agent.raw["some_future_field"] == 42
