# agentmux-sdk

Official TypeScript SDK for [AgentMux](https://github.com/wangning19940904/AgentMux).
Fetch-based, works in browsers and Node (>=18), ships ESM + CJS + types.

```bash
npm install agentmux-sdk
```

> **Browser usage**: never embed the bridge token in frontend code. Either
> proxy AgentMux calls through your own backend (BFF), or have your backend
> mint a Console session (`console.createSession()`) so the browser
> authenticates with an HttpOnly cookie.

## Health handshake

```ts
import { AgentMuxClient } from "agentmux-sdk";

const client = new AgentMuxClient({
  baseUrl: "http://127.0.0.1:8765",
  token: process.env.AGENTMUX_BRIDGE_TOKEN, // backend only
  minVersion: "v0.1.3",
});

const report = await client.health();
// report.state: ready | unauthorized | incompatible | unreachable | missing
```

## Run an Agent

```ts
const result = await client.invoke({ agentId: "agent-abc", input: "summarize today" });

for await (const event of client.invokeStream({ agentId: "agent-abc", input: "analyze" })) {
  if (event.type === "output" || event.type === "thinking") {
    render(event.text); // full snapshot: replace previous text, don't append
  } else if (event.type === "completed") {
    console.log(event.result?.answer);
  }
}
```

## Management resources

```ts
await client.agents.list();
await client.channels.list();
await client.triggers.run("trigger-id");
await client.orchestrations.create([{ id: "t1", agent_id: "agent-abc", input: "..." }]);
await client.usage({ period: "daily" });
await client.send({ channelId: "channel-id", conversationKey: "root:message-id", text: "deploy finished" });
```

## Build a native host integration page

Use the SDK from the host backend/BFF; never expose its tenant token to browser
code:

```ts
const snapshot = await client.integration.snapshot({ orchestrationLimit: 8 });
console.log(snapshot.identity.tenant, snapshot.agents, snapshot.triggers);
```

The aggregate uses only public SDK resources and is filtered by the caller's
tenant scope. The normative architecture and acceptance checklist live in
[`contract/HOST_INTEGRATION.md`](../../contract/HOST_INTEGRATION.md).

## Embed the Console (backend)

```ts
const session = await client.console.createSession();
// Redirect the browser or use session.enter_url as a sandboxed iframe src.
```

The session inherits the client's scope: with a tenant token the embedded
Console shows only that application's agents and channels.

## Register as a tenant

One AgentMux instance can be shared by several applications. Each is a
*tenant* and sees only the resources it owns, the ones marked public, and the
ones an administrator granted it.

Your backend self-registers once and stores the returned token. New tenants
start empty; an administrator grants resources later:

```ts
const registrationClient = new AgentMuxClient({ baseUrl: "http://127.0.0.1:8765" });
const result = await registrationClient.tenancy.register("homebook", "web");
await saveSecret("AGENTMUX_TENANT_TOKEN", result.token); // shown once
```

To check what a credential can see:

```ts
const report = await client.health();
if (report.capabilities?.auth?.scope === "tenant") {
  console.log("scoped to", report.capabilities.auth.tenant);
}

const identity = await client.tenancy.self();
```

Agents and channels carry `owner_tenant_id`, `owner_tenant_name` and
`visibility`. Ownership is assigned by the server from the calling credential.

## Contract

This SDK speaks contract major `2` as defined in
[`contract/CONTRACT.md`](https://github.com/wangning19940904/AgentMux/blob/main/contract/CONTRACT.md).
Feature-detect tenancy with
`capabilities.features.includes("tenancy")`.
