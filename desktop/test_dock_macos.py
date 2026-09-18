#!/usr/bin/env python3
"""Exercise the real Wails/AppKit Dock lifecycle in an isolated test app."""

import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile


def main():
    if sys.platform != "darwin":
        raise SystemExit("This test requires a macOS desktop session.")
    root = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="agentmux-dock-test-") as directory:
        contents = Path(directory) / "DockSmoke.app" / "Contents"
        executable = contents / "MacOS" / "DockSmoke"
        executable.parent.mkdir(parents=True)
        with (contents / "Info.plist").open("wb") as output:
            plistlib.dump({
                "CFBundleExecutable": "DockSmoke",
                "CFBundleIdentifier": "com.agentmux.dock-smoke",
                "CFBundleName": "AgentMux Dock Smoke Test",
                "CFBundlePackageType": "APPL",
            }, output)
        env = os.environ.copy()
        # Match the framework flag supplied by the Wails build command.
        env["CGO_LDFLAGS"] = env.get("CGO_LDFLAGS", "") + " -framework UniformTypeIdentifiers"
        subprocess.run([
            "go", "build", "-tags", "desktop production", "-o", str(executable),
            "./desktop/testdata/dock_smoke",
        ], cwd=root, env=env, check=True)
        subprocess.run([str(executable)], check=True, timeout=45)


if __name__ == "__main__":
    main()
