export const FLEET_WARNING_EVENT = "agentmux:fleet-warning";

const warningsByRequest = new Map<string, string[]>();
// The machines each request found offline, so the Console can tell when
// they are back and its data is worth reloading.
const offlineByRequest = new Map<string, string[]>();
const latestRequests = new Map<string, number>();
let revision = 0;

export function fleetWarningMessage(name: string, error: string): string {
  const prefix = `remote ${name}: `;
  const detail = error.startsWith(prefix) ? error.slice(prefix.length) : error;
  return `${name}: ${/context deadline exceeded|i\/o timeout/i.test(detail) ? "request timed out" : detail}`;
}

// An offline machine that connects to this hub itself is answered from what
// it last reported. The data is real but may be out of date, so it is noted
// instead of shown as a failed load. Every list of one machine gives the
// same notice, so a page of several lists shows it once; the Machines page
// has the time of the last report.
const STALE_NOTICE = " is offline; showing the data it last reported";

export function fleetStaleMessage(name: string): string {
  return `${name}${STALE_NOTICE}`;
}

// A stale notice says the data is old, not that a load failed, so the
// Console words its banner differently when notices are all there is.
export function isFleetStaleNotice(message: string): boolean {
  return message.includes(STALE_NOTICE);
}

export function fleetWarningWithContext(message: string, request: { path: string; method?: string }, occurredAt = new Date()): string {
  // Query strings may contain credentials or user input. The endpoint and
  // receipt time are enough to identify the failed load in the Console.
  const endpoint = request.path.split("?", 1)[0];
  return `${message} · ${request.method || "GET"} ${endpoint} · ${occurredAt.toLocaleString()}`;
}

export function fleetWarningResourceKey(path: string): string {
  const [base, query = ""] = path.split("?", 2);
  const params = new URLSearchParams(query);
  // Date/filter changes refresh one read; distinct CLI/provider identities
  // must not clear each other's failures just because the endpoint matches.
  const identity = ["id", "kind", "tool", "provider_id", "channel_id"]
    .filter((key) => params.has(key)).map((key) => [key, params.get(key)]);
  return JSON.stringify([base, identity]);
}

export function currentFleetWarnings(): string[] {
  return [...new Set([...warningsByRequest.values()].flat())];
}

export function currentOfflineTargets(): string[] {
  return [...new Set([...offlineByRequest.values()].flat())];
}

function publish() {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent<string[]>(FLEET_WARNING_EVENT, { detail: currentFleetWarnings() }));
  }
}

// A successful unrelated query must not hide a failed read. Only a newer
// result for the same request can replace it, including an empty recovery.
export function beginFleetWarningUpdate(key: string) {
  const request = ++revision;
  latestRequests.set(key, request);
  return (warnings: string[], offline: string[] = []) => {
    if (latestRequests.get(key) !== request) return;
    if (warnings.length) warningsByRequest.set(key, warnings);
    else warningsByRequest.delete(key);
    if (offline.length) offlineByRequest.set(key, [...new Set(offline)]);
    else offlineByRequest.delete(key);
    publish();
  };
}

export function resetFleetWarnings() {
  latestRequests.clear();
  warningsByRequest.clear();
  offlineByRequest.clear();
  publish();
}
