import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { CheckCircle2, ChevronRight, Copy, Download, Ellipsis, ExternalLink, List, LogIn, LogOut, Monitor, Package, Play, RefreshCw, Trash2, UserRound, X } from "lucide-react";
import type {
  Framework,
  FrameworkAuthStatus,
  FrameworkHealthRecord,
  FrameworkSmokeResult,
  FrameworkUpdateCheck,
  OperationProgress,
} from "../../api";
import { isDesktopApp, openExternalURL } from "../../api/desktop";
import { OperationProgress as OperationProgressView } from "../../components/OperationProgress";
import { useI18n } from "../../i18n";
import type { FrameworkLoginFlow } from "./frameworkAuthModel";
import { TargetBadge } from "../../components/TargetBadge";
import { FrameworkSmokeResultView } from "./FrameworkSmokeResult";
import { FrameworkHealthRecordView } from "./FrameworkHealthRecord";
import { frameworkAccountSummary, frameworkCompanyName, frameworkSupportsLogin } from "./frameworkPresentation";
import type { FrameworkAccountFieldKey, FrameworkAccountSummary } from "./frameworkPresentation";

export type FrameworkBusyAction = "install" | "update" | "uninstall" | "check" | "auth" | "logout" | "complete" | "cancel";

// A group's header row in the catalog: a framework with a row per machine, or
// a machine with a row per framework. Groups start collapsed, so the header
// carries the summary: how many installs, which can be updated, which failed
// their update check, and one action to update the outdated ones.
export function FrameworkGroupRow({
  by,
  item,
  machineName,
  count,
  latestVersion,
  outdatedCount,
  failedCount,
  updating,
  checking,
  expanded,
  onToggle,
  updateDisabled,
  onUpdateOutdated,
}: {
  by: "framework" | "machine";
  item: Framework;
  machineName: string;
  count: number;
  latestVersion?: string;
  outdatedCount: number;
  failedCount: number;
  updating: boolean;
  checking: boolean;
  expanded: boolean;
  onToggle: () => void;
  updateDisabled: boolean;
  onUpdateOutdated: () => void;
}) {
  const { t } = useI18n();
  const { spec } = item;
  const byFramework = by === "framework";
  return (
    <tr className={`framework-group-row${expanded ? " expanded" : ""}`}>
      <td colSpan={5}>
        <div className="framework-group-head">
          <button aria-expanded={expanded} className="framework-group-toggle" onClick={onToggle} type="button">
            <ChevronRight className="framework-group-chevron" size={16} />
            <span className="provider-icon">{byFramework ? <Package size={16} /> : <Monitor size={16} />}</span>
            <span className="framework-group-copy">
              <strong>{byFramework ? spec.display : machineName}</strong>
              {byFramework && (
                <span className="framework-meta-tags">
                  <span className="pill framework-company">{frameworkCompanyName(spec)}</span>
                  <span className="pill framework-type">{spec.kind_type.toUpperCase()}</span>
                </span>
              )}
            </span>
          </button>
          <span className="framework-group-summary">
            <span>{byFramework ? t("resources.machineCount", { count }) : t("frameworks.frameworkCount", { count })}</span>
            {latestVersion && <span className="mono">{t("frameworks.latestVersion")} v{latestVersion}</span>}
            {updating
              ? <span className="framework-update-state"><RefreshCw className="spin" size={12} />{t("frameworks.groupUpdating")}</span>
              : outdatedCount > 0
                ? <span className="framework-update-state warning">{t(byFramework ? "frameworks.groupOutdated" : "frameworks.machineGroupOutdated", { count: outdatedCount })}</span>
                : checking
                  ? <span className="framework-update-state"><RefreshCw className="spin" size={12} />{t("tools.checkingUpdate")}</span>
                  : failedCount === 0 && <span className="framework-update-state success"><CheckCircle2 size={12} />{t("frameworks.groupUpToDate")}</span>}
            {failedCount > 0 && <span className="framework-update-state warning">{t(byFramework ? "frameworks.groupCheckFailed" : "frameworks.machineGroupCheckFailed", { count: failedCount })}</span>}
          </span>
          {outdatedCount > 0 && (
            <button className="ghost-action" disabled={updateDisabled} onClick={onUpdateOutdated} type="button">
              <Download size={14} />{t(byFramework ? "frameworks.updateGroup" : "frameworks.updateMachineGroup", { count: outdatedCount })}
            </button>
          )}
        </div>
      </td>
    </tr>
  );
}

// One machine's install of a framework: its version, signed-in account and
// actions. Every machine gets its own row so versions can be compared at a glance.
export function FrameworkTableRows({
  identity = "machine",
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
  health,
  record,
  testing,
  onTest,
  onModels,
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
  identity?: "machine" | "framework";
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
  health?: FrameworkSmokeResult;
  record?: FrameworkHealthRecord;
  testing?: boolean;
  onTest?: () => void;
  onModels?: () => void;
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
  const updateStatus = frameworkUpdateStatusLabel(check, t);
  const updateStatusClass = check?.error || check?.update_available ? "warning" : "success";
  const authActive = loginFlow?.state === "waiting";
  const supportsLogin = frameworkSupportsLogin(spec, auth);
  const authChecking = supportsLogin && !auth;
  const authenticated = auth?.state === "authenticated";
  const canLogout = Boolean(authenticated && auth?.logout_supported);
  const account = frameworkAccountSummary(auth);
  const buttonLabel = busy === "update" ? t("frameworks.installing") : t("tools.update");

  return (
    <>
      <tr className={`catalog-row framework-installed-row framework-instance-row${hasUpdate ? " outdated" : ""}`}>
        {identity === "machine" ? (
          <td className="framework-machine-cell" data-label={t("frameworks.machine")}>
            <span className="framework-machine-name" title={currentMachine}>{currentMachine}</span>
          </td>
        ) : (
          <td className="framework-machine-cell" data-label={t("frameworks.framework")}>
            <span className="framework-machine-name" title={spec.display}>{spec.display}</span>
            <span className="framework-meta-tags">
              <span className="pill framework-company">{frameworkCompanyName(spec)}</span>
            </span>
          </td>
        )}
        <td data-label={t("frameworks.versionAndUpdate")}>
          <span className="framework-version-copy">
            <strong className="mono">{item.version ? `v${item.version}` : "—"}</strong>
            {updateStatus && busy !== "check" ? (
              <span className={`framework-update-state ${updateStatusClass}`} title={check?.error || undefined}>
                {updateStatusClass === "success" && <CheckCircle2 size={13} />}{updateStatus}
              </span>
            ) : <span className="framework-update-state"><RefreshCw className="spin" size={12} />{t("tools.checkingUpdate")}</span>}
            {check?.error && busy !== "check" && <span className="framework-update-error" title={check.error}>{check.error}</span>}
          </span>
        </td>
        <td className="framework-account-cell" data-label={t("frameworks.account")}>
          {account
            ? <FrameworkAccountLine account={account} />
            : supportsLogin && !authenticated
              ? (
                <button
                  className="framework-login-link"
                  disabled={disabled || Boolean(busy) || authActive || authChecking}
                  onClick={onAuth}
                  title={frameworkAuthStatusLabel(auth, spec.env_required ?? [], t)}
                  type="button"
                >
                  {authChecking || busy === "auth" ? <RefreshCw className="spin" size={12} /> : <LogIn size={12} />}
                  {authChecking ? t("tools.authChecking") : t("tools.authLogin")}
                </button>
              )
              : <span className="muted">{authenticated ? t("tools.authAuthenticated") : "—"}</span>}
        </td>
        <td data-label={t("frameworks.availability")}>
          <div className="framework-health-cell">
            <FrameworkSmokeResultView result={health} busy={testing} showModel />
            <FrameworkHealthRecordView record={record} />
            {item.registered && (
              <div className="framework-row-actions">
                <button className="ghost-action" disabled={disabled || testing || authActive} onClick={onTest}
                  title={t("frameworks.health.testHint")} type="button">
                  <Play size={14} />{t("frameworks.health.test")}
                </button>
                <button className="ghost-action" disabled={disabled} onClick={onModels} type="button">
                  <List size={14} />{t("frameworks.health.models")}
                </button>
              </div>
            )}
          </div>
        </td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
          <div className="framework-row-actions">
            {spec.update_supported && (hasUpdate || busy === "update") && (
              <button
                className="action"
                disabled={disabled || Boolean(busy) || authActive}
                onClick={() => onInstall?.("update")}
                type="button"
              >
                <Download size={14} />{buttonLabel}
              </button>
            )}
            <FrameworkRowMenu
              disabled={disabled || Boolean(busy) || authActive}
              label={t("common.more")}
              items={[
                ...(spec.update_supported ? [{
                  key: "check", icon: <RefreshCw size={14} />, label: t("tools.checkUpdate"), onSelect: () => onCheck?.(),
                }] : []),
                ...(canLogout ? [{
                  key: "logout", icon: <LogOut size={14} />,
                  label: busy === "logout" ? t("tools.authLoggingOut") : t("frameworks.authLogout"),
                  title: account ? t("frameworks.authLogoutAccountHint", { account: account.identity }) : t("tools.authLogoutHint"),
                  onSelect: onLogout,
                }] : []),
                {
                  key: "uninstall", icon: <Trash2 size={14} />, danger: true,
                  label: busy === "uninstall" ? t("frameworks.uninstalling") : t("frameworks.uninstall"),
                  title: spec.uninstall_supported ? undefined : t("frameworks.uninstallUnsupported"),
                  disabled: !spec.uninstall_supported,
                  onSelect: onUninstall,
                },
              ]}
            />
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

// A framework whose install is running in the background. It is listed in the
// catalog (not the install dialog) so the dialog can close as soon as the
// install starts while progress stays visible.
interface FrameworkRowMenuItem {
  key: string;
  icon: ReactNode;
  label: string;
  title?: string;
  danger?: boolean;
  disabled?: boolean;
  onSelect: () => void;
}

// The less frequent actions of a row. The list is positioned against the
// viewport: the catalog table scrolls sideways, which would clip it.
function FrameworkRowMenu({ items, label, disabled }: { items: FrameworkRowMenuItem[]; label: string; disabled?: boolean }) {
  const [position, setPosition] = useState<{ top: number; right: number } | null>(null);
  const anchor = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!position) return;
    const close = () => setPosition(null);
    const onPointer = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!anchor.current?.contains(target) && !list.current?.contains(target)) close();
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      close();
      anchor.current?.focus();
    };
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("keydown", onKey);
    window.addEventListener("resize", close);
    window.addEventListener("scroll", close, true);
    list.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    return () => {
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
    };
  }, [position]);
  useEffect(() => { if (disabled) setPosition(null); }, [disabled]);

  function toggle() {
    if (position) {
      setPosition(null);
      return;
    }
    const rect = anchor.current?.getBoundingClientRect();
    if (rect) setPosition({ top: rect.bottom + 4, right: Math.max(8, window.innerWidth - rect.right) });
  }

  return (
    <>
      <button
        aria-expanded={Boolean(position)}
        aria-haspopup="menu"
        aria-label={label}
        className="ghost-action icon-only framework-row-menu-button"
        disabled={disabled}
        onClick={toggle}
        ref={anchor}
        title={label}
        type="button"
      >
        <Ellipsis size={15} />
      </button>
      {position && (
        <div className="framework-row-menu" ref={list} role="menu" style={{ top: position.top, right: position.right }}>
          {items.map((item) => (
            <button
              className={item.danger ? "danger" : undefined}
              disabled={item.disabled}
              key={item.key}
              onClick={() => {
                setPosition(null);
                item.onSelect();
              }}
              role="menuitem"
              title={item.title}
              type="button"
            >
              {item.icon}{item.label}
            </button>
          ))}
        </div>
      )}
    </>
  );
}

export function FrameworkInstallingRow({ item, progress }: { item: Framework; progress?: OperationProgress }) {
  const { t } = useI18n();
  return (
    <>
      <tr className="catalog-row framework-installed-row framework-installing-row" aria-busy="true">
        <FrameworkIdentityCell item={item} />
        <td data-label={t("frameworks.versionAndUpdate")}>
          <span className="framework-version-copy">
            <strong className="mono">—</strong>
            <span className="framework-update-state"><RefreshCw className="spin" size={13} />{t("frameworks.installing")}</span>
          </span>
        </td>
        <td className="framework-account-cell" />
        <td data-label={t("frameworks.availability")} />
        <td className="catalog-action-cell" data-label={t("common.actions")} />
      </tr>
      <tr className="catalog-progress-row">
        <td colSpan={5}><OperationProgressView progress={progress ?? { phase: "preparing", percent: 4 }} /></td>
      </tr>
    </>
  );
}

function FrameworkIdentityCell({ item }: { item: Framework }) {
  const { t } = useI18n();
  const { spec } = item;
  return (
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
  );
}

const accountFieldLabels: Record<FrameworkAccountFieldKey, string> = {
  email: "frameworks.accountEmail",
  user_id: "frameworks.accountUserID",
  name: "frameworks.accountName",
  organization: "frameworks.accountOrganization",
  plan: "frameworks.accountPlan",
  method: "frameworks.accountMethod",
  region: "frameworks.accountRegion",
};

function FrameworkAccountLine({ account }: { account: FrameworkAccountSummary }) {
  const { t } = useI18n();
  const tooltip = [
    t("frameworks.accountSignedInAs", { account: account.identity }),
    ...account.fields.map((field) => `${t(accountFieldLabels[field.key])}: ${field.value}`),
  ].join("\n");
  return (
    <span className="framework-account" title={tooltip}>
      <UserRound aria-hidden="true" size={12} />
      <span className="framework-account-identity">{account.identity}</span>
      {account.details.map((detail) => <span className="framework-account-detail" key={detail}>{detail}</span>)}
    </span>
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
