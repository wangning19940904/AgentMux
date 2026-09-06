import { afterEach, describe, expect, it, vi } from "vitest";
import { channelAvatarRequestPath, fetchChannelAvatar } from "./channelAvatar";

function scope(target: string, tenant = "") {
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => key === "agentmux:active-remote" ? target : tenant,
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("channel avatar requests", () => {
  it("routes the selected SSH machine through the controller instead of a loopback image URL", async () => {
    scope("lemon_claw");
    const request = vi.fn(async () => new Response("avatar", { headers: { "Content-Type": "image/png" } }));
    vi.stubGlobal("fetch", request);
    const signal = new AbortController().signal;
    const path = channelAvatarRequestPath({ id: "bot /1" });
    const blob = await fetchChannelAvatar(path, signal);
    expect(request).toHaveBeenCalledWith("/api/v1/remote/proxy/lemon_claw/channel-avatar?id=bot%20%2F1", {
      headers: { "X-AgentMux-Console": "1" }, signal,
    });
    expect(blob.type).toBe("image/png");
    expect(await blob.text()).toBe("avatar");
  });

  it("uses each fleet row's machine even when channel IDs overlap", () => {
    scope("all");
    expect(channelAvatarRequestPath({ id: "same", target_id: "local" }))
      .toBe("/api/v1/channel-avatar?id=same");
    expect(channelAvatarRequestPath({ id: "same", target_id: "ssh/a" }))
      .toBe("/api/v1/remote/proxy/ssh%2Fa/channel-avatar?id=same");
    scope("another-remote");
    expect(channelAvatarRequestPath({ id: "same", target_id: "local" }))
      .toBe("/api/v1/channel-avatar?id=same");
  });

  it("preserves tenant preview scope and its owning machine", async () => {
    scope("all", "ssh-1::ten-1");
    const request = vi.fn(async () => new Response("avatar", { headers: { "Content-Type": "image/jpeg" } }));
    vi.stubGlobal("fetch", request);
    await fetchChannelAvatar(channelAvatarRequestPath({ id: "bot" }), new AbortController().signal);
    expect(request).toHaveBeenCalledWith("/api/v1/remote/proxy/ssh-1/channel-avatar?id=bot", expect.objectContaining({
      headers: { "X-AgentMux-Console": "1", "X-AgentMux-Tenant-Scope": "ten-1" },
    }));
  });

  it.each([
    [404, "text/plain"],
    [200, "text/html"],
  ])("rejects unavailable/legacy endpoints (%s, %s) so the component can use the direct avatar", async (status, contentType) => {
    scope("local");
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not an image", {
      status, headers: { "Content-Type": contentType },
    })));
    await expect(fetchChannelAvatar(channelAvatarRequestPath({ id: "bot" }), new AbortController().signal)).rejects.toThrow();
  });
});
