import { describe, expect, it } from "vitest";
import type { FrameworkAuthStatus } from "../../api";
import { frameworkAccountSummary } from "./frameworkPresentation";

function status(overrides: Partial<FrameworkAuthStatus>): FrameworkAuthStatus {
  return { kind: "codex", state: "authenticated", installed: true, login_supported: true, ...overrides };
}

describe("frameworkAccountSummary", () => {
  it("summarizes the signed-in identity with plan and team organization", () => {
    expect(frameworkAccountSummary(status({
      account: { email: "dev@example.test", name: "Dev", organization: "Acme", plan: "pro", method: "ChatGPT" },
    }))).toEqual({
      identity: "dev@example.test",
      details: ["Pro", "Acme"],
      fields: [
        { key: "email", value: "dev@example.test" },
        { key: "name", value: "Dev" },
        { key: "organization", value: "Acme" },
        { key: "plan", value: "Pro" },
        { key: "method", value: "ChatGPT" },
      ],
    });
  });

  it("keeps personal workspaces out of the visible summary", () => {
    expect(frameworkAccountSummary(status({ account: { email: "dev@example.test", organization: "Personal" } }))?.details).toEqual([]);
    expect(frameworkAccountSummary(status({
      account: { email: "Dev@Example.test", organization: "dev@example.test's Organization", plan: "max" },
    }))?.details).toEqual(["Max"]);
  });

  it("falls back to user ID, then sign-in method", () => {
    expect(frameworkAccountSummary(status({ account: { user_id: "dev.example", region: "CN" } })))
      .toMatchObject({ identity: "dev.example", details: ["CN"] });
    expect(frameworkAccountSummary(status({ account: { method: "API key" } })))
      .toMatchObject({ identity: "API key", details: [] });
  });

  it("shows nothing unless the framework is signed in with a known account", () => {
    expect(frameworkAccountSummary(undefined)).toBeNull();
    expect(frameworkAccountSummary(status({}))).toBeNull();
    expect(frameworkAccountSummary(status({ account: { email: "  " } }))).toBeNull();
    expect(frameworkAccountSummary(status({ state: "unauthenticated", account: { email: "dev@example.test" } }))).toBeNull();
  });
});
