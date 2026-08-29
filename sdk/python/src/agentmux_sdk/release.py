"""GitHub release discovery and integrity verification for AgentMux.

Downloads are verified against the ``checksums.txt`` asset that GoReleaser
publishes with every release; consumers must not maintain their own release
manifests.
"""

from __future__ import annotations

import hashlib
import re
import time
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

import httpx

from .errors import AgentMuxError
from .models import version_key

DEFAULT_REPOSITORY = "wangning19940904/AgentMux"
_ARCHIVE_PATTERN = re.compile(r"^agentmux_(darwin|linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$")
_GITHUB_HEADERS = {
    "Accept": "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
    "User-Agent": "agentmux-sdk",
}


class AgentMuxReleaseError(AgentMuxError):
    """Release metadata could not be fetched or validated."""


@dataclass(frozen=True)
class Release:
    """A published AgentMux GitHub release."""

    version: str
    release_url: str
    published_at: datetime | None
    archives: tuple[str, ...]


def archive_name(os_name: str, machine: str) -> str:
    """Map platform.system()/platform.machine() values to a release asset name."""
    normalized_os = {"darwin": "darwin", "linux": "linux"}.get(os_name.lower())
    normalized_arch = {
        "x86_64": "amd64",
        "amd64": "amd64",
        "arm64": "arm64",
        "aarch64": "arm64",
    }.get(machine.lower())
    if not normalized_os or not normalized_arch:
        raise AgentMuxReleaseError(f"unsupported platform: {os_name}/{machine}")
    return f"agentmux_{normalized_os}_{normalized_arch}.tar.gz"


def latest_release(
    repository: str = DEFAULT_REPOSITORY,
    *,
    timeout: float = 8.0,
    client: httpx.Client | None = None,
) -> Release:
    """Fetch the latest stable (non-draft, non-prerelease) release."""
    own_client = client is None
    if client is None:
        client = httpx.Client(headers=_GITHUB_HEADERS, follow_redirects=True, timeout=timeout)
    try:
        response = client.get(f"https://api.github.com/repos/{repository}/releases/latest")
        response.raise_for_status()
        payload = response.json()
    except httpx.HTTPStatusError as exc:
        raise AgentMuxReleaseError(_status_error_message(exc.response)) from exc
    except (httpx.HTTPError, ValueError) as exc:
        raise AgentMuxReleaseError(
            f"could not query the latest AgentMux release: {type(exc).__name__}"
        ) from exc
    finally:
        if own_client:
            client.close()
    return _parse_release(payload, repository)


def _status_error_message(response: httpx.Response) -> str:
    """Turn a GitHub HTTP failure into something an operator can act on.

    Anonymous GitHub API calls are capped at 60 per hour per IP, and exceeding
    that returns 403 with the reset time in a header. Reporting only the
    exception type leaves the user staring at "HTTPStatusError" with no idea
    that waiting is the fix.
    """
    status = response.status_code
    remaining = response.headers.get("x-ratelimit-remaining")
    if status in (403, 429) and remaining == "0":
        minutes = _minutes_until_reset(response.headers.get("x-ratelimit-reset"))
        if minutes is not None:
            return (
                "GitHub API rate limit reached (60 requests per hour for "
                f"unauthenticated callers); retry in about {minutes} minute(s)"
            )
        return "GitHub API rate limit reached; retry later"
    if status == 404:
        return "the AgentMux repository has no published release yet (HTTP 404)"
    return f"could not query the latest AgentMux release: HTTP {status}"


def _minutes_until_reset(header: str | None) -> int | None:
    if not header:
        return None
    try:
        reset_at = int(header)
    except ValueError:
        return None
    seconds = reset_at - int(time.time())
    if seconds <= 0:
        return 0
    return max(1, (seconds + 59) // 60)


def _parse_release(payload: object, repository: str) -> Release:
    if not isinstance(payload, dict) or payload.get("draft") or payload.get("prerelease"):
        raise AgentMuxReleaseError("latest release payload is invalid")
    version = str(payload.get("tag_name") or "").strip()
    if version_key(version) is None:
        raise AgentMuxReleaseError(f"release tag is not a semantic version: {version!r}")
    release_url = str(payload.get("html_url") or "").strip()
    if not release_url.startswith(f"https://github.com/{repository}/releases/tag/"):
        raise AgentMuxReleaseError("release URL does not belong to the configured repository")
    archives = tuple(
        str(asset.get("name"))
        for asset in payload.get("assets") or ()
        if isinstance(asset, dict) and _ARCHIVE_PATTERN.fullmatch(str(asset.get("name") or ""))
    )
    published_at = None
    published_raw = str(payload.get("published_at") or "").strip()
    if published_raw:
        published_at = datetime.fromisoformat(published_raw.replace("Z", "+00:00"))
    return Release(
        version=version, release_url=release_url, published_at=published_at, archives=archives
    )


def download_url(repository: str, version: str, asset: str) -> str:
    return f"https://github.com/{repository}/releases/download/{version}/{asset}"


def fetch_checksums(
    repository: str,
    version: str,
    *,
    timeout: float = 15.0,
    client: httpx.Client | None = None,
) -> dict[str, str]:
    """Fetch and parse the GoReleaser checksums.txt for a release."""
    own_client = client is None
    if client is None:
        client = httpx.Client(follow_redirects=True, timeout=timeout)
    try:
        response = client.get(download_url(repository, version, "checksums.txt"))
        response.raise_for_status()
        text = response.text
    except httpx.HTTPError as exc:
        raise AgentMuxReleaseError(
            f"could not download checksums.txt for {version}: {type(exc).__name__}"
        ) from exc
    finally:
        if own_client:
            client.close()
    checksums: dict[str, str] = {}
    for line in text.splitlines():
        parts = line.split()
        if len(parts) == 2 and re.fullmatch(r"[0-9a-f]{64}", parts[0]):
            checksums[parts[1].lstrip("*")] = parts[0]
    if not checksums:
        raise AgentMuxReleaseError(f"checksums.txt for {version} is empty or malformed")
    return checksums


def verify_sha256(path: Path, expected: str) -> None:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    actual = digest.hexdigest()
    if actual != expected:
        raise AgentMuxReleaseError(
            f"checksum mismatch for {path.name}: expected {expected}, got {actual}"
        )


def is_newer(candidate: str | None, current: str | None) -> bool:
    candidate_key = version_key(candidate)
    current_key = version_key(current)
    return bool(candidate_key and current_key and candidate_key > current_key)
