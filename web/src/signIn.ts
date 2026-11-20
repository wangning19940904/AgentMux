import type { AccountIdentity, AuthSession, IdentifyResult, SavedAccount } from "./api";

// Sign-in and identity helpers. Nobody uses the Console without signing in:
// a browser signs in to an account, or creates the first administrator where
// none exists yet. The packaged desktop app, and the Console of a node
// linked to a hub, are their machine's own app: the machine is signed in to
// one account, and they show nothing but sign-in until it is. The CLI and
// the bridge token stay the machine's administrator.

// "device-sign-in" is the desktop app of a machine that is not signed in.
export type SignInGateState = "checking" | "signed-out" | "device-sign-in" | "open";

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
  if (options.failed) return "open";
  if (!session) return "checking";
  if (options.nativeDesktopAdmin) {
    // A daemon from before device sign-in reports no device: open as before.
    return session.device && !session.device.signed_in ? "device-sign-in" : "open";
  }
  // A node's own Console signs its machine in with the hub's account.
  if (session.device?.authority === "hub") return session.device.signed_in ? "open" : "device-sign-in";
  if (session.sign_in_required) return "signed-out";
  // No account exists yet: the first administrator is created before use.
  if (!session.accounts_enabled && !session.account) return "signed-out";
  return "open";
}

// ConsoleRole is what the Console renders for. An administrator manages
// everything; a member reaches only their own devices, direct messages and
// password.
export type ConsoleRole = "admin" | "member";

// consoleRole reads the role from /api/v1/auth/session. Only a signed-in,
// non-administrator account is a member; anything else (no accounts yet, the
// desktop shell) is the machine's administrator, and the server still
// enforces what each request may do.
export function consoleRole(session: AuthSession | null, nativeDesktopAdmin = false): ConsoleRole {
  if (nativeDesktopAdmin && !session) return "admin";
  if (session && session.authenticated && !session.admin && session.account) return "member";
  return "admin";
}

function retryableIdentityError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  const status = typeof error === "object" && error && "status" in error ? Number((error as { status: unknown }).status) : 0;
  if ([502, 503, 504].includes(status)) return true;
  return /(?:\b502\b|\b503\b|\b504\b|failed to fetch|load failed|networkerror)/i.test(message);
}

// loadSessionWithRetry absorbs the short startup window in which the Wails
// asset server is already rendering but the in-process API is still opening
// its database. Authentication failures fail closed immediately; only
// transient gateway/network failures are retried.
export async function loadSessionWithRetry(
  loadSession: () => Promise<AuthSession>,
  options: { attempts?: number; delayMs?: number; wait?: (ms: number) => Promise<void> } = {},
): Promise<AuthSession> {
  const attempts = Math.max(1, options.attempts ?? 30);
  const delayMs = Math.max(0, options.delayMs ?? 1000);
  const wait = options.wait ?? ((ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms)));
  let lastError: unknown;

  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      return await loadSession();
    } catch (error) {
      lastError = error;
      if (!retryableIdentityError(error) || attempt === attempts - 1) throw error;
      await wait(delayMs);
    }
  }
  throw lastError;
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

function errorStatus(error: unknown): number {
  return typeof error === "object" && error && "status" in error ? Number((error as { status: unknown }).status) : 0;
}

// accountErrorCode reads the machine-readable code an account endpoint sends
// with an error ("pending", "registered", "authority_unreachable", …).
export function accountErrorCode(error: unknown): string {
  if (typeof error !== "object" || !error || !("body" in error)) return "";
  const body = (error as { body: unknown }).body;
  if (typeof body !== "object" || !body || !("code" in body)) return "";
  const code = (body as { code: unknown }).code;
  return typeof code === "string" ? code : "";
}

// signInErrorKey turns a failed sign-in, registration or setup into an
// i18n key.
export function signInErrorKey(error: unknown): string {
  const status = errorStatus(error);
  const code = accountErrorCode(error);
  if (code === "pending") return "auth.pendingApproval";
  if (code === "registered") return "auth.loginTaken";
  if (code === "accounts_exist") return "auth.accountsExist";
  if (code === "setup_required") return "auth.registrationClosed";
  if (code === "authority_unreachable") return "auth.authorityUnreachable";
  if (code === "not_hub_account") return "auth.notHubAccount";
  if (code === "approval_denied") return "auth.approvalDenied";
  if (code === "approval_expired") return "auth.approvalExpired";
  if (status === 401) return "auth.wrongCredentials";
  if (status === 429) return "auth.tooManyAttempts";
  return "auth.failed";
}

// The sign-in screen asks for the login name first, then goes on to the
// step that fits it, as Persona's sign-in does.
//
//   identify  → the login name
//   password  → a registered name signs in
//   register  → an unknown name asks for an account
//   pending   → a registration is waiting for an administrator
//   closed    → no account exists yet; only the administrator can start
//   setup     → no account exists yet, and this machine creates the first
//               administrator (the desktop app of a hub)
//   choose    → pick one of the accounts saved on this machine or browser
//   create    → a new account, login name included, before identify decides
//               whether it is a registration or the first administrator
//   hub       → a node: sign in with its hub's account, or leave the hub
//   approval  → a node waits for a hub to approve its sign-in
//   leave     → a node leaves its hub for an account of its own
export type SignInStep =
  | "identify" | "password" | "register" | "pending" | "closed" | "setup" | "choose" | "create"
  | "hub" | "approval" | "leave";

export function nextSignInStep(
  result: Pick<IdentifyResult, "state" | "registration_open"> & { setup_required?: boolean },
  options: { setupAllowed?: boolean } = {},
): SignInStep {
  if (result.state === "registered") return "password";
  if (result.state === "pending") return "pending";
  if (result.registration_open) return "register";
  return result.setup_required && options.setupAllowed ? "setup" : "closed";
}

// identifyUnsupported: a daemon from before identify answers 404 or 405, and
// the screen then asks for the password straight away, as it used to.
export function identifyUnsupported(error: unknown): boolean {
  const status = errorStatus(error);
  return status === 404 || status === 405;
}

// Saved accounts. The desktop app's machine keeps its list itself
// (device_saved_accounts.go); a browser keeps its own in local storage. Only
// the login and display name are kept, never a password.
const BROWSER_SAVED_ACCOUNTS_KEY = "agentmux.signIn.savedAccounts";
const MAX_SAVED_ACCOUNTS = 8;

function readStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function cleanSavedAccounts(value: unknown): SavedAccount[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    if (!entry || typeof entry !== "object" || typeof (entry as SavedAccount).login !== "string") return [];
    const { login, display_name, last_signed_in_at } = entry as SavedAccount;
    return [{
      login,
      display_name: typeof display_name === "string" && display_name ? display_name : undefined,
      last_signed_in_at: typeof last_signed_in_at === "string" ? last_signed_in_at : undefined,
    }];
  });
}

export function loadBrowserSavedAccounts(): SavedAccount[] {
  try {
    return cleanSavedAccounts(JSON.parse(readStorage()?.getItem(BROWSER_SAVED_ACCOUNTS_KEY) ?? "[]"));
  } catch {
    return [];
  }
}

function writeBrowserSavedAccounts(accounts: SavedAccount[]) {
  try {
    readStorage()?.setItem(BROWSER_SAVED_ACCOUNTS_KEY, JSON.stringify(accounts));
  } catch {
    // Storage may be full or blocked: the browser just won't remember.
  }
}

// rememberSavedAccount puts the account that just signed in first.
export function rememberSavedAccount(
  accounts: SavedAccount[],
  account: Pick<AccountIdentity, "login" | "display_name">,
  now = new Date(),
): SavedAccount[] {
  const entry: SavedAccount = { login: account.login, display_name: account.display_name || undefined, last_signed_in_at: now.toISOString() };
  return [entry, ...accounts.filter((saved) => saved.login !== account.login)].slice(0, MAX_SAVED_ACCOUNTS);
}

export function rememberBrowserAccount(account: Pick<AccountIdentity, "login" | "display_name">) {
  writeBrowserSavedAccounts(rememberSavedAccount(loadBrowserSavedAccounts(), account));
}

export function forgetBrowserAccount(login: string) {
  writeBrowserSavedAccounts(loadBrowserSavedAccounts().filter((saved) => saved.login !== login));
}

// accountInitials are the one or two letters an account's avatar shows.
export function accountInitials(account: Pick<AccountIdentity, "login" | "display_name">): string {
  const label = accountLabel(account).trim();
  const words = label.split(/[\s._-]+/).filter(Boolean);
  // Two Latin words ("Ning Wang", "ning.wang") give two letters; anything
  // else, a CJK name included, its first character.
  if (words.length >= 2 && /^[a-z]/i.test(words[0]) && /^[a-z]/i.test(words[1])) {
    return (words[0][0] + words[1][0]).toUpperCase();
  }
  return (Array.from(label)[0] ?? "?").toUpperCase();
}

// accountHue gives each login a steady avatar color.
export function accountHue(login: string): number {
  let hash = 0;
  for (const char of login) hash = (hash * 31 + char.codePointAt(0)!) >>> 0;
  return hash % 360;
}
