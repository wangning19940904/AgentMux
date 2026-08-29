import hashlib

import httpx
import pytest

from agentmux_sdk.release import (
    AgentMuxReleaseError,
    archive_name,
    fetch_checksums,
    is_newer,
    latest_release,
    verify_sha256,
)


def test_archive_name_mapping() -> None:
    assert archive_name("Darwin", "arm64") == "agentmux_darwin_arm64.tar.gz"
    assert archive_name("Linux", "x86_64") == "agentmux_linux_amd64.tar.gz"
    with pytest.raises(AgentMuxReleaseError):
        archive_name("Plan9", "mips")


def test_latest_release_parsing() -> None:
    payload = {
        "tag_name": "v0.1.4",
        "html_url": "https://github.com/wangning19940904/AgentMux/releases/tag/v0.1.4",
        "published_at": "2026-08-01T00:00:00Z",
        "draft": False,
        "prerelease": False,
        "assets": [
            {"name": "agentmux_darwin_arm64.tar.gz"},
            {"name": "checksums.txt"},
            {"name": "agentmux_linux_amd64.tar.gz"},
        ],
    }

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/repos/wangning19940904/AgentMux/releases/latest"
        return httpx.Response(200, json=payload)

    client = httpx.Client(transport=httpx.MockTransport(handler))
    release = latest_release(client=client)
    assert release.version == "v0.1.4"
    assert release.archives == (
        "agentmux_darwin_arm64.tar.gz",
        "agentmux_linux_amd64.tar.gz",
    )
    assert release.published_at is not None


def test_latest_release_rejects_foreign_url() -> None:
    payload = {
        "tag_name": "v0.1.4",
        "html_url": "https://github.com/evil/repo/releases/tag/v0.1.4",
        "assets": [],
    }
    client = httpx.Client(
        transport=httpx.MockTransport(lambda request: httpx.Response(200, json=payload))
    )
    with pytest.raises(AgentMuxReleaseError):
        latest_release(client=client)


def test_fetch_checksums_parses_goreleaser_format() -> None:
    text = (
        "0" * 64
        + "  agentmux_darwin_arm64.tar.gz\n"
        + "f" * 64
        + "  agentmux_linux_amd64.tar.gz\n"
        + "not a checksum line\n"
    )
    client = httpx.Client(
        transport=httpx.MockTransport(lambda request: httpx.Response(200, text=text))
    )
    checksums = fetch_checksums("wangning19940904/AgentMux", "v0.1.4", client=client)
    assert checksums["agentmux_darwin_arm64.tar.gz"] == "0" * 64
    assert len(checksums) == 2


def test_verify_sha256(tmp_path) -> None:
    path = tmp_path / "blob"
    path.write_bytes(b"agentmux")
    verify_sha256(path, hashlib.sha256(b"agentmux").hexdigest())
    with pytest.raises(AgentMuxReleaseError):
        verify_sha256(path, "0" * 64)


def test_is_newer() -> None:
    assert is_newer("v0.1.4", "v0.1.3")
    assert not is_newer("v0.1.3", "v0.1.4")
    assert not is_newer("v0.1.4", "v0.1.4")
    assert not is_newer(None, "v0.1.4")
    assert not is_newer("v0.1.4", "garbage")
