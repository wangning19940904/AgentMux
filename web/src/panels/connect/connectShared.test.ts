import { describe, expect, it } from "vitest";
import { completeFeishuDraft, defaultChannelConfig } from "./connectShared";

describe("Feishu channel setup without local access lists", () => {
  it("does not turn the registration owner into an access restriction", () => {
    const draft = completeFeishuDraft({ type: "feishu" }, {
      status: "completed", app_id: "cli_test", app_secret: "secret", owner_open_id: "ou_owner",
    });
    expect(draft.config).not.toHaveProperty("allowed_user_ids");
    expect(draft.config).not.toHaveProperty("admin_user_ids");
    expect(draft.config?.reply_scope).toBe("dm_and_mentions");
  });

  it("drops legacy access lists from a recovered setup draft", () => {
    const draft = completeFeishuDraft({ type: "lark", config: {
      allowed_user_ids: "ou_old", admin_user_ids: "ou_old", reply_scope: "mentions_only",
    } }, { status: "completed", owner_open_id: "ou_new" });
    expect(draft.config).not.toHaveProperty("allowed_user_ids");
    expect(draft.config).not.toHaveProperty("admin_user_ids");
    expect(draft.config?.reply_scope).toBe("mentions_only");
    expect(defaultChannelConfig("lark")).not.toHaveProperty("allowed_user_ids");
  });
});
