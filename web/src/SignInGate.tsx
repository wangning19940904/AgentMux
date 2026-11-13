import { LogIn } from "lucide-react";
import { Fragment, useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { api, type AuthSession } from "./api";
import { useI18n } from "./i18n";
import { AUTH_CHANGED_EVENT, notifyAuthChanged, resolveSignInGate, signInErrorKey } from "./tenancyGate";

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

export function SignInScreen() {
  const { t } = useI18n();
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy || !login.trim() || !password) return;
    setBusy(true);
    setError("");
    try {
      await api.signIn(login.trim(), password);
      setPassword("");
      notifyAuthChanged();
    } catch (failure) {
      setError(t(signInErrorKey(failure)));
    } finally {
      setBusy(false);
    }
  }

  return (
    <SignInFrame>
      <form onSubmit={(event) => void submit(event)} aria-labelledby="sign-in-title">
        <div className="surface-header">
          <div>
            <h2 id="sign-in-title">{t("auth.title")}</h2>
            <p className="subtle-copy">{t("auth.subtitle")}</p>
          </div>
        </div>
        <div className="surface-body" style={{ display: "grid", gap: 12 }}>
          {error && <p className="error" role="alert">{error}</p>}
          <label style={{ display: "grid", gap: 4 }}>
            <span>{t("auth.login")}</span>
            <input
              autoFocus
              autoCapitalize="none"
              autoComplete="username"
              spellCheck={false}
              value={login}
              onChange={(event) => setLogin(event.target.value)}
            />
          </label>
          <label style={{ display: "grid", gap: 4 }}>
            <span>{t("auth.password")}</span>
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </label>
          <button className="primary-action" type="submit" disabled={busy || !login.trim() || !password}>
            <LogIn size={15} /> {t(busy ? "auth.signingIn" : "auth.signIn")}
          </button>
          <p className="muted">{t("auth.noAccountHint")}</p>
        </div>
      </form>
    </SignInFrame>
  );
}
