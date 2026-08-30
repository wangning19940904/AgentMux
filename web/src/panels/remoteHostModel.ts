import type { RemoteHost } from "../api";

export type RemoteHealthState = "checking" | "healthy" | "error" | "unverified";

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
    return Boolean(
      host.trusted &&
      snapshot?.health === "healthy" &&
      isRemoteUpdateAvailable(snapshot.version, localVersion),
    );
  });
}
