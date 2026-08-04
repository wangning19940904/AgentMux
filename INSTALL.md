# Installing & building AgentMux

## Prerequisites

- **Go 1.25+** (core, CLI, daemon)
- **Node.js 20+** with npm (WebUI build)
- **PostgreSQL 16+** (runtime data store)
- **Wails v2 toolchain** (only for the desktop app):
  `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- **Xcode command line tools** (only for the macOS menu bar app)

At least one coding agent installed for the gateway/usage features, e.g.:

```bash
npm install -g @anthropic-ai/claude-code   # Claude Code
```

## Build the CLI

```bash
make build          # ./amux (dev placeholder WebUI)
make release        # embeds the React WebUI (runs `make web` first)
make cross          # dist/ binaries for Linux/macOS/Windows (amd64+arm64)
```

`make cross` produces, e.g.:

```
dist/amux-0.1.0-linux-amd64
dist/amux-0.1.0-darwin-arm64
dist/amux-0.1.0-windows-amd64.exe
```

The CLI binary is statically linked (CGO disabled) and self-contained.

## Set up PostgreSQL

AgentMux uses PostgreSQL as its runtime store. On macOS with Homebrew:

```bash
brew install postgresql@16
amux database setup
```

The default connection is
`postgresql:///agentmux?host=/tmp&sslmode=disable`. Override it with
`[database].url`, `AGENTMUX_DATABASE_URL`, or `--database-url`.

To migrate an existing AgentMux SQLite store while retaining 30 days of
detailed observations:

```bash
amux database migrate-sqlite --source ~/.agentmux/agentmux.db \
  --observations-since 30d --dry-run
# Stop AgentMux, then run again without --dry-run.
amux database migrate-sqlite --source ~/.agentmux/agentmux.db \
  --observations-since 30d
```

The migration creates a consistent timestamped SQLite backup before copying.

## Linux headless client

AgentMux does not require a desktop shell on Linux. Use the same `amux`
binary as a foreground client, a server daemon, or a CLI toolbox:

```bash
sudo install -m 0755 dist/amux-0.1.0-linux-amd64 /usr/local/bin/amux
amux config init
amux client --web
```

The config lookup order is:

1. `--config/-c`
2. `AMUX_CONFIG`
3. `./config.toml`
4. `$XDG_CONFIG_HOME/agentmux/config.toml`
5. `/etc/agentmux/config.toml`

Useful client commands:

```bash
amux client                  # run the local daemon in the foreground
amux client --web            # print the WebUI URL without opening a browser
amux client --web --open     # also try xdg-open
amux tools list              # inspect supported local CLI tools
amux usage daily --since 7d  # report token usage
```

## Build the WebUI only

```bash
make web            # outputs web/dist (also served via Vite dev: cd web && npm run dev)
```

In dev, run `npm run dev` in `web/` (proxies `/api` to `127.0.0.1:8765`) and
`amux serve` in another terminal.

## Build the desktop app (Wails)

```bash
# one-time: let Wails find the shared React app
ln -s ../web desktop/frontend

make desktop        # builds the native app via the desktop build tag
```

On macOS, `make desktop` also builds and bundles the menu bar helper so the
desktop app can show a status item while it is running. It also bundles
Linux/macOS amd64/arm64 CLI payloads used by **Machines → Import** to install
AgentMux on an SSH target when the remote service is missing.

The desktop shell starts the daemon in-process and renders the same WebUI.

## Build the macOS menu bar app

```bash
make menubar        # produces macos-menubar/AgentMuxMenuBar
```

Run it after starting the daemon (`amux serve`). It shows only the AgentMux
logo by default; the Menu Bar settings page can opt into the animated status
icon, estimated cost, token counts, and message count.

## Sign & notarize (macOS distribution)

```bash
export MACOS_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)"
export AC_PROFILE="notarytool-profile"
make sign-macos
# then: xcrun notarytool submit <zipped artifact> --keychain-profile "$AC_PROFILE" --wait
```

## Linux service

Run `amux client` or `amux serve` under systemd. A minimal unit:

```ini
[Unit]
Description=AgentMux
After=network-online.target postgresql.service

[Service]
ExecStart=/usr/local/bin/amux client --config /etc/agentmux/config.toml --web
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## SSH token statistics

Add one or more `[[usage.ssh]]` targets to `config.toml`, then:

```bash
amux usage daily --ssh
```

The collector connects, tars the configured remote session directories, syncs
them into `~/.agentmux/ssh/<target>/`, and runs the same parsers locally.
Records are tagged with the target name so you can break usage down per host.
