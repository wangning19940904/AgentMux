import { describe, expect, it } from "vitest";
import { providerModelHealthRows } from "./providerModelHealth";

describe("providerModelHealthRows", () => {
  it("adds health state to selectable models and prioritizes failures", () => {
    expect(
      providerModelHealthRows(
        ["model-a", "model-b"],
        [
          { model: "model-a", state: "healthy", message: "OK", checked_at: "2026-08-30T00:00:00Z" },
          { model: "model-b", state: "unavailable", status_code: 504, message: "timeout", checked_at: "2026-08-30T00:00:00Z" },
        ],
      ),
    ).toEqual([
      { model: "model-b", state: "unhealthy", statusCode: 504, message: "timeout", offline: false },
      { model: "model-a", state: "healthy", statusCode: undefined, message: "OK", offline: false },
    ]);
  });

  it("keeps an auto-offlined model visible with its error", () => {
    expect(
      providerModelHealthRows(
        ["model-a"],
        [
          { model: "model-a", state: "healthy", message: "OK", checked_at: "2026-08-30T00:00:00Z" },
          { model: "model-b", state: "unavailable", status_code: 404, message: "not found", checked_at: "2026-08-30T00:00:00Z" },
        ],
      ),
    ).toEqual([
      { model: "model-b", state: "unhealthy", statusCode: 404, message: "not found", offline: true },
      { model: "model-a", state: "healthy", statusCode: undefined, message: "OK", offline: false },
    ]);
  });

  it("marks models unknown before the first health check", () => {
    expect(providerModelHealthRows(["model-a"])).toEqual([
      { model: "model-a", state: "unknown", statusCode: undefined, message: "", offline: false },
    ]);
  });
});
