# Removing LLM providers and routing: framework-native models and health

Status: implemented · 2026-11-14 · branch `claude/llm-provider-routing-integration-86a81b`

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

The existing **Agent 框架** (Frameworks) page already lists each framework
with its install and sign-in state, so model availability is added there
rather than on a separate page. "LLM Provider 与路由" (LLM Provider and
Routing) leaves the navigation in P1. Its `#gateway` address still opens it
until P3, so existing provider settings stay readable during the hand-off.

- **Framework table.** A new "可用性" (Availability) column shows each
  framework's latest test: "Works · 4.7 s" with the model and the model a
  router resolved it to, or the failure kind with the framework's own
  message. It has "测试" (Test) and "模型" (Models) buttons. "全部测试" (Test
  all) tests every framework shown, two at a time.
- **Models dialog.** This is the framework catalog from §3, with Refresh. It
  has a Test button per model and an input for model IDs the framework does
  not list (a router's group, for example). Results are kept per model.
- **Agent form.** The model section is titled "模型" (Model). The provider
  selector is gone. The model select ends with "其他模型（手动填写）…" (Other
  model, enter an ID…), which shows a text input. A "测试" (Test) button tests
  the form's current model, effort and speed in the Agent's saved environment
  (`agent_id` + explicit `kind`).
- **Super-agent model picker**
  ([web/src/panels/superAgentModelPicker.tsx](../../web/src/panels/superAgentModelPicker.tsx)).
  It always reads runtime-settings. That was the old "route not configured"
  branch, and it is now the only branch. It also gains an "Other model"
  input.
- **Overview.** The "模型服务" (Model services) health row, which came from
  the provider monitor, becomes "框架可用性" (Framework availability). It
  counts each framework's latest test on each machine, from `GET
  /api/v1/frameworks/health`.
- **Copy.** Strings say providers and routing live "in the framework or a
  router such as Magpie". That copy is the only place Magpie is mentioned;
  no code knows about it.

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

1. **Release ownership on every start** (`provider.Service.RetireLocalRouting`,
   [provider/handoff.go](../../provider/handoff.go)). It replaces the old
   boot-time proxy restore, and the proxy never starts again. For each taken-over
   tool (Claude Code, Codex, Claude Desktop):
   - **With an ownership journal,** it puts back only the values AgentMux
     wrote and that are still AgentMux's. A value someone else changed since,
     for example Magpie rewriting `ANTHROPIC_BASE_URL`, is left as it is and
     reported, rather than failing the release as `DisableTakeover` does.
     Version-one journals hold whole files; such a file is restored only while
     it still points at the proxy.
   - **Without a journal,** it rewrites a config to the direct provider only if
     the config still points at the proxy. Any other config is left untouched.
   - **Leftovers** are not removed separately. Restoring owned values already
     removes the `[model_providers.agentmux]` block and the Claude Desktop
     profile when AgentMux added them. Direct-mode blocks hold a working
     upstream URL and key, so they stay and are now the user's own
     configuration.
   - **Failures** (for example, a locked file) keep that tool's journal and are
     retried every few seconds until they succeed. The outcome per tool is
     recorded in `settings` (`provider_handoff_v1`) and merged across retries.
2. **Hand over the provider list.** The former provider page (`#gateway`,
   [ProviderHandoffPanel.tsx](../../web/src/panels/ProviderHandoffPanel.tsx))
   is now read-only. It shows:
   - what was given back;
   - each saved provider's name, base URL, protocol, models, key name and the
     tools that used it, with keys never shown;
   - a "删除已保存的 Key" (delete saved keys) action, which deletes the
     keychain items only after the user confirms
     (`POST /api/v1/providers/handoff/forget-keys` requires `confirm`).

   The Frameworks page links there when this machine has a record or saved
   providers. Every provider and routing write endpoint answers 410 Gone. The
   provider monitor no longer runs. Fleet sync no longer copies providers or
   keys in either direction.
3. **Agents.** Each agent keeps its model ID. The hand-off page groups agents
   by framework, model, effort and speed, and tests each group with one
   agent's saved settings. At run time an agent's settings are its own: the
   runtime no longer reads provider routes for defaults or model lists, and
   Claude Code sessions fill their in-chat model menu from the framework's own
   (cached) list.
4. **Schema cleanup.** Drop `providers`, `active_provider`, `proxy_config`,
   `proxy_live_backup` and `proxy_traces`. Delete the monitor and gateway-token
   keys from `settings`. Do this in both the SQLite and Postgres migrations.

## 8. Phases

| Phase | Ships | Removes |
| --- | --- | --- |
| **P0 Native catalog and test** ✅ | Claude Code `RuntimeSettingsCatalog`, `framework.SmokeTest` and error classification, `POST /api/v1/frameworks/test` (`kind`/`model`/`reasoning_effort`/`service_tier`, or `agent_id` to reuse an Agent's runtime, env and defaults), `amux frameworks models|test` | — |
| **P1 Read path** ✅ | Availability on the Frameworks page (§5); latest results stored per machine (`GET /api/v1/frameworks/health`). The picker and agent form use only runtime-settings, plus typed IDs. Agent defaults are validated against the framework catalog, never a provider. Typed IDs are accepted, with effort and speed checked against what the framework offers. Saving an agent stops calling `provider.Switch`. The Overview row uses framework tests. | Gateway page removed from the navigation (still at `#gateway`). Provider and route loading removed from the Agents page. |
| **P2 Hand-off** ✅ | Release on every start, with drift kept and retries (§7). The read-only hand-off page with a confirmed key deletion. Grouped agent model tests. Runtime defaults no longer come from providers. | Proxy never started; `EnableTakeover` returns `ErrLocalRoutingRetired`. Provider, route, proxy, monitor and Claude 3p write endpoints return 410, and their handlers are deleted. The provider monitor loop is gone. Fleet sync no longer copies providers or keys. |
| **P3a Delete what the hand-off doesn't need** ✅ | — | `provider/` reduced to the hand-off (journal restore, live-config writers for the rebuild path, keychain secrets): the proxy, protocol translation, presets, provider CRUD and switching are gone. Server provider, route, proxy, monitor, probe and Claude 3p handlers and routes are deleted (404), leaving only `GET /providers/handoff` and `POST /providers/handoff/forget-keys`. Also removed: `cmd/amux/provider.go`; desktop `SwitchProvider`; fleet-sync provider sections (manifests from older machines are ignored); usage and observability proxy hooks; `core.ProviderManager`; tenancy provider grants (agents are authorized by agent grants alone); the web gateway UI, provider API client and types, proxy traces in Sessions, provider grants in Tenants, and their strings and styles. |
| **P3b Schema cleanup** ✅ | Postgres migration v28 (`retire_provider_routing`) drops `providers`, `active_provider`, `proxy_config`, `proxy_live_backup`, `proxy_traces` and `agent_instances.provider_tool`/`provider_id`, and deletes stored Provider grants and the provider settings keys. Legacy SQLite stores drop the same tables on open. Contract 3.0. Checked on a fresh database (all Postgres integration tests) and by upgrading a database written by the previous build. | The hand-off (`provider/`, the hand-off page and its endpoints), `core.Provider*`, `core.ProxyTrace`, the store's provider and trace methods, the proxy half of the legacy observation import, `[provider]` config, `provider_*` fields in the SDKs, and the `#gateway` route (old links open the Frameworks page). |

All phases ship together. That skips the hand-off for anyone upgrading
straight from a build that had local routing turned on: their framework
configs keep pointing at the retired proxy (`127.0.0.1:15733` with a
`PROXY_MANAGED` token) and must be repointed by hand, for example by letting
the framework or a router such as Magpie rewrite them. This was accepted
because no deployment relied on local routing.

### Multi-tenancy removed alongside (contract 3.0)

The same release removes the tenancy model. It existed partly to scope
providers per host application; without providers, nothing relied on it.

- **Gone:** tenants, tenant tokens (`amxt_`) and self-registration, resource
  grants, ownership and visibility on agents, channels, triggers, event
  subscriptions and orchestrations, `/api/v1/tenancy/*`, `amux tenants`, the
  Tenants page, and the `tenancy` capability feature. Integrations use the
  bridge token, which is admin.
- **Kept:** accounts. A principal is now admin or member
  ([core/principal.go](../../core/principal.go)). Members reach only their own
  devices, mailbox, push subscriptions and password (`memberRoutePolicy` in
  [server/auth_policy.go](../../server/auth_policy.go)); everything else is
  admin-only. Devices, pairing codes and push subscriptions belong to an
  account (`owner_account_id`, `account:<id>`). Admins reassign devices on
  System → Accounts, with `POST /api/v1/devices/owner` or
  `amux accounts assign-device`.
- **Migration v29 (`retire_tenancy`)** maps device and pairing-code owners and
  push subscribers from each member's tenant to the account, keeps event
  subscriptions that shared a key across tenants by suffixing `@<tenant id>`,
  drops push subscriptions of non-account tenants, then drops the tenancy
  tables and columns and the hub's stored device reports of the removed
  lists. Checked on a fresh database and by upgrading a database seeded by
  the previous build.

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
