import type { Tenant, TenancySelf } from "./api";

export type TenancyGateState = "loading" | "ready" | "required";

export interface TenancyGateResolution {
  identity: TenancySelf;
  state: TenancyGateState;
}

// Configuration is available only after AgentMux can prove that at least one
// active tenant exists. A tenant-scoped session proves this through /self;
// an administrator session proves it through the tenant registry.
export function resolveTenancyGate(
  self: TenancySelf | null,
  tenants: Tenant[] | null,
): TenancyGateState {
  if (!self) return "loading";
  if (!self.admin) {
    return self.tenant_id && self.status !== "disabled" ? "ready" : "required";
  }
  if (!tenants) return "loading";
  return tenants.some((tenant) => tenant.status === "active") ? "ready" : "required";
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
  loadTenants: () => Promise<Tenant[] | null>,
  options: { attempts?: number; delayMs?: number; wait?: (ms: number) => Promise<void> } = {},
): Promise<TenancyGateResolution> {
  const attempts = Math.max(1, options.attempts ?? 30);
  const delayMs = Math.max(0, options.delayMs ?? 1000);
  const wait = options.wait ?? ((ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms)));
  let lastError: unknown;

  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const identity = await loadIdentity();
      const tenants = identity.admin ? await loadTenants() : null;
      return { identity, state: resolveTenancyGate(identity, tenants ?? []) };
    } catch (error) {
      lastError = error;
      if (!retryableIdentityError(error) || attempt === attempts - 1) throw error;
      await wait(delayMs);
    }
  }
  throw lastError;
}
