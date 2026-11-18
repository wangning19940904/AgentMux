import { activeMachineScope, api } from "../../api";
import type { FrameworkAuthStatus, FrameworkUpdateCheck, FrameworksResponse } from "../../api";
import { prefetchCachedResource, useCachedResource } from "../../useCachedResource";

// The Agent frameworks page reads a list from every machine, then an update
// check and a sign-in status per framework and machine. These caches let the
// page show the last known values at once when it is opened again, while it
// refreshes them in the background.

const FRAMEWORKS_RESOURCE = "frameworks";
// Short: opening the page always refreshes the list in the background (the
// server caches CLI versions, so this is cheap); it only avoids asking twice
// right after a prefetch.
const FRAMEWORKS_STALE_MS = 10_000;
export const UPDATE_CHECK_TTL_MS = 10 * 60_000;
export const AUTH_STATUS_TTL_MS = 5 * 60_000;

export function useFrameworksResource() {
  return useCachedResource<FrameworksResponse>(FRAMEWORKS_RESOURCE, () => api.frameworks(), FRAMEWORKS_STALE_MS);
}

export function prefetchFrameworks() {
  prefetchCachedResource<FrameworksResponse>(FRAMEWORKS_RESOURCE, () => api.frameworks(), FRAMEWORKS_STALE_MS);
}

type Timed<T> = { value: T; at: number };

// Keys are targetKey values; they are scoped by the machine selection because
// "local" means a different machine when the Console follows a remote one.
function scoped(key: string) {
  return `${activeMachineScope()}|${key}`;
}

function freshEntries<T>(store: Map<string, Timed<T>>, ttlMs: number, now = Date.now()): Record<string, T> {
  const prefix = scoped("");
  const fresh: Record<string, T> = {};
  for (const [key, entry] of store) {
    if (!key.startsWith(prefix)) continue;
    if (now - entry.at >= ttlMs) {
      store.delete(key);
      continue;
    }
    fresh[key.slice(prefix.length)] = entry.value;
  }
  return fresh;
}

const updateChecks = new Map<string, Timed<FrameworkUpdateCheck>>();

export function cachedUpdateChecks(now = Date.now()) {
  return freshEntries(updateChecks, UPDATE_CHECK_TTL_MS, now);
}

// Failed checks are not kept: the next visit retries them.
export function rememberUpdateCheck(key: string, check: FrameworkUpdateCheck, now = Date.now()) {
  if (check.error) updateChecks.delete(scoped(key));
  else updateChecks.set(scoped(key), { value: check, at: now });
}

export function forgetUpdateCheck(key: string) {
  updateChecks.delete(scoped(key));
}

export function forgetAllUpdateChecks() {
  const prefix = scoped("");
  for (const key of [...updateChecks.keys()]) if (key.startsWith(prefix)) updateChecks.delete(key);
}

const authStatuses = new Map<string, Timed<FrameworkAuthStatus>>();

export function cachedAuthStatuses(now = Date.now()) {
  return freshEntries(authStatuses, AUTH_STATUS_TTL_MS, now);
}

export function rememberAuthStatus(key: string, status: FrameworkAuthStatus, now = Date.now()) {
  if (status.state === "unknown") authStatuses.delete(scoped(key));
  else authStatuses.set(scoped(key), { value: status, at: now });
}

export function forgetAuthStatus(key: string) {
  authStatuses.delete(scoped(key));
}

// For tests: forget everything remembered between visits.
export function resetFrameworkPageCache() {
  updateChecks.clear();
  authStatuses.clear();
}
