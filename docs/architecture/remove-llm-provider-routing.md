# Removing LLM providers and routing: framework-native models and health

Status: proposal (rev 2) · 2026-11-14 · branch `claude/llm-provider-routing-integration-86a81b`

This proposal makes three changes.

1. **Remove AgentMux's "LLM Provider 与路由" (LLM Provider and Routing) module.** That covers:
   - the provider store and keychain secrets,
   - the local proxy and config takeover,
   - protocol translation and failover,
   - the provider monitor.
2. **Leave providers and routing to whatever the user runs**, such as
   [Magpie](https://github.com/yetone/magpie), another gateway, or plain
   official logins. The user configures each agent framework through that
   tool.
3. **AgentMux talks only to the agent frameworks.** It asks each framework
   which models it offers. It checks that a framework works by running it with
   the chosen model and asking it to reply "ok".

AgentMux does not know Magpie exists. It needs no gateway address, no
`/v1/models` call, no routing check, and no reads of the framework config
files. The only path to an LLM is the CLI path AgentMux already uses to run
agents.

---

## 1. What we remove

| AgentMux today | Size | Replacement |
| --- | --- | --- |
| Provider CRUD, presets and keychain secrets ([provider/manager.go](../../provider/manager.go), [provider/presets.json](../../provider/presets.json), [provider/secrets.go](../../provider/secrets.go)) | ~1k LOC | The user's router, or the framework's own login |
| Local proxy `127.0.0.1:15733` and forwarding ([provider/proxy.go](../../provider/proxy.go), [provider/forward.go](../../provider/forward.go)) | ~1.6k | The user's router |
| Protocol translation ([provider/xlate*.go](../../provider/), [provider/convert.go](../../provider/convert.go)) | ~2.1k | The user's router |
| Takeover, which rewrites `~/.claude/settings.json`, `~/.codex/config.toml` and the Claude Desktop 3p profile ([provider/takeover*.go](../../provider/takeover.go), [provider/liveconfig.go](../../provider/liveconfig.go), [provider/codex.go](../../provider/codex.go), [provider/claudedesktop.go](../../provider/claudedesktop.go)) | ~2.4k | The user's router. Magpie, for example, rewrites these keys itself |
| Failover queue, circuit breaker and model blocking | — | The user's router (Magpie routing groups, for example) |
| Six-hourly model refresh and liveness probe ([server/provider_monitor.go](../../server/provider_monitor.go), [server/provider_probe.go](../../server/provider_probe.go)) | ~1.6k | §3 and §4 |
| Fleet sync of providers and keys ([server/fleet_sync.go](../../server/fleet_sync.go) provider sections, `ProviderTransfer.tsx`) | — | Not needed |
| Proxy traces into usage and cost (`usage.Engine.RecordProxy`, `proxy_traces`) | — | Usage from agent transcripts, which already exists |
| Gateway web panel ([web/src/panels/gateway/](../../web/src/panels/gateway/)) and `ProvidersPanel.tsx`, which is unused | ~3.6k TS | A smaller "模型与可用性" (Models and Availability) view (§5) |
| `amux provider` CLI ([cmd/amux/provider.go](../../cmd/amux/provider.go)), tenancy `ResourceTypeProvider` | — | Removed |

Some existing code stays and becomes the foundation:
- Framework install and auth detection ([framework/detect.go](../../framework/detect.go), [framework/auth.go](../../framework/auth.go)).
- The per-framework runtime-settings catalog (`core.RuntimeSettingsCataloger`, `GET /api/v1/frameworks/runtime-settings`).
- The per-agent `--model` launch arguments.

---

## 2. Responsibilities after the change

```
┌──────── user's choice: Magpie / other gateway / official login ────────┐
│ providers · keys · routing · failover · writes framework configs        │
└───────────────────────────────▲────────────────────────────────────────┘
                                │ LLM traffic (AgentMux never sees it)
┌───────────────────────────────┴────────────────────────────────────────┐
│ claude / codex / cursor / trae …  (framework CLI, its own config)       │
└───────▲──────────────────▲──────────────────────────▲──────────────────┘
        │ installed? authed?│ "which models?"          │ run: --model X, "reply ok"
┌───────┴──────────────────┴──────────────────────────┴──────────────────┐
│ AgentMux: framework health · model catalog · per-agent model choice     │
└────────────────────────────────────────────────────────────────────────┘
```

AgentMux never writes any framework's config. It influences routing in only
one way: the model ID it passes when it launches an agent.

---

## 3. Model catalog, from the framework

The catalog needs no new endpoint. `GET /api/v1/frameworks/runtime-settings`
already asks each framework for its catalog ([server/frameworks.go:61](../../server/frameworks.go)).
When a router is in front, the framework's answer already reflects it.

| Framework | Source today | Status |
| --- | --- | --- |
| Codex | app-server `model/list` ([agent/cliagents/codex_app_server.go:109](../../agent/cliagents/codex_app_server.go)) | Works. A router that edits Codex's provider and catalog shows up here. |
| Trae CLI | `models --json` ([agent/cliagents/cliagents.go:81](../../agent/cliagents/cliagents.go)) | Works |
| Cursor | `--list-models` ([agent/cliagents/cliagents.go:147](../../agent/cliagents/cliagents.go)) | Works |
| Claude Code | stream-json `initialize` control response ([agent/claudecode/catalog.go](../../agent/claudecode/catalog.go)), added in P0 | Opens `claude --print --input-format stream-json --no-session-persistence`, sends the `initialize` request the Agent SDK sends for `supportedModels()`, reads `models[]` (`value`, `displayName`, `supportedEffortLevels`) and stops the process. No prompt is sent. Cached for 10 min on success and 1 min on failure. |

**Free-form model IDs.** Routers accept IDs that a framework catalog may not
list, such as Magpie's `group/opus-anywhere`. So the model picker always
allows a typed ID alongside the catalog. The health test (§4) is what
validates the ID. Recently used IDs are remembered per framework.

**Caching.** Keep the existing catalog TTL in `cliagent`. Add a "Refresh"
button. There is no background six-hourly loop.

**P0 findings.**
- **Codex.** On a machine where Magpie manages Codex, `amux frameworks models
  codex` already returns Magpie's namespaced IDs (`cursor/...`, `auto/...`).
  The framework catalog reflects the router without AgentMux knowing it
  exists.
- **Claude Code.** The `initialize` response shape comes from the Agent SDK
  0.3.296 type definitions. The Claude Code settings schema says its picker
  includes "gateway-discovered models". It has not yet been checked against a
  live `claude`, because the development machine has none installed.
- **Fallback.** If Claude Code lists only aliases, use the aliases (`sonnet`,
  `opus`, `haiku`, …) plus a typed ID. That fallback is still router-agnostic.

---

## 4. Reachability: run the framework and ask it to reply "ok"

### 4.1 Mechanism

Add a `framework.SmokeTest(ctx, kind, model, effort)` that goes through
**the same agent path real agents use**: `core.CreateAgent`, then
`StartSession`, then `Send`. It sends one prompt:

> Reply with exactly: ok

It succeeds when the final assistant text, trimmed and lowercased, contains
`ok` and the run exits cleanly. Because it uses the real launch path, it
exercises the same binary, environment, config, auth, `--model` argument and
router as a real agent run. A pass therefore means agents will work.

### 4.2 Keeping the test cheap and harmless

- **Empty directory.** Run it in a fresh temporary directory, delete it
  afterwards, and never use a project directory.
- **No tools.** Run with the most restrictive permission mode the adapter
  supports: read-only or plan, approvals denied, and tools disabled where the
  framework allows it. The prompt doesn't need tools anyway.
- **One turn and a timeout.** Limit the run to one turn, with a 60 s timeout
  that can be configured. A hang counts as a failure.
- **No new thread or session.** Agents are created with the
  `core.AgentConfigHealthCheck` config key, and adapters that can honour it
  do:
  - **Claude Code** adds `--tools= --max-turns 1 --no-session-persistence`.
  - **Codex** starts its thread with `ephemeral: true`. A test confirmed that
    no rollout file is written.
  - **Other adapters** ignore the key for now. They still run in plan or
    read-only mode in an empty temporary directory.
- **No usage cost.** Tag the run as `health_check` so usage accounting skips
  it, or counts it separately.
- **On demand only.** Nothing runs on a timer, because each run spends real
  tokens.

### 4.3 Result and error classification

```go
type SmokeResult struct {
    Framework, Model string
    OK               bool
    LatencyMs        int64
    Reply            string // first ~200 chars, for display
    ErrorKind        string // not_installed | unauthenticated | model_not_found |
                            // rate_limited | network | timeout | unexpected_reply | unknown
    ErrorDetail      string // tail of stderr / error event, secrets redacted
    CheckedAt        time.Time
}
```

`ErrorKind` comes from the adapter's error events and exit codes plus simple
pattern matching on the error text: 401/403, 404 or "model not found", 429,
connection refused, and so on. When a router is in front, its errors come
back through the framework. For example, Magpie returns 404 for an unknown
group. The user gets a useful message, and AgentMux still doesn't need to
know which router produced it.

Store the latest result per (machine, framework, model) in `settings` or in a
small `framework_health` table. This is display state only. It never blocks
running an agent.

### 4.4 When it runs

- The **"测试" (Test)** button on a framework row tests the framework's
  default model, or a model the user picks.
- **"测试全部" (Test all)** runs every installed, authenticated framework,
  with a concurrency limit of 2.
- **Saving an agent** after changing its model offers to test that model. The
  check isn't required and doesn't block the save.
- **`amux frameworks test <kind> [--model X]`** is the CLI form and supports
  `--json`.
- **Multiple machines:** a test against another machine goes through the
  existing fleet call, so the CLI runs on that machine.

### 4.5 Health levels

Each framework shows three badges: **Installed** (existing check), **Logged
in** (existing check) and **Reachable** (this smoke test).

Logged in can be "unknown" or "not logged in" while Reachable passes. That
happens when a router supplies the credential through config or an
environment variable. The UI treats Reachable as the deciding signal and
shows Logged in only as a hint.

---

## 5. UI

Replace "LLM Provider 与路由" with **"模型与可用性"**:

- **Framework table.** One row per framework, with Installed, Logged in and
  Reachable badges. Each row shows the default model, the last test time and
  latency, and has "测试" (Test) and "模型列表" (Model list) buttons. A failure
  expands to show `ErrorKind`, the error detail and a one-line hint, such as
  "Model not found — check the model ID or your router's configuration".
- **Model list.** This is the framework catalog from §3, filtered per
  framework, with Refresh. Clicking a model can test it, or use it for an
  agent.
- **Help text.** Something like: "Providers, API keys and routing are
  configured in the framework itself or in a router such as Magpie." This
  is the only mention of Magpie, and it lives in docs and copy, not in code.

The agent form and the super-agent model picker
([web/src/panels/superAgentModelPicker.tsx](../../web/src/panels/superAgentModelPicker.tsx))
always read from runtime-settings. That is today's "route not configured"
branch, which becomes the only branch. They also add a free-form model input.

---

## 6. Per-agent model choice

- Agents already launch with `--model <id>`.
- The `provider_tool`, `provider_id` and `provider_name` fields go away from
  all of these:
  - `AgentInstance` ([core/agent_instance.go](../../core/agent_instance.go)),
  - the Python SDK ([sdk/python/src/agentmux_sdk/models.py](../../sdk/python/src/agentmux_sdk/models.py)),
  - the super-agent worker copy ([superagent/autoworker.go:91](../../superagent/autoworker.go)).

  What remains is the framework plus its runtime settings (model, effort,
  service tier).
- Saving an agent **no longer calls `provider.Switch`**
  ([server/agent_instances.go:78](../../server/agent_instances.go)). Today
  that call rewrites the global framework config as a side effect, so two
  agents with different providers overwrite each other.
- Model options come from the framework catalog, replacing
  `agentProviderRuntimeDefaults` in [core/connect.go:554](../../core/connect.go).

---

## 7. Migration

The takeover feature that AgentMux shipped has left AgentMux-owned keys in
users' framework configs. Those keys have to be removed before the code that
knows about them is deleted. This step is independent of the router.

1. **Release ownership on upgrade.** If takeover is on, run the existing
   `DisableTakeover`. It restores only the keys AgentMux owns, using the
   ownership journal. Also remove the `[model_providers.agentmux]` block and
   the Claude Desktop 3p profile.

   Some users switched providers in "direct" mode, which wrote a real
   upstream URL and key into a config. Those values work, so leave them, and
   tell the user they are now the user's own configuration.
2. **Hand over the provider list.** Show a read-only list of each provider's
   name, base URL, protocol and models, with keys never shown. The user can
   re-create them in their router. Keychain items `AGENTMUX_PROVIDER_*` are
   deleted only after the user confirms, because the deletion can't be
   undone.
3. **Agents.** Keep each agent's model ID as it is. Run the smoke test for
   every distinct (framework, model) pair the agents use, and flag the
   failures in the UI.
4. **Schema cleanup.** Drop `providers`, `active_provider`, `proxy_config`,
   `proxy_live_backup` and `proxy_traces`. Delete the monitor and gateway-token
   keys from `settings`. Do this in both the SQLite and Postgres migrations.

## 8. Phases

| Phase | Ships | Removes |
| --- | --- | --- |
| **P0 Native catalog and test** ✅ | Claude Code `RuntimeSettingsCatalog`, `framework.SmokeTest` and error classification, `POST /api/v1/frameworks/test` (`kind`/`model`/`reasoning_effort`/`service_tier`, or `agent_id` to reuse an Agent's runtime, env and defaults), `amux frameworks models|test` | — |
| **P1 Read path** | The "模型与可用性" panel. The picker and agent form use only runtime-settings, plus free-form IDs. Saving an agent stops calling `provider.Switch`. | Gateway panel hidden behind a flag |
| **P2 Hand-off** | Release takeover on upgrade, the read-only provider list, the bulk agent test | Proxy no longer started at boot (`RestoreProxyState` removed) |
| **P3 Delete** | Schema cleanup | `provider/`, server provider, proxy, monitor and claude3p handlers and routes, `cmd/amux/provider.go`, provider sections of fleet sync, `web/src/panels/gateway/`, `ProvidersPanel.tsx`, provider API client and types, usage proxy hooks, tenancy provider grants |

P1 and P2 ship in separate releases from P3. That way, users who upgrade get
their configs restored by code that still knows the ownership journal.

## 9. Risks and open questions

- **The Claude Code catalog may list only aliases.** See §3. The fallback is
  aliases plus a typed ID that the smoke test validates.
- **Health checks cost tokens.** One short reply per test, on demand only, so
  the cost is negligible. Tag the run so usage accounting can exclude it.
- **Side effects of running the CLI.** User hooks, MCP servers and plugins
  still load during a smoke test. That is realistic, because real agents load
  them too, but it adds latency and can fail for reasons that aren't about the
  model. Classify those failures as `unknown`, show the error detail, and use
  a timeout. The no-tools mode keeps the test from doing anything.
- **"ok" is a heuristic.** A safety-tuned model might add words. We match
  "contains ok" after trimming, and report `unexpected_reply` with the actual
  text instead of failing silently.
- **Lost features.** Failover, hot switching and model blocking inside
  AgentMux go away, and per-request proxy traces and cost go with them. Those
  move to the user's router. Confirm that no workflow depends on the
  AgentMux-specific behaviour.
