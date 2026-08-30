export const PRIMARY_SIDEBAR_WIDTH = { default: 208, min: 184, max: 320 } as const;
export const SECONDARY_SIDEBAR_WIDTH = { default: 184, min: 152, max: 300 } as const;

export const PRIMARY_SIDEBAR_STORAGE_KEY = "agentmux:primary-sidebar-width";
export const SECONDARY_SIDEBAR_STORAGE_KEY = "agentmux:secondary-sidebar-width";

export function clampSidebarWidth(value: number, min: number, max: number) {
  if (!Number.isFinite(value)) return min;
  return Math.min(max, Math.max(min, Math.round(value)));
}

export function readSidebarWidth(
  storage: Pick<Storage, "getItem">,
  key: string,
  fallback: number,
  min: number,
  max: number,
) {
  const raw = storage.getItem(key);
  if (raw === null || raw.trim() === "") return fallback;
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? clampSidebarWidth(parsed, min, max) : fallback;
}
