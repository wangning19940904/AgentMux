// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { clearResourceCache, invalidateResourceCache } from "./resourceCache";
import { useCachedResource } from "./useCachedResource";

afterEach(() => { clearResourceCache(); localStorage.clear(); vi.useRealTimers(); vi.unstubAllGlobals(); });

it("reuses fresh data, keeps stale data during refresh/failure, and isolates machine and tenant caches", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-22T00:00:00Z"));
  let finish!: (value: string) => void;
  const loader = vi.fn().mockResolvedValue("original");
  const container = document.createElement("div");
  const root = createRoot(container);
  function Panel() {
    const resource = useCachedResource<string>("agents", loader);
    return <><output>{resource.data ?? "empty"}</output><span>{resource.error}</span><button onClick={() => void resource.reload()}>refresh</button></>;
  }
  const mount = () => act(async () => { root.render(<StrictMode><Panel /></StrictMode>); });
  const unmount = () => act(async () => { root.render(null); });
  try {
    await mount();
    expect(loader).toHaveBeenCalledTimes(1);
    await unmount(); await mount();
    expect(loader).toHaveBeenCalledTimes(1);
    await unmount();
    vi.setSystemTime(Date.now() + 31_000);
    loader.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await mount();
    expect(container.querySelector("output")!.textContent).toBe("original");
    expect(loader).toHaveBeenCalledTimes(2);
    await act(async () => { finish("updated"); });
    loader.mockRejectedValueOnce(new Error("offline"));
    await act(async () => container.querySelector("button")!.click());
    expect(container.textContent).toContain("updated");
    expect(container.textContent).toContain("offline");
    await act(async () => { invalidateResourceCache(); });
    expect(loader).toHaveBeenCalledTimes(4);
    await unmount();
    localStorage.setItem("agentmux:active-remote", "another-machine");
    loader.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await mount();
    expect(container.querySelector("output")!.textContent).toBe("empty");
    await act(async () => { finish("other-machine"); });
    await unmount();
    localStorage.setItem("agentmux:active-tenant-scope", "another-machine::tenant-1");
    loader.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await mount();
    expect(container.querySelector("output")!.textContent).toBe("empty");
    await act(async () => { finish("tenant-data"); });
    expect(loader).toHaveBeenCalledTimes(6);
  } finally { await act(async () => root.unmount()); }
});

it("does not let an in-flight read overwrite a write's revalidated result", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  let finishOld!: (value: string) => void;
  const loader = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve; })).mockResolvedValue("after-write");
  const container = document.createElement("div");
  const root = createRoot(container);
  function Panel() { return <output>{useCachedResource<string>("agents", loader).data ?? "empty"}</output>; }
  try {
    await act(async () => root.render(<Panel />));
    await act(async () => invalidateResourceCache());
    expect(container.textContent).toBe("after-write");
    await act(async () => finishOld("before-write"));
    expect(container.textContent).toBe("after-write");
  } finally { await act(async () => root.unmount()); }
});
