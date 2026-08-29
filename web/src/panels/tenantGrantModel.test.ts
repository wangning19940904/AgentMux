import { describe, expect, it } from "vitest";
import {
  resourcesForType,
  toggleResourceSelection,
  type GrantResourceSources,
} from "./tenantGrantModel";

const sources: GrantResourceSources = {
  agents: [{ id: "a1", name: "Agent" }],
  channels: [{ id: "c1", name: "Channel" }],
  triggers: [{ id: "t1", name: "Trigger" }],
  providers: [{ id: "p1", name: "Provider" }],
};

describe("resourcesForType", () => {
  it.each([
    ["agent", "a1"],
    ["channel", "c1"],
    ["trigger", "t1"],
    ["provider", "p1"],
  ] as const)("filters %s resources", (type, id) => {
    expect(resourcesForType(type, sources)).toEqual([
      expect.objectContaining({ id, type }),
    ]);
  });

  it("adds and removes resources from a batch selection", () => {
    expect(toggleResourceSelection([], "a1")).toEqual(["a1"]);
    expect(toggleResourceSelection(["a1", "a2"], "a1")).toEqual(["a2"]);
  });
});
