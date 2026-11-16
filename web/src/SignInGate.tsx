import { ArrowLeft, ArrowRight, LogIn, UserPlus, UserRound } from "lucide-react";
import { Fragment, useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { api, type AuthSession } from "./api";
import { useI18n } from "./i18n";
import {
  accountErrorCode,
  accountFormError,
  AUTH_CHANGED_EVENT,
  identifyUnsupported,
  MIN_PASSWORD_LENGTH,
  nextSignInStep,
  notifyAuthChanged,
  resolveSignInGate,
  signInErrorKey,
  type SignInStep,
} from "./signIn";

// SignInGate shows the sign-in screen instead of the Console when accounts
// exist and this browser has no credential of its own. With no accounts it
// renders the Console straight away, exactly as before accounts existed.
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
      const next = await api.authSession();
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
  }, []);

  useEffect(() => {
    if (nativeDesktopAdmin) return;
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
  }, [check, nativeDesktopAdmin]);

  const state = resolveSignInGate(session, { nativeDesktopAdmin, failed });
  if (state === "checking") return <SignInFrame><p className="muted">…</p></SignInFrame>;
  if (state === "signed-out") return <SignInScreen />;
  return <Fragment key={generation}>{children}</Fragment>;
}

function SignInFrame({ children }: { children: ReactNode }) {
  return (
    <div
      className="sign-in-screen"
      style={{ minHeight: "100vh", display: "grid", placeItems: "center", padding: "16px", boxSizing: "border-box" }}
    >
      <section className="surface" style={{ width: "100%", maxWidth: 380 }}>
        {children}
      </section>
    </div>
  );
}

const fieldStyle = { display: "grid", gap: 4 } as const;

// SignInScreen asks for the login name first and checks whether it is
// registered: a registered name goes on to its password, an unknown one to
// registration, which an administrator approves.
export function SignInScreen() {
  const { t } = useI18n();
  const [step, setStep] = useState<SignInStep>("identify");
  const [login, setLogin] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  function goTo(next: SignInStep) {
    setStep(next);
    setPassword("");
    setConfirm("");
    setError("");
  }

  async function run(action: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (failure) {
      const code = accountErrorCode(failure);
      if (code === "pending") {
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
        const result = await api.identify(name);
        setLogin(result.login || name);
        goTo(nextSignInStep(result));
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
      await api.signIn(login, password);
      setPassword("");
      notifyAuthChanged();
    });
  }

  function register(event: FormEvent) {
    event.preventDefault();
    const problem = accountFormError({ login, password, confirm }, { passwordRequired: true });
    if (problem) {
      setError(t(problem, { min: String(MIN_PASSWORD_LENGTH) }));
      return;
    }
    void run(async () => {
      await api.register({ login, display_name: displayName.trim() || undefined, password });
      goTo("pending");
    });
  }

  const who = (
    <div className="control-row" style={{ justifyContent: "space-between" }}>
      <span><UserRound size={14} /> <strong>{login}</strong></span>
      <button className="ghost-action" type="button" disabled={busy} onClick={() => { setNotice(""); goTo("identify"); }}>
        <ArrowLeft size={14} /> {t("auth.otherLogin")}
      </button>
    </div>
  );
  const alert = error ? <p className="error" role="alert">{error}</p> : null;
  const status = notice ? <p className="muted" role="status">{notice}</p> : null;

  let title = t("auth.title");
  let subtitle = t("auth.identifySubtitle");
  let body: ReactNode;
  let onSubmit: ((event: FormEvent) => void) | undefined;
  switch (step) {
    case "identify":
      onSubmit = identify;
      body = (
        <>
          {alert}
          {status}
          <label style={fieldStyle}>
            <span>{t("auth.login")}</span>
            <input autoFocus autoCapitalize="none" autoComplete="username" spellCheck={false}
              value={login} onChange={(event) => setLogin(event.target.value)} />
          </label>
          <button className="primary-action" type="submit" disabled={busy || !login.trim()}>
            <ArrowRight size={15} /> {t(busy ? "auth.checking" : "auth.continue")}
          </button>
        </>
      );
      break;
    case "password":
      subtitle = t("auth.passwordSubtitle");
      onSubmit = signIn;
      body = (
        <>
          {who}
          {alert}
          {status}
          {/* The login name rides along so password managers file the
              password under it. */}
          <input type="text" autoComplete="username" value={login} readOnly hidden />
          <label style={fieldStyle}>
            <span>{t("auth.password")}</span>
            <input autoFocus type="password" autoComplete="current-password"
              value={password} onChange={(event) => setPassword(event.target.value)} />
          </label>
          <button className="primary-action" type="submit" disabled={busy || !password}>
            <LogIn size={15} /> {t(busy ? "auth.signingIn" : "auth.signIn")}
          </button>
        </>
      );
      break;
    case "register":
      title = t("auth.registerTitle");
      subtitle = t("auth.registerSubtitle", { login });
      onSubmit = register;
      body = (
        <>
          {who}
          {alert}
          <input type="text" autoComplete="username" value={login} readOnly hidden />
          <label style={fieldStyle}>
            <span>{t("accounts.displayName")}</span>
            <input autoFocus autoComplete="name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} />
          </label>
          <label style={fieldStyle}>
            <span>{t("auth.password")}</span>
            <input type="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} />
          </label>
          <label style={fieldStyle}>
            <span>{t("auth.confirmPassword")}</span>
            <input type="password" autoComplete="new-password" value={confirm} onChange={(event) => setConfirm(event.target.value)} />
          </label>
          <button className="primary-action" type="submit" disabled={busy || !password || !confirm}>
            <UserPlus size={15} /> {t(busy ? "auth.registering" : "auth.register")}
          </button>
          <p className="muted">{t("auth.registerHint")}</p>
        </>
      );
      break;
    case "pending":
      title = t("auth.pendingTitle");
      subtitle = t("auth.pendingSubtitle", { login });
      body = (
        <>
          <p className="muted" role="status">{t("auth.pendingHint")}</p>
          <button className="ghost-action" type="button" onClick={() => goTo("password")}>
            <LogIn size={14} /> {t("auth.pendingRetry")}
          </button>
          <button className="ghost-action" type="button" onClick={() => { setNotice(""); goTo("identify"); }}>
            <ArrowLeft size={14} /> {t("auth.otherLogin")}
          </button>
        </>
      );
      break;
    case "closed":
      title = t("auth.closedTitle");
      subtitle = t("auth.closedSubtitle");
      body = (
        <button className="ghost-action" type="button" onClick={() => goTo("identify")}>
          <ArrowLeft size={14} /> {t("auth.otherLogin")}
        </button>
      );
      break;
  }

  const content = (
    <>
      <div className="surface-header">
        <div>
          <h2 id="sign-in-title">{title}</h2>
          <p className="subtle-copy">{subtitle}</p>
        </div>
      </div>
      <div className="surface-body" style={{ display: "grid", gap: 12 }}>{body}</div>
    </>
  );
  return (
    <SignInFrame>
      {onSubmit ? (
        <form key={step} onSubmit={onSubmit} aria-labelledby="sign-in-title">{content}</form>
      ) : (
        <div key={step} aria-labelledby="sign-in-title">{content}</div>
      )}
    </SignInFrame>
  );
}
