import { describe, expect, it } from "vitest";
import { en } from "./en";
import { zh } from "./zh";

describe("i18n resources", () => {
  it("keeps English and Chinese key sets identical", () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort());
  });

  it("does not ship empty translations", () => {
    for (const [key, value] of [...Object.entries(en), ...Object.entries(zh)]) {
      expect(value.trim(), key).not.toBe("");
    }
  });
});
