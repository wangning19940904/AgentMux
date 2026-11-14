import { describe, expect, it } from "vitest";
import { groupResources, selectedGroupMember } from "./resourceGroups";
import { targetKey } from "./TargetBadge";

describe("fleet resource grouping", () => {
  type Endpoint = { id: string; base_url: string; target_id: string; model?: string };
  const local: Endpoint = { id: "relay", base_url: "https://example.test/", target_id: "local" };
  const endpointKey = (e: Endpoint) => JSON.stringify([e.id, e.base_url.trim().replace(/\/+$/, "")]);
  const remote = { ...local, base_url: "https://example.test", target_id: "ssh-a", model: "remote-model" };
  const another = { ...remote, base_url: "https://different.test" };
  const instance = (p: Endpoint) => targetKey(p.target_id, p.id);
  it("aggregates matching endpoints while preserving machine-specific settings", () => {
    const groups = groupResources([local, remote, another], endpointKey, instance, true);
    expect(groups).toHaveLength(2);
    expect(groups[0].members).toEqual([local, remote]);
    expect(selectedGroupMember(groups[0], instance(remote), instance)).toBe(remote);
    expect(selectedGroupMember(groups[0], "removed", instance)).toBe(local);
  });
  it("does not aggregate in a single-machine view", () => {
    expect(groupResources([local, remote], endpointKey, instance, false)).toHaveLength(2);
  });
  it("groups frameworks and skills by identity, without losing installed instances", () => {
    const rows = [{ id: "codex", machine: "local", version: "1" }, { id: "codex", machine: "ssh-a", version: "2" }, { id: "pdf", machine: "ssh-a", version: "1" }];
    const grouped = groupResources(rows, (r) => r.id, (r) => `${r.machine}:${r.id}`, true);
    expect(grouped.map((g) => g.members.length)).toEqual([2, 1]);
    expect(grouped.flatMap((g) => g.members)).toEqual(rows);
  });
});
