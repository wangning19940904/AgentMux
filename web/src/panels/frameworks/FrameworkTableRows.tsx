import { CheckCircle2, Copy, Download, ExternalLink, LogIn, LogOut, Package, RefreshCw, Trash2, X } from "lucide-react";
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
import { TargetBadge } from "../../components/TargetBadge";
import { frameworkCompanyName, frameworkSupportsLogin } from "./frameworkPresentation";

export type FrameworkBusyAction = "install" | "update" | "uninstall" | "check" | "auth" | "logout" | "complete" | "cancel";

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
  onUninstall,
  onAuth,
  onLogout,
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
  onUninstall: () => void;
  onAuth: () => void;
  onLogout: () => void;
  onLoginCodeChange: (code: string) => void;
  onCompleteAuth: (sessionID: string) => void;
  onCancelAuth: (sessionID: string) => void;
  onDismissAuth: () => void;
  onCopyCode: (code: string) => void;
}) {
  const { t } = useI18n();
  const { spec } = item;
  const hasUpdate = Boolean(check?.update_available);
  const action: "update" | "check" = hasUpdate ? "update" : "check";
  const updateStatus = frameworkUpdateStatusLabel(check, t);
  const updateStatusClass = check?.error || check?.update_available ? "warning" : "success";
  const authActive = loginFlow?.state === "waiting";
  const supportsLogin = frameworkSupportsLogin(spec, auth);
  const authChecking = supportsLogin && !auth;
  const authenticated = auth?.state === "authenticated";
  const canLogout = Boolean(authenticated && auth?.logout_supported);
  const buttonLabel = busy === "check"
    ? t("tools.checkingUpdate")
    : busy === "update"
      ? t("frameworks.installing")
      : action === "update"
          ? t("tools.update")
          : t("tools.checkUpdate");

  return (
    <>
      <tr className="catalog-row framework-installed-row">
        <td className="catalog-primary-cell" data-label={t("common.name")}>
          <span className="provider-icon"><Package size={16} /></span>
          <span className="catalog-primary-copy">
            <strong>{spec.display}</strong>
            <span className="framework-meta-tags">
              <span className="pill framework-company">{frameworkCompanyName(spec)}</span>
              <span className="pill framework-type">{spec.kind_type.toUpperCase()}</span>
              <TargetBadge target_id={item.target_id} target_name={item.target_name} />
            </span>
          </span>
        </td>
        <td data-label={t("frameworks.versionAndUpdate")}>
          <span className="framework-version-copy">
            <strong className="mono">{item.version ? `v${item.version}` : "—"}</strong>
            {updateStatus ? (
              <span className={`framework-update-state ${updateStatusClass}`} title={check?.error || undefined}>
                {updateStatusClass === "success" && <CheckCircle2 size={13} />}{updateStatus}
              </span>
            ) : <span className="framework-update-state">{t("tools.checkingUpdate")}</span>}
          </span>
        </td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
          <div className="framework-row-actions">
            {spec.update_supported && (
              <button
                className={hasUpdate ? "action" : "ghost-action"}
                disabled={disabled || Boolean(busy) || authActive}
                onClick={() => (action === "check" ? onCheck?.() : onInstall?.(action))}
                type="button"
              >
                {busy === "check" || action === "check" ? <RefreshCw className={busy === "check" ? "spin" : ""} size={14} /> : <Download size={14} />}
                {buttonLabel}
              </button>
            )}
            {supportsLogin && (
              <button
                className={`ghost-action${authenticated ? " framework-authenticated-action" : ""}`}
                disabled={Boolean(busy) || authActive || authChecking || Boolean(authenticated && !canLogout)}
                onClick={canLogout ? onLogout : onAuth}
                title={canLogout ? t("tools.authLogoutHint") : frameworkAuthStatusLabel(auth, spec.env_required ?? [], t)}
                type="button"
              >
                {authChecking || busy === "auth" || busy === "logout"
                  ? <RefreshCw className="spin" size={14} />
                  : authenticated
                    ? <LogOut size={14} />
                    : <LogIn size={14} />}
                {busy === "logout"
                  ? t("tools.authLoggingOut")
                  : authenticated
                    ? t("tools.authAuthenticated")
                    : authChecking
                      ? t("tools.authChecking")
                      : t("tools.authLogin")}
              </button>
            )}
            <button
              className="ghost-action danger-action"
              disabled={!spec.uninstall_supported || Boolean(busy) || authActive}
              onClick={onUninstall}
              title={!spec.uninstall_supported ? t("frameworks.uninstallUnsupported") : undefined}
              type="button"
            >
              <Trash2 size={14} />{busy === "uninstall" ? t("frameworks.uninstalling") : t("frameworks.uninstall")}
            </button>
          </div>
        </td>
      </tr>
      {progress && <tr className="catalog-progress-row"><td colSpan={3}><OperationProgressView progress={progress} /></td></tr>}
      {loginFlow && (
        <tr className="catalog-progress-row framework-auth-row">
          <td colSpan={3}>
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

function frameworkUpdateStatusLabel(check: FrameworkUpdateCheck | undefined, t: (key: string) => string) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("frameworks.latestVersion")} · v${check.latest_version || "?"}`;
  return t("tools.upToDate");
}
