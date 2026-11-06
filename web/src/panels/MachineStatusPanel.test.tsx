// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { FleetBatchResult, RemoteHost, SystemMetrics } from "../api";
import { I18nProvider } from "../i18n";
import { MachineStatusPanel } from "./MachineStatusPanel";

const GiB = 1024 ** 3;
let root: Root;
let container: HTMLDivElement;

function metrics(overrides: Partial<SystemMetrics>): SystemMetrics {
  return {
    supported: true,
    host: { hostname: "box", os: "linux", platform: "Debian GNU/Linux 12 (bookworm)", platform_id: "debian", arch: "amd64", cpu_cores: 16 },
    collected_at: "2026-11-04T10:00:00Z",
    interval_seconds: 5,
    cpu: { percent: 15, load: { one: 32.4, five: 30, fifteen: 28 } },
    memory: { total: 13.5 * GiB, used: 8.2 * GiB, percent: 61 },
    disks: [{ mount: "/", total: 100 * GiB, used: 95 * GiB, free: 5 * GiB, percent: 95 }],
    network: { rx_bytes_per_sec: 17.6 * 1024 * 1024, tx_bytes_per_sec: 2.08 * 1024 * 1024 },
    history: [{ t: 1, cpu: 12, mem: 61, rx: 100, tx: 50 }, { t: 2, cpu: 15, mem: 61, rx: 200, tx: 40 }],
    agentmux_version: "0.1.20",
    sessions: { needs_input: 0, working: 0, idle: 1 },
    ...overrides,
  };
}

const batch: FleetBatchResult<SystemMetrics> = {
  targets: [
    { target: { id: "home", name: "Home Linux", kind: "ssh", trusted: true, online: true }, responses: [{ key: "metrics", status: 200, ok: true, data: metrics({}), duration_ms: 40 }] },
    {
      target: { id: "local", name: "Local machine", kind: "local", trusted: true, online: true },
      responses: [{
        key: "metrics", status: 200, ok: true, duration_ms: 3,
        data: metrics({
          host: { hostname: "mac", os: "darwin", platform: "macOS 15.6.1", platform_id: "macos", arch: "arm64", cpu_cores: 14 },
          cpu: { percent: 14, load: { one: 8.6, five: 7, fifteen: 6 } },
          memory: { total: 48 * GiB, used: 40.1 * GiB, percent: 83.5 },
          disks: [{ mount: "/System/Volumes/Data", total: 1000 * GiB, used: 400 * GiB, free: 600 * GiB, percent: 40 }],
          sessions: { needs_input: 1, working: 0, idle: 2 },
        }),
      }],
    },
    { target: { id: "gone", name: "Dev box", kind: "ssh", trusted: true, online: false }, responses: [{ key: "metrics", status: 502, ok: false, error: "ssh: handshake timed out", duration_ms: 5000 }] },
  ],
};

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

async function settle() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

const hosts: RemoteHost[] = [
  { id: "home", name: "Home Linux", host: "10.0.0.2", port: 22, user: "me", remote_addr: "127.0.0.1:8765", trusted: true },
  { id: "gone", name: "Dev box", host: "10.0.0.3", port: 22, user: "me", remote_addr: "127.0.0.1:8765", trusted: true },
  // Untrusted hosts are excluded from "all", exactly like the fleet API.
  { id: "new", name: "Unverified", host: "10.0.0.4", port: 22, user: "me", remote_addr: "127.0.0.1:8765", trusted: false },
];

function batchFor(targetID: string): FleetBatchResult<SystemMetrics> {
  return { targets: batch.targets.filter((item) => item.target.id === targetID) };
}

it("renders each machine with its alerts, sessions and offline state", async () => {
  vi.spyOn(api, "remoteHosts").mockResolvedValue(hosts);
  const machineStatus = vi.spyOn(api, "machineStatus").mockImplementation(async (ids) => batchFor(ids![0]));
  await act(async () => root.render(<I18nProvider language="zh"><MachineStatusPanel /></I18nProvider>));
  await settle();

  expect(machineStatus.mock.calls.map(([ids]) => ids)).toEqual([["local"], ["home"], ["gone"]]);
  const text = container.textContent ?? "";
  expect(text).toContain("2/3 台在线");
  expect(text).toContain("2 台用量偏高");
  expect(text).toContain("1 个会话需要你");

  const cards = [...container.querySelectorAll(".machine-status-card")];
  expect(cards).toHaveLength(3);
  const [local, home, gone] = cards;
  expect(home.classList.contains("critical")).toBe(true);
  expect(home.textContent).toContain("Home Linux");
  expect(home.textContent).toContain("磁盘快满");
  expect(home.textContent).toContain("负载高");
  expect(home.textContent).toContain("17.6 MB/s");
  expect(home.textContent).toContain("8.20 GB / 13.5 GB");
  expect(home.querySelectorAll(".machine-status-sparkline path.spark-line")).not.toHaveLength(0);

  expect(local.textContent).toContain("本机");
  expect(local.classList.contains("warning")).toBe(true);
  expect(local.textContent).toContain("内存紧张");
  // The macOS data volume reads as the system disk, without its mount path.
  expect(local.textContent).not.toContain("/System/Volumes/Data");
  expect(local.querySelector(".machine-status-session.needs-input.active")?.textContent).toContain("1");

  expect(gone.textContent).toContain("离线");
  expect(gone.textContent).toContain("ssh: handshake timed out");
  expect(gone.querySelector('a[href="#machines"]')).not.toBeNull();
});

it("polls machines independently so a hung host neither blocks nor stacks", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  vi.spyOn(api, "remoteHosts").mockResolvedValue(hosts);
  let releaseGone!: (value: FleetBatchResult<SystemMetrics>) => void;
  const machineStatus = vi.spyOn(api, "machineStatus").mockImplementation((ids) => (
    ids![0] === "gone"
      ? new Promise((done) => { releaseGone = done; })
      : Promise.resolve(batchFor(ids![0]))
  ));
  const callsFor = (id: string) => machineStatus.mock.calls.filter(([ids]) => ids![0] === id).length;
  await act(async () => root.render(<I18nProvider language="en"><MachineStatusPanel /></I18nProvider>));
  await settle();

  const cards = () => [...container.querySelectorAll(".machine-status-card")];
  expect(cards()[0].textContent).toContain("This machine");
  expect(cards()[1].textContent).toContain("17.6 MB/s");
  expect(cards()[2].textContent).toContain("Connecting");

  for (let tick = 0; tick < 3; tick++) {
    await act(async () => { vi.advanceTimersByTime(5_000); });
    await settle();
  }
  expect(callsFor("local")).toBe(4);
  expect(callsFor("gone")).toBe(1);

  await act(async () => releaseGone(batchFor("gone")));
  await settle();
  expect(cards()[2].textContent).toContain("Offline");
  await act(async () => { vi.advanceTimersByTime(5_000); });
  await settle();
  expect(callsFor("gone")).toBe(2);
});
