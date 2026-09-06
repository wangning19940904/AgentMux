import { describe, expect, it, vi } from "vitest";
import { runBulkUpdates } from "./bulkUpdateModel";

describe("bulk updates", () => {
  it("runs serially and reports progress only when each update finishes", async () => {
    let releaseFirst!: () => void;
    const first = new Promise<void>((resolve) => { releaseFirst = resolve; });
    const update = vi.fn(async (item: string) => {
      if (item === "first") await first;
      return { ok: true, log: item };
    });
    const onResult = vi.fn();
    const pending = runBulkUpdates(["first", "second"], update, onResult);
    expect(update.mock.calls).toEqual([["first"]]);
    expect(onResult).not.toHaveBeenCalled();
    releaseFirst();
    expect(await pending).toEqual([
      { item: "first", result: { ok: true, log: "first" } },
      { item: "second", result: { ok: true, log: "second" } },
    ]);
    expect(onResult.mock.calls.map(([, completed]) => completed)).toEqual([1, 2]);
  });

  it("continues after API failures and exceptions, preserving every result", async () => {
    const update = vi.fn()
      .mockResolvedValueOnce({ ok: false, error: "permission denied", log: "install log" })
      .mockRejectedValueOnce(new Error("offline"))
      .mockRejectedValueOnce("timeout")
      .mockResolvedValueOnce({ ok: true });
    const progress = vi.fn();
    const results = await runBulkUpdates([1, 2, 3, 4], update, progress);
    expect(results.map((entry) => entry.result)).toEqual([
      { ok: false, error: "permission denied", log: "install log" },
      { ok: false, error: "offline" },
      { ok: false, error: "timeout" },
      { ok: true },
    ]);
    expect(progress).toHaveBeenCalledTimes(4);
  });

  it("does nothing when there are no updates", async () => {
    const update = vi.fn();
    const progress = vi.fn();
    expect(await runBulkUpdates([], update, progress)).toEqual([]);
    expect(update).not.toHaveBeenCalled();
    expect(progress).not.toHaveBeenCalled();
  });
});
