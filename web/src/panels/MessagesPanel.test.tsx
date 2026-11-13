// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, MAILBOX_UNREAD_EVENT, mailboxAPI } from "../api";
import type { MailContact, MailDirectory, MailMessage, MailOverview, MailParty } from "../api";
import { I18nProvider } from "../i18n";
import { MessagesPanel } from "./MessagesPanel";
import { contactState, messageState, partyFromAddress, relativeTime } from "./messagesModel";

let root: Root | null = null;
let container: HTMLElement;

// Two turns: a load that finishes in the first can start another (opening a
// conversation reloads the list), and that one should land inside act too.
async function settle() {
  for (let turn = 0; turn < 2; turn++) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  }
}

// until settles first, so whatever the last click started resolves inside act.
async function until<T>(find: () => T | null | undefined | false): Promise<T> {
  for (let attempt = 0; attempt < 50; attempt++) {
    await settle();
    const found = find();
    if (found) return found;
  }
  throw new Error(`not found in: ${container.textContent}`);
}

const hasText = (text: string) => (container.textContent ?? "").includes(text);
const button = (name: RegExp) => [...container.querySelectorAll("button")].find((item) => name.test((item.textContent ?? "").trim()));
const row = (name: string) => [...container.querySelectorAll<HTMLButtonElement>(".mail-row")].find((item) => item.textContent?.includes(name));
const composer = () => container.querySelector<HTMLTextAreaElement>(".mail-composer textarea");
const bubbles = () => [...container.querySelectorAll(".mail-bubble")].map((item) => item.textContent);

function click(element: Element | undefined | null) {
  if (!element) throw new Error("nothing to click");
  act(() => { element.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
}

function setValue(element: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, value: string) {
  const prototype = Object.getPrototypeOf(element) as object;
  Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(element, value);
  act(() => { element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? "change" : "input", { bubbles: true })); });
}

function press(element: Element, key: string, shiftKey = false) {
  act(() => { element.dispatchEvent(new KeyboardEvent("keydown", { key, shiftKey, bubbles: true, cancelable: true })); });
}

const me: MailParty = { address: "person:ning", kind: "person", name: "Ning", online: true };
const planner: MailParty = { address: "agent:planner", kind: "agent", name: "Planner", description: "Plans the week", online: true };
const builder: MailParty = { address: "agent:builder@dev_1", kind: "agent", name: "Builder", machine: "Mac mini", online: false };
const reviewer: MailParty = { address: "agent:reviewer@dev_2", kind: "agent", name: "Reviewer", machine: "Lab box", owner_name: "Mei", online: true };
const mei: MailParty = { address: "person:mei", kind: "person", name: "Mei", owner_name: "Mei", online: true };

const recent = () => new Date(Date.now() - 5 * 60_000).toISOString();

function message(id: string, from: MailParty, to: MailParty, text: string, extra: Partial<MailMessage> = {}): MailMessage {
  return { id, from: from.address, from_name: from.name, to: to.address, to_name: to.name, text, created_at: recent(), delivered_at: recent(), ...extra };
}

function overviewData(): MailOverview {
  return {
    me, admin: false, unread: 2, controlled: [me.address, planner.address, builder.address],
    conversations: [
      { self: me, peer: planner, last: message("m2", planner, me, "Draft is ready\nfor review"), unread: 2, messages: 2 },
      { self: me, peer: builder, last: message("m4", me, builder, "Ship it", { delivered_at: undefined }), unread: 0, messages: 1 },
    ],
    agent_conversations: [
      { self: planner, peer: reviewer, last: message("m6", reviewer, planner, "Looks good to me"), unread: 0, messages: 2 },
    ],
  };
}

const directoryData: MailDirectory = { parties: [planner, builder, reviewer, mei], reachable: [planner.address, builder.address] };

const pendingContact: MailContact = {
  id: "mct_1", a: planner.address, a_name: "Planner", b: reviewer.address, b_name: "Reviewer",
  status: "pending", requested_by: mei.address, waiting_for: planner.address, created_at: recent(),
};

let threads: Record<string, MailMessage[]>;

async function renderPanel(language: "en" | "zh" = "en") {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(<I18nProvider language={language}><MessagesPanel /></I18nProvider>));
  // The first loads land inside act.
  await settle();
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  threads = {
    [planner.address]: [message("m1", me, planner, "Plan the week"), message("m2", planner, me, "Draft is ready\nfor review")],
    [builder.address]: [message("m4", me, builder, "Ship it", { delivered_at: undefined })],
    [`${planner.address} ${reviewer.address}`]: [message("m5", planner, reviewer, "Please review the draft"), message("m6", reviewer, planner, "Looks good to me")],
  };
  vi.spyOn(mailboxAPI, "overview").mockImplementation(async () => overviewData());
  vi.spyOn(mailboxAPI, "directory").mockResolvedValue(directoryData);
  vi.spyOn(mailboxAPI, "contacts").mockResolvedValue([]);
  vi.spyOn(mailboxAPI, "messages").mockImplementation(async (withAddress, andAddress = "") =>
    threads[andAddress ? `${withAddress} ${andAddress}` : withAddress] ?? []);
});

afterEach(async () => {
  // Let reloads a test started finish inside act before the panel goes away.
  await settle();
  act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("MessagesPanel", () => {
  it("lists the viewer's conversations and the ones between agents", async () => {
    const unread: number[] = [];
    const listen = (event: Event) => unread.push((event as CustomEvent<number>).detail);
    window.addEventListener(MAILBOX_UNREAD_EVENT, listen);
    await renderPanel();
    await until(() => row("Planner"));
    window.removeEventListener(MAILBOX_UNREAD_EVENT, listen);

    const groups = [...container.querySelectorAll(".mail-group-title")].map((item) => item.firstChild?.textContent);
    expect(groups).toEqual(["Yours", "Between agents"]);

    const mine = row("Draft is ready")!;
    expect(mine.querySelector(".mail-row-preview")?.textContent).toBe("Draft is ready for review");
    expect(mine.querySelector(".mail-unread")?.textContent).toBe("2");
    expect(mine.querySelector(".mail-dot")?.className).toContain("online");
    expect(mine.querySelector("time")?.textContent).toBe("5 min ago");

    const offline = row("Builder")!;
    expect(offline.textContent).toContain("Mac mini");
    expect(offline.querySelector(".mail-dot")?.className).toContain("offline");
    expect(offline.querySelector(".mail-row-preview")?.textContent).toBe("You: Ship it");

    const between = row("Planner ↔ Reviewer")!;
    expect(between.textContent).toContain("Lab box");
    expect(between.querySelector(".mail-row-preview")?.textContent).toBe("Reviewer: Looks good to me");
    // Addresses stay out of the list when there is a name.
    expect(hasText("agent:planner")).toBe(false);
    // The shell's navigation badge hears the unread count.
    expect(unread).toEqual([2]);
  });

  it("opens a conversation, shows its messages and marks it read", async () => {
    await renderPanel();
    click(await until(() => row("Draft is ready")));
    await until(() => bubbles().length === 2);
    expect(mailboxAPI.messages).toHaveBeenCalledWith(planner.address);
    expect(bubbles()).toEqual(["Plan the week", "Draft is ready\nfor review"]);
    const [own, theirs] = [...container.querySelectorAll(".mail-message")];
    expect(own.className).toContain("end");
    expect(theirs.className).toContain("start");
    expect(composer()).not.toBeNull();
    // Reading it clears the count in the list and reloads the overview.
    expect(row("Draft is ready")!.querySelector(".mail-unread")).toBeNull();
    await until(() => vi.mocked(mailboxAPI.overview).mock.calls.length >= 2);
    // The back control returns to the list.
    click(container.querySelector(".mail-back"));
    await settle();
    expect(container.querySelector(".mail-thread")).toBeNull();
    expect(hasText("Choose a conversation")).toBe(true);
  });

  it("says when a message is waiting for a machine or was not delivered", async () => {
    threads[builder.address] = [
      message("m4", me, builder, "Ship it", { delivered_at: undefined }),
      message("m7", me, builder, "Hello again", { delivered_at: undefined, failed_at: recent(), error: "Mac mini has been offline for 3 days" }),
    ];
    await renderPanel();
    click(await until(() => row("Builder")));
    await until(() => bubbles().length === 2);
    const [waiting, failed] = [...container.querySelectorAll(".mail-message")];
    expect(waiting.textContent).toContain("Waiting for the recipient's machine to come online");
    expect(failed.textContent).toContain("Not delivered: Mac mini has been offline for 3 days");
    expect(hasText("Builder's machine is offline")).toBe(true);
  });

  it("sends a message with Enter and shows it", async () => {
    const send = vi.spyOn(mailboxAPI, "send").mockImplementation(async (to, text) => message("m9", me, planner, text, { to }));
    await renderPanel();
    click(await until(() => row("Draft is ready")));
    const input = await until(composer);
    await until(() => bubbles().length === 2);

    // Shift+Enter is a new line, not a send.
    setValue(input, "On it");
    press(input, "Enter", true);
    expect(send).not.toHaveBeenCalled();

    press(input, "Enter");
    await until(() => bubbles().includes("On it"));
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith(planner.address, "On it");
    expect(composer()!.value).toBe("");
    expect([...container.querySelectorAll(".mail-message")].pop()!.className).toContain("end");
  });

  it("shows why a message was refused and keeps the draft", async () => {
    const refusal = "Ning and Planner are not contacts, so Ning cannot write to Planner";
    const send = vi.spyOn(mailboxAPI, "send").mockRejectedValue(new APIError(refusal, 422, { error: refusal }));
    await renderPanel();
    click(await until(() => row("Draft is ready")));
    const input = await until(composer);
    setValue(input, "Are you there?");
    click(button(/^Send$/));
    const alert = await until(() => container.querySelector(".mail-composer [role=alert]"));
    expect(send).toHaveBeenCalledWith(planner.address, "Are you there?");
    expect(alert.textContent).toBe(refusal);
    expect(composer()!.value).toBe("Are you there?");
    expect(bubbles()).toHaveLength(2);
    expect((button(/^Send$/) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows a watched conversation from both sides, read-only", async () => {
    await renderPanel();
    click(await until(() => row("Planner ↔ Reviewer")));
    await until(() => bubbles().length === 2);
    expect(mailboxAPI.messages).toHaveBeenCalledWith(planner.address, reviewer.address);
    const [first, second] = [...container.querySelectorAll(".mail-message")];
    expect(first.className).toContain("end");
    expect(first.querySelector("header")?.textContent).toBe("Planner");
    expect(second.className).toContain("start");
    expect(second.querySelector("header")?.textContent).toBe("Reviewer");
    expect(composer()).toBeNull();
    expect(button(/^Send$/)).toBeUndefined();
    expect(hasText("Read-only. This conversation is between Planner and Reviewer.")).toBe(true);
    // Watching does not touch the unread count.
    expect(vi.mocked(mailboxAPI.overview).mock.calls).toHaveLength(1);
  });

  it("starts a new message with someone the viewer may write to", async () => {
    const send = vi.spyOn(mailboxAPI, "send").mockImplementation(async (to, text) => message("m10", me, builder, text, { to, delivered_at: undefined }));
    await renderPanel();
    await until(() => row("Planner"));
    click(button(/^New message$/));
    const picker = await until(() => container.querySelector(".mail-picker-list"));
    // Only reachable parties are offered: not Reviewer, and never another person.
    expect([...picker.querySelectorAll(".mail-row-title")].map((item) => item.textContent)).toEqual(["Builder", "Planner"]);
    expect(picker.textContent).toContain("Agent · Mac mini · Offline");
    click([...picker.querySelectorAll(".mail-row")][0]);
    const input = await until(composer);
    setValue(input, "Wake up");
    click(button(/^Send$/));
    await until(() => send.mock.calls.length > 0);
    expect(send).toHaveBeenCalledWith(builder.address, "Wake up");
  });

  it("explains what to do when there are no messages", async () => {
    vi.mocked(mailboxAPI.overview).mockResolvedValue({ ...overviewData(), conversations: [], agent_conversations: [], unread: 0 });
    await renderPanel();
    await until(() => hasText("No messages yet. Write to an agent to start a conversation."));
    expect(container.querySelector(".mail-list .action")?.textContent?.trim()).toBe("New message");
  });

  it("accepts a pending contact the viewer answers for", async () => {
    let stored: MailContact[] = [pendingContact];
    vi.mocked(mailboxAPI.contacts).mockImplementation(async () => stored);
    const accept = vi.spyOn(mailboxAPI, "acceptContact").mockImplementation(async (id) => {
      stored = [{ ...pendingContact, status: "accepted", waiting_for: undefined, accepted_at: recent() }];
      return stored.find((contact) => contact.id === id)!;
    });
    await renderPanel();
    // The view switch counts what waits for the viewer.
    const tab = await until(() => button(/^Contacts/)?.querySelector(".mail-unread") && button(/^Contacts/));
    expect(tab.textContent).toBe("Contacts1");
    click(tab);
    await until(() => hasText("Waiting for you to accept"));
    expect(hasText("People do not write to people here.")).toBe(true);
    expect(container.querySelector(".mail-contact-parties")?.textContent).toContain("Planner");
    expect(container.querySelector(".mail-contact-parties")?.textContent).toContain("Reviewer");
    expect(button(/^Decline$/)).toBeDefined();

    click(button(/^Accept$/));
    await until(() => hasText("Connected"));
    expect(accept).toHaveBeenCalledWith("mct_1");
    expect(button(/^Accept$/)).toBeUndefined();
    expect(button(/^Remove$/)).toBeDefined();
  });

  it("lets the viewer withdraw a contact that waits for the other side", async () => {
    vi.mocked(mailboxAPI.contacts).mockResolvedValue([{ ...pendingContact, requested_by: me.address, waiting_for: reviewer.address }]);
    const remove = vi.spyOn(mailboxAPI, "removeContact").mockResolvedValue({ ok: true });
    await renderPanel();
    click(await until(() => button(/^Contacts/)));
    await until(() => hasText("Waiting for Mei to accept"));
    expect(button(/^Accept$/)).toBeUndefined();
    click(button(/^Withdraw$/));
    await until(() => remove.mock.calls.length > 0);
    expect(remove).toHaveBeenCalledWith("mct_1");
  });

  it("adds a contact between one of the viewer's parties and someone else's", async () => {
    const refusal = "Planner and Builder have the same owner and can already write to each other";
    const add = vi.spyOn(mailboxAPI, "addContact")
      .mockRejectedValueOnce(new APIError(refusal, 422, { error: refusal }))
      .mockResolvedValue({ ...pendingContact, id: "mct_2", requested_by: me.address, waiting_for: reviewer.address });
    await renderPanel();
    click(await until(() => button(/^Contacts/)));
    await until(() => hasText("No contacts yet."));
    click(button(/^Add contact$/));
    const [yours, other] = await until(() => {
      const selects = [...container.querySelectorAll<HTMLSelectElement>(".mail-contact-form select")];
      return selects.length === 2 && selects;
    });
    const values = (select: HTMLSelectElement) => [...select.querySelectorAll("option")].map((item) => item.value).filter(Boolean);
    // One side is what the viewer answers for; the other is someone else's.
    expect(values(yours)).toEqual([builder.address, planner.address, me.address]);
    expect(values(other)).toEqual([reviewer.address, mei.address]);
    setValue(yours, planner.address);
    setValue(other, reviewer.address);

    click(button(/^Add contact$/));
    const alert = await until(() => container.querySelector(".mail-contact-form [role=alert]"));
    expect(alert.textContent).toBe(refusal);
    expect(add).toHaveBeenCalledWith(planner.address, reviewer.address);
    expect(yours.value).toBe(planner.address);

    click(button(/^Add contact$/));
    await until(() => hasText("Request sent. It takes effect once Mei accepts."));
    expect(container.querySelector(".mail-contact-form")).toBeNull();
  });

  it("reads naturally in Chinese", async () => {
    await renderPanel("zh");
    await until(() => row("Planner"));
    expect([...container.querySelectorAll(".mail-group-title")].map((item) => item.firstChild?.textContent)).toEqual(["我的对话", "Agent 之间"]);
    expect(row("Builder")!.querySelector(".mail-row-preview")?.textContent).toBe("我：Ship it");
    expect(row("Draft is ready")!.querySelector("time")?.textContent).toBe("5 分钟前");
  });
});

describe("messagesModel", () => {
  it("reads a message's delivery state", () => {
    expect(messageState({ delivered_at: "2026-11-12T00:00:00Z" })).toBe("delivered");
    expect(messageState({})).toBe("waiting");
    expect(messageState({ failed_at: "2026-11-12T00:00:00Z" })).toBe("failed");
  });

  it("knows which pending contacts the viewer can accept", () => {
    expect(contactState(pendingContact, [planner.address])).toBe("accept");
    expect(contactState(pendingContact, [reviewer.address])).toBe("waiting");
    expect(contactState({ ...pendingContact, status: "accepted" }, [])).toBe("accepted");
  });

  it("names a party nobody listed after its address", () => {
    expect(partyFromAddress("agent:scout@dev_9")).toMatchObject({ kind: "agent", name: "scout", online: false });
    expect(partyFromAddress("person:mei", "Mei")).toMatchObject({ kind: "person", name: "Mei" });
  });

  it("writes recent times relative to now", () => {
    const now = Date.parse("2026-11-13T12:00:00Z");
    expect(relativeTime("2026-11-13T11:59:40Z", now)).toEqual({ key: "messages.time.now" });
    expect(relativeTime("2026-11-13T11:15:00Z", now)).toEqual({ key: "messages.time.minutes", count: 45 });
    expect(relativeTime("2026-11-13T07:00:00Z", now)).toEqual({ key: "messages.time.hours", count: 5 });
    expect(relativeTime("2026-11-10T12:00:00Z", now)).toEqual({ key: "messages.time.days", count: 3 });
    expect(relativeTime("2026-10-01T12:00:00Z", now)).toMatchObject({ date: new Date("2026-10-01T12:00:00Z") });
    expect(relativeTime(undefined, now)).toBeNull();
  });
});
