import { describe, expect, it } from "vitest";
import type { RemoteHost } from "../api";
import { fleetOnlyMachines, isRemoteUpdateAvailable, linkPresence, remoteUpdateCandidates } from "./remoteHostModel";
import type { FleetView } from "../api";

const host = (id: string, trusted = true): RemoteHost => ({
  id,
  name: id,
  host: `${id}.example.test`,
  port: 22,
  user: "root",
  remote_addr: "127.0.0.1:8765",
  trusted,
});

describe("remote AgentMux version comparison", () => {
  it("recognizes newer semantic and development builds", () => {
    expect(isRemoteUpdateAvailable("0.1.3", "0.1.4")).toBe(true);
    expect(isRemoteUpdateAvailable("0.1.4-dev.20260830.3", "0.1.4-dev.20260830.4")).toBe(true);
    expect(isRemoteUpdateAvailable("v0.1.4-dev.20260829.9", "v0.1.4-dev.20260830.1")).toBe(true);
  });

  it("does not reinstall equal versions or downgrade newer remotes", () => {
    expect(isRemoteUpdateAvailable("0.1.4", "0.1.4")).toBe(false);
    expect(isRemoteUpdateAvailable("0.1.5", "0.1.4")).toBe(false);
    expect(isRemoteUpdateAvailable("0.1.4-dev.20260830.5", "0.1.4-dev.20260830.4")).toBe(false);
  });

  it("orders stable releases above same-core development builds", () => {
    expect(isRemoteUpdateAvailable("0.1.4-dev.20260830.4", "0.1.4")).toBe(true);
    expect(isRemoteUpdateAvailable("0.1.4", "0.1.4-dev.20260830.4")).toBe(false);
  });

  it("ignores missing and unsupported versions", () => {
    expect(isRemoteUpdateAvailable("", "0.1.4")).toBe(false);
    expect(isRemoteUpdateAvailable("development", "0.1.4")).toBe(false);
    expect(isRemoteUpdateAvailable("0.1.4-rc.1", "0.1.4")).toBe(false);
  });
});

describe("remote update candidates", () => {
  it("selects only trusted, healthy, outdated SSH machines", () => {
    const hosts = [host("outdated"), host("current"), host("offline"), host("untrusted", false)];
    expect(remoteUpdateCandidates(hosts, {
      outdated: { health: "healthy", version: "0.1.3" },
      current: { health: "healthy", version: "0.1.4" },
      offline: { health: "error", version: "0.1.3" },
      untrusted: { health: "healthy", version: "0.1.3" },
    }, "0.1.4").map((item) => item.id)).toEqual(["outdated"]);
  });

  it("leaves out a linked machine, which has no SSH connection to update through", () => {
    const linked = { ...host("linked"), kind: "link" as const };
    expect(remoteUpdateCandidates([linked], { linked: { health: "healthy", version: "0.1.3" } }, "0.1.4")).toEqual([]);
  });
});

describe("fleet presence", () => {
  const linked = (id: string, online?: boolean): RemoteHost => ({ ...host(id), kind: "link", online });
  const fleet: FleetView = {
    source: "hub",
    fetched_at: "2026-11-15T01:00:00Z",
    machines: [
      { id: "node", name: "node", online: true, version: "0.1.0" },
      { id: "gone", name: "gone", online: false, last_seen_at: "2026-11-14T01:00:00Z" },
      { id: "hub", name: "primary", online: true, hub_role: "hub" },
    ],
  };

  it("takes a linked machine's presence from the hub when this machine follows one", () => {
    expect(linkPresence(linked("node", false), fleet)).toMatchObject({ online: true, viaHub: true });
    expect(linkPresence(linked("gone", false), fleet)).toMatchObject({ online: false, lastSeenAt: "2026-11-14T01:00:00Z" });
  });

  it("uses this hub's own connections otherwise, and nothing for SSH machines", () => {
    expect(linkPresence(linked("node", false), { ...fleet, source: "local" })).toMatchObject({ online: false, viaHub: false });
    expect(linkPresence(host("ssh"), fleet)).toBeUndefined();
  });

  it("lists the machines only the hub knows", () => {
    expect(fleetOnlyMachines([linked("node")], fleet).map((machine) => machine.id)).toEqual(["gone", "hub"]);
    expect(fleetOnlyMachines([], { ...fleet, source: "local" })).toEqual([]);
  });
});
