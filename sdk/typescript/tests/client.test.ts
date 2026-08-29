import { describe, expect, it } from "vitest";

import { AgentMuxClient } from "../src/client.js";
import {
  AgentMuxBusyError,
  AgentMuxUnauthorizedError,
} from "../src/errors.js";

const CAPABILITIES = {
  ok: true,
  product: "agentmux",
  version: "v0.1.4",
  contract_version: "1.0",
  features: ["invocations", "invocations.stream", "send"],
  auth: { bridge_enabled: true },
};

type Handler = (url: URL, init: RequestInit) => Response | Promise<Response>;

function clientWith(handler: Handler, options: Record<string, unknown> = {}): AgentMuxClient {
  const mockFetch: typeof fetch = async (input, init) =>
    handler(new URL(String(input)), init ?? {});
  return new AgentMuxClient({ fetch: mockFetch, token: "secret", ...options });
}

function sseResponse(body: string): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(body));
      controller.close();
    },
  });
  return new Response(stream, {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

describe("health", () => {
  it("reports ready from capabilities", async () => {
    const client = clientWith(() => Response.json(CAPABILITIES));
    const report = await client.health();
    expect(report.state).toBe("ready");
    expect(report.version).toBe("v0.1.4");
    expect(report.capabilities?.features).toContain("invocations.stream");
  });

  it("reports unauthorized on 401", async () => {
    const client = clientWith(() => Response.json({ error: "nope" }, { status: 401 }));
    expect((await client.health()).state).toBe("unauthorized");
  });

  it("reports incompatible on foreign contract major", async () => {
    const client = clientWith(() =>
      Response.json({ ...CAPABILITIES, contract_version: "2.0" }),
    );
    expect((await client.health()).state).toBe("incompatible");
  });

  it("reports incompatible below minVersion", async () => {
    const client = clientWith(() => Response.json(CAPABILITIES), { minVersion: "v9.0.0" });
    expect((await client.health()).state).toBe("incompatible");
  });

  it("falls back to the legacy probe on 404", async () => {
    const client = clientWith((url) => {
      if (url.pathname === "/api/v1/capabilities") {
        return Response.json({ error: "not found" }, { status: 404 });
      }
      if (url.pathname === "/api/v1/status") {
        return Response.json({ ok: true, version: "v0.1.2" });
      }
      return Response.json([]);
    });
    const report = await client.health();
    expect(report.state).toBe("ready");
    expect(report.version).toBe("v0.1.2");
  });

  it("reports unreachable when fetch rejects", async () => {
    const client = clientWith(() => {
      throw new TypeError("fetch failed");
    });
    expect((await client.health()).state).toBe("unreachable");
  });
});

describe("invocations", () => {
  it("requires exactly one target", async () => {
    const client = clientWith(() => Response.json({}));
    await expect(client.invoke({ input: "hi" })).rejects.toThrow(/exactly one/);
    await expect(
      client.invoke({ input: "hi", agentId: "a", project: "p" }),
    ).rejects.toThrow(/exactly one/);
  });

  it("posts the contract payload and parses the result", async () => {
    let captured: unknown;
    const client = clientWith((url, init) => {
      expect(url.pathname).toBe("/api/v1/invocations");
      expect((init.headers as Record<string, string>).Authorization).toBe("Bearer secret");
      captured = JSON.parse(String(init.body));
      return Response.json({
        id: "inv-1",
        conversation_id: "conv-1",
        answer: "42",
        duration_ms: 7,
      });
    });
    const result = await client.invoke({
      agentId: "agent-abc",
      input: "meaning",
      conversationId: "conv-1",
    });
    expect(captured).toEqual({
      agent_id: "agent-abc",
      input: "meaning",
      conversation_id: "conv-1",
    });
    expect(result.answer).toBe("42");
  });

  it("streams events and exposes snapshot semantics", async () => {
    const body =
      'event: output\ndata: {"type":"output","text":"partial"}\n\n' +
      ": keepalive\n\n" +
      'event: output\ndata: {"type":"output","text":"partial answer"}\n\n' +
      'event: completed\ndata: {"type":"completed","final":true,"result":' +
      '{"id":"inv-1","conversation_id":"conv-1","answer":"partial answer","duration_ms":5}}\n\n';
    const client = clientWith((url) => {
      expect(url.pathname).toBe("/api/v1/invocations/stream");
      return sseResponse(body);
    });
    const events = [];
    for await (const event of client.invokeStream({ agentId: "a", input: "q" })) {
      events.push(event);
    }
    expect(events.map((event) => event.type)).toEqual(["output", "output", "completed"]);
    expect(events[1]?.text).toBe("partial answer");
    expect(events[2]?.result?.answer).toBe("partial answer");
  });
});

describe("errors and resources", () => {
  it("maps status codes to typed errors", async () => {
    const codes = [401, 409];
    const client = clientWith(() =>
      Response.json({ error: "nope" }, { status: codes.shift() ?? 500 }),
    );
    await expect(client.status()).rejects.toBeInstanceOf(AgentMuxUnauthorizedError);
    await expect(client.status()).rejects.toBeInstanceOf(AgentMuxBusyError);
  });

  it("round-trips resources", async () => {
    const client = clientWith((url, init) => {
      const { pathname } = url;
      const method = init.method ?? "GET";
      if (pathname === "/api/v1/agent-instances" && method === "GET") {
        return Response.json([{ id: "a1", name: "A", runtime_id: "codex", enabled: true }]);
      }
      if (pathname === "/api/v1/console/sessions" && method === "POST") {
        expect(url.searchParams.get("landing")).toBe("tenants");
        return Response.json({
          enter_url: "http://127.0.0.1:8765/console/enter?nonce=n",
          expires_at: "2026-01-01T00:00:00Z",
          session_ttl_seconds: 28800,
        });
      }
      if (pathname === "/api/v1/orchestrations" && method === "POST") {
        const payload = JSON.parse(String(init.body));
        expect(payload.tasks).toEqual([{ id: "t1", input: "do it" }]);
        return Response.json(
          { id: "orch-1", status: "queued", max_concurrency: 4 },
          { status: 202 },
        );
      }
      if (pathname === "/api/v1/usage") {
        expect(url.searchParams.get("period")).toBe("daily");
        return Response.json({ days: [] });
      }
      throw new Error(`unexpected ${method} ${pathname}`);
    });
    expect((await client.agents.list())[0]?.runtime_id).toBe("codex");
    expect((await client.console.createSession({ landing: "tenants" })).session_ttl_seconds).toBe(28800);
    expect((await client.orchestrations.create([{ id: "t1", input: "do it" }])).id).toBe("orch-1");
    expect(await client.usage()).toEqual({ days: [] });
  });

  it("composes a tenant-scoped integration snapshot", async () => {
    const client = clientWith((url) => {
      switch (url.pathname) {
        case "/api/v1/capabilities":
          return Response.json({
            ...CAPABILITIES,
            contract_version: "1.1",
            auth: { bridge_enabled: true, scope: "tenant", tenant: "host" },
          });
        case "/api/v1/tenancy/self":
          return Response.json({ admin: false, tenant: "host", tenant_id: "ten_host" });
        case "/api/v1/agents":
          return Response.json(["codex", "claude"]);
        case "/api/v1/platforms":
          return Response.json(["feishu", "webhook"]);
        case "/api/v1/agent-instances":
          return Response.json([{ id: "a", name: "Agent", runtime_id: "codex", enabled: true }]);
        case "/api/v1/channels":
          return Response.json([{ id: "c", name: "Channel", type: "feishu", enabled: true }]);
        case "/api/v1/triggers":
          return Response.json([{ id: "t", name: "Daily", kind: "cron", enabled: true }]);
        case "/api/v1/orchestrations":
          expect(url.searchParams.get("limit")).toBe("3");
          return Response.json([{ id: "o", status: "running", max_concurrency: 2 }]);
        default:
          throw new Error(`unexpected ${url.pathname}`);
      }
    });
    const snapshot = await client.integration.snapshot({ orchestrationLimit: 3 });
    expect(snapshot.identity.tenant).toBe("host");
    expect(snapshot.runtimes).toEqual(["codex", "claude"]);
    expect(snapshot.platforms).toEqual(["feishu", "webhook"]);
    expect(snapshot.agents[0]?.id).toBe("a");
    expect(snapshot.channels[0]?.id).toBe("c");
    expect(snapshot.triggers[0]?.id).toBe("t");
    expect(snapshot.orchestrations[0]?.id).toBe("o");
  });
});
