"""Install, upgrade and start a local AgentMux daemon.

This is the official replacement for the per-project ``ensure-agentmux.sh``
scripts. It merges both historical implementations (health probe, macOS app
reuse, port-occupancy protection, pinned release download, checksum
verification, atomic symlink switch with rollback, systemd management, health
wait) behind one entry point::

    python -m agentmux_sdk.bootstrap --mode local --version v0.1.4
    python -m agentmux_sdk.bootstrap --mode production --version v0.1.4

Modes:

* ``local`` — installs under ``<install-root>`` (default ``./.agentmux``),
  writes a minimal config.toml, runs ``amux database setup`` and spawns
  ``amux client --web`` in the background.
* ``production`` — installs under ``/opt/agentmux`` as root, refreshes
  ``/etc/agentmux/agentmux.env`` and restarts the ``agentmux.service``
  systemd unit. Host-specific assets (the systemd unit file and a custom
  config.toml) can be provided with ``--systemd-unit`` / ``--config``.

Release integrity is always verified against the ``checksums.txt`` asset
published by GoReleaser; there is no separate release manifest.
"""

from __future__ import annotations

import argparse
import enum
import os
import platform
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlparse

import httpx

from .client import AgentMuxClient
from .errors import AgentMuxError
from .models import HealthReport, HealthState, version_key
from .release import (
    DEFAULT_REPOSITORY,
    AgentMuxReleaseError,
    archive_name,
    download_url,
    fetch_checksums,
    latest_release,
    verify_sha256,
)

MACOS_APP = Path("/Applications/AgentMux.app")
PRODUCTION_INSTALL_ROOT = Path("/opt/agentmux")
PRODUCTION_CONFIG_DIR = Path("/etc/agentmux")
SYSTEMD_UNIT_NAME = "agentmux.service"
DEFAULT_LOCAL_DATABASE_URL = "postgresql:///agentmux?host=/tmp&sslmode=disable"


class Action(enum.Enum):
    DONE = "done"
    PRESERVE = "preserve"
    UPGRADE = "upgrade"
    INSTALL = "install"


class BootstrapError(AgentMuxError):
    pass


@dataclass
class Options:
    mode: str
    version: str | None
    repository: str
    base_url: str
    bridge_token: str
    auto_install: bool
    install_root: Path
    database_url: str
    service_user: str
    config_file: Path | None
    systemd_unit_file: Path | None
    reuse_app: bool
    wait_seconds: int


def decide_action(report: HealthReport, target_version: str, mode: str) -> Action:
    """Pure decision logic shared by both modes (unit-tested)."""
    if report.state is not HealthState.READY:
        return Action.INSTALL
    running = version_key(report.version)
    target = version_key(target_version)
    if running is None or target is None:
        raise BootstrapError(
            f"cannot compare AgentMux versions: running={report.version!r} target={target_version!r}"
        )
    if running == target:
        return Action.DONE
    if running > target:
        return Action.PRESERVE
    # Running version is older than the pin: only production upgrades in place.
    return Action.UPGRADE if mode == "production" else Action.PRESERVE


def render_config(addr: str, database_url: str, bridge_token: str) -> str:
    """Minimal config.toml matching what both legacy scripts generated."""
    lines = [
        "[server]",
        f'addr = "{addr}"',
        "",
        "[database]",
        f'url = "{database_url}"',
        "",
        "[bridge]",
        f"enabled = {'true' if bridge_token else 'false'}",
    ]
    if bridge_token:
        lines.append(f'token = "{bridge_token}"')
    lines.append("")
    return "\n".join(lines)


def port_from_base_url(base_url: str) -> tuple[str, int]:
    parsed = urlparse(base_url)
    return parsed.hostname or "127.0.0.1", parsed.port or 8765


def port_is_listening(host: str, port: int, timeout: float = 1.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _log(message: str) -> None:
    print(f"agentmux-bootstrap: {message}", flush=True)


def _probe(options: Options) -> HealthReport:
    with AgentMuxClient(
        options.base_url,
        token=options.bridge_token or None,
        timeout=4.0,
        install_locations=(options.install_root / "current/amux",),
    ) as client:
        return client.health()


def _wait_healthy(options: Options, target_version: str | None) -> HealthReport | None:
    for _ in range(options.wait_seconds):
        time.sleep(1)
        report = _probe(options)
        if report.state is HealthState.READY and (
            target_version is None
            or version_key(report.version) == version_key(target_version)
        ):
            return report
    return None


def _resolve_target_version(options: Options) -> str:
    if options.version:
        if version_key(options.version) is None:
            raise BootstrapError(f"invalid target version: {options.version!r}")
        return options.version if options.version.startswith("v") else f"v{options.version}"
    release = latest_release(options.repository)
    _log(f"no pinned version given; using latest release {release.version}")
    return release.version


def _download_release(options: Options, version: str, destination: Path) -> Path:
    asset = archive_name(platform.system(), platform.machine())
    checksums = fetch_checksums(options.repository, version)
    if asset not in checksums:
        raise BootstrapError(f"release {version} has no checksum for {asset}")
    archive_path = destination / asset
    url = download_url(options.repository, version, asset)
    _log(f"downloading {url}")
    with httpx.Client(follow_redirects=True, timeout=120.0) as client:
        with client.stream("GET", url) as response:
            response.raise_for_status()
            with archive_path.open("wb") as handle:
                for chunk in response.iter_bytes():
                    handle.write(chunk)
    verify_sha256(archive_path, checksums[asset])
    return archive_path


def _extract_release(archive_path: Path, release_dir: Path) -> None:
    release_dir.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive_path, "r:gz") as archive:
        archive.extractall(release_dir, filter="data")
    binary = release_dir / "amux"
    if not binary.is_file():
        raise BootstrapError("release archive does not contain amux")
    binary.chmod(0o755)
    hook = release_dir / "agentmux-hook"
    if hook.is_file():
        hook.chmod(0o755)


def _switch_current(install_root: Path, release_dir: Path) -> Path | None:
    """Atomically point <root>/current at release_dir; return the previous target."""
    current = install_root / "current"
    previous = current.resolve() if current.is_symlink() else None
    staging = install_root / "current.next"
    if staging.is_symlink() or staging.exists():
        staging.unlink()
    staging.symlink_to(release_dir)
    staging.replace(current)
    return previous


def _rollback_current(install_root: Path, previous: Path | None) -> None:
    if previous is None:
        return
    current = install_root / "current"
    staging = install_root / "current.rollback"
    if staging.is_symlink() or staging.exists():
        staging.unlink()
    staging.symlink_to(previous)
    staging.replace(current)


def _run(command: list[str], **kwargs: object) -> None:
    _log("$ " + " ".join(command))
    subprocess.run(command, check=True, **kwargs)  # noqa: S603


def _start_local(options: Options, release_dir: Path) -> None:
    config_dir = options.install_root / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    (options.install_root / "data").mkdir(parents=True, exist_ok=True)
    config_path = config_dir / "config.toml"
    host, port = port_from_base_url(options.base_url)
    config_path.write_text(
        render_config(f"{host}:{port}", options.database_url, options.bridge_token),
        encoding="utf-8",
    )
    binary = release_dir / "amux"
    _run([str(binary), "--config", str(config_path), "database", "setup"])
    log_path = options.install_root / "agentmux.log"
    with log_path.open("ab") as log_handle:
        process = subprocess.Popen(  # noqa: S603
            [str(binary), "--config", str(config_path), "client", "--web"],
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    (options.install_root / "agentmux.pid").write_text(f"{process.pid}\n", encoding="utf-8")
    _log(f"spawned amux client --web (pid {process.pid}, log {log_path})")


def _start_production(options: Options) -> None:
    if os.geteuid() != 0:
        raise BootstrapError("production bootstrap must run as root")
    PRODUCTION_CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    config_target = PRODUCTION_CONFIG_DIR / "config.toml"
    if options.config_file is not None:
        shutil.copyfile(options.config_file, config_target)
    elif not config_target.exists():
        host, port = port_from_base_url(options.base_url)
        config_target.write_text(
            render_config(f"{host}:{port}", options.database_url, options.bridge_token),
            encoding="utf-8",
        )
    env_target = PRODUCTION_CONFIG_DIR / "agentmux.env"
    env_target.write_text(
        f"AGENTMUX_DATABASE_URL={options.database_url}\n"
        f"AGENTMUX_BRIDGE_TOKEN={options.bridge_token}\n",
        encoding="utf-8",
    )
    env_target.chmod(0o640)
    if options.systemd_unit_file is not None:
        shutil.copyfile(
            options.systemd_unit_file, Path("/etc/systemd/system") / SYSTEMD_UNIT_NAME
        )
    if options.service_user:
        _run(
            [
                "chown",
                "-R",
                f"{options.service_user}:{options.service_user}",
                str(options.install_root),
            ]
        )
    _run(["systemctl", "daemon-reload"])
    _run(["systemctl", "enable", "--now", SYSTEMD_UNIT_NAME])
    _run(["systemctl", "restart", SYSTEMD_UNIT_NAME])


def _reuse_macos_app(options: Options) -> bool:
    if platform.system() != "Darwin" or not MACOS_APP.exists() or not options.reuse_app:
        return False
    _log(f"starting existing {MACOS_APP}")
    subprocess.run(["open", "-a", "AgentMux"], check=True)  # noqa: S603, S607
    report = _wait_healthy(options, target_version=None)
    if report is not None:
        _log(f"reused the AgentMux desktop installation ({report.version})")
        return True
    raise BootstrapError(
        "AgentMux.app exists but did not expose a healthy API; refusing a second installation"
    )


def ensure(options: Options) -> int:
    target_version = _resolve_target_version(options)
    report = _probe(options)

    if report.state is HealthState.UNAUTHORIZED:
        raise BootstrapError(
            "AgentMux answered but rejected the credential; fix AGENTMUX_TOKEN or AGENTMUX_BRIDGE_TOKEN"
        )

    action = decide_action(report, target_version, options.mode)
    if action is Action.DONE:
        _log(f"AgentMux {report.version} is already healthy at {options.base_url}")
        return 0
    if action is Action.PRESERVE:
        _log(
            f"preserving healthy AgentMux {report.version}; pinned baseline is {target_version}"
        )
        return 0

    if action is Action.INSTALL:
        if _reuse_macos_app(options):
            return 0
        host, port = port_from_base_url(options.base_url)
        if port_is_listening(host, port):
            raise BootstrapError(
                f"port {port} is occupied by an unknown or unhealthy process; refusing to overwrite it"
            )
        if not options.auto_install:
            _log("AgentMux is missing and auto-install is disabled")
            return 1 if options.mode == "production" else 0

    with tempfile.TemporaryDirectory(prefix="agentmux-bootstrap-") as temp_dir:
        archive_path = _download_release(options, target_version, Path(temp_dir))
        release_dir = options.install_root / "releases" / target_version
        _extract_release(archive_path, release_dir)

    options.install_root.mkdir(parents=True, exist_ok=True)
    previous = _switch_current(options.install_root, release_dir)
    try:
        if options.mode == "production":
            _start_production(options)
        else:
            _start_local(options, release_dir)
        report = _wait_healthy(options, target_version)
        if report is None:
            raise BootstrapError(
                "AgentMux installation completed but the service is not healthy"
            )
    except Exception:
        _rollback_current(options.install_root, previous)
        if options.mode == "production" and previous is not None:
            subprocess.run(  # noqa: S603, S607
                ["systemctl", "restart", SYSTEMD_UNIT_NAME], check=False
            )
        raise
    _log(f"AgentMux {report.version} installed and healthy at {options.base_url}")
    return 0


def _env_flag(name: str, default: bool) -> bool:
    raw = os.environ.get(name, "").strip().lower()
    if raw == "":
        return default
    return raw in {"1", "true", "yes", "on"}


def parse_args(argv: list[str] | None = None) -> Options:
    parser = argparse.ArgumentParser(
        prog="python -m agentmux_sdk.bootstrap",
        description="Install, upgrade and start a local AgentMux daemon.",
    )
    parser.add_argument("--mode", choices=("local", "production"), default="local")
    parser.add_argument(
        "--version",
        default=os.environ.get("AGENTMUX_TARGET_VERSION") or None,
        help="pinned release tag (vX.Y.Z); defaults to the latest GitHub release",
    )
    parser.add_argument(
        "--repository",
        default=os.environ.get("AGENTMUX_REPOSITORY") or DEFAULT_REPOSITORY,
    )
    parser.add_argument(
        "--base-url",
        default=os.environ.get("AGENTMUX_BASE_URL") or "http://127.0.0.1:8765",
    )
    parser.add_argument("--install-root", default=os.environ.get("AGENTMUX_INSTALL_ROOT") or None)
    parser.add_argument(
        "--database-url",
        default=os.environ.get("AGENTMUX_DATABASE_URL") or DEFAULT_LOCAL_DATABASE_URL,
    )
    parser.add_argument(
        "--service-user", default=os.environ.get("AGENTMUX_SERVICE_USER") or ""
    )
    parser.add_argument("--config", default=None, help="config.toml to install (production)")
    parser.add_argument(
        "--systemd-unit", default=None, help="systemd unit file to install (production)"
    )
    parser.add_argument(
        "--no-app-reuse", action="store_true", help="do not reuse /Applications/AgentMux.app"
    )
    parser.add_argument("--wait-seconds", type=int, default=30)
    args = parser.parse_args(argv)

    if args.install_root:
        install_root = Path(args.install_root).expanduser()
    elif args.mode == "production":
        install_root = PRODUCTION_INSTALL_ROOT
    else:
        install_root = Path.cwd() / ".agentmux"

    return Options(
        mode=args.mode,
        version=args.version,
        repository=args.repository,
        base_url=args.base_url.rstrip("/"),
        bridge_token=(
            os.environ.get("AGENTMUX_TOKEN")
            or os.environ.get("AGENTMUX_BRIDGE_TOKEN", "")
        ).strip(),
        auto_install=_env_flag("AGENTMUX_AUTO_INSTALL", True),
        install_root=install_root,
        database_url=args.database_url,
        service_user=args.service_user,
        config_file=Path(args.config) if args.config else None,
        systemd_unit_file=Path(args.systemd_unit) if args.systemd_unit else None,
        reuse_app=not args.no_app_reuse,
        wait_seconds=args.wait_seconds,
    )


def main(argv: list[str] | None = None) -> int:
    try:
        return ensure(parse_args(argv))
    except (BootstrapError, AgentMuxReleaseError, AgentMuxError) as exc:
        print(f"agentmux-bootstrap: error: {exc}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(f"agentmux-bootstrap: command failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
