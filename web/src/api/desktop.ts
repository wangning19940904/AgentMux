// Wails desktop bridge: native directory picker, login-item management and
// external URL opening with browser fallbacks.
import type { DesktopUpdateStatus, LaunchAtLoginStatus } from "./types";

const LATEST_RELEASE_API = "https://api.github.com/repos/wangning19940904/AgentMux/releases/latest";

export async function selectSystemDirectory(defaultDirectory = ""): Promise<{ path: string }> {
  const picker = window.go?.main?.App?.SelectDirectory;
  if (!picker) {
    throw new Error("desktop directory picker unavailable");
  }
  const path = await picker(defaultDirectory);
  return { path };
}

export function isDesktopApp(): boolean {
  return (
    window.location.hostname.toLowerCase() === "wails.localhost" ||
    Boolean(window.go?.main?.App)
  );
}

export async function getLaunchAtLogin(): Promise<LaunchAtLoginStatus> {
  const getStatus = window.go?.main?.App?.GetLaunchAtLogin;
  if (!getStatus) {
    return { supported: false, enabled: false };
  }
  return getStatus();
}

export async function setLaunchAtLogin(enabled: boolean): Promise<LaunchAtLoginStatus> {
  const update = window.go?.main?.App?.SetLaunchAtLogin;
  if (!update) {
    throw new Error("launch at login is unavailable");
  }
  return update(enabled);
}

export async function openLocalWebUI(): Promise<void> {
  const open = window.go?.main?.App?.OpenLocalWebUI;
  if (!open) {
    throw new Error("local Web UI is unavailable");
  }
  await open();
}

export async function openExternalURL(url: string): Promise<void> {
  const open = window.go?.main?.App?.OpenExternalURL;
  if (open) {
    await open(url);
    return;
  }
  window.open(url, "_blank", "noopener,noreferrer");
}

export async function checkDesktopUpdate(currentVersion: string): Promise<DesktopUpdateStatus> {
  const check = window.go?.main?.App?.CheckDesktopUpdate;
  if (check) return check();

  const response = await fetch(LATEST_RELEASE_API, {
    cache: "no-store",
    headers: { Accept: "application/vnd.github+json" },
  });
  if (!response.ok) throw new Error(`update service: ${response.status}`);
  const release = await response.json() as {
    tag_name?: string;
    html_url?: string;
    published_at?: string;
  };
  const latest = (release.tag_name ?? "").replace(/^v/, "");
  if (!latest) throw new Error("update service returned no version");
  return {
    supported: false,
    current_version: currentVersion.replace(/^v/, ""),
    latest_version: latest,
    update_available: versionLess(currentVersion, latest),
    release_url: release.html_url,
    published_at: release.published_at,
  };
}

export async function installDesktopUpdate(): Promise<DesktopUpdateStatus> {
  const install = window.go?.main?.App?.InstallDesktopUpdate;
  if (!install) throw new Error("automatic updates are available in the packaged desktop app");
  return install();
}

function versionLess(current: string, latest: string) {
  const parse = (value: string) => value.replace(/^v/, "").split("-", 1)[0].split(".").map(Number);
  const left = parse(current);
  const right = parse(latest);
  if (right.length !== 3 || right.some((value) => !Number.isInteger(value))) return false;
  if (left.length !== 3 || left.some((value) => !Number.isInteger(value))) return true;
  for (let index = 0; index < 3; index += 1) {
    if (left[index] !== right[index]) return left[index] < right[index];
  }
  return false;
}
