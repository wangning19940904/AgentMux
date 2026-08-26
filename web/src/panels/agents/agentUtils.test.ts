import { describe, expect, it } from "vitest";
import { approvalModesForRuntime, composeInjectedPrompt, newAgent, routeToolForRuntime, runtimeLabel, toggleID } from "./agentUtils";

describe("agent form model", () => {
  it("creates Agents with safe workspace defaults", () => {
    const agent = newAgent(["codex"]);
    expect(agent.runtime_id).toBe("codex");
    expect(agent.workspace_mode).toBe("shared");
    expect(agent.session_backend).toBe("structured");
  });

  it("composes channel prompts, logs, and CLI notes without duplicate prompt bodies", () => {
    const prompt = composeInjectedPrompt(
      "Base instructions",
      ["/tmp/channel.jsonl"],
      [{ name: "cis-cli", note: "Enterprise operations" }],
      [
        { name: "Feishu", prompt: "Answer concisely." },
        { name: "Slack", prompt: "Answer concisely." },
      ],
      "Channel defaults",
    );
    expect(prompt).toContain("Base instructions");
    expect(prompt).toContain("**Feishu、Slack**");
    expect(prompt.match(/Answer concisely\./g)).toHaveLength(1);
    expect(prompt).toContain("/tmp/channel.jsonl");
    expect(prompt).toContain("cis-cli：Enterprise operations");
  });

  it("keeps route and id toggles deterministic", () => {
    expect(routeToolForRuntime("claude-code")).toBe("claudecode");
	expect(routeToolForRuntime("codex-app")).toBe("codex-app");
	expect(runtimeLabel("codex-app")).toBe("Codex Desktop");
	expect(approvalModesForRuntime("codex-app")).toContain("manual");
    expect(toggleID(["a"], "b")).toEqual(["a", "b"]);
    expect(toggleID(["a", "b"], "a")).toEqual(["b"]);
  });
});
