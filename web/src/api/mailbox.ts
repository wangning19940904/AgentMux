// Direct messages ("the mailbox"): people write to agents and agents write
// back, agents write to each other, and contacts connect parties that have
// different owners. Like push, these calls always go to the Console's own
// server: the mailbox belongs to the person signed in to this Console.
import { APIError } from "./client";

export type MailPartyKind = "agent" | "person";

export type MailParty = {
  // agent:<id>, agent:<id>@<device id> or person:<login>
  address: string;
  kind: MailPartyKind;
  name: string;
  description?: string;
  machine?: string;
  owner_name?: string;
  online: boolean;
};

export type MailMessage = {
  id: string;
  from: string;
  from_name?: string;
  to: string;
  to_name?: string;
  text: string;
  created_at: string;
  // Missing while the recipient's machine is offline.
  delivered_at?: string;
  read_at?: string;
  attempts?: number;
  error?: string;
  // Set when AgentMux gave up; error says why.
  failed_at?: string;
};

export type MailConversation = {
  peer: MailParty;
  self: MailParty;
  last: MailMessage;
  unread: number;
  messages: number;
};

export type MailContact = {
  id: string;
  a: string;
  a_name?: string;
  b: string;
  b_name?: string;
  status: "pending" | "accepted";
  requested_by?: string;
  // The address whose owner has not accepted yet.
  waiting_for?: string;
  created_at: string;
  accepted_at?: string;
};

export type MailOverview = {
  me: MailParty;
  admin: boolean;
  // The viewer's own conversations with agents.
  conversations: MailConversation[];
  // Conversations the viewer may watch but is not part of.
  agent_conversations: MailConversation[];
  unread: number;
  // The addresses the viewer answers for.
  controlled: string[];
};

export type MailDirectory = {
  // Everyone the viewer can name.
  parties: MailParty[];
  // The addresses the viewer may write to right now.
  reachable: string[];
};

// MAILBOX_UNREAD_EVENT tells the shell how many messages are unread, so the
// navigation badge follows the Messages page without a request of its own.
export const MAILBOX_UNREAD_EVENT = "agentmux:mailbox-unread";

export function notifyMailboxUnread(unread: number) {
  window.dispatchEvent(new CustomEvent<number>(MAILBOX_UNREAD_EVENT, { detail: unread }));
}

const HEADERS = { "X-AgentMux-Console": "1" } as const;

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    cache: "no-store",
    headers: body === undefined ? HEADERS : { ...HEADERS, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const payload = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) {
    throw new APIError(typeof payload.error === "string" ? payload.error : `${path}: ${res.status}`, res.status, payload);
  }
  return payload as T;
}

const BASE = "/api/v1/mailbox";

// The server writes an empty list as null.
const list = <T>(items: T[] | null | undefined): T[] => items ?? [];

export const mailboxAPI = {
  overview: () => call<MailOverview>("GET", BASE).then((overview) => ({
    ...overview,
    conversations: list(overview.conversations),
    agent_conversations: list(overview.agent_conversations),
    controlled: list(overview.controlled),
    unread: overview.unread ?? 0,
  })),
  // With one address: the viewer's conversation with it, which also marks it
  // read. With two: the conversation between two other parties, only watched.
  messages: (withAddress: string, andAddress = "") =>
    call<MailMessage[] | null>("GET", `${BASE}/messages?with=${encodeURIComponent(withAddress)}${andAddress ? `&and=${encodeURIComponent(andAddress)}` : ""}`).then(list),
  send: (to: string, text: string) => call<MailMessage>("POST", `${BASE}/messages`, { to, text }),
  directory: () => call<MailDirectory>("GET", `${BASE}/directory`).then((directory) => ({
    parties: list(directory.parties),
    reachable: list(directory.reachable),
  })),
  contacts: () => call<MailContact[] | null>("GET", `${BASE}/contacts`).then(list),
  addContact: (a: string, b: string) => call<MailContact>("POST", `${BASE}/contacts`, { a, b }),
  acceptContact: (id: string) => call<MailContact>("POST", `${BASE}/contacts/accept`, { id }),
  removeContact: (id: string) => call<{ ok: boolean }>("DELETE", `${BASE}/contacts?id=${encodeURIComponent(id)}`),
};
