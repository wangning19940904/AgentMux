// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Channel } from "../../api";
import { AgentAvatar } from "./AgentAvatar";
import { boundChannelsForAgent, newAgent } from "./agentUtils";
import { ChannelLogoGroup } from "./ChannelLogo";

let root: Root;
let container: HTMLDivElement;
const portrait: Channel = { id: "bot", name: "Feishu channel", bot_name: "Coding Bot", type: "feishu", enabled: true,
  agent_id: "coding", target_id: "ssh", bot_avatar_url: "https://example.test/avatar.png" };

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("image", { headers: { "Content-Type": "image/png" } })));
  vi.stubGlobal("URL", Object.assign(class extends URL {}, {
    createObjectURL: vi.fn(() => "blob:portrait"), revokeObjectURL: vi.fn(),
  }));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  localStorage.clear();
  vi.unstubAllGlobals();
});

it("inherits the first available bound portrait from the same machine and keeps the old fallback", async () => {
  const agent = { ...newAgent(["codex"]), id: "coding", target_id: "ssh" };
  const channels = [
    { ...portrait, target_id: "other-machine", bot_avatar_url: "https://example.test/other.png" },
    { ...portrait, id: "missing", bot_avatar_url: "" }, portrait,
  ];
  await act(async () => root.render(<AgentAvatar channels={boundChannelsForAgent(agent, channels)} />));
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch).toHaveBeenCalledWith("/api/v1/remote/proxy/ssh/channel-avatar?id=bot", expect.anything());
  expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:portrait");
  await act(async () => container.querySelector("img")!.dispatchEvent(new Event("error")));
  expect(container.querySelector("img")?.getAttribute("src")).toBe(portrait.bot_avatar_url);
  await act(async () => container.querySelector("img")!.dispatchEvent(new Event("error")));
  expect(container.querySelector(".provider-icon .lucide-bot")).not.toBeNull();
  await act(async () => root.render(<AgentAvatar channels={[]} />));
  expect(container.querySelector(".provider-icon .lucide-bot")).not.toBeNull();
  expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:portrait");
});

it("shows each channel's brand, bot portrait and bot name, falling back to its configured name", async () => {
  await act(async () => root.render(<ChannelLogoGroup channels={[portrait, { ...portrait, id: "no-bot", type: "webhook", bot_name: "", bot_avatar_url: "" }]}
    label="Bound channels" emptyLabel="No channels" />));
  const badges = container.querySelectorAll(".agent-channel-badge");
  expect(badges).toHaveLength(2);
  expect(badges[0].querySelector(".agent-channel-logo img")).not.toBeNull();
  expect(badges[0].querySelector(".channel-avatar img")).not.toBeNull();
  expect(badges[0].textContent).toContain("Coding Bot");
  expect(badges[1].textContent).toContain("Feishu channel");
  expect(badges[1].querySelector(".channel-avatar .lucide-bot")).not.toBeNull();
});
