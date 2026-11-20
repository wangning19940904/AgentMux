import { afterEach, describe, expect, it, vi } from "vitest";
import type { Framework, FrameworkInstallResult, OperationProgress } from "../../api";

const installFramework = vi.fn();
vi.mock("../../api", () => ({ api: { installFramework: (...args: unknown[]) => installFramework(...args) } }));
vi.mock("./frameworkPageCache", () => ({ forgetUpdateCheck: vi.fn() }));

const store = await import("./frameworkBulkStore");

function framework(kind: string, targetID: string): Framework {
  return {
    spec: { kind, display: kind, kind_type: "cli", supported: true, install_supported: true, install_requires_npm: false, update_supported: true },
    installed: true, registered: true, target_id: targetID,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => {
  installFramework.mockReset();
  store.clearFrameworkBulkRun();
});

describe("framework bulk update store", () => {
  it("keeps the run, its live progress and its results outside the page", async () => {
    const first = deferred<FrameworkInstallResult>();
    let report: (progress: OperationProgress) => void = () => {};
    installFramework
      .mockImplementationOnce((_kind, _action, onProgress) => { report = onProgress; return first.promise; })
      .mockResolvedValueOnce({ kind: "claudecode", action: "update", ok: false, error: "update failed: exit status 1", log: "npm ERR" });

    const done = store.startFrameworkBulkUpdate([
      { item: framework("codex", "local"), label: "Codex · Local machine" },
      { item: framework("claudecode", "home"), label: "Claude Code · home" },
    ], "local", {});
    expect(store.frameworkBulkRunning()).toBe(true);

    report({ phase: "updating", detail: "==> Upgrading codex", percent: 30, log: "==> Upgrading codex\n" });
    // A second "Update all" while one runs (say, from a remounted page) is ignored.
    await store.startFrameworkBulkUpdate([{ item: framework("codex", "local"), label: "again" }], "local", {});
    expect(installFramework).toHaveBeenCalledTimes(1);

    first.resolve({ kind: "codex", action: "update", ok: true, warning: "brew exited 1, codex is now 0.169.0" });
    await done;

    expect(store.frameworkBulkRunning()).toBe(false);
    expect(installFramework.mock.calls.map((call) => [call[0], call[4]])).toEqual([["codex", "local"], ["claudecode", "home"]]);
  });

  it("reports progress counts and exposes the running item's progress", async () => {
    const pending = deferred<FrameworkInstallResult>();
    let seen: ReturnType<typeof store.bulkRunProgress> = null;
    let report: (progress: OperationProgress) => void = () => {};
    installFramework.mockImplementationOnce((_kind, _action, onProgress) => { report = onProgress; return pending.promise; });
    let snapshot: Parameters<typeof store.bulkRunProgress>[0] = null;
    const unsubscribe = subscribeForTest((run) => { snapshot = run; });
    const done = store.startFrameworkBulkUpdate([{ item: framework("codex", "local"), label: "Codex" }], "local", {});
    report({ phase: "updating", detail: "downloading", percent: 30, log: "downloading\n" });
    seen = store.bulkRunProgress(snapshot);
    expect(seen).toEqual({ completed: 0, total: 1 });
    const entry = snapshot!.entries[0];
    expect(entry.running).toBe(true);
    expect(entry.progress?.detail).toBe("downloading");
    expect(entry.progress?.log).toBe("downloading\n");
    pending.resolve({ kind: "codex", action: "update", ok: true });
    await done;
    expect(store.bulkRunProgress(snapshot)).toBeNull();
    expect(snapshot!.entries[0].result).toEqual({ ok: true, error: undefined, log: undefined, warning: undefined });
    store.clearFrameworkBulkRun();
    expect(snapshot).toBeNull();
    unsubscribe();
  });
});

function subscribeForTest(listener: (run: Parameters<typeof store.bulkRunProgress>[0]) => void) {
  return store.subscribeFrameworkBulkRun(listener);
}
