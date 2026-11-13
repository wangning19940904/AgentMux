import type { MailContact, MailConversation, MailMessage, MailParty } from "../api";

// An open thread is either the viewer's own conversation with one party, or
// a conversation between two other parties that the viewer only watches.
export type OpenThread =
  | { watched: false; peer: string }
  | { watched: true; self: string; peer: string };

export function threadKey(thread: OpenThread | null): string {
  if (!thread) return "";
  return thread.watched ? `watch ${thread.self} ${thread.peer}` : `mine ${thread.peer}`;
}

export function conversationThread(conversation: MailConversation, watched: boolean): OpenThread {
  return watched
    ? { watched: true, self: conversation.self.address, peer: conversation.peer.address }
    : { watched: false, peer: conversation.peer.address };
}

// partyFromAddress describes an address nobody listed: a party that is gone
// or lives elsewhere. The name falls back to the id part of the address.
export function partyFromAddress(address: string, name = ""): MailParty {
  const colon = address.indexOf(":");
  const rest = colon < 0 ? address : address.slice(colon + 1);
  const at = rest.indexOf("@");
  return {
    address,
    kind: address.startsWith("person:") ? "person" : "agent",
    name: name || (at < 0 ? rest : rest.slice(0, at)) || address,
    online: false,
  };
}

export type MessageState = "delivered" | "waiting" | "failed";

// A message without delivered_at is still waiting for the recipient's
// machine; failed_at means AgentMux gave up.
export function messageState(message: Pick<MailMessage, "delivered_at" | "failed_at">): MessageState {
  if (message.failed_at) return "failed";
  return message.delivered_at ? "delivered" : "waiting";
}

export type ContactState = "accepted" | "accept" | "waiting";

// contactState says what the viewer can do with a contact: nothing more when
// it is accepted, accept it when it waits for a party they answer for, or
// wait for the other side.
export function contactState(contact: MailContact, controlled: ReadonlyArray<string>): ContactState {
  if (contact.status === "accepted") return "accepted";
  return contact.waiting_for && controlled.includes(contact.waiting_for) ? "accept" : "waiting";
}

export type RelativeTime = { key: string; count?: number } | { date: Date };

// relativeTime turns a timestamp into an i18n key for recent times and a
// plain date for anything older than a week.
export function relativeTime(value: string | undefined, now = Date.now()): RelativeTime | null {
  if (!value) return null;
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return null;
  const minutes = Math.floor(Math.max(0, now - time) / 60_000);
  if (minutes < 1) return { key: "messages.time.now" };
  if (minutes < 60) return { key: "messages.time.minutes", count: minutes };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { key: "messages.time.hours", count: hours };
  const days = Math.floor(hours / 24);
  if (days < 7) return { key: "messages.time.days", count: days };
  return { date: new Date(time) };
}

// Parties sort by name, agents before people.
export function sortParties(parties: ReadonlyArray<MailParty>): MailParty[] {
  return [...parties].sort((left, right) =>
    left.kind === right.kind ? left.name.localeCompare(right.name) : left.kind === "agent" ? -1 : 1);
}
