import { describe, expect, it } from "vitest";
import { resolveTenancyGate, resolveTenancyGateWithRetry } from "./tenancyGate";

describe("resolveTenancyGate", () => {
  it("resolves loading, administrator, active tenant, and blocked tenant scopes", () => {
    const cases = [
      { identity: null, state: "loading" },
      { identity: { admin: true }, state: "ready" },
      { identity: { admin: false, tenant_id: "ten_rookie", tenant: "rookie-trade", status: "active" }, state: "ready" },
      { identity: { admin: false }, state: "required" },
      { identity: { admin: false, tenant_id: "ten_disabled", tenant: "old", status: "disabled" }, state: "required" },
    ] as const;
    for (const item of cases) {
      expect(resolveTenancyGate(item.identity)).toBe(item.state);
    }
  });

  it("retries a transient Desktop startup failure and resolves the administrator", async () => {
    let identityAttempts = 0;
    const result = await resolveTenancyGateWithRetry(
      async () => {
        identityAttempts += 1;
        if (identityAttempts === 1) throw new Error("/api/v1/tenancy/self: 503");
        return { admin: true };
      },
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
      { attempts: 3, delayMs: 0, wait: async () => undefined },
    )).rejects.toThrow("401");
    expect(identityAttempts).toBe(1);
  });
});
