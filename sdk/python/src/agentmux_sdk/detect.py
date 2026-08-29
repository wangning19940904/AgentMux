"""Local AgentMux installation detection.

This is the single implementation of the "does this machine have AgentMux
installed?" check that host applications previously each hand-rolled. It only
distinguishes `unreachable` (installed but not answering) from `missing`
(never installed); it does not start anything.
"""

from __future__ import annotations

import shutil
from pathlib import Path

MANAGED_BINARY = Path("/opt/agentmux/current/amux")

DEFAULT_INSTALL_LOCATIONS: tuple[Path, ...] = (
    Path("/Applications/AgentMux.app"),
    MANAGED_BINARY,
    Path("/usr/local/bin/amux"),
    Path.home() / ".local/bin/amux",
)


def looks_installed(extra_locations: tuple[Path, ...] = ()) -> bool:
    """True when an AgentMux binary or app bundle is present on this machine.

    ``extra_locations`` lets a host application add its project-local install
    root (e.g. ``<repo>/.agentmux/current/amux``).
    """
    if shutil.which("amux") is not None:
        return True
    return any(path.exists() for path in (*DEFAULT_INSTALL_LOCATIONS, *extra_locations))
