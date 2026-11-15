import { describe, expect, it } from "vitest";
import type { SuperAgentActivity, SuperAgentTicket, SuperAgentTrigger } from "../api";
import { buildConversations, locate, previewText } from "./superAgentConversations";

const trigger = (over: Partial<SuperAgentTrigger>): SuperAgentTrigger => ({
  id: "t", source_kind: "feishu_message", text: "", disposition: "accepted", received_at: "2026-11-15T09:00:00Z", ...over,
});

const ticket = (over: Partial<SuperAgentTicket>): SuperAgentTicket => ({
  id: "k", title: "", status: "ready", worker: { id: "w", agent_id: "a" } as never, updated_at: "2026-11-15T09:05:00Z", ...over,
});

const activity = (over: Partial<SuperAgentActivity>): SuperAgentActivity => ({ tickets: [], triggers: [], workers: [], sources: {}, ...over });

describe("locate", () => {
  it("groups every thread of a Feishu group into one chat", () => {
    expect(locate({ thread_key: "feishu:c1:oc_team:om_1" })).toEqual({ key: "chat:c1:oc_team", kind: "chat", channelID: "c1", chatID: "oc_team" });
    expect(locate({ thread_key: "feishu:c1:oc_team:om_2" }).key).toBe("chat:c1:oc_team");
  });

  it("finds a channel event's chat from where it replies", () => {
    expect(locate({ thread_key: "channel:c1:root:om_1", reply: { channel_id: "c1", chat_id: "oc_x" } }).key).toBe("chat:c1:oc_x");
    expect(locate({ thread_key: "channel:c2:chat:dm_1" }).key).toBe("chat:c2:dm_1");
  });

  it("keeps the console, mail peers and other sources apart", () => {
    expect(locate({ thread_key: "console:sat_1", source_kind: "console" }).key).toBe("console");
    expect(locate({ thread_key: "mail:bob" }).key).toBe("thread:mail:bob");
    expect(locate({ thread_key: "github:o/r#1", source_id: "gh" }).key).toBe("source:gh");
  });
});

describe("buildConversations", () => {
  it("puts requests, replies and the work they started in one contact, in order", () => {
    const conversations = buildConversations(activity({
      triggers: [
        trigger({ id: "t2", thread_key: "feishu:c1:oc_team:om_2", actor_name: "Bob", text: "and this?", disposition: "pending", received_at: "2026-11-15T10:00:00Z" }),
        trigger({ id: "t1", thread_key: "feishu:c1:oc_team:om_1", actor_name: "Alice", text: "review orders", answer: "On it", ticket_id: "k1" }),
        trigger({ id: "u1", kind: "ticket_update", thread_key: "feishu:c1:oc_team:om_1", text: "worker output", answer: "Done: approved 1", received_at: "2026-11-15T09:10:00Z" }),
        trigger({ id: "u2", kind: "ticket_update", thread_key: "feishu:c1:oc_team:om_1", text: "silent" }),
      ],
      tickets: [ticket({ id: "k1", thread_key: "feishu:c1:oc_team:om_1", created_at: "2026-11-15T09:00:01Z" })],
      plans: [{ id: "p1", super_agent_id: "a", title: "Plan", thread_key: "feishu:c1:oc_team:om_2", tickets: [], tier: "low", status: "pending", created_at: "2026-11-15T10:00:05Z" }],
    }));
    expect(conversations.map((item) => item.key)).toEqual(["console", "chat:c1:oc_team"]);
    const team = conversations[1];
    expect(team.items.map((item) => item.type)).toEqual(["request", "answer", "ticket", "event", "request", "plan"]);
    expect(team.participants).toEqual(["Bob", "Alice"]);
    expect(team.active).toBe(1);
    expect(team.attention).toBe(true);
    expect(team.lastAt).toBe("2026-11-15T10:00:05Z");
  });

  it("lists the most recent contact first, after the console", () => {
    const conversations = buildConversations(activity({
      triggers: [
        trigger({ id: "a", thread_key: "feishu:c1:oc_a:1", received_at: "2026-11-15T08:00:00Z" }),
        trigger({ id: "b", thread_key: "github:o/r#1", source_id: "gh", source_kind: "github_webhook", received_at: "2026-11-15T09:00:00Z" }),
        trigger({ id: "c", thread_key: "console:c", source_kind: "console", received_at: "2026-11-14T09:00:00Z" }),
      ],
    }));
    expect(conversations.map((item) => item.key)).toEqual(["console", "source:gh", "chat:c1:oc_a"]);
    expect(conversations[0].participants).toEqual([]);
  });

  it("always offers the console, even with no history", () => {
    expect(buildConversations(null).map((item) => item.key)).toEqual(["console"]);
  });
});

describe("previewText", () => {
  it("flattens markdown to one line", () => {
    expect(previewText({ type: "answer", at: "", trigger: trigger({ answer: "| Order | OK |\n**done**" }) })).toBe("Order OK done");
  });
});
