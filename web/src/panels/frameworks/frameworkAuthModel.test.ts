import { describe, expect, it } from "vitest";
import type { FrameworkAuthStatus } from "../../api";
import { resolveFrameworkLoginFlow } from "./frameworkAuthModel";
import type { FrameworkLoginFlow } from "./frameworkAuthModel";

describe("framework login flow", () => {
  it("resolves lifecycle and authentication transitions without changing stale flows", () => {
    const flow: FrameworkLoginFlow = {
      kind: "codex", session_id: "session-1", login_url: "https://example.test",
      state: "waiting",
    };
    const auth = (state: FrameworkAuthStatus["state"]): FrameworkAuthStatus => ({
      kind: "codex", state, installed: true, login_supported: true,
    });
    const cases = [
      { lifecycleState: "succeeded", lifecycleActive: false, auth: auth("authenticated"), state: "succeeded", error: "" },
      { lifecycleState: "failed", lifecycleActive: false, auth: auth("unauthenticated"), state: "failed", error: "ended" },
      { lifecycleState: "cancelled", lifecycleActive: false, auth: auth("unauthenticated"), state: "cancelled", error: "" },
      { lifecycleState: "unknown", lifecycleActive: false, auth: auth("unknown"), state: "failed", error: "ended" },
      { lifecycleState: "waiting", lifecycleActive: true, auth: auth("authenticated"), state: "succeeded", error: "" },
    ] as const;
    for (const item of cases) {
      const next = resolveFrameworkLoginFlow(flow, "session-1", {
        ...item, lifecycleError: item.lifecycleState === "failed" ? "ended" : "", endedMessage: "ended",
      });
      expect(next?.state).toBe(item.state);
      expect(next?.error || "").toBe(item.error);
    }
    expect(resolveFrameworkLoginFlow(flow, "stale", {
      auth: auth("authenticated"), lifecycleActive: false, lifecycleState: "succeeded",
      lifecycleError: "", endedMessage: "ended",
    })).toBe(flow);
  });

  it("requires lifecycle success when re-authenticating an already logged-in CLI", () => {
    const flow: FrameworkLoginFlow = {
      kind: "codex", session_id: "session-1", login_url: "https://example.test",
      state: "waiting", reAuthenticate: true,
    };
    const next = resolveFrameworkLoginFlow(flow, "session-1", {
      auth: { kind: "codex", state: "authenticated", installed: true, login_supported: true },
      lifecycleActive: true, lifecycleState: "waiting", lifecycleError: "", endedMessage: "ended",
    });
    expect(next).toBe(flow);
  });
});
