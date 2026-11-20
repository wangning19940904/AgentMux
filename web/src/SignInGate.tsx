import {
  AlertCircle, AlertTriangle, ArrowLeft, ArrowRight, ChevronRight, Clock, Eye, EyeOff, HardDrive, Info, KeyRound, Laptop,
  Loader2, LogIn, Network, ShieldCheck, Unlink, UserPlus, UserRound, UsersRound, X,
} from "lucide-react";
import { Fragment, useCallback, useEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode } from "react";
import { api, type AccountIdentity, type AuthSession, type DeviceAccount, type HubAccount, type SavedAccount } from "./api";
import { useI18n } from "./i18n";
import { relativeTime } from "./panels/messagesModel";
import {
  accountErrorCode,
  accountFormError,
  accountHue,
  accountInitials,
  accountLabel,
  AUTH_CHANGED_EVENT,
  forgetBrowserAccount,
  identifyUnsupported,
  loadBrowserSavedAccounts,
  loadSessionWithRetry,
  MIN_PASSWORD_LENGTH,
  nextSignInStep,
  notifyAuthChanged,
  rememberBrowserAccount,
  resolveSignInGate,
  signInErrorKey,
  type SignInStep,
} from "./signIn";

// SignInGate shows the sign-in screen instead of the Console when accounts
// exist and this browser has no credential of its own. With no accounts it
// renders the Console straight away, exactly as before accounts existed.
// The desktop app is its machine's own app: it shows sign-in, or the first
// administrator's setup, until the machine is signed in to an account.
export function SignInGate({ children }: { children: ReactNode }) {
  const nativeDesktopAdmin = window.location.protocol === "wails:";
  const [session, setSession] = useState<AuthSession | null>(null);
  const [failed, setFailed] = useState(false);
  // A new identity remounts the Console so nothing cached for the previous
  // person survives.
  const [generation, setGeneration] = useState(0);

  const lastAccountID = useRef<string | null>(null);

  const check = useCallback(async () => {
    try {
      // The desktop app renders before its in-process API is up (and on first
      // launch, before PostgreSQL is installed): wait for it rather than
      // opening the Console unchecked.
      const next = nativeDesktopAdmin
        ? await loadSessionWithRetry(() => api.authSession(), { attempts: 1200 })
        : await api.authSession();
      const accountID = next.account?.id ?? "";
      if (lastAccountID.current !== null && lastAccountID.current !== accountID) {
        setGeneration((value) => value + 1);
      }
      lastAccountID.current = accountID;
      setSession(next);
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, [nativeDesktopAdmin]);

  useEffect(() => {
    void check();
    // Look again when the tab returns, so a session that ended elsewhere
    // (signed out, disabled, password reset) shows the sign-in screen.
    const onVisible = () => {
      if (document.visibilityState === "visible") void check();
    };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener(AUTH_CHANGED_EVENT, check);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener(AUTH_CHANGED_EVENT, check);
    };
  }, [check]);

  const state = resolveSignInGate(session, { nativeDesktopAdmin, failed });
  if (state === "checking") return <SignInChecking />;
  if (state === "signed-out") return <SignInScreen setupAvailable={!session!.accounts_enabled && session!.setup_available} />;
  if (state === "device-sign-in") return <SignInScreen device={session!.device} />;
  return <Fragment key={generation}>{children}</Fragment>;
}

function SignInFrame({ children, busy }: { children: ReactNode; busy?: boolean }) {
  const desktop = window.location.protocol === "wails:";
  return (
    <div className={`sign-in-screen${desktop ? " sign-in-desktop" : ""}`}>
      <div className="sign-in-column">
        <div className="sign-in-brand">
          <img src="/agentmux-logo.png" alt="" aria-hidden="true" />
          <span>AgentMux</span>
        </div>
        <section className="sign-in-card" aria-busy={busy || undefined}>
          {children}
        </section>
      </div>
    </div>
  );
}

function SignInChecking() {
  return (
    <SignInFrame busy>
      <div className="sign-in-checking"><Loader2 size={18} className="sign-in-spin" aria-hidden="true" /></div>
    </SignInFrame>
  );
}

// The calls a sign-in screen makes: a browser's own, or the machine's when
// the desktop app signs its machine in.
type AccountForm = { login: string; display_name?: string; password: string };
const browserCalls = {
  identify: (login: string) => api.identify(login),
  register: (payload: AccountForm) => api.register(payload),
  signIn: (login: string, password: string) => api.signIn(login, password),
  setup: (payload: AccountForm) => api.setupFirstAdmin(payload),
};
const deviceCalls = {
  identify: (login: string) => api.deviceIdentify(login),
  register: (payload: AccountForm) => api.deviceRegister(payload),
  signIn: (login: string, password: string) => api.deviceSignIn(login, password),
  setup: (payload: AccountForm) => api.deviceSetup(payload),
};

function AccountAvatar({ account, size = 34 }: { account: SavedAccount; size?: number }) {
  return (
    <span
      className="sign-in-avatar"
      aria-hidden="true"
      style={{ width: size, height: size, fontSize: Math.round(size * 0.4), "--avatar-hue": accountHue(account.login) } as CSSProperties}
    >
      {accountInitials(account)}
    </span>
  );
}

function PasswordField({ label, autoComplete, value, onChange, autoFocus }: {
  label: string;
  autoComplete: string;
  value: string;
  onChange: (value: string) => void;
  autoFocus?: boolean;
}) {
  const { t } = useI18n();
  const [shown, setShown] = useState(false);
  return (
    <label className="sign-in-field">
      <span>{label}</span>
      <span className="sign-in-password">
        <input autoFocus={autoFocus} type={shown ? "text" : "password"} autoComplete={autoComplete}
          value={value} onChange={(event) => onChange(event.target.value)} />
        <button type="button" className="sign-in-reveal" aria-pressed={shown}
          aria-label={t(shown ? "auth.hidePassword" : "auth.showPassword")}
          title={t(shown ? "auth.hidePassword" : "auth.showPassword")}
          onClick={() => setShown((value) => !value)}>
          {shown ? <EyeOff size={14} /> : <Eye size={14} />}
        </button>
      </span>
    </label>
  );
}

// SignInScreen offers the accounts saved on this machine (or, in a browser,
// in this browser) first: picking one asks only for its password. Otherwise
// it asks for the login name and checks whether it is registered: a
// registered name goes on to its password, an unknown one to registration,
// which an administrator approves. "Create account" starts there directly.
// With device set it signs this machine in, and where no account exists yet
// and the machine may, it creates the first administrator instead.
export function SignInScreen({ device, setupAvailable = false }: { device?: DeviceAccount; setupAvailable?: boolean } = {}) {
  const { t, language } = useI18n();
  const calls = device ? deviceCalls : browserCalls;
  // node: a machine linked to a hub, which signs in with the hub's account
  // or leaves the hub.
  const node = device?.authority === "hub";
  const hubAccounts: HubAccount[] = device?.hub_accounts ?? [];
  const setupAllowed = device ? device.setup_allowed : setupAvailable;
  const [saved, setSaved] = useState<SavedAccount[]>(() => (node ? [] : device ? device.saved_accounts ?? [] : loadBrowserSavedAccounts()));
  const entry: SignInStep = node ? "hub" : setupAvailable ? "create" : saved.length ? "choose" : "identify";
  const [step, setStep] = useState<SignInStep>(entry);
  const [login, setLogin] = useState("");
  // chosen: the saved account being signed in to, for its name and avatar.
  const [chosen, setChosen] = useState<SavedAccount | null>(null);
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [approvalID, setApprovalID] = useState("");
  const [leaveConfirmed, setLeaveConfirmed] = useState(false);

  // While a hub decides, ask every two seconds whether it has.
  useEffect(() => {
    if (step !== "approval" || !approvalID) return;
    let stopped = false;
    const timer = window.setInterval(async () => {
      try {
        const result = await api.deviceApprovalStatus(approvalID);
        if (!stopped && result.account) {
          window.clearInterval(timer);
          notifyAuthChanged();
        }
      } catch (failure) {
        if (stopped) return;
        window.clearInterval(timer);
        setApprovalID("");
        setStep("hub");
        setError(t(signInErrorKey(failure)));
      }
    }, 2000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [step, approvalID, t]);

  function goTo(next: SignInStep) {
    setStep(next);
    setPassword("");
    setConfirm("");
    setError("");
  }

  function startOver(next: SignInStep = entry) {
    setNotice("");
    setChosen(null);
    if (next === "identify" || next === "create") setLogin("");
    goTo(next);
  }

  async function run(action: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (failure) {
      const code = accountErrorCode(failure);
      if (code === "registered" && step === "leave") {
        setError(t("auth.leaveNameTaken"));
      } else if (code === "pending") {
        goTo("pending");
      } else if (code === "registered") {
        goTo("password");
        setNotice(t("auth.loginTaken"));
      } else {
        setError(t(signInErrorKey(failure)));
      }
    } finally {
      setBusy(false);
    }
  }

  // signedIn finishes a sign-in: a browser remembers the account (the
  // machine's own list is kept by its AgentMux), and the gate looks again.
  function signedIn(account?: AccountIdentity) {
    if (!device && account) rememberBrowserAccount(account);
    setPassword("");
    setConfirm("");
    notifyAuthChanged();
  }

  // useHubAccount signs in with the hub's account: by password, or by
  // asking a hub signed in as it to approve.
  function signInWithHubAccount(entry: HubAccount, how: "password" | "approval") {
    const account: SavedAccount = { login: entry.account.login, display_name: entry.account.display_name };
    setChosen(account);
    setLogin(account.login);
    setNotice("");
    if (how === "password") {
      goTo("password");
      return;
    }
    void run(async () => {
      const result = await api.deviceRequestApproval(account.login);
      setApprovalID(result.approval.id);
      setStep("approval");
    });
  }

  function leaveHub(event: FormEvent) {
    event.preventDefault();
    const name = login.trim().toLowerCase();
    const problem = accountFormError({ login: name, password, confirm }, { passwordRequired: true });
    if (problem) {
      setError(t(problem, { min: String(MIN_PASSWORD_LENGTH) }));
      return;
    }
    if (!leaveConfirmed) return;
    void run(async () => {
      await api.deviceLeaveHub({ login: name, display_name: displayName.trim() || undefined, password });
      signedIn();
    });
  }

  function choose(account: SavedAccount) {
    setChosen(account);
    setLogin(account.login);
    setNotice("");
    goTo("password");
  }

  async function forget(account: SavedAccount) {
    const rest = saved.filter((entry) => entry.login !== account.login);
    setSaved(rest);
    if (!rest.length) goTo("identify");
    if (device) {
      try {
        await api.deviceForget(account.login);
      } catch {
        // An older daemon cannot forget: the account shows again next time.
      }
    } else {
      forgetBrowserAccount(account.login);
    }
  }

  function identify(event: FormEvent) {
    event.preventDefault();
    const name = login.trim().toLowerCase();
    const problem = accountFormError({ login: name, password: "" }, { passwordRequired: false });
    if (problem) {
      setError(t(problem));
      return;
    }
    setNotice("");
    void run(async () => {
      try {
        const result = await calls.identify(name);
        setLogin(result.login || name);
        setChosen(saved.find((entry) => entry.login === (result.login || name)) ?? null);
        goTo(nextSignInStep(result, { setupAllowed }));
      } catch (failure) {
        if (!identifyUnsupported(failure)) throw failure;
        setLogin(name);
        goTo("password");
      }
    });
  }

  function signIn(event: FormEvent) {
    event.preventDefault();
    if (!password) return;
    void run(async () => {
      const result = await calls.signIn(login, password);
      signedIn(result?.account);
    });
  }

  // register asks for an account, or in the setup step creates the first
  // administrator and signs in with it. In the create step the login name
  // is new too: identify decides which of the two it is.
  function register(event: FormEvent) {
    event.preventDefault();
    const name = login.trim().toLowerCase();
    const problem = accountFormError({ login: name, password, confirm }, { passwordRequired: true });
    if (problem) {
      setError(t(problem, { min: String(MIN_PASSWORD_LENGTH) }));
      return;
    }
    const payload = { login: name, display_name: displayName.trim() || undefined, password };
    void run(async () => {
      let kind: SignInStep = step;
      if (step === "create") {
        setLogin(name);
        try {
          const result = await calls.identify(name);
          kind = nextSignInStep(result, { setupAllowed });
        } catch (failure) {
          if (!identifyUnsupported(failure)) throw failure;
          kind = "register";
        }
        if (kind === "password") {
          goTo("password");
          setNotice(t("auth.loginTaken"));
          return;
        }
        if (kind === "pending" || kind === "closed") {
          goTo(kind);
          return;
        }
      }
      if (kind === "setup") {
        const result = await calls.setup(payload);
        signedIn(result?.account);
        return;
      }
      await calls.register(payload);
      goTo("pending");
    });
  }

  const shownAccount: SavedAccount = chosen ?? { login };
  const who = (
    <div className="sign-in-who">
      <AccountAvatar account={shownAccount} size={32} />
      <span className="sign-in-who-text">
        <strong>{accountLabel(shownAccount)}</strong>
        {shownAccount.display_name && <small>{shownAccount.login}</small>}
      </span>
      <button className="sign-in-link" type="button" disabled={busy} onClick={() => startOver()}>
        {t(node || saved.length ? "auth.switchAccount" : "auth.otherLogin")}
      </button>
    </div>
  );
  const alert = error ? <p className="sign-in-message sign-in-error" role="alert"><AlertCircle size={14} aria-hidden="true" />{error}</p> : null;
  const status = notice ? <p className="sign-in-message" role="status"><Info size={14} aria-hidden="true" />{notice}</p> : null;
  const where = t(device ? "auth.savedOnMachine" : "auth.savedInBrowser");
  const hubMachine = hubAccounts.find((entry) => entry.account.login === login)?.machine || "hub";

  function lastUsed(account: SavedAccount): string {
    const relative = relativeTime(account.last_signed_in_at);
    if (!relative) return "";
    const time = "date" in relative
      ? relative.date.toLocaleDateString(language === "zh" ? "zh-CN" : "en", { month: "short", day: "numeric" })
      : t(relative.key, { count: String(relative.count ?? "") });
    return t("auth.savedLastUsed", { time });
  }

  let title = t(device ? "auth.deviceTitle" : "auth.title");
  let subtitle = t(device ? "auth.deviceSubtitle" : "auth.identifySubtitle");
  let icon: ReactNode = <LogIn size={18} />;
  let body: ReactNode;
  let footer: ReactNode = null;
  let onSubmit: ((event: FormEvent) => void) | undefined;
  switch (step) {
    case "hub":
      title = t("auth.nodeTitle");
      subtitle = t("auth.nodeSubtitle");
      icon = <Network size={18} />;
      body = (
        <>
          {alert}
          {device?.hub_unreachable ? (
            <p className="sign-in-message sign-in-error" role="alert"><AlertCircle size={14} aria-hidden="true" />{t("auth.hubUnreachable")}</p>
          ) : hubAccounts.length === 0 ? (
            <p className="sign-in-message" role="status"><Info size={14} aria-hidden="true" />{t("auth.hubNotSignedIn")}</p>
          ) : null}
          {hubAccounts.length > 0 && (
            <ul className="sign-in-accounts sign-in-hub-accounts" aria-label={t("auth.hubAccountsTitle")}>
              {hubAccounts.map((entry) => {
                const account: SavedAccount = { login: entry.account.login, display_name: entry.account.display_name };
                return (
                  <li key={entry.account.id}>
                    <div className="sign-in-hub-account">
                      <AccountAvatar account={account} />
                      <span className="sign-in-account-text">
                        <strong>{accountLabel(account)}</strong>
                        <small>{t("auth.hubAccountOn", { login: account.login, machine: entry.machine || "hub" })}</small>
                      </span>
                    </div>
                    <div className="sign-in-hub-actions">
                      <button type="button" className="action" disabled={busy} onClick={() => signInWithHubAccount(entry, "password")}>
                        <KeyRound size={14} /> {t("auth.usePassword")}
                      </button>
                      <button type="button" className="ghost-action" disabled={busy} onClick={() => signInWithHubAccount(entry, "approval")}>
                        {busy ? <Loader2 size={14} className="sign-in-spin" /> : <ShieldCheck size={14} />} {t("auth.askHub")}
                      </button>
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
          <div className="sign-in-divider"><span>{t("auth.or")}</span></div>
          <button type="button" className="ghost-action sign-in-secondary" disabled={busy}
            onClick={() => { startOver("leave"); setLeaveConfirmed(false); }}>
            <Unlink size={14} /> {t("auth.useOther")}
          </button>
          <p className="sign-in-hint"><Info size={14} aria-hidden="true" /><span>{t("auth.otherAccountLeavesHub")}</span></p>
        </>
      );
      break;
    case "approval":
      title = t("auth.approvalTitle");
      subtitle = t("auth.approvalSubtitle", { machine: hubMachine, name: accountLabel(shownAccount) });
      icon = <ShieldCheck size={18} />;
      body = (
        <>
          {who}
          <div className="sign-in-waiting" role="status">
            <Loader2 size={18} className="sign-in-spin" aria-hidden="true" />
            <span>{t("auth.approvalWaiting")}</span>
          </div>
          <button type="button" className="ghost-action sign-in-secondary" onClick={() => { setApprovalID(""); startOver(); }}>
            <ArrowLeft size={14} /> {t("auth.cancel")}
          </button>
        </>
      );
      break;
    case "leave":
      title = t("auth.leaveTitle");
      subtitle = t("auth.leaveSubtitle");
      icon = <Unlink size={18} />;
      onSubmit = leaveHub;
      body = (
        <>
          <div className="sign-in-warning" role="note">
            <AlertTriangle size={15} aria-hidden="true" />
            <span>
              <strong>{t("auth.leaveWarningTitle")}</strong>
              <small>{t("auth.leaveWarning")}</small>
            </span>
          </div>
          {alert}
          <label className="sign-in-field">
            <span>{t("auth.login")}</span>
            <input autoFocus autoCapitalize="none" autoComplete="username" spellCheck={false}
              placeholder={t("auth.loginPlaceholder")}
              value={login} onChange={(event) => setLogin(event.target.value)} />
            <small>{t("auth.leaveLoginRules")}</small>
          </label>
          <label className="sign-in-field">
            <span>{t("accounts.displayName")} <em>{t("auth.optional")}</em></span>
            <input autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </label>
          <PasswordField label={t("auth.password")} autoComplete="new-password" value={password} onChange={setPassword} />
          <PasswordField label={t("auth.confirmPassword")} autoComplete="new-password" value={confirm} onChange={setConfirm} />
          <label className="sign-in-check">
            <input type="checkbox" checked={leaveConfirmed} onChange={(event) => setLeaveConfirmed(event.target.checked)} />
            <span>{t("auth.leaveConfirm")}</span>
          </label>
          <button className="action sign-in-submit sign-in-danger" type="submit"
            disabled={busy || !leaveConfirmed || !login.trim() || !password || !confirm}>
            {busy ? <Loader2 size={15} className="sign-in-spin" /> : <Unlink size={15} />}
            {t(busy ? "auth.leaving" : "auth.leaveSubmit")}
          </button>
        </>
      );
      footer = (
        <button className="sign-in-link sign-in-back" type="button" onClick={() => startOver()}>
          <ArrowLeft size={13} /> {t("auth.backToHub")}
        </button>
      );
      break;
    case "choose":
      title = t("auth.savedTitle");
      subtitle = t(device ? "auth.savedSubtitleMachine" : "auth.savedSubtitleBrowser");
      icon = <UsersRound size={18} />;
      body = (
        <>
          <div className="sign-in-local" role="note">
            {device ? <Laptop size={15} aria-hidden="true" /> : <HardDrive size={15} aria-hidden="true" />}
            <span>
              <strong>{where}</strong>
              <small>{t(device ? "auth.savedLocalHintMachine" : "auth.savedLocalHintBrowser")}</small>
            </span>
          </div>
          <ul className="sign-in-accounts" aria-label={where}>
            {saved.map((account, index) => (
              <li key={account.login}>
                <button type="button" className="sign-in-account" autoFocus={index === 0} onClick={() => choose(account)}>
                  <AccountAvatar account={account} />
                  <span className="sign-in-account-text">
                    <strong>{accountLabel(account)}</strong>
                    <small>
                      {account.display_name ? account.login : ""}
                      {account.display_name && lastUsed(account) ? " · " : ""}
                      {lastUsed(account)}
                    </small>
                  </span>
                  <ChevronRight size={15} className="sign-in-account-go" aria-hidden="true" />
                </button>
                <button type="button" className="sign-in-forget"
                  aria-label={t("auth.savedForget", { name: accountLabel(account) })}
                  title={t("auth.savedForget", { name: accountLabel(account) })}
                  onClick={() => void forget(account)}>
                  <X size={13} />
                </button>
              </li>
            ))}
          </ul>
          <div className="sign-in-divider"><span>{t("auth.or")}</span></div>
          <div className="sign-in-choices">
            <button type="button" className="ghost-action" onClick={() => startOver("identify")}>
              <UserRound size={14} /> {t("auth.useOther")}
            </button>
            <button type="button" className="ghost-action" onClick={() => startOver("create")}>
              <UserPlus size={14} /> {t("auth.createAccount")}
            </button>
          </div>
        </>
      );
      break;
    case "identify":
      onSubmit = identify;
      body = (
        <>
          {alert}
          {status}
          <label className="sign-in-field">
            <span>{t("auth.login")}</span>
            <input autoFocus autoCapitalize="none" autoComplete="username" spellCheck={false}
              placeholder={t("auth.loginPlaceholder")}
              value={login} onChange={(event) => setLogin(event.target.value)} />
          </label>
          <button className="action sign-in-submit" type="submit" disabled={busy || !login.trim()}>
            {busy ? <Loader2 size={15} className="sign-in-spin" /> : null}
            {t(busy ? "auth.checking" : "auth.continue")}
            {busy ? null : <ArrowRight size={15} />}
          </button>
          <div className="sign-in-divider"><span>{t("auth.noAccountPrompt")}</span></div>
          <button className="ghost-action sign-in-secondary" type="button" onClick={() => startOver("create")}>
            <UserPlus size={14} /> {t("auth.createAccount")}
          </button>
        </>
      );
      footer = saved.length > 0 ? (
        <button className="sign-in-link sign-in-back" type="button" onClick={() => startOver("choose")}>
          <ArrowLeft size={13} /> {t("auth.backToSaved")}
        </button>
      ) : null;
      break;
    case "password":
      title = t("auth.passwordTitle");
      subtitle = t("auth.passwordSubtitle");
      icon = <KeyRound size={18} />;
      onSubmit = signIn;
      body = (
        <>
          {who}
          {alert}
          {status}
          {/* The login name rides along so password managers file the
              password under it. */}
          <input type="text" autoComplete="username" value={login} readOnly hidden />
          <PasswordField autoFocus label={t("auth.password")} autoComplete="current-password" value={password} onChange={setPassword} />
          <button className="action sign-in-submit" type="submit" disabled={busy || !password}>
            {busy ? <Loader2 size={15} className="sign-in-spin" /> : <LogIn size={15} />}
            {t(busy ? "auth.signingIn" : "auth.signIn")}
          </button>
        </>
      );
      break;
    case "create":
    case "register":
    case "setup": {
      const creating = step === "create";
      const first = creating && setupAvailable;
      title = t(step === "setup" || first ? "accounts.setupTitle" : "auth.registerTitle");
      subtitle = first
        ? t("auth.firstAccountSubtitle")
        : creating
        ? t(device ? "auth.createSubtitleMachine" : "auth.createSubtitle")
        : t(step === "setup" ? "auth.setupSubtitle" : "auth.registerSubtitle", { login });
      icon = <UserPlus size={18} />;
      onSubmit = register;
      body = (
        <>
          {creating ? null : who}
          {alert}
          {creating ? (
            <label className="sign-in-field">
              <span>{t("auth.login")}</span>
              <input autoFocus autoCapitalize="none" autoComplete="username" spellCheck={false}
                placeholder={t("auth.loginPlaceholder")}
                value={login} onChange={(event) => setLogin(event.target.value)} />
              <small>{t("auth.loginRules")}</small>
            </label>
          ) : (
            <input type="text" autoComplete="username" value={login} readOnly hidden />
          )}
          <label className="sign-in-field">
            <span>{t("accounts.displayName")} <em>{t("auth.optional")}</em></span>
            <input autoFocus={!creating} autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </label>
          <PasswordField label={t("auth.password")} autoComplete="new-password" value={password} onChange={setPassword} />
          <PasswordField label={t("auth.confirmPassword")} autoComplete="new-password" value={confirm} onChange={setConfirm} />
          <button className="action sign-in-submit" type="submit" disabled={busy || !password || !confirm || (creating && !login.trim())}>
            {busy ? <Loader2 size={15} className="sign-in-spin" /> : <UserPlus size={15} />}
            {t(busy ? "auth.registering" : step === "setup" ? "accounts.setupConfirm" : creating ? "auth.createAccount" : "auth.register")}
          </button>
          <p className="sign-in-hint">
            <ShieldCheck size={14} aria-hidden="true" />
            <span>{t(first ? "auth.setupHint" : creating ? "auth.createHint" : step === "setup" ? "auth.setupHint" : "auth.registerHint")}</span>
          </p>
        </>
      );
      footer = creating && !first ? (
        <p className="sign-in-footer">
          {t("auth.haveAccountPrompt")}{" "}
          <button className="sign-in-link" type="button" onClick={() => startOver()}>{t("auth.signIn")}</button>
        </p>
      ) : null;
      break;
    }
    case "pending":
      title = t("auth.pendingTitle");
      subtitle = t("auth.pendingSubtitle", { login });
      icon = <Clock size={18} />;
      body = (
        <>
          <p className="sign-in-message" role="status"><Info size={14} aria-hidden="true" />{t("auth.pendingHint")}</p>
          <button className="action sign-in-submit" type="button" onClick={() => goTo("password")}>
            <LogIn size={15} /> {t("auth.pendingRetry")}
          </button>
          <button className="ghost-action sign-in-secondary" type="button" onClick={() => startOver()}>
            <ArrowLeft size={14} /> {t(node || saved.length ? "auth.switchAccount" : "auth.otherLogin")}
          </button>
        </>
      );
      break;
    case "closed":
      title = t("auth.closedTitle");
      subtitle = t(device?.authority === "hub" ? "auth.closedHubSubtitle" : "auth.closedSubtitle");
      icon = <Info size={18} />;
      body = (
        <button className="ghost-action sign-in-secondary" type="button" onClick={() => startOver()}>
          <ArrowLeft size={14} /> {t("auth.otherLogin")}
        </button>
      );
      break;
  }

  const content = (
    <>
      <header className="sign-in-header">
        <span className="sign-in-icon" aria-hidden="true">{icon}</span>
        <h1 id="sign-in-title">{title}</h1>
        <p>{subtitle}</p>
      </header>
      <div className="sign-in-body">{body}</div>
      {footer && <div className="sign-in-card-footer">{footer}</div>}
    </>
  );
  return (
    <SignInFrame busy={busy}>
      {onSubmit ? (
        <form key={step} onSubmit={onSubmit} aria-labelledby="sign-in-title" noValidate>{content}</form>
      ) : (
        <div key={step} role="group" aria-labelledby="sign-in-title">{content}</div>
      )}
    </SignInFrame>
  );
}
