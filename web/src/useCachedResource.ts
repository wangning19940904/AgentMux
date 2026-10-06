import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { activeMachineScope, activeTenantScopeKey } from "./api/client";
import { loadResource, resourceEntry } from "./resourceCache";

export function useCachedResource<T>(name: string, loader: () => Promise<T>, staleTimeMs = 30_000) {
  const key = JSON.stringify([activeMachineScope(), activeTenantScopeKey(), name]);
  const entry = resourceEntry<T>(key);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const subscribe = useCallback((listener: () => void) => {
    entry.listeners.add(listener);
    return () => { entry.listeners.delete(listener); };
  }, [entry]);
  const snapshot = useSyncExternalStore(subscribe, () => entry.snapshot);
  const reload = useCallback(() => loadResource(entry, loaderRef.current), [entry]);
  useEffect(() => {
    if (entry.snapshot.error || !entry.snapshot.updatedAt || Date.now() - entry.snapshot.updatedAt >= staleTimeMs) void reload();
  }, [entry, reload, staleTimeMs, snapshot.updatedAt, snapshot.revision]);
  return { ...snapshot, reload };
}
