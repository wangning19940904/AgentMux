import { describe, expect, it } from "vitest";
import { shouldAutoStartChannelSetup } from "./channelSetupModel";

describe("shouldAutoStartChannelSetup", () => {
  it("auto-starts Feishu and Lark setup for new channels", () => {
    expect(shouldAutoStartChannelSetup({ type: "feishu" }, "")).toBe(true);
    expect(shouldAutoStartChannelSetup({ type: "lark" }, "")).toBe(true);
  });

  it("does not auto-start for existing, non-Feishu, or already-started channels", () => {
    expect(shouldAutoStartChannelSetup({ id: "channel-1", type: "feishu" }, "")).toBe(false);
    expect(shouldAutoStartChannelSetup({ type: "slack" }, "")).toBe(false);
    expect(shouldAutoStartChannelSetup({ type: "feishu" }, "feishu")).toBe(false);
  });
});
