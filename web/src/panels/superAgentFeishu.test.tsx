// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "../api";
import { I18nProvider } from "../i18n";
import { FeishuActorPicker, FeishuChatPicker, FeishuUserAuthorize, useFeishuDirectory } from "./superAgentFeishu";

// Polling waits between requests; here it does not.
vi.mock("./connect/connectShared", async (original) => ({ ...(await original<object>()), sleep: () => Promise.resolve() }));

let root: Root | null = null;
let container: HTMLElement;

async function settle(ms = 0) {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, ms)); });
}

async function until<T>(find: () => T | null | undefined | false): Promise<T> {
  for (let attempt = 0; attempt < 50; attempt++) {
    const found = find();
    if (found) return found;
    await settle();
  }
  throw new Error(`not found in: ${container.textContent}`);
}

const hasText = (text: string) => (container.textContent ?? "").includes(text);
const button = (name: RegExp) => [...container.querySelectorAll("button")].find((item) => name.test((item.textContent ?? "").trim()));
const input = (label: string) => container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;

function click(element: Element | undefined) {
  if (!element) throw new Error(`nothing to click in: ${container.textContent}`);
  act(() => { element.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
}

function type(element: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(element, value);
  act(() => { element.dispatchEvent(new Event("input", { bubbles: true })); });
}

function press(element: Element, key: string) {
  act(() => { element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })); });
}

// Harness is the Feishu part of a trigger: authorize, pick groups, pick people.
function Harness() {
  const [chats, setChats] = useState("");
  const [actors, setActors] = useState<string[]>(["ou_saved"]);
  const directory = useFeishuDirectory("c1", [], actors);
  return (
    <>
      <FeishuUserAuthorize channelID="c1" bot="Team bot" directory={directory} />
      <FeishuChatPicker label="Chats" value={chats} channelID="c1" directory={directory} onChange={setChats} />
      <FeishuActorPicker label="Senders" values={actors} allowEveryone scoped={false} help="" channelID="c1" directory={directory} onChange={setActors} />
    </>
  );
}

function render() {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(<I18nProvider language="en"><Harness /></I18nProvider>));
}

const me = { open_id: "ou_me", name: "Wang Ning", scopes: ["im:chat:read", "im:chat.members:write_only", "contact:user:search", "offline_access"] };

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.spyOn(api, "feishuUserAuth").mockResolvedValue({ user: null });
  vi.spyOn(api, "feishuChats").mockResolvedValue({ items: [{ chat_id: "oc_bot", name: "Bot's group" }] });
  vi.spyOn(api, "feishuMembers").mockResolvedValue({ items: [] });
  vi.spyOn(api, "feishuUsers").mockResolvedValue([]);
  vi.spyOn(api, "feishuMyChats").mockResolvedValue({ items: [{ chat_id: "oc_team", name: "Team", bot_in: true }, { chat_id: "oc_side", name: "Side", bot_in: false }] });
  vi.spyOn(api, "feishuContacts").mockImplementation(async (_channel, lookup) => {
    if (lookup.open_ids) return { items: [{ open_id: "ou_saved", name: "Saved Person" }] };
    if (lookup.query) return { items: [{ open_id: "ou_found", name: "Zhang San", department: "R&D" }] };
    return { items: [{ open_id: "ou_a", name: "Alice", email: "alice@acme.com" }, { open_id: "ou_b", name: "Bob" }] };
  });
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Feishu user authorization", () => {
  it("authorizes once, then lists the person's groups and adds the bot to one", async () => {
    vi.spyOn(api, "startFeishuUserAuth").mockResolvedValue({ session_id: "s1", verification_url: "https://accounts.feishu.cn/oauth/v1/device/verify?code=UC-1", user_code: "UC-1", expires_in: 600, interval: 5 });
    const poll = vi.spyOn(api, "pollFeishuUserAuth")
      .mockResolvedValueOnce({ status: "pending" })
      .mockResolvedValueOnce({ status: "completed", user: me });
    const add = vi.spyOn(api, "addFeishuBotToChat").mockResolvedValue({ added: true });
    render();
    await until(() => hasText("The bot is in 1 chats."));
    expect(api.feishuMyChats).not.toHaveBeenCalled();
    expect(hasText("add Team bot to a group from here")).toBe(true);

    click(button(/^Authorize with Feishu$/));
    await until(() => hasText("Authorized as Wang Ning."));
    expect(api.startFeishuUserAuth).toHaveBeenCalledWith("c1");
    expect(poll).toHaveBeenCalledTimes(2);
    expect(poll).toHaveBeenCalledWith("c1", "s1");

    // The person's groups replace the bot's, and say where the bot is missing.
    await until(() => hasText("You're in 2 groups."));
    const chats = input("Chats");
    act(() => { chats.focus(); });
    type(chats, "si");
    expect(container.querySelector("[role=option]")?.textContent).toContain("bot not in it");
    press(chats, "Enter");
    await until(() => hasText("The bot isn't in “Side” yet."));
    click(button(/^Add the bot$/));
    await until(() => hasText("The bot joined “Side”."));
    expect(add).toHaveBeenCalledWith("c1", "oc_side");
  });

  it("shows the link and code while waiting, and lets the person retry", async () => {
    vi.spyOn(api, "startFeishuUserAuth").mockResolvedValue({ session_id: "s1", verification_url: "https://accounts.feishu.cn/verify?code=UC-2", user_code: "UC-2", expires_in: 600, interval: 5 });
    let answer: (value: { status: "denied" }) => void = () => undefined;
    vi.spyOn(api, "pollFeishuUserAuth").mockImplementation(() => new Promise((resolve) => { answer = resolve; }));
    render();
    click(await until(() => button(/^Authorize with Feishu$/)));
    await until(() => hasText("Scan with Feishu, or open the link, and approve."));
    expect(container.querySelector(".qr-frame svg")).not.toBeNull();
    expect(container.querySelector<HTMLAnchorElement>("a.super-agent-scope-apply")?.href).toBe("https://accounts.feishu.cn/verify?code=UC-2");
    expect(hasText("Code: UC-2")).toBe(true);
    await act(async () => { answer({ status: "denied" }); });
    await until(() => hasText("Authorization was declined."));
    expect(button(/Try again|Retry/)).toBeDefined();
  });

  it("lists the people the person has chatted with, searches everyone, and names saved senders", async () => {
    vi.mocked(api.feishuUserAuth).mockResolvedValue({ user: me });
    render();
    await until(() => (container.querySelector(".super-agent-token")?.textContent ?? "").includes("Saved Person"));
    expect(api.feishuContacts).toHaveBeenCalledWith("c1", { open_ids: ["ou_saved"] });
    expect(api.feishuUsers).not.toHaveBeenCalled();

    const senders = input("Senders");
    act(() => { senders.focus(); });
    await until(() => hasText("2 people you've chatted with."));
    expect(api.feishuMembers).not.toHaveBeenCalled();
    type(senders, "Zhang");
    await settle(350);
    await until(() => [...container.querySelectorAll("[role=option]")].some((item) => item.textContent?.includes("Zhang San")));
    expect(api.feishuContacts).toHaveBeenCalledWith("c1", { query: "Zhang" });
    expect([...container.querySelectorAll("[role=option]")].find((item) => item.textContent?.includes("Zhang San"))?.textContent).toContain("R&D");
  });

  it("falls back to the bot's view when the authorization has lapsed", async () => {
    vi.mocked(api.feishuUserAuth).mockResolvedValue({ user: me });
    vi.mocked(api.feishuMyChats).mockRejectedValue(new APIError("authorize with your Feishu account first", 409, { error: "authorize with your Feishu account first", user_auth_required: true }));
    render();
    await until(() => hasText("Your Feishu authorization has lapsed. Authorize again."));
    await until(() => hasText("The bot is in 1 chats."));
    expect(button(/^Authorize with Feishu$/)).toBeDefined();
  });

  it("says which scopes an authorization lacks and where to enable them", async () => {
    const applyURL = "https://open.feishu.cn/app/cli_a/auth?q=contact:user:search&op_from=openapi&token_type=user";
    vi.mocked(api.feishuUserAuth).mockResolvedValue({ user: { ...me, missing: ["contact:user:search"], apply_url: applyURL } });
    vi.spyOn(api, "addFeishuBotToChat").mockRejectedValue(new APIError("99991679 denied", 502, {
      error: "99991679 denied", scopes: ["im:chat.members:write_only"], token_type: "user",
      apply_url: "https://open.feishu.cn/app/cli_a/auth?q=im:chat.members:write_only&op_from=openapi&token_type=user",
    }));
    render();
    await until(() => hasText("This authorization lacks contact:user:search."));
    const links = () => [...container.querySelectorAll<HTMLAnchorElement>("a.super-agent-scope-apply")].map((link) => link.href);
    expect(links()).toContain(applyURL);

    await until(() => hasText("You're in 2 groups."));
    const chats = input("Chats");
    act(() => { chats.focus(); });
    type(chats, "si");
    press(chats, "Enter");
    click(await until(() => button(/^Add the bot$/)));
    await until(() => hasText("After the app has it and is published, authorize again."));
    expect(links()).toContain("https://open.feishu.cn/app/cli_a/auth?q=im:chat.members:write_only&op_from=openapi&token_type=user");
  });
});
