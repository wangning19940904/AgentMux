import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { baseMachineScope, withBaseMachineScope } from "./api/client";
import { loadResource, resourceEntry } from "./resourceCache";

export function useCachedResource<T>(name: string, loader: () => Promise<T>, staleTimeMs = 30_000) {
  // A page's lists follow the whole fleet even while one of its views talks to
  // a single machine.
  const key = JSON.stringify([baseMachineScope(), name]);
  const entry = resourceEntry<T>(key);
  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const subscribe = useCallback((listener: () => void) => {
    entry.listeners.add(listener);
    return () => { entry.listeners.delete(listener); };
  }, [entry]);
  const snapshot = useSyncExternalStore(subscribe, () => entry.snapshot);
  const reload = useCallback(() => loadResource(entry, () => withBaseMachineScope(loaderRef.current)), [entry]);
  useEffect(() => {
    if (entry.snapshot.error || !entry.snapshot.updatedAt || Date.now() - entry.snapshot.updatedAt >= staleTimeMs) void reload();
  }, [entry, reload, staleTimeMs, snapshot.updatedAt, snapshot.revision]);
  return { ...snapshot, reload };
}
