import { describe, expect, it } from "vitest";
import { agentLabel, formatShare, formatTokens, projectLabel } from "./format";

const t = (key: string) => key;
const names = { agents: new Map([["a1", "Reviewer"]]), channels: new Map([["c1", "Support bot"]]) };

describe("usage formatting", () => {
  it("abbreviates token counts and never prints a fraction of a token", () => {
    expect(formatTokens(999)).toBe("999");
    expect(formatTokens(12.3333)).toBe("12");
    expect(formatTokens(1500)).toBe("1.50K");
    expect(formatTokens(2_500_000)).toBe("2.50M");
    expect(formatTokens(3_210_000_000)).toBe("3.21B");
  });

  it("does not round a real share down to nothing", () => {
    expect(formatShare(0, 100)).toBe("0%");
    expect(formatShare(1, 1000)).toBe("<1%");
    expect(formatShare(335, 1000)).toBe("34%");
    expect(formatShare(5, 0)).toBe("0%");
  });

  it("names agents, channels and project directories", () => {
    expect(agentLabel("agent:a1", names, t)).toBe("Reviewer");
    expect(agentLabel("agent:gone", names, t)).toBe("gone");
    expect(agentLabel("channel:c1", names, t)).toBe("usage.channelPrefix Support bot");
    expect(agentLabel("unknown", names, t)).toBe("usage.unknownAgent");
    expect(agentLabel("(other)", names, t)).toBe("usage.otherAgents");
    expect(agentLabel("(temporary directory)", names, t)).toBe("usage.temporaryAgent");
    expect(projectLabel("/Users/me/Projects/playground/AgentMux")).toBe("playground/AgentMux");
    expect(projectLabel("/Users/me/.codex/worktrees/*/knode-os")).toBe("knode-os");
    expect(projectLabel("-Users-me-Projects-playground-AgentMux")).toBe("Projects-playground-AgentMux");
    expect(projectLabel("repo")).toBe("repo");
  });
});
