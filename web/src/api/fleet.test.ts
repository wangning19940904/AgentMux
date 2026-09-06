import { afterEach, describe, expect, it, vi } from "vitest";
import { activeMachineScope, fleetQuery, resolveMachineScope, resolveTenantScope, tenantScopeHeaders, tenantScopeKey } from "./client";
import { beginFleetWarningUpdate, currentFleetWarnings, fleetWarningMessage, fleetWarningResourceKey, resetFleetWarnings } from "./fleetWarnings";
import { api, mergeFleetObservationOverview, mergeFleetUsage } from "./index";
import { fleetAdminReadArray, fleetCall, fleetReadArray } from "./fleet";
import type { FleetBatchResult, ObservationOverview, UsageReport } from "./types";

function target<T>(id: string, name: string, key: string, data?: T, error?: string) {
  return {
    target: { id, name, kind: id === "local" ? "local" as const : "ssh" as const, trusted: true, online: !error },
    responses: [{ key, status: error ? 502 : 200, ok: !error, data, error, duration_ms: 1 }],
  };
}

describe("machine scope", () => {
  it("defaults missing legacy state to all machines", () => {
    expect(resolveMachineScope(null)).toBe("all");
    expect(resolveMachineScope("")).toBe("all");
    expect(resolveMachineScope("local")).toBe("local");
    expect(resolveMachineScope("ssh-1")).toBe("ssh-1");
  });

	it("binds a tenant preview to its owning machine", () => {
		const values = new Map([
			["agentmux:active-remote", "all"],
			["agentmux:active-tenant-scope", tenantScopeKey("ten_remote", "ssh-1")],
		]);
		vi.stubGlobal("localStorage", { getItem: (key: string) => values.get(key) ?? null });
		expect(resolveTenantScope(values.get("agentmux:active-tenant-scope"))).toEqual({ tenantID: "ten_remote", targetID: "ssh-1" });
		expect(activeMachineScope()).toBe("ssh-1");
		expect(tenantScopeHeaders("/api/v1/agent-instances")).toEqual({ "X-AgentMux-Tenant-Scope": "ten_remote" });
		expect(tenantScopeHeaders("/api/v1/tenancy/tenants")).toEqual({});
	});
});

afterEach(() => {
  resetFleetWarnings();
  vi.unstubAllGlobals();
});

describe("Console login identity", () => {
  it.each([
    ["all", ""],
    ["local", ""],
    ["offline-ssh", ""],
    ["all", tenantScopeKey("ten_remote", "offline-ssh")],
  ])("checks the current session with machine %s and preview %s", async (machine, preview) => {
    vi.stubGlobal("localStorage", { getItem: (key: string) => key.includes("tenant-scope") ? preview : machine });
    const fetchMock = vi.fn(async (path: string, _init?: RequestInit) => path === "/api/v1/tenancy/self"
      ? new Response(JSON.stringify({ admin: false, tenant_id: "ten_current", status: "active" }))
      : new Response("remote unavailable", { status: 502 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.tenancySelf()).resolves.toEqual({ admin: false, tenant_id: "ten_current", status: "active" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const headers = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
    expect(headers.get("X-AgentMux-Console")).toBe("1");
    expect(headers.has("X-AgentMux-Tenant-Scope")).toBe(false);
  });
});

describe("fleet warning recovery", () => {
  it("deduplicates the same host timeout across tunnel and request errors", () => {
    beginFleetWarningUpdate("frameworks")([fleetWarningMessage("lemon_claw", "remote lemon_claw: open SSH tunnel to 127.0.0.1:8765: context deadline exceeded")]);
    beginFleetWarningUpdate("auth")([fleetWarningMessage("lemon_claw", "remote lemon_claw: context deadline exceeded")]);
    expect(currentFleetWarnings()).toEqual(["lemon_claw: request timed out"]);
    beginFleetWarningUpdate("frameworks")([]);
    expect(currentFleetWarnings()).toHaveLength(1);
    beginFleetWarningUpdate("auth")([]);
    expect(currentFleetWarnings()).toEqual([]);
  });

  it("distinguishes CLI identities but keeps date refreshes in the same read", () => {
    expect(fleetWarningResourceKey("/api/v1/frameworks/auth?kind=codex"))
      .not.toBe(fleetWarningResourceKey("/api/v1/frameworks/auth?kind=claude"));
    expect(fleetWarningResourceKey("/api/v1/usage?from=yesterday"))
      .toBe(fleetWarningResourceKey("/api/v1/usage?from=today"));
  });
  it("reports total failure and clears it only when the same read recovers", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined });
    let failure = true;
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      targets: [target("ssh-1", "Remote", "data", failure ? undefined : [], failure ? "Host key verification failed" : undefined)],
    }), { status: 200 })));

    await expect(fleetReadArray("/api/v1/agents")).rejects.toThrow("Host key verification failed");
    expect(currentFleetWarnings()).toEqual(["Remote: Host key verification failed"]);
    failure = false;
    await fleetQuery([{ key: "data", path: "/api/v1/usage?from=today" }]);
    expect(currentFleetWarnings()).toEqual(["Remote: Host key verification failed"]);
    await fleetReadArray("/api/v1/agents");
    expect(currentFleetWarnings()).toEqual([]);
  });

  it("tracks transport failures and recovery even for raw fleet queries", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "" });
    vi.stubGlobal("fetch", vi.fn().mockRejectedValueOnce(new Error("Connection lost"))
      .mockResolvedValueOnce(new Response(JSON.stringify({ targets: [target("ssh-1", "Remote", "usage", {})] }), { status: 200 })));
    await expect(fleetQuery([{ key: "usage", path: "/api/v1/usage?from=yesterday" }])).rejects.toThrow("Connection lost");
    expect(currentFleetWarnings()).toEqual(["Connection lost"]);
    await fleetQuery([{ key: "usage", path: "/api/v1/usage?from=today" }]);
    expect(currentFleetWarnings()).toEqual([]);
  });

  it("ignores out-of-order results and requests from a previous machine scope", () => {
    const old = beginFleetWarningUpdate("agents");
    const current = beginFleetWarningUpdate("agents");
    current([]);
    old(["stale failure"]);
    expect(currentFleetWarnings()).toEqual([]);
    const pending = beginFleetWarningUpdate("usage");
    resetFleetWarnings();
    pending(["previous scope failure"]);
    expect(currentFleetWarnings()).toEqual([]);
  });
});

describe("fleet array responses", () => {
  it("treats a successful null list as empty rather than unavailable", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "all", setItem: () => undefined });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      targets: [{
        target: { id: "ssh", name: "Remote", kind: "ssh", trusted: true, online: true },
        responses: [{ key: "data", status: 200, ok: true, data: null, duration_ms: 1 }],
      }],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(fleetReadArray<{ id: string }>("/api/v1/guard/policies")).resolves.toEqual([]);
  });

	it("loads the administrator tenant index across machines without applying the preview scope", async () => {
		vi.stubGlobal("localStorage", { getItem: (key: string) => key.includes("tenant-scope") ? tenantScopeKey("ten_remote", "ssh-1") : "all" });
		const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
			targets: [{
				target: { id: "ssh-1", name: "Remote", kind: "ssh", trusted: true, online: true },
				responses: [{ key: "data", status: 200, ok: true, data: [{ id: "ten_remote", name: "Rookie" }], duration_ms: 1 }],
			}],
		}), { status: 200, headers: { "Content-Type": "application/json" } }));
		vi.stubGlobal("fetch", fetchMock);
		await expect(fleetAdminReadArray<{ id: string; name: string }>("/api/v1/tenancy/tenants")).resolves.toEqual([
			{ id: "ten_remote", name: "Rookie", target_id: "ssh-1", target_name: "Remote" },
		]);
		const headers = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
		expect(headers.has("X-AgentMux-Tenant-Scope")).toBe(false);
	});
});

describe("fleet actions", () => {
  it("allows safe routine actions to skip the fleet confirmation", async () => {
    const confirm = vi.fn(() => false);
    vi.stubGlobal("window", { confirm, dispatchEvent: vi.fn() });
    vi.stubGlobal("document", { documentElement: { lang: "zh-CN" } });
    vi.stubGlobal("localStorage", { getItem: () => "", setItem: () => undefined });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      targets: [{
        target: { id: "local", name: "Local", kind: "local", trusted: true, online: true },
        responses: [{ key: "monitor", status: 200, ok: true, data: { ok: true }, duration_ms: 1 }],
      }],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(fleetCall(
      { key: "monitor", method: "POST", path: "/api/v1/providers/monitor/run", body: {} },
      ["all"],
      { confirm: false },
    )).resolves.toMatchObject({ first: { ok: true } });
    expect(confirm).not.toHaveBeenCalled();
  });

  it("updates only the framework row's machine without a fleet confirmation", async () => {
    const confirm = vi.fn(() => false);
    vi.stubGlobal("window", { confirm, dispatchEvent: vi.fn() });
    vi.stubGlobal("localStorage", { getItem: () => "all", setItem: () => undefined });
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
      targets: [{
        target: { id: "ssh-1", name: "Remote", kind: "ssh", trusted: true, online: true },
        responses: [{ key: "install", status: 200, ok: true, data: { kind: "codex", action: "update", ok: true }, duration_ms: 1 }],
      }],
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.installFramework("codex", "update", undefined, false, "ssh-1")).resolves.toMatchObject({ ok: true });
    expect(confirm).not.toHaveBeenCalled();
    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    expect(body.target_ids).toEqual(["ssh-1"]);
  });

	it("installs a managed CLI only on explicitly selected machines without a second confirmation", async () => {
		const confirm = vi.fn(() => false);
		vi.stubGlobal("window", { confirm, dispatchEvent: vi.fn() });
		vi.stubGlobal("localStorage", { getItem: () => "all", setItem: () => undefined });
		const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
			targets: [
				{
					target: { id: "local", name: "Local", kind: "local", trusted: true, online: true },
					responses: [{ key: "install", status: 200, ok: true, data: { id: "opencli", action: "install", ok: true }, duration_ms: 1 }],
				},
				{
					target: { id: "ssh-1", name: "Remote", kind: "ssh", trusted: true, online: true },
					responses: [{ key: "install", status: 200, ok: true, data: { id: "opencli", action: "install", ok: true }, duration_ms: 1 }],
				},
			],
		}), { status: 200, headers: { "Content-Type": "application/json" } }));
		vi.stubGlobal("fetch", fetchMock);

		const result = await api.installCLI("opencli", "install", undefined, false, ["local", "ssh-1"]);
		expect(result.successes).toHaveLength(2);
		expect(confirm).not.toHaveBeenCalled();
		const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
		expect(body.target_ids).toEqual(["local", "ssh-1"]);
	});
});

describe("tenant administration details", () => {
	it("loads every grant editor collection in one target-scoped request", async () => {
		vi.stubGlobal("localStorage", { getItem: () => "all", setItem: () => undefined });
		const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({
			targets: [{
				target: { id: "ssh-1", name: "Remote", kind: "ssh", trusted: true, online: true },
				responses: [
					{ key: "grants", status: 200, ok: true, data: [], duration_ms: 1 },
					{ key: "agents", status: 200, ok: true, data: [{ id: "agent-1", name: "Agent" }], duration_ms: 1 },
					{ key: "channels", status: 200, ok: true, data: [], duration_ms: 1 },
					{ key: "triggers", status: 200, ok: true, data: [], duration_ms: 1 },
					{ key: "providers", status: 200, ok: true, data: [], duration_ms: 1 },
				],
			}],
		}), { status: 200, headers: { "Content-Type": "application/json" } }));
		vi.stubGlobal("fetch", fetchMock);

		const details = await api.tenantAdminDetails("ten_rookie", "ssh-1");
		expect(details.agents).toEqual([
			{ id: "agent-1", name: "Agent", target_id: "ssh-1", target_name: "Remote" },
		]);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
		expect(body.target_ids).toEqual(["ssh-1"]);
		expect(body.requests).toHaveLength(5);
	});
});

describe("fleet mergers", () => {
  it("sums usage and preserves partial failure warnings", () => {
    const report = (tokens: number, estimated: number): UsageReport => ({
      period: "daily",
      totals: { input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: tokens / 100, records: 1, sessions: 1, estimated_tokens: estimated, estimated_records: estimated > 0 ? 1 : 0 },
      buckets: [{
        key: "2026-08-30 10:00",
        totals: { input_tokens: tokens, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: tokens / 100, records: 1, sessions: 1, estimated_tokens: estimated, estimated_records: estimated > 0 ? 1 : 0 },
        by_runtime: [{ runtime: "codex", tokens, cost_usd: tokens / 100, estimated_tokens: estimated }],
      }],
      by_model: [], by_source: [], by_agent: [{ agent: "demo", tokens, cost_usd: tokens / 100 }], by_runtime: [{ runtime: "codex", tokens, cost_usd: tokens / 100, estimated_tokens: estimated }],
    });
    const batch: FleetBatchResult<UsageReport> = { targets: [
      target("local", "Local", "usage", report(10, 2)),
      target("ssh", "Remote", "usage", report(20, 3)),
      target("down", "Down", "usage", undefined, "offline"),
    ] };
    const merged = mergeFleetUsage(batch);
    expect(merged.totals.input_tokens).toBe(30);
    expect(merged.totals.estimated_tokens).toBe(5);
    expect(merged.buckets[0].totals.input_tokens).toBe(30);
    expect(merged.buckets[0].by_runtime?.[0].tokens).toBe(30);
    expect(merged.buckets[0].by_runtime?.[0].estimated_tokens).toBe(5);
    expect(merged.by_machine?.[0].buckets?.[0].key).toBe("2026-08-30 10:00");
    expect(merged.by_agent?.map((item) => item.agent)).toEqual(["Remote · demo", "Local · demo"]);
    expect(merged.warnings).toContain("Down: offline");
  });

  it("recomputes observability error rate", () => {
    const overview = (traces: number, failed: number): ObservationOverview => ({ traces, failed_traces: failed, usage: { total_tokens: traces * 10 } });
    const batch: FleetBatchResult<ObservationOverview> = { targets: [
      target("local", "Local", "overview", overview(4, 1)),
      target("ssh", "Remote", "overview", overview(6, 2)),
    ] };
    const merged = mergeFleetObservationOverview(batch);
    expect(merged.traces).toBe(10);
    expect(merged.error_rate).toBe(0.3);
    expect(merged.usage?.total_tokens).toBe(100);
  });
});
