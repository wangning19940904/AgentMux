# Native observer integrations

AgentNexus ships isolated, additive observer plugins for Claude Code and Codex:

- `marketplaces/claude` is a Claude Code marketplace with
  `agentnexus-observer@agentnexus-local`.
- `marketplaces/codex` is a Codex marketplace with the same stable plugin ID.
  Its `.codex-plugin/plugin.json` intentionally omits a `hooks` field and uses
  default `hooks/hooks.json` discovery.

Both plugins call only `~/.agentnexus/bin/agentnexus-hook`. Missing helpers and
collector failures are fail-open, so other hooks and the agent continue.

The `integrations/native` Go package exposes read-only `Preview` and `Doctor`
operations plus conflict-safe `Install`, `Repair`, and `Uninstall` methods. Host
configuration changes always go through the native `claude plugin` or
`codex plugin` CLI. The manager never edits `hooks.state`, a shared hooks file,
CC Switch state, or Flux Island state. Its ownership manifests live under
`~/.agentnexus/integrations/`.

Package the helper next to the AgentNexus executable, or pass its build path as
`native.Options.HelperSource`:

```sh
go build -o dist/agentnexus-hook ./cmd/agentnexus-hook
```

For a real-CLI smoke test that still uses an isolated temporary HOME:

```sh
ANX_RUN_NATIVE_CLI_TEST=1 go test -run TestNativeCLIEndToEnd ./integrations/native
```
