// @vitest-environment jsdom
import { act, StrictMode, Suspense, useCallback, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { RegisteredPanel } from "./panelRegistry";

// Hold the actual lazy module behind a gate to reproduce a cold route load.
const gate = vi.hoisted(() => {
  let release!: () => void;
  const ready = new Promise<void>((resolve) => { release = resolve; });
  return { ready, release };
});
vi.mock("./panels/AgentsPanel", async () => {
  await gate.ready;
  return await import("./panels/agents/AgentsPanel");
});
vi.mock("./api", () => ({
  api: {
    agentInstances: async () => [], agents: async () => ["codex"],
    providers: async () => [], activeRoutes: async () => [],
    channels: async () => [], triggers: async () => [],
    mcp: async () => [], skills: async () => [], tools: async () => ({ cli: [] }),
  },
}));
vi.mock("./panels/agents/AgentForm", () => ({ AgentForm: () => <div>agent form</div> }));

let root: Root;
let container: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

it("retains a create request across lazy loading, consumes it, and accepts another request", async () => {
  const onHandled = vi.fn();
  function Harness() {
    const [pending, setPending] = useState(false);
    const [showAgents, setShowAgents] = useState(false);
    const handled = useCallback(() => { onHandled(); setPending(false); }, []);
    return <>
      <button id="new" onClick={() => { setPending(true); setShowAgents(true); }}>New agent</button>
      <button id="navigate" onClick={() => setShowAgents((value) => !value)}>Navigate</button>
      <output>{pending ? "pending" : "handled"}</output>
      <Suspense fallback={<p>loading panel</p>}>
        {showAgents && <RegisteredPanel tab="agents" createRequested={pending} onCreateRequestHandled={handled} />}
      </Suspense>
    </>;
  }
  const click = async (selector: string) => {
    const button = container.querySelector<HTMLButtonElement>(selector);
    expect(button).not.toBeNull();
    await act(async () => button!.click());
  };
  await act(async () => root.render(<StrictMode><Harness /></StrictMode>));
  await click("#new");
  expect(container.textContent).toContain("loading panel");
  expect(container.querySelector("output")?.textContent).toBe("pending");
  await act(async () => {
    gate.release();
    await import("./panels/AgentsPanel");
  });
  expect(container.querySelector('[role="dialog"]')).not.toBeNull();
  expect(container.querySelector("output")?.textContent).toBe("handled");
  expect(onHandled).toHaveBeenCalledOnce();
  await click(".provider-drawer-backdrop");
  expect(container.querySelector('[role="dialog"]')).toBeNull();
  await click("#navigate");
  await click("#navigate");
  expect(container.querySelector('[role="dialog"]')).toBeNull();
  await click("#new");
  expect(container.querySelector('[role="dialog"]')).not.toBeNull();
  expect(onHandled).toHaveBeenCalledTimes(2);
});
