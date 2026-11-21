import type { SuperAgentActivity, SuperAgentPlan, SuperAgentTicket, SuperAgentTimer, SuperAgentTrigger } from "../api";

// A conversation is one "contact" in an agent's chat history: a Feishu group
// or direct chat, one trigger source such as a GitHub repository or mailbox,
// or the console where people talk to the agent from AgentMux itself.
export type ConversationKind = "console" | "chat" | "source" | "thread";

export type ChatItem =
  | { type: "request"; at: string; trigger: SuperAgentTrigger }
  | { type: "answer"; at: string; trigger: SuperAgentTrigger }
  // An event AgentMux raised itself, such as a ticket update, and what the
  // agent then told the thread.
  | { type: "event"; at: string; trigger: SuperAgentTrigger }
  | { type: "ticket"; at: string; ticket: SuperAgentTicket }
  | { type: "plan"; at: string; plan: SuperAgentPlan }
  | { type: "timer"; at: string; timer: SuperAgentTimer };

export interface Conversation {
  key: string;
  kind: ConversationKind;
  sourceKind: string;
  sourceID?: string;
  channelID?: string;
  chatID?: string;
  // Who wrote in it, by display name, in the order they first did.
  participants: string[];
  // Their portraits, by display name, when the platform had one.
  avatars: Record<string, string>;
  // Oldest first.
  items: ChatItem[];
  lastAt: string;
  // Work still going: events being routed, tickets queued or running.
  active: number;
  // Something waits on a person: a plan to approve, or a failed message,
  // failed ticket or question to people that nobody has written after.
  attention: boolean;
}

type Located = { key: string; kind: ConversationKind; channelID?: string; chatID?: string };

type Origin = {
  thread_key?: string;
  source_kind?: string;
  source_id?: string;
  reply?: { channel_id?: string; chat_id?: string };
};

export const CONSOLE_CONVERSATION = "console";

// locate names the conversation an event belongs to. Feishu and channel
// events group by chat, so every thread of a group lands in one contact;
// mail groups by peer; other sources are one contact each.
export function locate(origin: Origin): Located {
  const thread = origin.thread_key ?? "";
  if (thread.startsWith("console:") || origin.source_kind === "console") return { key: CONSOLE_CONVERSATION, kind: "console" };
  if (thread.startsWith("feishu:")) {
    const [, channelID, chatID] = thread.split(":");
    if (channelID && chatID) return chat(channelID, chatID);
  }
  const replyChannel = typeof origin.reply?.channel_id === "string" ? origin.reply.channel_id : "";
  const replyChat = typeof origin.reply?.chat_id === "string" ? origin.reply.chat_id : "";
  if (replyChannel && replyChat) return chat(replyChannel, replyChat);
  if (thread.startsWith("channel:")) {
    const rest = thread.slice("channel:".length);
    const split = rest.indexOf(":");
    const conversation = split >= 0 ? rest.slice(split + 1) : "";
    if (split > 0 && conversation.startsWith("chat:")) return chat(rest.slice(0, split), conversation.slice("chat:".length));
  }
  if (thread.startsWith("mail:")) return { key: `thread:${thread}`, kind: "thread" };
  if (origin.source_id) return { key: `source:${origin.source_id}`, kind: "source" };
  return { key: thread ? `thread:${thread}` : "thread:", kind: "thread" };
}

function chat(channelID: string, chatID: string): Located {
  return { key: `chat:${channelID}:${chatID}`, kind: "chat", channelID, chatID };
}

const itemOrder: Record<ChatItem["type"], number> = { request: 0, answer: 1, event: 1, plan: 2, ticket: 3, timer: 4 };

const stamp = (value?: string) => {
  const parsed = value ? Date.parse(value) : NaN;
  return Number.isNaN(parsed) ? 0 : parsed;
};

// buildConversations turns an agent's activity into contacts, most recent
// first, with the console always present so people can start a request.
export function buildConversations(activity: SuperAgentActivity | null | undefined): Conversation[] {
  const byKey = new Map<string, Conversation>();
  // Plans and timers carry only a thread key; the events and tickets on the
  // same thread say which chat it is.
  const byThread = new Map<string, Located>();
  const remember = (origin: Origin, located: Located) => {
    if (origin.thread_key && !byThread.has(origin.thread_key)) byThread.set(origin.thread_key, located);
  };
  const open = (located: Located, sourceKind: string, sourceID?: string) => {
    let conversation = byKey.get(located.key);
    if (!conversation) {
      conversation = {
        key: located.key, kind: located.kind, sourceKind, sourceID, channelID: located.channelID, chatID: located.chatID,
        participants: [], avatars: {}, items: [], lastAt: "", active: 0, attention: false,
      };
      byKey.set(located.key, conversation);
    }
    return conversation;
  };
  const touch = (conversation: Conversation, at?: string) => {
    if (at && stamp(at) > stamp(conversation.lastAt)) conversation.lastAt = at;
  };
  // A failure or a question to people stops asking for attention once
  // someone writes in the conversation again and the agent takes it up: the
  // person resent the message or answered, or moved on.
  const lastReply = new Map<string, number>();
  const raised = new Map<string, number>();
  const raise = (conversation: Conversation, at?: string) => {
    raised.set(conversation.key, Math.max(raised.get(conversation.key) ?? 0, stamp(at) || Number.MAX_SAFE_INTEGER));
  };

  const triggers = activity?.triggers ?? [];
  const tickets = activity?.tickets ?? [];
  for (const trigger of triggers) remember(trigger, locate(trigger));
  for (const ticket of tickets) remember(ticket, locate(ticket));

  for (const trigger of triggers) {
    const conversation = open(locate(trigger), trigger.source_kind, trigger.source_id);
    if (trigger.kind) {
      if (trigger.answer?.trim()) conversation.items.push({ type: "event", at: trigger.received_at, trigger });
    } else {
      conversation.items.push({ type: "request", at: trigger.received_at, trigger });
      const name = trigger.actor_name || trigger.actor;
      if (name && trigger.source_kind !== "console" && !conversation.participants.includes(name)) conversation.participants.push(name);
      if (name && trigger.actor_avatar && trigger.source_kind !== "console") conversation.avatars[name] ??= trigger.actor_avatar;
      if (trigger.answer?.trim()) conversation.items.push({ type: "answer", at: trigger.received_at, trigger });
      if (trigger.disposition === "pending") conversation.active++;
      if (trigger.disposition === "failed") raise(conversation, trigger.received_at);
      if (handledReply(trigger)) lastReply.set(conversation.key, Math.max(lastReply.get(conversation.key) ?? 0, stamp(trigger.received_at)));
    }
    touch(conversation, trigger.updated_at || trigger.received_at);
  }
  for (const ticket of tickets) {
    const conversation = open(locate(ticket), ticket.source_kind ?? "", ticket.source_id);
    conversation.items.push({ type: "ticket", at: ticket.created_at || ticket.updated_at, ticket });
    if (ticket.status === "queued" || ticket.status === "running") conversation.active++;
    const review = ticket.reviews?.[ticket.reviews.length - 1];
    if (ticket.status === "failed") raise(conversation, ticket.updated_at);
    else if (ticket.status !== "stopped" && review?.verdict === "needs_user") raise(conversation, review.at || ticket.updated_at);
    touch(conversation, ticket.updated_at);
  }
  for (const plan of activity?.plans ?? []) {
    const located = byThread.get(plan.thread_key) ?? locate({ thread_key: plan.thread_key });
    const conversation = open(located, located.kind === "console" ? "console" : "");
    conversation.items.push({ type: "plan", at: plan.created_at, plan });
    if (plan.status === "pending") conversation.attention = true;
    touch(conversation, plan.decided_at || plan.created_at);
  }
  for (const timer of activity?.timers ?? []) {
    const located = byThread.get(timer.thread_key) ?? locate({ thread_key: timer.thread_key });
    const conversation = open(located, located.kind === "console" ? "console" : "");
    conversation.items.push({ type: "timer", at: timer.created_at || timer.due_at, timer });
    touch(conversation, timer.updated_at || timer.created_at);
  }

  if (!byKey.has(CONSOLE_CONVERSATION)) open({ key: CONSOLE_CONVERSATION, kind: "console" }, "console");
  const conversations = [...byKey.values()];
  for (const conversation of conversations) {
    if ((raised.get(conversation.key) ?? 0) > (lastReply.get(conversation.key) ?? 0)) conversation.attention = true;
    conversation.items.sort((a, b) => stamp(a.at) - stamp(b.at) || itemOrder[a.type] - itemOrder[b.type]);
  }
  return conversations.sort((a, b) => {
    // The console stays on top as the place to start a request.
    if (a.kind === "console" || b.kind === "console") return a.kind === "console" ? -1 : 1;
    return stamp(b.lastAt) - stamp(a.lastAt) || a.key.localeCompare(b.key);
  });
}

// handledReply is a message from a person that the agent took up. Callback
// sources such as the Meego poller are machines, not someone answering.
function handledReply(trigger: SuperAgentTrigger): boolean {
  if (trigger.kind || trigger.source_kind === "callback") return false;
  return trigger.disposition === "accepted" || trigger.disposition === "answered" || trigger.disposition === "follow_up";
}

// lastText is a one-line preview of an item, without markdown marks.
export function previewText(item: ChatItem | undefined): string {
  if (!item) return "";
  const raw = item.type === "request" ? item.trigger.title || item.trigger.text
    : item.type === "answer" || item.type === "event" ? item.trigger.answer ?? ""
    : item.type === "ticket" ? item.ticket.title
    : item.type === "plan" ? item.plan.title
    : item.timer.note;
  return raw.replace(/[#*_`>|]/g, "").replace(/\s+/g, " ").trim();
}
