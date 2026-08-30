import { describe, expect, it } from "vitest";
import { channelBrandKey } from "./ChannelLogo";

describe("channel brand marks", () => {
  it("maps supported brands and aliases to local logo assets", () => {
    expect(channelBrandKey("feishu")).toBe("feishu");
    expect(channelBrandKey("lark")).toBe("lark");
    expect(channelBrandKey("wechat")).toBe("wechat");
    expect(channelBrandKey("wecom")).toBe("wechat");
    expect(channelBrandKey("dingtalk")).toBe("dingtalk");
    expect(channelBrandKey("telegram")).toBe("telegram");
    expect(channelBrandKey("slack")).toBe("slack");
    expect(channelBrandKey("discord")).toBe("discord");
  });

  it("uses the generic fallback for webhook and unknown channel types", () => {
    expect(channelBrandKey("webhook")).toBeNull();
    expect(channelBrandKey("custom-channel")).toBeNull();
  });
});
