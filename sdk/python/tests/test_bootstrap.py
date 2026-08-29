import pytest

from agentmux_sdk.bootstrap import (
    Action,
    BootstrapError,
    decide_action,
    port_from_base_url,
    render_config,
)
from agentmux_sdk.models import HealthReport, HealthState


def report(state: HealthState, version: str | None = None) -> HealthReport:
    return HealthReport(state=state, message="", version=version)


def test_healthy_matching_version_is_done() -> None:
    assert decide_action(report(HealthState.READY, "v0.1.4"), "v0.1.4", "local") is Action.DONE


def test_newer_running_version_is_preserved() -> None:
    assert (
        decide_action(report(HealthState.READY, "v0.2.0"), "v0.1.4", "production")
        is Action.PRESERVE
    )


def test_older_version_upgrades_only_in_production() -> None:
    stale = report(HealthState.READY, "v0.1.3")
    assert decide_action(stale, "v0.1.4", "production") is Action.UPGRADE
    assert decide_action(stale, "v0.1.4", "local") is Action.PRESERVE


@pytest.mark.parametrize(
    "state", [HealthState.MISSING, HealthState.UNREACHABLE, HealthState.INCOMPATIBLE]
)
def test_unhealthy_states_install(state: HealthState) -> None:
    assert decide_action(report(state), "v0.1.4", "local") is Action.INSTALL


def test_unparseable_versions_raise() -> None:
    with pytest.raises(BootstrapError):
        decide_action(report(HealthState.READY, "garbage"), "v0.1.4", "local")


def test_render_config_with_bridge() -> None:
    config = render_config("127.0.0.1:8765", "postgresql:///agentmux", "tok")
    assert '[server]\naddr = "127.0.0.1:8765"' in config
    assert 'url = "postgresql:///agentmux"' in config
    assert "enabled = true" in config
    assert 'token = "tok"' in config


def test_render_config_without_bridge() -> None:
    config = render_config("127.0.0.1:8765", "postgresql:///agentmux", "")
    assert "enabled = false" in config
    assert "token" not in config


def test_port_from_base_url() -> None:
    assert port_from_base_url("http://127.0.0.1:8766") == ("127.0.0.1", 8766)
    assert port_from_base_url("http://localhost") == ("localhost", 8765)
