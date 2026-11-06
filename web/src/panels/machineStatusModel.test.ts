import { describe, expect, it } from "vitest";
import type { FleetBatchResult, MachineTarget, RemoteHost, SystemMetrics } from "../api/types";
import {
  downloadCounterStalled,
  formatBytes,
  formatLoad,
  formatPercent,
  formatRate,
  formatUptime,
  isSystemDisk,
  machineAlerts,
  machineStatuses,
  machineStatusTargets,
  sparklinePaths,
  summarizeFleet,
} from "./machineStatusModel";

const GiB = 1024 ** 3;

function metrics(overrides: Partial<SystemMetrics> = {}): SystemMetrics {
  return {
    supported: true,
    host: { hostname: "box", os: "linux", platform: "Debian GNU/Linux 12 (bookworm)", arch: "amd64", cpu_cores: 16 },
    collected_at: "2026-11-04T10:00:00Z",
    interval_seconds: 5,
    cpu: { percent: 15, load: { one: 2, five: 2, fifteen: 2 } },
    memory: { total: 13.5 * GiB, used: 8.2 * GiB, percent: 61 },
    disks: [{ mount: "/", total: 100 * GiB, used: 40 * GiB, free: 60 * GiB, percent: 40 }],
    network: { rx_bytes_per_sec: 0, tx_bytes_per_sec: 0 },
    history: [],
    agentmux_version: "0.1.0",
    sessions: { needs_input: 0, working: 0, idle: 1 },
    ...overrides,
  };
}

function target(id: string, name = id): MachineTarget {
  return { id, name, kind: id === "local" ? "local" : "ssh", trusted: true, online: true };
}

describe("machineAlerts", () => {
  it("flags the reference home Linux box: full disk and high load", () => {
    const alerts = machineAlerts(metrics({
      cpu: { percent: 15, load: { one: 32.4, five: 30, fifteen: 28 } },
      disks: [
        { mount: "/", total: 1, used: 1, free: 0, percent: 71 },
        { mount: "/data", total: 1, used: 1, free: 0, percent: 96.5 },
        { mount: "/home", total: 1, used: 1, free: 0, percent: 91 },
      ],
    }));
    expect(alerts).toEqual([
      { kind: "disk", severity: "critical", value: 96.5, mount: "/data" },
      { kind: "load", severity: "critical", value: 32.4 / 16 },
    ]);
  });

  it("warns about memory pressure without flagging a busy-but-healthy load", () => {
    const alerts = machineAlerts(metrics({
      host: { hostname: "mac", os: "darwin", platform: "macOS 15.6.1", arch: "arm64", cpu_cores: 14 },
      cpu: { percent: 14, load: { one: 8.6, five: 7, fifteen: 6 } },
      memory: { total: 48 * GiB, used: 40.1 * GiB, percent: 83.5 },
    }));
    expect(alerts).toEqual([{ kind: "memory", severity: "warning", value: 83.5 }]);
  });

  it("averages recent CPU so one spike does not alert", () => {
    const history = [10, 12, 11, 9, 10, 100].map((cpu, index) => ({ t: index, cpu }));
    expect(machineAlerts(metrics({ history, cpu: { percent: 100 } }))).toEqual([]);
    const busy = [96, 97, 99, 98, 100, 96].map((cpu, index) => ({ t: index, cpu }));
    expect(machineAlerts(metrics({ history: busy, cpu: { percent: 96 } }))[0]).toMatchObject({ kind: "cpu", severity: "critical" });
  });
});

describe("machineStatuses", () => {
  it("classifies reachable, outdated, unsupported and offline machines", () => {
    const batch: FleetBatchResult<unknown> = {
      targets: [
        { target: target("local", "Local machine"), responses: [{ key: "metrics", status: 200, ok: true, data: metrics(), duration_ms: 3 }] },
        {
          target: target("home"),
          responses: [{
            key: "metrics", status: 200, ok: true, duration_ms: 40,
            data: metrics({ disks: [{ mount: "/", total: 1, used: 1, free: 0, percent: 95 }], sessions: { needs_input: 2, working: 1, idle: 0 } }),
          }],
        },
        { target: target("old"), responses: [{ key: "metrics", status: 404, ok: false, error: "404 page not found", duration_ms: 5 }] },
        { target: target("win"), responses: [{ key: "metrics", status: 200, ok: true, data: metrics({ supported: false }), duration_ms: 5 }] },
        { target: target("gone"), responses: [{ key: "metrics", status: 502, ok: false, error: "ssh: connect timed out", duration_ms: 5000 }] },
      ],
    };
    const statuses = machineStatuses(batch);
    expect(statuses.map((status) => status.state)).toEqual(["online", "online", "outdated", "unsupported", "offline"]);
    expect(statuses[1].severity).toBe("critical");
    expect(statuses[4].error).toBe("ssh: connect timed out");
    expect(summarizeFleet(statuses)).toEqual({ total: 5, reachable: 4, attention: 1, critical: 1, needsInput: 2 });
  });
});

describe("formatting", () => {
  it("uses three significant digits like the reference", () => {
    expect(formatBytes(8.2 * GiB)).toBe("8.20 GB");
    expect(formatBytes(13.5 * GiB)).toBe("13.5 GB");
    expect(formatBytes(252 * GiB)).toBe("252 GB");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(undefined)).toBe("—");
    expect(formatRate(17.6 * 1024 * 1024)).toBe("17.6 MB/s");
    expect(formatRate(243 * 1024)).toBe("243 KB/s");
    expect(formatRate(16.9 * 1024)).toBe("16.9 KB/s");
    expect(formatRate(0.2)).toBe("0 B/s");
    // 1000-1023 KB rolls over instead of printing four digits.
    expect(formatRate(1010 * 1024)).toBe("0.99 MB/s");
  });

  it("formats percentages, load and uptime", () => {
    expect(formatPercent(2.9)).toBe("2.9%");
    expect(formatPercent(61.2)).toBe("61%");
    expect(formatPercent(0)).toBe("0%");
    expect(formatLoad(32.4)).toBe("32.4");
    expect(formatLoad(2.56)).toBe("2.56");
    expect(formatUptime(0)).toBeNull();
    // Booted at t=1000s; now is 26h05m later.
    expect(formatUptime(1_000, 1_000_000 + (26 * 60 + 5) * 60_000)).toEqual({ days: 1, hours: 2, minutes: 5 });
  });
});

describe("sparklinePaths", () => {
  it("right-aligns a short history and breaks the line at gaps", () => {
    const { line, area } = sparklinePaths([0, 50, undefined, 100], 100, 21, { max: 100, slots: 5 });
    // A lone point after a gap becomes a 1px tick so it stays visible.
    expect(line).toBe("M25.0 20.5 L50.0 10.5 M99.0 0.5 L100.0 0.5");
    expect(area.split("Z").filter(Boolean)).toHaveLength(2);
  });

  it("scales to the series peak when no max is given", () => {
    const { line } = sparklinePaths([0, 10], 10, 11);
    expect(line).toBe("M0.0 10.5 L10.0 0.5");
    expect(sparklinePaths([], 10, 10).line).toBe("");
  });
});

describe("machineStatusTargets", () => {
  const hosts: RemoteHost[] = [
    { id: "a", name: "Home", host: "h", port: 22, user: "u", remote_addr: "x", trusted: true },
    { id: "b", name: "", host: "dev.example", port: 22, user: "u", remote_addr: "x", trusted: false },
  ];

  it("follows the fleet scope rules without contacting any machine", () => {
    expect(machineStatusTargets("all", hosts).map((target) => target.id)).toEqual(["local", "a"]);
    expect(machineStatusTargets("local", hosts).map((target) => target.id)).toEqual(["local"]);
    // A single untrusted machine is still shown so its error is visible.
    expect(machineStatusTargets("b", hosts)).toEqual([{ id: "b", name: "dev.example", kind: "ssh", trusted: false, online: false }]);
    expect(machineStatusTargets("missing", hosts)[0]).toMatchObject({ id: "missing", kind: "ssh" });
  });
});

describe("network and disk helpers", () => {
  it("recognises a received-bytes counter that never moves", () => {
    const stuck = [1, 2, 3].map((t) => ({ t, rx: 0, tx: 40_000 }));
    expect(downloadCounterStalled(stuck)).toBe(true);
    expect(downloadCounterStalled(stuck.slice(0, 2))).toBe(false);
    expect(downloadCounterStalled(stuck.map((point) => ({ ...point, tx: 200 })))).toBe(false);
    expect(downloadCounterStalled([...stuck, { t: 4, rx: 10, tx: 40_000 }])).toBe(false);
  });

  it("treats the macOS data volume as the system disk", () => {
    expect(isSystemDisk("/")).toBe(true);
    expect(isSystemDisk("/System/Volumes/Data")).toBe(true);
    expect(isSystemDisk("/data")).toBe(false);
  });
});
