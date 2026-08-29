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
client.agents.list()
client.channels.list()
client.triggers.run("trigger-id")
client.orchestrations.create([{"id": "t1", "input": "..."}])
client.usage(period="daily")
client.send(project="myproj", text="deploy finished")
```

## Build a native host integration page

Host applications should expose AgentMux through their own backend/BFF rather
than making Console navigation the primary experience:

```python
snapshot = client.integration.snapshot(orchestration_limit=8)
print(snapshot.identity.tenant)
for agent in snapshot.agents:
    print(agent.id, agent.enabled)
```

The aggregate uses only public SDK resources and remains tenant-scoped. Follow
[`contract/HOST_INTEGRATION.md`](../../contract/HOST_INTEGRATION.md) for the
required token boundary, SSE semantics, degradation rules and acceptance list.

## Embed the Console

```python
session = client.console.create_session()
# Redirect the browser or use session.enter_url as a sandboxed iframe src
# (valid ~60s, single use). It sets an HttpOnly cookie and loads the Console.
```

The session inherits this client's scope: with a tenant token the embedded
Console shows only that application's agents and channels.

## Register as a tenant

One AgentMux instance can be shared by several applications. Each is a
*tenant* and sees only the resources it owns, the ones marked public, and the
ones an administrator granted it.

Your backend self-registers once and stores the returned token. A new tenant
starts with an empty private namespace; an administrator grants resources
later:

```python
client = AgentMuxClient(base_url="http://127.0.0.1:8765")
result = client.tenancy.register("homebook", kind="web")
save_secret("AGENTMUX_TENANT_TOKEN", result.token)   # shown once, never again
```

From then on the token is used like any bridge token. To check what a
credential can see:

```python
report = client.health()
if report.capabilities and report.capabilities.tenant_scoped:
    print("scoped to", report.capabilities.tenant)

identity = client.tenancy.self()   # admin flag, tenant name, status
```

Agents and channels carry `owner_tenant_id`, `owner_tenant_name` and
`visibility`. Ownership is assigned by the server from the calling credential,
so writes never need to set it.

## Install / upgrade AgentMux (bootstrap)

```bash
python -m agentmux_sdk.bootstrap --mode local --version vX.Y.Z
sudo AGENTMUX_BRIDGE_TOKEN=... python -m agentmux_sdk.bootstrap --mode production --version vX.Y.Z
```

Downloads are verified against the GoReleaser `checksums.txt`; installs use
an atomic `current` symlink switch with automatic rollback on failed health
checks.

## Contract

This SDK speaks contract major `1` as defined in
[`contract/CONTRACT.md`](https://github.com/wangning19940904/AgentMux/blob/main/contract/CONTRACT.md).
Tenancy needs a server on contract `1.1` or later; feature-detect it with
`report.capabilities.supports("tenancy")`.
