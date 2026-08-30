import { describe, expect, it } from "vitest";

import { formatUsageCost } from "./currency";

describe("formatUsageCost", () => {
  it("uses the plain dollar sign without a US prefix", () => {
    expect(formatUsageCost(500, "usd", 7, "zh")).toBe("$500.00");
    expect(formatUsageCost(500, "usd", 7, "en")).toBe("$500.00");
    expect(formatUsageCost(1234.5, "usd", 7, "zh")).toBe("$1,234.50");
  });

  it("keeps the yuan symbol when converting to CNY", () => {
    expect(formatUsageCost(500, "cny", 7, "zh")).toBe("¥3,500.00");
  });
});
