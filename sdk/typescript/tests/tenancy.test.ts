import { describe, expect, it } from "vitest";
import { AgentMuxClient } from "../src/client.js";
import type { AgentInstance, Capabilities } from "../src/types.js";

function respond(status: number, body: unknown): typeof fetch {
  return (async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })) as unknown as typeof fetch;
}

describe("tenant registration", () => {
  it("registers an empty tenant without an existing token", async () => {
    const seen: { url?: string; body?: unknown } = {};
    const fetchImpl = (async (url: string, init?: RequestInit) => {
      seen.url = url;
      seen.body = JSON.parse(String(init?.body));
      expect(init?.headers).not.toHaveProperty("Authorization");
      return new Response(
        JSON.stringify({
          tenant: { id: "ten_abc", name: "homebook", kind: "web", status: "active" },
          token: "amxt_secret",
          prefix: "amxt_secret1",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as unknown as typeof fetch;

    const client = new AgentMuxClient({
      baseUrl: "http://agentmux.test",
      fetch: fetchImpl,
    });
    const result = await client.tenancy.register("homebook", "web");

    expect(seen.url).toBe("http://agentmux.test/api/v1/tenancy/register");
    expect(seen.body).toEqual({ kind: "web", name: "homebook" });
    expect(result.token).toBe("amxt_secret");
    expect(result.tenant.name).toBe("homebook");
  });

  it("defaults the kind to app", async () => {
    let body: unknown = null;
    const fetchImpl = (async (_url: string, init?: RequestInit) => {
      body = JSON.parse(String(init?.body));
      return new Response(
        JSON.stringify({ tenant: { id: "t", name: "n", status: "active" }, token: "amxt_x" }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as unknown as typeof fetch;

    const client = new AgentMuxClient({ baseUrl: "http://agentmux.test", fetch: fetchImpl });
    await client.tenancy.register("n");
    expect(body).toEqual({ name: "n", kind: "app" });
  });
});

describe("tenancy scope", () => {
  it("reads back the calling credential's identity", async () => {
    const client = new AgentMuxClient({
      baseUrl: "http://agentmux.test",
      token: "amxt_x",
      fetch: respond(200, {
        admin: false,
        tenant: "rookie",
        tenant_id: "ten_r",
        status: "active",
      }),
    });
    const identity = await client.tenancy.self();
    expect(identity.admin).toBe(false);
    expect(identity.tenant).toBe("rookie");
  });

  it("surfaces the scope through capabilities", () => {
    const scoped: Capabilities = {
      ok: true,
      product: "agentmux",
      version: "0.1.4",
      contract_version: "1.1",
      features: ["send", "tenancy"],
      auth: { bridge_enabled: true, scope: "tenant", tenant: "homebook", tenant_id: "ten_a" },
    };
    expect(scoped.auth?.scope).toBe("tenant");
    expect(scoped.features).toContain("tenancy");

    // A pre-1.1 server omits the scope, which means no tenancy at all.
    const legacy: Capabilities = {
      ok: true,
      product: "agentmux",
      version: "0.1.3",
      contract_version: "1.0",
      features: ["send"],
      auth: { bridge_enabled: true },
    };
    expect(legacy.auth?.scope).toBeUndefined();
  });
});

describe("ownership fields", () => {
  it("are readable on agents and channels", async () => {
    const client = new AgentMuxClient({
      baseUrl: "http://agentmux.test",
      token: "amxt_x",
      fetch: respond(200, [
        {
          id: "agent-1",
          name: "one",
          runtime_id: "codex",
          enabled: true,
          owner_tenant_id: "ten_abc",
          owner_tenant_name: "homebook",
          visibility: "public",
        },
      ]),
    });
    const agents = await client.agents.list();
    expect(agents[0]?.owner_tenant_name).toBe("homebook");
    expect(agents[0]?.visibility).toBe("public");
  });

  it("keep unknown server fields at runtime", async () => {
    const client = new AgentMuxClient({
      baseUrl: "http://agentmux.test",
      token: "amxt_x",
      fetch: respond(200, [
        { id: "a", name: "n", runtime_id: "codex", enabled: true, some_future_field: 42 },
      ]),
    });
    const agents = (await client.agents.list()) as AgentInstance[];
    expect(agents[0]?.some_future_field).toBe(42);
  });
});
