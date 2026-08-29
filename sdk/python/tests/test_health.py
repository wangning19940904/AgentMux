import json

import httpx
import pytest

from agentmux_sdk import AgentMuxClient, HealthState

CAPABILITIES = {
    "ok": True,
    "product": "agentmux",
    "version": "v0.1.4",
    "contract_version": "1.0",
    "features": ["invocations", "invocations.stream", "send", "console.session"],
    "modules": {"connect": {"registered": True, "active": True}},
    "agents": {"count": 2, "runtimes": ["codex"]},
    "channels": {"count": 1},
    "projects": 0,
    "auth": {"bridge_enabled": True},
}


def make_client(handler, **kwargs) -> AgentMuxClient:
    return AgentMuxClient(transport=httpx.MockTransport(handler), **kwargs)


def test_ready_via_capabilities() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/v1/capabilities"
        return httpx.Response(200, json=CAPABILITIES)

    report = make_client(handler).health()
    assert report.state is HealthState.READY
    assert report.version == "v0.1.4"
    assert report.contract_version == "1.0"
    assert report.capabilities is not None
    assert report.capabilities.supports("invocations.stream")


def test_unauthorized() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"error": "missing bearer token"})

    report = make_client(handler).health()
    assert report.state is HealthState.UNAUTHORIZED


def test_incompatible_contract_major() -> None:
    payload = dict(CAPABILITIES, contract_version="2.0")

    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json=payload)

    report = make_client(handler).health()
    assert report.state is HealthState.INCOMPATIBLE


def test_incompatible_min_version() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json=CAPABILITIES)

    report = make_client(handler, min_version="v9.0.0").health()
    assert report.state is HealthState.INCOMPATIBLE
    assert "v9.0.0" in report.message


def test_legacy_fallback_when_capabilities_missing() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/api/v1/capabilities":
            return httpx.Response(404, text="not found")
        if path == "/api/v1/status":
            return httpx.Response(200, json={"ok": True, "version": "v0.1.2", "projects": 0})
        if path in ("/api/v1/agent-instances", "/api/v1/channels"):
            return httpx.Response(200, json=[])
        raise AssertionError(f"unexpected path {path}")

    report = make_client(handler).health()
    assert report.state is HealthState.READY
    assert report.version == "v0.1.2"
    assert report.capabilities is None


def test_legacy_fallback_contract_mismatch() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/api/v1/capabilities":
            return httpx.Response(404, text="not found")
        if path == "/api/v1/status":
            return httpx.Response(200, json={"ok": True, "version": "v0.1.2"})
        return httpx.Response(200, json={"unexpected": "shape"})

    report = make_client(handler).health()
    assert report.state is HealthState.INCOMPATIBLE


def test_missing_when_unreachable_and_not_installed(tmp_path, monkeypatch) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused", request=request)

    monkeypatch.setattr("agentmux_sdk.detect.shutil.which", lambda _: None)
    monkeypatch.setattr(
        "agentmux_sdk.detect.DEFAULT_INSTALL_LOCATIONS", (tmp_path / "nope",)
    )
    report = make_client(handler).health()
    assert report.state is HealthState.MISSING


def test_unreachable_when_installed(tmp_path, monkeypatch) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("refused", request=request)

    marker = tmp_path / "amux"
    marker.write_text("")
    monkeypatch.setattr("agentmux_sdk.detect.shutil.which", lambda _: None)
    monkeypatch.setattr("agentmux_sdk.detect.DEFAULT_INSTALL_LOCATIONS", (marker,))
    report = make_client(handler).health()
    assert report.state is HealthState.UNREACHABLE


@pytest.mark.parametrize("status", [500, 503])
def test_server_errors_are_unreachable(status: int) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(status, json={"error": "boom"})

    report = make_client(handler).health()
    assert report.state is HealthState.UNREACHABLE
    assert str(status) in json.dumps(report.message)
