import { describe, expect, it } from "vitest";
import { resolveTenancyGate, resolveTenancyGateWithRetry } from "./tenancyGate";

describe("resolveTenancyGate", () => {
  it("requires registration until identity is known", () => {
    expect(resolveTenancyGate(null, null)).toBe("loading");
  });

  it("allows an active tenant-scoped Console", () => {
    expect(
      resolveTenancyGate(
        { admin: false, tenant_id: "ten_rookie", tenant: "rookie-trade", status: "active" },
        null,
      ),
    ).toBe("ready");
  });

  it("blocks an administrator until an active tenant exists", () => {
    expect(resolveTenancyGate({ admin: true }, [])).toBe("required");
    expect(
      resolveTenancyGate(
        { admin: true },
        [{ id: "ten_disabled", name: "old", kind: "app", status: "disabled" }],
      ),
    ).toBe("required");
    expect(
      resolveTenancyGate(
        { admin: true },
        [{ id: "ten_homebook", name: "homebook", kind: "web", status: "active" }],
      ),
    ).toBe("ready");
  });

  it("retries a transient Desktop startup failure and resolves the administrator", async () => {
    let identityAttempts = 0;
    const result = await resolveTenancyGateWithRetry(
      async () => {
        identityAttempts += 1;
        if (identityAttempts === 1) throw new Error("/api/v1/tenancy/self: 503");
        return { admin: true };
      },
      async () => [{ id: "ten_homebook", name: "homebook", kind: "web", status: "active" }],
      { attempts: 2, delayMs: 0, wait: async () => undefined },
    );
    expect(identityAttempts).toBe(2);
    expect(result).toEqual({ identity: { admin: true }, state: "ready" });
  });

  it("does not retry invalid credentials", async () => {
    let identityAttempts = 0;
    await expect(resolveTenancyGateWithRetry(
      async () => {
        identityAttempts += 1;
        throw new Error("/api/v1/tenancy/self: 401");
      },
      async () => [],
      { attempts: 3, delayMs: 0, wait: async () => undefined },
    )).rejects.toThrow("401");
    expect(identityAttempts).toBe(1);
  });
});
