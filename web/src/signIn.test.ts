import { describe, expect, it } from "vitest";
import type { AuthSession } from "./api";
import {
  accountFormError,
  accountLabel,
  resolveSignInGate,
  signInErrorKey,
  consoleRole,
  loadSessionWithRetry,
} from "./signIn";

const sessionFor = (overrides: Partial<AuthSession>): AuthSession => ({
  accounts_enabled: true,
  setup_available: false,
  sign_in_required: false,
  authenticated: true,
  admin: true,
  sign_in_methods: ["password"],
  ...overrides,
});

describe("console identity", () => {
  it("treats administrators, open machines and the desktop shell as admin", () => {
    expect(consoleRole(sessionFor({}))).toBe("admin");
    expect(consoleRole(sessionFor({ accounts_enabled: false, authenticated: false }))).toBe("admin");
    expect(consoleRole(null, true)).toBe("admin");
  });

  it("treats a signed-in account without administrator rights as a member", () => {
    const member = sessionFor({ admin: false, account: { id: "acct_1", login: "bob", role: "member" } });
    expect(consoleRole(member)).toBe("member");
    expect(consoleRole(member, true)).toBe("member");
  });

  it("retries a transient Desktop startup failure and resolves the session", async () => {
    let attempts = 0;
    const result = await loadSessionWithRetry(
      async () => {
        attempts += 1;
        if (attempts === 1) throw new Error("/api/v1/auth/session: 503");
        return sessionFor({});
      },
      { attempts: 2, delayMs: 0, wait: async () => undefined },
    );
    expect(attempts).toBe(2);
    expect(result.admin).toBe(true);
  });

  it("does not retry refused requests", async () => {
    let attempts = 0;
    await expect(loadSessionWithRetry(
      async () => {
        attempts += 1;
        throw new Error("/api/v1/auth/session: 401");
      },
      { attempts: 3, delayMs: 0, wait: async () => undefined },
    )).rejects.toThrow("401");
    expect(attempts).toBe(1);
  });

  it("allows a longer first-launch install and stops on a setup failure", async () => {
    let attempts = 0;
    const result = await loadSessionWithRetry(async () => {
      if (++attempts <= 35) throw new Error("/api/v1/auth/session: 503");
      return sessionFor({});
    }, { attempts: 1200, delayMs: 0, wait: async () => undefined });
    expect(result.admin).toBe(true);
    expect(attempts).toBe(36);

    attempts = 0;
    await expect(loadSessionWithRetry(async () => {
      attempts++;
      throw new Error("/api/v1/auth/session: 500 PostgreSQL setup failed");
    }, { attempts: 1200, delayMs: 0, wait: async () => undefined })).rejects.toThrow("setup failed");
    expect(attempts).toBe(1);
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
