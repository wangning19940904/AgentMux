import { describe, expect, it } from "vitest";
import { clampSidebarWidth, readSidebarWidth } from "./sidebarSizing";

describe("sidebar sizing", () => {
  it("clamps drag values to the configured range", () => {
    expect(clampSidebarWidth(120, 184, 320)).toBe(184);
    expect(clampSidebarWidth(248.6, 184, 320)).toBe(249);
    expect(clampSidebarWidth(500, 184, 320)).toBe(320);
  });

  it("restores a valid persisted width", () => {
    const storage = { getItem: () => "226" };
    expect(readSidebarWidth(storage, "width", 208, 184, 320)).toBe(226);
  });

  it("falls back when persisted data is invalid", () => {
    expect(readSidebarWidth({ getItem: () => "invalid" }, "width", 208, 184, 320)).toBe(208);
    expect(readSidebarWidth({ getItem: () => null }, "width", 208, 184, 320)).toBe(208);
  });
});
