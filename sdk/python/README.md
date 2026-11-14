# agentmux-sdk

Official Python SDK for [AgentMux](https://github.com/wangning19940904/AgentMux):
health/capability discovery, direct Agent invocation (sync + SSE streaming),
management resources, Console session embedding and a bootstrap installer.

> The PyPI name `agentmux` belongs to an unrelated project, so this package
> is `agentmux-sdk` and imports as `agentmux_sdk`.

```bash
pip install agentmux-sdk    # or: uv add agentmux-sdk
```

## Health handshake

```python
from agentmux_sdk import AgentMuxClient

client = AgentMuxClient(
    base_url="http://127.0.0.1:8765",
    token="<bridge token>",           # optional when bridge auth is disabled
    min_version="v0.1.3",             # optional consumer floor
)

report = client.health()
# report.state: ready | unauthorized | incompatible | unreachable | missing
print(report.state, report.version, report.contract_version)
```

## Run an Agent

```python
result = client.invoke(agent_id="agent-abc", input="summarize today's spending")
print(result.answer)

for event in client.invoke_stream(agent_id="agent-abc", input="analyze this portfolio"):
    if event.type in ("output", "thinking"):
        render(event.text)   # full snapshot: replace previous text, don't append
    elif event.type == "completed":
        print(event.result.answer)
```

An async twin (`AsyncAgentMuxClient`) offers the same API with `await` /
`async for`.

## Management resources

```python
from agentmux_sdk import OrchestrationTask

client.agents.list()
client.channels.list()
client.triggers.run("trigger-id")
client.orchestrations.create([OrchestrationTask(id="t1", agent_id="agent-abc", input="...")])
client.usage(period="daily")
client.send(channel_id="channel-id", conversation_key="root:message-id", text="deploy finished")
```

## Build a native host integration page

Host applications should expose AgentMux through their own backend/BFF rather
than making Console navigation the primary experience:

```python
snapshot = client.integration.snapshot(orchestration_limit=8)
print(snapshot.capabilities.contract_version)
for agent in snapshot.agents:
    print(agent.id, agent.enabled)
```

The aggregate uses only public SDK resources. Follow
[`contract/HOST_INTEGRATION.md`](../../contract/HOST_INTEGRATION.md) for the
required token boundary, SSE semantics, degradation rules and acceptance list.

## Embed the Console

```python
session = client.console.create_session()
# Redirect the browser or use session.enter_url as a sandboxed iframe src
# (valid ~60s, single use). It sets an HttpOnly cookie and loads the Console.
```

Minting a Console session requires the bridge token and yields an
administrator Console.

## Authentication

Third-party integrations authenticate with the AgentMux bridge token
(`[bridge].token` in `config.toml`), sent as `Authorization: Bearer <token>`.
It grants administrator access to every route and resource, so keep it on your
backend/BFF and never ship it to a browser. Contract 3.0 removed multi-tenancy:
there are no tenant tokens, registration endpoints, ownership or visibility
fields. `report.capabilities.scope` is `"admin"` for the bridge token
(`"member"` only appears for signed-in Console member accounts).

## Install / upgrade AgentMux (bootstrap)

```bash
python -m agentmux_sdk.bootstrap --mode local --version vX.Y.Z
sudo AGENTMUX_BRIDGE_TOKEN=... python -m agentmux_sdk.bootstrap --mode production --version vX.Y.Z
```

Downloads are verified against the GoReleaser `checksums.txt`; installs use
an atomic `current` symlink switch with automatic rollback on failed health
checks.

## Contract

This SDK speaks contract major `3` as defined in
[`contract/CONTRACT.md`](https://github.com/wangning19940904/AgentMux/blob/main/contract/CONTRACT.md).
Servers reporting another major are reported as `incompatible`. Contract 3.0
is breaking: the `client.tenancy` resource, the `Tenant`, `TenantRegistration`
and `TenancySelf` models, `IntegrationSnapshot.identity`,
`Capabilities.tenant`/`tenant_id`/`tenant_scoped`, and the `owner_tenant_id`,
`owner_tenant_name` and `visibility` fields were removed. Feature-detect
optional surfaces with `report.capabilities.supports(...)`.

## Local event subscriptions (contract 2.2)

`client.events` (also available on `AsyncAgentMuxClient`) supports `sources()`,
`list()`, `upsert(config)`, `delete(id)`, `test(id)`, `rotate_secret(id)`,
`deliveries(...)`, `delivery(id)` and `retry(id)`. Probe the
`event_subscriptions` capability before using it. Registration returns an
`EventSubscriptionResult`; `signing_secret` is present only on first creation.
Use `verify_event_signature` on the raw HTTP body before parsing JSON, then
persist the event ID in a durable inbox before returning a successful response.

See [the event contract](../../contract/EVENT_SUBSCRIPTIONS.md) and the
[FastAPI example](../../integrations/homebook-events/README.md). Subscription
keys are unique per AgentMux instance.
