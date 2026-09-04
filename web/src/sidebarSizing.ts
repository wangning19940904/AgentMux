// The two navigation levels add up to Multica's 247px reference sidebar.
export const PRIMARY_SIDEBAR_WIDTH = { default: 64, min: 60, max: 84 } as const;
export const SECONDARY_SIDEBAR_WIDTH = { default: 183, min: 164, max: 240 } as const;

export const PRIMARY_SIDEBAR_STORAGE_KEY = "agentmux:primary-sidebar-width-v3";
export const SECONDARY_SIDEBAR_STORAGE_KEY = "agentmux:secondary-sidebar-width-v2";

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
