import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError } from "./client";
import { mailboxAPI } from "./mailbox";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch(respond: (path: string, init?: RequestInit) => Response) {
  const fetchMock = vi.fn(async (path: string, init?: RequestInit) => respond(path, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("mailbox API", () => {
  it("asks the Console's own server as the signed-in person, whatever machine is selected", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "ssh-1" });
    const fetchMock = stubFetch(() => new Response(JSON.stringify({ me: { address: "person:ning" }, admin: false, conversations: null, agent_conversations: null, controlled: null })));

    await expect(mailboxAPI.overview()).resolves.toEqual({
      me: { address: "person:ning" }, admin: false, conversations: [], agent_conversations: [], controlled: [], unread: 0,
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/mailbox");
    const headers = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
    expect(headers.get("X-AgentMux-Console")).toBe("1");
  });

  it("reads the viewer's conversation with one address, or the one between two others", async () => {
    const fetchMock = stubFetch(() => new Response("null"));
    await expect(mailboxAPI.messages("agent:planner@dev_1")).resolves.toEqual([]);
    await mailboxAPI.messages("agent:planner", "agent:reviewer@dev_2");
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/mailbox/messages?with=agent%3Aplanner%40dev_1",
      "/api/v1/mailbox/messages?with=agent%3Aplanner&and=agent%3Areviewer%40dev_2",
    ]);
  });

  it("sends a message and reports why one was refused", async () => {
    const fetchMock = stubFetch((_path, init) => String(init?.body).includes("refused")
      ? new Response(JSON.stringify({ error: "Ning and Reviewer are not contacts, so Ning cannot write to Reviewer" }), { status: 422 })
      : new Response(JSON.stringify({ id: "m1", from: "person:ning", to: "agent:planner", text: "hello", created_at: "2026-11-13T00:00:00Z" })));

    await expect(mailboxAPI.send("agent:planner", "hello")).resolves.toMatchObject({ id: "m1" });
    const [path, init] = fetchMock.mock.calls[0]!;
    expect(path).toBe("/api/v1/mailbox/messages");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ to: "agent:planner", text: "hello" });

    const refusal = await mailboxAPI.send("agent:reviewer", "refused").catch((error: unknown) => error);
    expect(refusal).toBeInstanceOf(APIError);
    expect((refusal as APIError).status).toBe(422);
    expect((refusal as APIError).message).toBe("Ning and Reviewer are not contacts, so Ning cannot write to Reviewer");
  });

  it("adds, accepts and removes contacts", async () => {
    const fetchMock = stubFetch(() => new Response(JSON.stringify({ ok: true })));
    await mailboxAPI.addContact("agent:planner", "agent:reviewer@dev_2");
    await mailboxAPI.acceptContact("mct_1");
    await mailboxAPI.removeContact("mct 1");
    expect(fetchMock.mock.calls.map(([path, init]) => [init?.method, path, init?.body])).toEqual([
      ["POST", "/api/v1/mailbox/contacts", JSON.stringify({ a: "agent:planner", b: "agent:reviewer@dev_2" })],
      ["POST", "/api/v1/mailbox/contacts/accept", JSON.stringify({ id: "mct_1" })],
      ["DELETE", "/api/v1/mailbox/contacts?id=mct%201", undefined],
    ]);
  });
});
