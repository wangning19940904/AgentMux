import { describe, expect, it } from "vitest";
import {
  resolveTenancyGate,
  resolveTenancyGateWithRetry,
  tenancyNavigationLocked,
} from "./tenancyGate";

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

  it("allows a longer first-launch install and stops on a setup failure", async () => {
    let attempts = 0;
    const result = await resolveTenancyGateWithRetry(async () => {
      if (++attempts <= 35) throw new Error("/api/v1/tenancy/self: 503");
      return { admin: true };
    }, { attempts: 1200, delayMs: 0, wait: async () => undefined });
    expect(result.state).toBe("ready");
    expect(attempts).toBe(36);

    attempts = 0;
    await expect(resolveTenancyGateWithRetry(async () => {
      attempts++;
      throw new Error("/api/v1/tenancy/self: 500 PostgreSQL setup failed");
    }, { attempts: 1200, delayMs: 0, wait: async () => undefined })).rejects.toThrow("setup failed");
    expect(attempts).toBe(1);
  });

  it("does not lock the packaged desktop navigation during backend startup", () => {
    expect(tenancyNavigationLocked("loading", true)).toBe(false);
    expect(tenancyNavigationLocked("required", true)).toBe(false);
    expect(tenancyNavigationLocked("required", false)).toBe(true);
    expect(tenancyNavigationLocked("ready", false)).toBe(false);
  });
});
