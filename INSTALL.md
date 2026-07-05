# Installing & building AgentNexus

## Prerequisites

- **Go 1.25+** (core, CLI, daemon)
- **Node.js 20+** with npm (WebUI build)
- **Wails v2 toolchain** (only for the desktop app):
  `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- **Xcode command line tools** (only for the macOS menu bar app)

At least one coding agent installed for the gateway/usage features, e.g.:

```bash
npm install -g @anthropic-ai/claude-code   # Claude Code
```

## Build the CLI

```bash
make build          # ./anx (dev placeholder WebUI)
make release        # embeds the React WebUI (runs `make web` first)
make cross          # dist/ binaries for Linux/macOS/Windows (amd64+arm64)
```

`make cross` produces, e.g.:

```
dist/anx-0.1.0-linux-amd64
dist/anx-0.1.0-darwin-arm64
dist/anx-0.1.0-windows-amd64.exe
```

The CLI binary is statically linked (CGO disabled) and self-contained.

## Build the WebUI only

```bash
make web            # outputs web/dist (also served via Vite dev: cd web && npm run dev)
```

In dev, run `npm run dev` in `web/` (proxies `/api` to `127.0.0.1:8765`) and
`anx serve` in another terminal.

## Build the desktop app (Wails)

```bash
# one-time: let Wails find the shared React app
ln -s ../web desktop/frontend

make desktop        # builds the native app via the desktop build tag
```

On macOS, `make desktop` also builds and bundles the menu bar helper so the
desktop app can show a status item while it is running.

The desktop shell starts the daemon in-process and renders the same WebUI.

## Build the macOS menu bar app

```bash
make menubar        # produces macos-menubar/AgentNexusMenuBar
```

Run it after starting the daemon (`anx serve`). It polls
`/api/v1/usage` and shows today's estimated cost in the menu bar.

## Sign & notarize (macOS distribution)

```bash
export MACOS_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)"
export AC_PROFILE="notarytool-profile"
make sign-macos
# then: xcrun notarytool submit <zipped artifact> --keychain-profile "$AC_PROFILE" --wait
```

## Linux service

Run `anx serve` under systemd. A minimal unit:

```ini
[Unit]
Description=AgentNexus
After=network-online.target

[Service]
ExecStart=/usr/local/bin/anx serve -c /etc/agentnexus/config.toml
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## SSH token statistics

Add one or more `[[usage.ssh]]` targets to `config.toml`, then:

```bash
anx usage daily --ssh
```

The collector connects, tars the configured remote session directories, syncs
them into `~/.agentnexus/ssh/<target>/`, and runs the same parsers locally.
Records are tagged with the target name so you can break usage down per host.
