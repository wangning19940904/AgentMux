import { CheckCircle2, Copy, Download, ExternalLink, LogIn, Package, RefreshCw, X } from "lucide-react";
import type {
  Framework,
  FrameworkAuthStatus,
  FrameworkUpdateCheck,
  OperationProgress,
} from "../../api";
import { isDesktopApp, openExternalURL } from "../../api/desktop";
import { OperationProgress as OperationProgressView } from "../../components/OperationProgress";
import { useI18n } from "../../i18n";
import type { FrameworkLoginFlow } from "./frameworkAuthModel";

export type FrameworkBusyAction = "install" | "update" | "check" | "auth" | "complete" | "cancel";

export function FrameworkTableRows({
  item,
  busy,
  progress,
  check,
  auth,
  loginFlow,
  loginCode,
  copiedCode,
  currentMachine,
  disabled,
  onCheck,
  onInstall,
  onAuth,
  onConfigureCredentials,
  onLoginCodeChange,
  onCompleteAuth,
  onCancelAuth,
  onDismissAuth,
  onCopyCode,
}: {
  item: Framework;
  busy?: FrameworkBusyAction;
  progress?: OperationProgress;
  check?: FrameworkUpdateCheck;
  auth?: FrameworkAuthStatus;
  loginFlow?: FrameworkLoginFlow;
  loginCode: string;
  copiedCode: boolean;
  currentMachine: string;
  disabled: boolean;
  onCheck?: () => void;
  onInstall?: (action: "install" | "update") => void;
  onAuth: () => void;
  onConfigureCredentials: () => void;
  onLoginCodeChange: (code: string) => void;
  onCompleteAuth: (sessionID: string) => void;
  onCancelAuth: (sessionID: string) => void;
  onDismissAuth: () => void;
  onCopyCode: (code: string) => void;
}) {
  const { t } = useI18n();
  const { spec } = item;
  const cli = spec.kind_type === "cli";
  const hasUpdate = Boolean(check?.update_available);
  const action: "install" | "update" | "check" = item.installed ? (hasUpdate ? "update" : "check") : "install";
  const updateStatus = frameworkUpdateStatusLabel(check, t);
  const updateStatusClass = check?.error || check?.update_available ? "warning" : "success";
  const showAction = spec.supported && (item.installed ? spec.update_supported : spec.install_supported);
  const authActive = loginFlow?.state === "waiting";
  const credentialEnvironment = auth?.detail === "credential environment is configured";
  const showAuthStatus = cli && item.installed && (!auth || auth.login_supported || Boolean(spec.env_required?.length));
  const loginPrimary = Boolean(auth?.login_supported && auth.state !== "authenticated");
  const providerPrimary = Boolean(auth && auth.state !== "authenticated" && !auth.login_supported && spec.env_required?.length);
  const authLabel = frameworkAuthStatusLabel(auth, spec.env_required ?? [], t);
  const authClass = auth?.state === "authenticated" ? "success" : auth?.state === "unknown" || !auth ? "" : "warning";
  const buttonLabel = busy === "check"
    ? t("tools.checkingUpdate")
    : busy === "install" || busy === "update"
      ? t("frameworks.installing")
      : action === "install"
        ? t("frameworks.install")
        : action === "update"
          ? t("tools.update")
          : t("tools.checkUpdate");

  return (
    <>
      <tr className={`catalog-row${item.installed ? " installed" : ""}`}>
        <td className="catalog-primary-cell" data-label={t("common.name")}>
          <span className="provider-icon"><Package size={16} /></span>
          <span className="catalog-primary-copy">
            <strong>{spec.display}</strong>
            {spec.internal_only && <span className="status-badge warning internal-only-badge">{t("tools.internalBadge")}</span>}
            <small className="mono">{spec.kind}</small>
            {spec.note && <small>{spec.note}</small>}
            {cli && !item.installed && spec.bin && <small className="mono">{spec.bin}</small>}
          </span>
        </td>
        <td data-label={t("common.type")}><span className="pill framework-type">{spec.kind_type.toUpperCase()}</span></td>
        <td data-label={t("frameworks.requirements")}>
          <span className="catalog-badge-list">
            {auth?.login_supported && <span className="pill"><LogIn size={13} /> {t("frameworks.browserLogin")}</span>}
            {spec.env_required?.map((env) => <span key={env} className="pill mono">{env}</span>)}
            {!auth?.login_supported && !spec.env_required?.length && <span className="muted">—</span>}
          </span>
        </td>
        <td data-label={t("common.status")}>
          <span className="cli-status-stack">
            {item.installed ? (
              <span className="status-badge success">
                <CheckCircle2 size={14} /> {t("frameworks.installed")}
                {item.registered && <span className="muted"> · {t("frameworks.routable")}</span>}
              </span>
            ) : !spec.supported ? (
              <span className="status-badge">{t("frameworks.comingSoon")}</span>
            ) : (
              <span className="status-badge"><span className="status-dot" />{t("frameworks.notDetected")}</span>
            )}
            {item.installed && item.version && <span className="status-badge version-badge mono">{t("frameworks.currentVersion")} · v{item.version}</span>}
            {item.installed && updateStatus && <span className={`status-badge ${updateStatusClass}`} title={check?.error || undefined}>{updateStatus}</span>}
            {showAuthStatus && (
              <span className={`status-badge ${authClass}`} title={auth?.detail}>
                {auth?.state === "authenticated" ? <CheckCircle2 size={14} /> : <LogIn size={14} />}{authLabel}
              </span>
            )}
          </span>
        </td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
          <div className="catalog-action-stack">
            {loginPrimary && (
              <button className="action" disabled={Boolean(busy) || authActive} onClick={onAuth} type="button">
                <LogIn className={busy === "auth" ? "spin" : ""} size={14} />
                {frameworkAuthActionLabel(auth, loginFlow, busy, credentialEnvironment, t)}
              </button>
            )}
            {providerPrimary && <button className="action" disabled={Boolean(busy)} onClick={onConfigureCredentials} type="button">{t("frameworks.configureCredentials")}</button>}
            {showAction && (
              <button
                className={loginPrimary || providerPrimary ? "ghost-action" : "action"}
                disabled={disabled || Boolean(busy) || authActive}
                onClick={() => (action === "check" ? onCheck?.() : onInstall?.(action))}
                type="button"
              >
                {busy === "check" || action === "check" ? <RefreshCw className={busy === "check" ? "spin" : ""} size={14} /> : <Download size={14} />}
                {buttonLabel}
              </button>
            )}
            {auth?.login_supported && auth.state === "authenticated" && (
              <button className="ghost-action" disabled={Boolean(busy) || authActive} onClick={onAuth} type="button">
                <LogIn className={busy === "auth" ? "spin" : ""} size={14} />
                {frameworkAuthActionLabel(auth, loginFlow, busy, credentialEnvironment, t)}
              </button>
            )}
          </div>
        </td>
      </tr>
      {progress && <tr className="catalog-progress-row"><td colSpan={5}><OperationProgressView progress={progress} /></td></tr>}
      {loginFlow && (
        <tr className="catalog-progress-row framework-auth-row">
          <td colSpan={5}>
            <FrameworkAuthPrompt
              busy={busy}
              copiedCode={copiedCode}
              currentMachine={currentMachine}
              displayName={spec.display}
              flow={loginFlow}
              loginCode={loginCode}
              onAuth={onAuth}
              onCancel={() => onCancelAuth(loginFlow.session_id)}
              onComplete={() => onCompleteAuth(loginFlow.session_id)}
              onCopyCode={onCopyCode}
              onDismiss={onDismissAuth}
              onLoginCodeChange={onLoginCodeChange}
              t={t}
            />
          </td>
        </tr>
      )}
    </>
  );
}

function FrameworkAuthPrompt({
  flow, displayName, currentMachine, loginCode, copiedCode, busy,
  onLoginCodeChange, onComplete, onCancel, onAuth, onDismiss, onCopyCode, t,
}: {
  flow: FrameworkLoginFlow;
  displayName: string;
  currentMachine: string;
  loginCode: string;
  copiedCode: boolean;
  busy?: FrameworkBusyAction;
  onLoginCodeChange: (code: string) => void;
  onComplete: () => void;
  onCancel: () => void;
  onAuth: () => void;
  onDismiss: () => void;
  onCopyCode: (code: string) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  const active = flow.state === "waiting";
  const title = flow.state === "succeeded"
    ? t("tools.authReady")
    : flow.state === "failed"
      ? t("tools.authFailed")
      : flow.state === "cancelled"
        ? t("tools.authCancelled")
        : t("frameworks.authAuthorizing", { framework: displayName });

  return (
    <div className={`cli-auth-prompt framework-auth-prompt ${flow.state}`} aria-live="polite">
      <div className="cli-auth-prompt-copy">
        <strong>{flow.state === "succeeded" ? <CheckCircle2 size={15} /> : <LogIn size={15} />}{title}</strong>
        {active && <span>{t("frameworks.authMachine", { machine: currentMachine })}</span>}
        {active && <span>{flow.codeSubmitted ? t("frameworks.authCodeSubmitted") : t("frameworks.authBrowserHelp")}</span>}
        {flow.error && <span className="error">{flow.error}</span>}
      </div>
      <div className="cli-auth-prompt-actions framework-auth-actions">
        {flow.login_url && active && (
          <a
            className="action"
            href={flow.login_url}
            onClick={(event) => {
              if (!isDesktopApp()) return;
              event.preventDefault();
              void openExternalURL(flow.login_url);
            }}
            rel="noreferrer"
            target="_blank"
          ><ExternalLink size={14} />{t("tools.openAuthLink")}</a>
        )}
        {flow.verification_code && active && (
          <span className="cli-auth-code framework-verification-code">
            {t("tools.authCode")} <code>{flow.verification_code}</code>
            <button className="ghost-action" onClick={() => onCopyCode(flow.verification_code || "")} type="button">
              <Copy size={13} /> {copiedCode ? t("frameworks.authCopied") : t("frameworks.authCopy")}
            </button>
          </span>
        )}
        {flow.input_required && active && !flow.codeSubmitted && (
          <span className="framework-auth-input">
            <input
              aria-label={t("frameworks.authCodePlaceholder")}
              onChange={(event) => onLoginCodeChange(event.target.value)}
              placeholder={t("frameworks.authCodePlaceholder")}
              value={loginCode}
            />
            <button className="ghost-action" disabled={!loginCode.trim() || busy === "complete"} onClick={onComplete} type="button">
              {busy === "complete" ? t("common.save") : t("frameworks.authSubmitCode")}
            </button>
          </span>
        )}
        {active && <button className="ghost-action" disabled={busy === "cancel"} onClick={onCancel} type="button"><X size={14} /> {t("tools.authCancel")}</button>}
        {(flow.state === "failed" || flow.state === "cancelled") && (
          <>
            <button className="action" disabled={Boolean(busy)} onClick={onAuth} type="button"><RefreshCw size={14} /> {t("frameworks.authRetry")}</button>
            <button className="ghost-action" onClick={onDismiss} type="button"><X size={14} /> {t("common.close")}</button>
          </>
        )}
      </div>
    </div>
  );
}

function frameworkAuthStatusLabel(
  auth: FrameworkAuthStatus | undefined,
  envRequired: string[],
  t: (key: string) => string,
) {
  if (!auth) return t("tools.authChecking");
  if (auth.state === "authenticated") {
    return auth.detail === "credential environment is configured"
      ? t("frameworks.authCredentialReady")
      : t("tools.authAuthenticated");
  }
  if (auth.login_supported) return auth.state === "unauthenticated" ? t("tools.authLoginRequired") : t("tools.authUnknown");
  if (envRequired.length > 0) return t("frameworks.authProviderRequired");
  return t("tools.authUnknown");
}

function frameworkAuthActionLabel(
  auth: FrameworkAuthStatus | undefined,
  flow: FrameworkLoginFlow | undefined,
  busy: FrameworkBusyAction | undefined,
  credentialEnvironment: boolean,
  t: (key: string) => string,
) {
  if (busy === "auth") return t("tools.authStarting");
  if (flow?.state === "waiting") return t("tools.authWaiting");
  if (flow?.state === "failed" || flow?.state === "cancelled") return t("frameworks.authRetry");
  if (auth?.state === "authenticated" && !credentialEnvironment) return t("tools.authAgain");
  return t("tools.authLogin");
}

function frameworkUpdateStatusLabel(check: FrameworkUpdateCheck | undefined, t: (key: string) => string) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("frameworks.latestVersion")} · v${check.latest_version || "?"}`;
  return t("tools.upToDate");
}
