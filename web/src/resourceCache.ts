type Snapshot<T> = { data: T | null; error: string | null; loading: boolean; updatedAt: number; hasValue: boolean; revision: number };
type Entry<T> = {
  snapshot: Snapshot<T>;
  listeners: Set<() => void>;
  pending?: Promise<T | null>;
  generation: number;
};

const entries = new Map<string, Entry<unknown>>();

export function resourceEntry<T>(key: string): Entry<T> {
  let entry = entries.get(key);
  if (!entry) {
    // Retain only a bounded number of inactive machine/tenant snapshots.
    if (entries.size >= 128) {
      for (const [oldKey, old] of entries) {
        if (!old.listeners.size && !old.pending) { entries.delete(oldKey); break; }
      }
    }
    entry = { snapshot: { data: null, error: null, loading: true, updatedAt: 0, hasValue: false, revision: 0 }, listeners: new Set(), generation: 0 };
    entries.set(key, entry);
  }
  return entry as Entry<T>;
}

export function loadResource<T>(entry: Entry<T>, loader: () => Promise<T>): Promise<T | null> {
  if (entry.pending) return entry.pending;
  const generation = ++entry.generation;
  const publish = (snapshot: Snapshot<T>) => {
    entry.snapshot = snapshot;
    entry.listeners.forEach((listener) => listener());
  };
  publish({ ...entry.snapshot, loading: true, error: null });
  let request: Promise<T>;
  try { request = loader(); } catch (error) { request = Promise.reject(error); }
  entry.pending = request.then((data) => {
    if (generation === entry.generation) publish({ ...entry.snapshot, data, error: null, loading: false, updatedAt: Date.now(), hasValue: true });
    return data;
  }, (error: unknown) => {
    if (generation === entry.generation) publish({ ...entry.snapshot, error: String(error), loading: false });
    return null;
  }).finally(() => {
    if (generation === entry.generation) entry.pending = undefined;
  });
  return entry.pending;
}

// Writes can affect related resources (for example, channel edits change Agent
// bindings). Keep the last good data, but require revalidation on the next read.
export function invalidateResourceCache() {
  entries.forEach((entry) => {
    entry.generation += 1;
    entry.pending = undefined;
    entry.snapshot = { ...entry.snapshot, updatedAt: 0, loading: false, revision: entry.snapshot.revision + 1 };
    entry.listeners.forEach((listener) => listener());
  });
}

export function clearResourceCache() { entries.clear(); }
