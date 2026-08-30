import { describe, expect, it } from "vitest";
import type { RemoteHost } from "../api";
import { isRemoteUpdateAvailable, remoteUpdateCandidates } from "./remoteHostModel";

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
});
