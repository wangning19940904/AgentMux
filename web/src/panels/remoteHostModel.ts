import type { FleetMachine, FleetView, RemoteHost } from "../api";

export type RemoteHealthState = "checking" | "healthy" | "error" | "unverified" | "offline";

export type RemoteHostSnapshot = {
  health: RemoteHealthState;
  version?: string;
  error?: string;
};

type ParsedVersion = {
  core: [number, number, number];
  channel: "stable" | "dev";
  devBuild?: [number, number];
};

function parseSupportedVersion(value?: string): ParsedVersion | null {
  const match = value?.trim().match(/^v?(\d+)\.(\d+)\.(\d+)(?:-(.+))?$/);
  if (!match) return null;
  const core = match.slice(1, 4).map(Number) as [number, number, number];
  if (core.some((part) => !Number.isSafeInteger(part))) return null;
  if (!match[4]) return { core, channel: "stable" };

  const dev = match[4].match(/^dev\.(\d{8})\.(\d+)$/);
  if (!dev) return null;
  const devBuild = [Number(dev[1]), Number(dev[2])] as [number, number];
  if (devBuild.some((part) => !Number.isSafeInteger(part))) return null;
  return { core, channel: "dev", devBuild };
}

function compareTuple(left: number[], right: number[]): number {
  for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
    const difference = (left[index] ?? 0) - (right[index] ?? 0);
    if (difference !== 0) return difference;
  }
  return 0;
}

export function isRemoteUpdateAvailable(remoteVersion?: string, localVersion?: string): boolean {
  const remote = parseSupportedVersion(remoteVersion);
  const local = parseSupportedVersion(localVersion);
  if (!remote || !local) return false;

  const coreComparison = compareTuple(remote.core, local.core);
  if (coreComparison !== 0) return coreComparison < 0;
  if (remote.channel !== local.channel) {
    return remote.channel === "dev" && local.channel === "stable";
  }
  if (remote.channel === "stable") return false;
  return compareTuple(remote.devBuild ?? [], local.devBuild ?? []) < 0;
}

export function remoteUpdateCandidates(
  hosts: RemoteHost[],
  snapshots: Record<string, RemoteHostSnapshot>,
  localVersion?: string,
): RemoteHost[] {
  return hosts.filter((host) => {
    const snapshot = snapshots[host.id];
    // A linked machine has no SSH connection to push an update through.
    return Boolean(
      host.kind !== "link" &&
      host.trusted &&
      snapshot?.health === "healthy" &&
      isRemoteUpdateAvailable(snapshot.version, localVersion),
    );
  });
}

// LinkPresence is whether a linked machine is connected to the hub, from the
// hub's own list when this machine follows a hub, or from this hub's
// connections when it is the hub.
export type LinkPresence = {
  online: boolean;
  lastSeenAt?: string;
  version?: string;
  viaHub: boolean;
};

export function linkPresence(host: RemoteHost, fleet: FleetView | null): LinkPresence | undefined {
  if (host.kind !== "link") return undefined;
  if (fleet?.source === "hub") {
    const machine = fleet.machines.find((item) => item.id === host.id);
    if (machine) {
      return { online: machine.online, lastSeenAt: machine.last_seen_at, version: machine.version, viaHub: true };
    }
  }
  if (host.online === undefined) return undefined;
  return { online: host.online, lastSeenAt: host.last_seen_at, version: host.version, viaHub: false };
}

// fleetOnlyMachines are the machines the hub knows that this machine does
// not list itself, such as the other nodes seen from a node.
export function fleetOnlyMachines(hosts: RemoteHost[], fleet: FleetView | null): FleetMachine[] {
  if (fleet?.source !== "hub") return [];
  const known = new Set(hosts.map((host) => host.id));
  return fleet.machines.filter((machine) => !known.has(machine.id));
}
