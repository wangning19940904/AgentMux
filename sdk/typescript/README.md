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

Use the SDK from the host backend/BFF; never expose its bridge token to browser
code:

```ts
const snapshot = await client.integration.snapshot({ orchestrationLimit: 8 });
console.log(snapshot.capabilities.contract_version, snapshot.agents, snapshot.triggers);
```

The aggregate uses only public SDK resources. The normative architecture and acceptance checklist live in
[`contract/HOST_INTEGRATION.md`](../../contract/HOST_INTEGRATION.md).

## Embed the Console (backend)

```ts
const session = await client.console.createSession();
// Redirect the browser or use session.enter_url as a sandboxed iframe src.
```

Minting a Console session requires the bridge token and yields an
administrator Console.

## Authentication

Third-party integrations authenticate with the AgentMux bridge token
(`[bridge].token` in `config.toml`), sent as `Authorization: Bearer <token>`.
It grants administrator access to every route and resource, so keep it on your
backend/BFF and never ship it to a browser. Contract 3.0 removed multi-tenancy:
there are no tenant tokens, registration endpoints, ownership or visibility
fields. `capabilities.auth.scope` is `"admin"` for the bridge token (`"member"`
only appears for signed-in Console member accounts).

## Contract

This SDK speaks contract major `3` as defined in
[`contract/CONTRACT.md`](https://github.com/wangning19940904/AgentMux/blob/main/contract/CONTRACT.md).
Servers reporting another major are reported as `incompatible`. Contract 3.0
is breaking: `client.tenancy`, the `Tenant`, `TenantRegistration`,
`TenancySelf` and `ResourceVisibility` types, `IntegrationSnapshot.identity`,
`auth.tenant`/`tenant_id`, and the `owner_tenant_id`, `owner_tenant_name` and
`visibility` fields were removed. Feature-detect optional surfaces with
`capabilities.features.includes(...)`.

## Local event subscriptions (contract 2.2)

Use `client.events.sources()`, `list()`, `upsert(config)`, `delete(id)`, `test(id)`,
`rotateSecret(id)`, `deliveries(options)`, `delivery(id)` and `retry(id)` after
checking the `event_subscriptions` capability. `upsert` returns
`{ subscription, signing_secret? }`; the signing secret is returned only once.
Keep that secret in the receiving backend, never in browser configuration.

`verifyEventSignature(secret, { timestamp, deliveryId, signature }, rawBytes)`
uses Web Crypto to verify the exact request body and a five-minute timestamp
window. Persist event IDs to deduplicate work before acknowledging callbacks.
Subscription keys are unique per AgentMux instance.
See [the event contract](../../contract/EVENT_SUBSCRIPTIONS.md).
