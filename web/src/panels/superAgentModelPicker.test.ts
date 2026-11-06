import { describe, expect, it } from "vitest";
import { placePopover } from "./superAgentModelPicker";

describe("placePopover", () => {
  const window = { top: 0, bottom: 800 };

  it("opens below when the content fits there", () => {
    expect(placePopover({ top: 100, bottom: 132 }, window, 300)).toEqual({ side: "bottom", maxHeight: 420 });
  });

  it("opens above when the content does not fit below and there is more room above", () => {
    // 800 - 12 margin - 640 - 6 gap leaves 142 below; 608 - 6 - 12 leaves 590 above.
    expect(placePopover({ top: 608, bottom: 640 }, window, 400)).toEqual({ side: "top", maxHeight: 420 });
  });

  it("shrinks to the room left when neither side fits", () => {
    expect(placePopover({ top: 280, bottom: 312 }, { top: 0, bottom: 620 }, 500)).toEqual({ side: "bottom", maxHeight: 290 });
    expect(placePopover({ top: 410, bottom: 442 }, { top: 0, bottom: 620 }, 500)).toEqual({ side: "top", maxHeight: 392 });
  });

  it("stays below when it fits nowhere and below has more room", () => {
    expect(placePopover({ top: 200, bottom: 232 }, { top: 0, bottom: 450 }, 500).side).toBe("bottom");
  });

  it("measures from the clipping container, not the window", () => {
    expect(placePopover({ top: 500, bottom: 532 }, { top: 400, bottom: 700 }, 300)).toEqual({ side: "bottom", maxHeight: 150 });
  });
});
