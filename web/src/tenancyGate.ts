import type { TenancySelf } from "./api";

export type TenancyGateState = "loading" | "ready" | "required";

export interface TenancyGateResolution {
  identity: TenancySelf;
  state: TenancyGateState;
}

// Administrators manage the whole selected AgentMux instance and do not need a
// tenant namespace of their own. Tenant-scoped sessions still have to prove
// through /self that their tenant exists and is enabled.
export function resolveTenancyGate(self: TenancySelf | null): TenancyGateState {
  if (!self) return "loading";
  if (self.admin) return "ready";
  return self.tenant_id && self.status !== "disabled" ? "ready" : "required";
}

function retryableIdentityError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  return /(?:\b502\b|\b503\b|\b504\b|failed to fetch|load failed|networkerror)/i.test(message);
}

// resolveTenancyGateWithRetry absorbs the short startup window in which the
// Wails asset server is already rendering but the in-process API is still
// opening PostgreSQL. Authentication failures fail closed immediately; only
// transient gateway/network failures are retried.
export async function resolveTenancyGateWithRetry(
  loadIdentity: () => Promise<TenancySelf>,
  options: { attempts?: number; delayMs?: number; wait?: (ms: number) => Promise<void> } = {},
): Promise<TenancyGateResolution> {
  const attempts = Math.max(1, options.attempts ?? 30);
  const delayMs = Math.max(0, options.delayMs ?? 1000);
  const wait = options.wait ?? ((ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms)));
  let lastError: unknown;

  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const identity = await loadIdentity();
      return { identity, state: resolveTenancyGate(identity) };
    } catch (error) {
      lastError = error;
      if (!retryableIdentityError(error) || attempt === attempts - 1) throw error;
      await wait(delayMs);
    }
  }
  throw lastError;
}
