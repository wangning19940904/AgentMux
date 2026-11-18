// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Channel, SuperAgent } from "../../api";
import { newSuperAgent } from "../superAgentModel";
import { AgentAvatar } from "./AgentAvatar";
import { agentChannels, agentRegistryMetrics } from "./agentUtils";
import { AVATAR_PRESETS, isImageAvatar, presetID, workerPersona } from "./avatars";

describe("worker personas", () => {
  it("gives the same worker the same name and portrait, and different workers different ones", () => {
    const worker = { id: "w1", agent_id: "codex-1", host_id: "mac" };
    expect(workerPersona(worker)).toEqual(workerPersona({ ...worker }));
    const names = new Set(["a", "b", "c", "d", "e", "f", "g", "h"].map((id) => workerPersona({ id, agent_id: `agent-${id}` }).name));
    expect(names.size).toBeGreaterThan(3);
    expect(workerPersona({ agent_id: "x", host_id: "one" })).not.toEqual(workerPersona({ agent_id: "x", host_id: "two" }));
  });

  it("names presets uniquely, as the server accepts them", () => {
    const ids = AVATAR_PRESETS.map((preset) => preset.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) expect(id).toMatch(/^[a-z0-9-]{1,32}$/);
    expect(presetID("preset:fox")).toBe("fox");
    expect(presetID("data:image/png;base64,AA==")).toBe("");
    expect(isImageAvatar("data:image/png;base64,AA==")).toBe(true);
    expect(isImageAvatar("https://example.test/a.png")).toBe(false);
  });
});

describe("agentChannels", () => {
  const channel = (over: Partial<Channel>): Channel => ({ id: "c", name: "Bot", type: "feishu", enabled: true, ...over });
  const agent: SuperAgent = {
    ...newSuperAgent(), id: "work", name: "Work Agent", router_agent_id: "router", target_id: "mac",
    sources: [{ id: "s1", kind: "feishu_message", enabled: true, allowed_actors: ["*"], config: { channel_id: "feishu-bot" } }],
  };

  it("finds the bots an agent's sources name and those bound to its brain, on its machine only", () => {
    const channels = [
      channel({ id: "feishu-bot", target_id: "mac" }),
      channel({ id: "feishu-bot", target_id: "other" }),
      channel({ id: "brain-bot", agent_id: "router", target_id: "mac" }),
      channel({ id: "unrelated", target_id: "mac" }),
    ];
    expect(agentChannels(agent, channels).map((item) => `${item.target_id}/${item.id}`)).toEqual(["mac/feishu-bot", "mac/brain-bot"]);
    expect(agentChannels(undefined, channels, [channels[3]])).toEqual([channels[3]]);
    expect(agentRegistryMetrics([], channels, [agent]).channelCount).toBe(2);
  });
});

describe("AgentAvatar", () => {
  let root: Root;
  let container: HTMLDivElement;
  const bot: Channel = { id: "bot", name: "Bot", type: "feishu", enabled: true, bot_avatar_url: "https://example.test/bot.png" };

  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  });

  it("prefers the agent's own portrait to its bot's", async () => {
    await act(async () => root.render(<AgentAvatar avatar="preset:rocket" channels={[bot]} />));
    expect(container.querySelector(".avatar-preset .lucide-rocket")).not.toBeNull();
    expect(fetch).not.toHaveBeenCalled();

    await act(async () => root.render(<AgentAvatar avatar="data:image/png;base64,AA==" channels={[bot]} />));
    expect(container.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,AA==");

    await act(async () => root.render(<AgentAvatar avatar="preset:unknown" channels={[bot]} />));
    expect(container.querySelector("img")?.getAttribute("src")).toBe(bot.bot_avatar_url);
  });
});
