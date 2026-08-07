// Wails desktop bridge: native directory picker, login-item management and
// external URL opening with browser fallbacks.
import type { LaunchAtLoginStatus } from "./types";

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
