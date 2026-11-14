import { describe, expect, it } from "vitest";
import { latestHealthResult, mergeHealthResults } from "./useFrameworkHealth";

const result = (model: string, minute: number, ok = true) => ({
  kind: "codex", model, ok, latency_ms: 100, checked_at: `2026-11-14T03:${String(minute).padStart(2, "0")}:00Z`,
});

describe("framework health results", () => {
  it("keeps the newest result per machine, framework and model", () => {
    let merged = mergeHealthResults({}, undefined, [result("", 5), result("gpt-6", 3, false)]);
    merged = mergeHealthResults(merged, undefined, [result("", 1, false)]);
    merged = mergeHealthResults(merged, "h1", [result("", 9)]);
    expect(merged["local::codex"][""].ok).toBe(true);
    expect(merged["local::codex"]["gpt-6"].ok).toBe(false);
    expect(Object.keys(merged)).toEqual(["local::codex", "h1::codex"]);
    expect(latestHealthResult(merged["local::codex"])?.model).toBe("");
    expect(latestHealthResult(undefined)).toBeUndefined();
  });
});
