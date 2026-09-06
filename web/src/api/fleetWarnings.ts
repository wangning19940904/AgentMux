export const FLEET_WARNING_EVENT = "agentmux:fleet-warning";

const warningsByRequest = new Map<string, string[]>();
const latestRequests = new Map<string, number>();
let revision = 0;

export function fleetWarningMessage(name: string, error: string): string {
  const prefix = `remote ${name}: `;
  const detail = error.startsWith(prefix) ? error.slice(prefix.length) : error;
  return `${name}: ${/context deadline exceeded|i\/o timeout/i.test(detail) ? "request timed out" : detail}`;
}

export function fleetWarningResourceKey(path: string): string {
  const [base, query = ""] = path.split("?", 2);
  const params = new URLSearchParams(query);
  // Date/filter changes refresh one read; distinct CLI/provider identities
  // must not clear each other's failures just because the endpoint matches.
  const identity = ["id", "kind", "tool", "provider_id", "channel_id", "tenant_id"]
    .filter((key) => params.has(key)).map((key) => [key, params.get(key)]);
  return JSON.stringify([base, identity]);
}

export function currentFleetWarnings(): string[] {
  return [...new Set([...warningsByRequest.values()].flat())];
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
  return (warnings: string[]) => {
    if (latestRequests.get(key) !== request) return;
    if (warnings.length) warningsByRequest.set(key, warnings);
    else warningsByRequest.delete(key);
    publish();
  };
}

export function resetFleetWarnings() {
  latestRequests.clear();
  warningsByRequest.clear();
  publish();
}
