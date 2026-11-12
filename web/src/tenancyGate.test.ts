import { describe, expect, it } from "vitest";
import type { AuthSession } from "./api";
import {
  accountFormError,
  accountLabel,
  resolveSignInGate,
  signInErrorKey,
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

describe("sign-in gate", () => {
  const session = (overrides: Partial<AuthSession>): AuthSession => ({
    accounts_enabled: false,
    setup_available: false,
    sign_in_required: false,
    authenticated: true,
    admin: true,
    sign_in_methods: ["password"],
    ...overrides,
  });

  it("opens as before on a machine without accounts", () => {
    expect(resolveSignInGate(session({}), { nativeDesktopAdmin: false, failed: false })).toBe("open");
  });

  it("asks for sign-in once accounts exist and the browser has no credential", () => {
    const required = session({ accounts_enabled: true, sign_in_required: true, authenticated: false, admin: false });
    expect(resolveSignInGate(required, { nativeDesktopAdmin: false, failed: false })).toBe("signed-out");
    expect(resolveSignInGate(null, { nativeDesktopAdmin: false, failed: false })).toBe("checking");
  });

  it("never blocks the desktop shell, and leaves check failures to the shell", () => {
    const required = session({ accounts_enabled: true, sign_in_required: true });
    expect(resolveSignInGate(required, { nativeDesktopAdmin: true, failed: false })).toBe("open");
    expect(resolveSignInGate(null, { nativeDesktopAdmin: false, failed: true })).toBe("open");
  });

  it("names accounts by display name, falling back to login", () => {
    expect(accountLabel({ login: "bob", display_name: " Bob Lee " })).toBe("Bob Lee");
    expect(accountLabel({ login: "bob", display_name: "" })).toBe("bob");
    expect(accountLabel(undefined)).toBe("");
  });

  it("validates new account forms like the server", () => {
    expect(accountFormError({ login: "Bob", password: "" }, { passwordRequired: false })).toBe("");
    expect(accountFormError({ login: "b", password: "" }, { passwordRequired: false })).toBe("accounts.loginInvalid");
    expect(accountFormError({ login: "has space", password: "" }, { passwordRequired: false })).toBe("accounts.loginInvalid");
    expect(accountFormError({ login: "bob", password: "" }, { passwordRequired: true })).toBe("accounts.passwordTooShort");
    expect(accountFormError({ login: "bob", password: "long-enough-1", confirm: "other" }, { passwordRequired: true })).toBe("accounts.passwordMismatch");
    expect(accountFormError({ login: "bob", password: "long-enough-1", confirm: "long-enough-1" }, { passwordRequired: true })).toBe("");
  });

  it("explains failed sign-ins", () => {
    expect(signInErrorKey({ status: 401 })).toBe("auth.wrongCredentials");
    expect(signInErrorKey({ status: 429 })).toBe("auth.tooManyAttempts");
    expect(signInErrorKey(new Error("offline"))).toBe("auth.failed");
  });
});
