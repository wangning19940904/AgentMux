import type { AccountIdentity, AuthSession, TenancySelf } from "./api";

export type TenancyGateState = "loading" | "ready" | "required" | "error";

export interface TenancyGateResolution {
  identity: TenancySelf;
  state: TenancyGateState;
}

// The packaged Wails shell is the local administrator surface. Its Go proxy
// still enforces the configured bridge credential server-side, so a transient
// identity request must not permanently strand the native UI behind the
// tenant onboarding navigation lock while PostgreSQL is starting.
export function tenancyNavigationLocked(
  state: TenancyGateState,
  nativeDesktopAdmin: boolean,
): boolean {
  return state !== "ready" && !nativeDesktopAdmin;
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

// Sign-in gate. Accounts exist only once an administrator creates the first
// one; until then the Console opens exactly as before. The packaged desktop
// shell is the machine's own administrator surface and never asks.

export type SignInGateState = "checking" | "signed-out" | "open";

// AUTH_CHANGED_EVENT asks the sign-in gate to look again, after a sign-in,
// sign-out or first-administrator setup.
export const AUTH_CHANGED_EVENT = "agentmux:auth-changed";

export function notifyAuthChanged() {
  window.dispatchEvent(new CustomEvent(AUTH_CHANGED_EVENT));
}

// resolveSignInGate decides whether the Console shows the sign-in screen. A
// failed check opens the shell, whose own identity check reports the error,
// so an older daemon without accounts keeps working.
export function resolveSignInGate(
  session: AuthSession | null,
  options: { nativeDesktopAdmin: boolean; failed: boolean },
): SignInGateState {
  if (options.nativeDesktopAdmin || options.failed) return "open";
  if (!session) return "checking";
  return session.sign_in_required ? "signed-out" : "open";
}

// accountLabel is how the Console names a signed-in person.
export function accountLabel(account: Pick<AccountIdentity, "login" | "display_name"> | undefined | null): string {
  if (!account) return "";
  return account.display_name?.trim() || account.login;
}

export const MIN_PASSWORD_LENGTH = 10;
const LOGIN_PATTERN = /^[a-z0-9][a-z0-9._-]{1,63}$/;

// accountFormError checks a new account form before it is sent and returns
// an i18n key, or "" when the form is fine. An empty password is allowed
// only where the server generates one.
export function accountFormError(
  form: { login: string; password: string; confirm?: string },
  options: { passwordRequired: boolean },
): string {
  const login = form.login.trim().toLowerCase();
  if (!LOGIN_PATTERN.test(login)) return "accounts.loginInvalid";
  if (!form.password && !options.passwordRequired) return "";
  if (form.password.length < MIN_PASSWORD_LENGTH) return "accounts.passwordTooShort";
  if (form.confirm !== undefined && form.confirm !== form.password) return "accounts.passwordMismatch";
  return "";
}

// signInErrorKey turns a failed sign-in into an i18n key.
export function signInErrorKey(error: unknown): string {
  const status = typeof error === "object" && error && "status" in error ? Number((error as { status: unknown }).status) : 0;
  if (status === 401) return "auth.wrongCredentials";
  if (status === 429) return "auth.tooManyAttempts";
  return "auth.failed";
}
