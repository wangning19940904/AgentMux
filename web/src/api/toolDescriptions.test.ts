import { afterEach, expect, it, vi } from "vitest";
import { api } from "./index";

afterEach(() => { vi.unstubAllGlobals(); });

it.each(["local", "ssh-1"])("saves the selected %s instance even in all-machines scope", async (targetID) => {
  vi.stubGlobal("localStorage", { getItem: () => "all" });
  const fetchMock = vi.fn(async () => new Response(JSON.stringify({ targets: [{
    target: { id: targetID, name: targetID, kind: targetID === "local" ? "local" : "ssh", trusted: true, online: true },
    responses: [{ key: "description", status: 200, ok: true, data: { ok: true, description: "新描述" } }],
  }] }), { status: 200 }));
  vi.stubGlobal("fetch", fetchMock);
  await expect(api.updateToolDescription("cli", "github-cli", "新描述", targetID))
    .resolves.toMatchObject({ ok: true, description: "新描述" });
  const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
  const body = JSON.parse(String(init.body));
  expect(body.target_ids).toEqual([targetID]);
  expect(body.requests).toEqual([{ key: "description", method: "POST", path: "/api/v1/tools/description",
    body: { kind: "cli", id: "github-cli", description: "新描述" } }]);
});
