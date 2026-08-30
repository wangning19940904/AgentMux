import {
  Bot,
  Download,
  ExternalLink,
  LogIn,
  Package,
  Plus,
  RefreshCw,
  TerminalSquare,
  Trash2,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  activeMachineScope,
  api,
  CLIAuthSession,
  CLIAuthStatus,
  CLIInstallResult,
  CLIUpdateCheck,
  MachineTarget,
  OperationProgress,
  TargetMetadata,
} from "../api";
import { isDesktopApp, openExternalURL } from "../api/desktop";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { InternalOnlyDialog } from "../components/InternalOnlyDialog";
import { OperationProgress as OperationProgressView } from "../components/OperationProgress";
import { TargetBadge } from "../components/TargetBadge";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import {
  buildInstalledToolRows,
  buildInstallCandidates,
  InstalledToolRow,
  normalizeToolTargets,
  ToolInstallCandidate,
} from "./tools/toolCatalogModel";

type ToolBusyAction = "install" | "update" | "uninstall" | "check" | "auth";
type InternalInstallTarget = { candidate: ToolInstallCandidate; targetIDs: string[] };

export function ToolsPanel() {
  const { t } = useI18n();
  const tools = useAsync(() => api.tools(), []);
  const marketplace = useAsync(() => api.skillMarketplace(), []);
  const fleetTargets = useAsync(() => api.fleetTargets(), []);
  const [busy, setBusy] = useState<Record<string, ToolBusyAction>>({});
  const [progress, setProgress] = useState<Record<string, OperationProgress>>({});
  const [checks, setChecks] = useState<Record<string, CLIUpdateCheck>>({});
  const [auth, setAuth] = useState<Record<string, CLIAuthStatus>>({});
  const [authSessions, setAuthSessions] = useState<Record<string, CLIAuthSession>>({});
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [result, setResult] = useState<CLIInstallResult | null>(null);
  const [installerOpen, setInstallerOpen] = useState(false);
  const [installSelections, setInstallSelections] = useState<Record<string, string[]>>({});
  const [internalTarget, setInternalTarget] = useState<InternalInstallTarget | null>(null);

  const scope = activeMachineScope();
  const allScope = scope === "all";
  const rawCLI = tools.data?.cli ?? [];
  const rawSkills = tools.data?.skills ?? [];
  const availableTargets = useMemo(
    () => resolveAvailableTargets(fleetTargets.data ?? [], rawCLI, scope, t),
    [fleetTargets.data, rawCLI, scope, t],
  );
  const scopedTargets = allScope ? availableTargets : availableTargets.filter((target) => target.id === scope);
  const fallbackTarget = scopedTargets[0] ?? fallbackMachine(scope, t);
  const cli = useMemo(
    () => normalizeToolTargets(rawCLI, fallbackTarget),
    [rawCLI, fallbackTarget.id, fallbackTarget.name],
  );
  const skills = useMemo(
    () => normalizeToolTargets(rawSkills, fallbackTarget),
    [rawSkills, fallbackTarget.id, fallbackTarget.name],
  );
  const rows = useMemo(() => buildInstalledToolRows(cli, skills), [cli, skills]);
  const activeAuthSessionKey = Object.entries(authSessions)
    .filter(([, session]) => !cliAuthSessionTerminal(session.state))
    .map(([key, session]) => `${key}:${session.session_id}`)
    .sort()
    .join(",");
  const candidates = useMemo(
    () => buildInstallCandidates(cli, skills, marketplace.data ?? tools.data?.marketplace ?? [], scopedTargets),
    [cli, skills, marketplace.data, tools.data?.marketplace, scopedTargets],
  );
  const pagination = useCatalogPagination(rows);
  const installTarget = cliInstallTarget();

  useEffect(() => {
    rows.forEach((row) => {
      if (!row.cli || checks[row.key] || busy[row.key]) return;
      void checkCLIUpdate(row, true);
    });
  }, [rows, checks, busy]);

  useEffect(() => {
    const loginRows = rows.filter((row) => row.cli?.spec.login_supported);
    if (loginRows.length === 0) return;
    let activeRequest = true;
    void Promise.all(loginRows.map(async (row) => {
      try {
        return { key: row.key, status: await api.cliAuth(row.cli!.spec.id, row.targetID) };
      } catch {
        return null;
      }
    })).then((statuses) => {
      if (!activeRequest) return;
      setAuth((current) => {
        const next = { ...current };
        statuses.forEach((entry) => {
          if (entry) next[entry.key] = entry.status;
        });
        return next;
      });
    });
    return () => { activeRequest = false; };
  }, [rows]);

  useEffect(() => {
    const activeSessions = Object.entries(authSessions).filter(([, session]) => !cliAuthSessionTerminal(session.state));
    if (!activeAuthSessionKey || activeSessions.length === 0) return;
    let activeRequest = true;
    const poll = async () => {
      await Promise.all(activeSessions.map(async ([key, session]) => {
        const row = rows.find((candidate) => candidate.key === key);
        if (!row?.cli) return;
        try {
          const snapshot = await api.cliAuthSession(session.session_id, row.targetID);
          if (!activeRequest) return;
          setAuthSessions((current) => ({ ...current, [key]: snapshot }));
          if (!cliAuthSessionTerminal(snapshot.state)) return;
          const status = await api.cliAuth(row.cli.spec.id, row.targetID);
          if (!activeRequest) return;
          setAuth((current) => ({ ...current, [key]: status }));
          if (snapshot.state === "succeeded") showNotice(t("tools.authReady"));
          else if (snapshot.error) showNotice(snapshot.error, true);
        } catch (error) {
          if (activeRequest) showNotice(errorMessage(error), true);
        }
      }));
    };
    void poll();
    const timer = window.setInterval(() => { void poll(); }, 1500);
    return () => {
      activeRequest = false;
      window.clearInterval(timer);
    };
  }, [activeAuthSessionKey, rows]);

  useEffect(() => {
    if (!installTarget || !candidates.some((candidate) => candidate.kind === "cli" && candidate.id === installTarget)) return;
    setInstallerOpen(true);
    const frame = window.requestAnimationFrame(() => {
      document.getElementById(`install-cli-${installTarget}`)?.scrollIntoView({ behavior: "smooth", block: "center" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [installTarget, candidates]);

  function showNotice(message: string, error = false) {
    setNotice(message);
    setNoticeError(error);
  }

  async function refreshAll() {
    setChecks({});
    await Promise.all([tools.reload(), marketplace.reload(), fleetTargets.reload()]);
  }

  function setToolBusy(key: string, action: ToolBusyAction) {
    setBusy((current) => ({ ...current, [key]: action }));
  }

  function clearToolBusy(key: string) {
    setBusy((current) => {
      const next = { ...current };
      delete next[key];
      return next;
    });
  }

  function beginProgress(key: string, phase: string) {
    setProgress((current) => ({ ...current, [key]: { phase, percent: 4, started_at: Date.now() } }));
  }

  function updateProgress(key: string, update: OperationProgress) {
    setProgress((current) => ({
      ...current,
      [key]: { ...update, started_at: current[key]?.started_at ?? Date.now() },
    }));
  }

  function clearProgress(key: string) {
    setProgress((current) => {
      const next = { ...current };
      delete next[key];
      return next;
    });
  }

  async function checkCLIUpdate(row: InstalledToolRow, silent = false) {
    if (!row.cli) return;
    setToolBusy(row.key, "check");
    if (!silent) {
      showNotice("");
      setResult(null);
    }
    try {
      const response = await api.checkCLIUpdate(row.cli.spec.id, row.targetID);
      setChecks((current) => ({ ...current, [row.key]: response }));
      if (!silent) {
        if (response.error) showNotice(`${t("tools.updateCheckFailed")}: ${response.error}`, true);
        else if (response.update_available || row.needsRepair) showNotice(t("tools.updateAvailable"));
        else showNotice(t("tools.upToDate"));
      }
    } catch (error) {
      const message = errorMessage(error);
      setChecks((current) => ({
        ...current,
        [row.key]: { id: row.cli!.spec.id, installed: true, update_available: false, error: message },
      }));
      if (!silent) showNotice(`${t("tools.updateCheckFailed")}: ${message}`, true);
    } finally {
      clearToolBusy(row.key);
    }
  }

  async function runCLIAction(row: InstalledToolRow, action: "update" | "uninstall") {
    if (!row.cli) return;
    setToolBusy(row.key, action);
    beginProgress(row.key, action === "update" ? "checking" : "preparing");
    showNotice("");
    setResult(null);
    try {
      const response = await api.installCLI(
        row.cli.spec.id,
        action,
        (update) => updateProgress(row.key, update),
        false,
        [row.targetID],
      );
      setResult(response.first);
      if (!response.first.ok) showNotice(response.first.error || t("tools.cliFailed"), true);
      else showNotice(action === "uninstall" ? t("tools.uninstalled") : t("tools.updated"));
      await refreshAll();
    } catch (error) {
      showNotice(errorMessage(error), true);
    } finally {
      clearProgress(row.key);
      clearToolBusy(row.key);
    }
  }

  function requestCLIUninstall(row: InstalledToolRow) {
    if (!row.cli) return;
    const linked = row.cli.spec.linked_skills?.map((skill) => skill.name).join(", ");
    const name = linked ? `${row.name} + ${linked}` : row.name;
    if (!window.confirm(t("tools.uninstallConfirm", { tool: name }))) return;
    void runCLIAction(row, "uninstall");
  }

  async function uninstallSkill(row: InstalledToolRow) {
    if (!row.skill || !window.confirm(t("tools.uninstallConfirm", { tool: row.name }))) return;
    setToolBusy(row.key, "uninstall");
    showNotice("");
    try {
      await api.uninstallSkill(row.skill.name, row.targetID);
      showNotice(t("tools.uninstalled"));
      await refreshAll();
    } catch (error) {
      showNotice(errorMessage(error), true);
    } finally {
      clearToolBusy(row.key);
    }
  }

  async function startCLIAuth(row: InstalledToolRow) {
    if (!row.cli) return;
    setToolBusy(row.key, "auth");
    showNotice("");
    setResult(null);
    try {
      const session = await api.startCLIAuth(row.cli.spec.id, auth[row.key]?.state === "authenticated", row.targetID);
      setAuthSessions((current) => ({ ...current, [row.key]: session }));
      if (session.login_url && isDesktopApp()) await openExternalURL(session.login_url);
      if (cliAuthSessionTerminal(session.state)) {
        const status = await api.cliAuth(row.cli.spec.id, row.targetID);
        setAuth((current) => ({ ...current, [row.key]: status }));
      } else {
        showNotice(session.phase === "setup" ? t("tools.authSetupWaiting") : t("tools.authLoginWaiting"));
      }
    } catch (error) {
      showNotice(errorMessage(error), true);
    } finally {
      clearToolBusy(row.key);
    }
  }

  async function cancelCLIAuth(row: InstalledToolRow, sessionID: string) {
    setToolBusy(row.key, "auth");
    try {
      await api.cancelCLIAuth(sessionID, row.targetID);
      const snapshot = await api.cliAuthSession(sessionID, row.targetID);
      setAuthSessions((current) => ({ ...current, [row.key]: snapshot }));
      showNotice(t("tools.authCancelled"));
    } catch (error) {
      showNotice(errorMessage(error), true);
    } finally {
      clearToolBusy(row.key);
    }
  }

  function selectedTargets(candidate: ToolInstallCandidate) {
    if (!allScope) return candidate.missingTargetIDs.slice(0, 1);
    return (installSelections[candidate.key] ?? []).filter((id) => candidate.missingTargetIDs.includes(id));
  }

  function requestInstall(candidate: ToolInstallCandidate) {
    const targetIDs = selectedTargets(candidate);
    if (targetIDs.length === 0) return;
    if (candidate.internalOnly) {
      setInternalTarget({ candidate, targetIDs });
      return;
    }
    void installCandidate(candidate, targetIDs);
  }

  async function installCandidate(candidate: ToolInstallCandidate, targetIDs: string[], acknowledgeInternal = false) {
    const key = candidate.key;
    setToolBusy(key, "install");
    beginProgress(key, "preparing");
    showNotice("");
    setResult(null);
    try {
      if (candidate.kind === "cli" && candidate.cli) {
        const response = await api.installCLI(
          candidate.cli.spec.id,
          "install",
          (update) => updateProgress(key, update),
          acknowledgeInternal,
          targetIDs,
        );
        setResult(response.first);
        const failedResults = response.successes
          .filter((item) => !item.ok)
          .map((item) => {
            const targeted = item as CLIInstallResult & TargetMetadata;
            return `${targeted.target_name || targeted.target_id || candidate.name}: ${item.error || t("tools.cliFailed")}`;
          });
        showFleetNotice(response.successes.length - failedResults.length, [...response.errors, ...failedResults]);
      } else if (candidate.skill) {
        const response = await api.installSkill(candidate.skill, targetIDs);
        showFleetNotice(response.successes.length, response.errors);
      }
      setInstallSelections((current) => ({ ...current, [candidate.key]: [] }));
      await refreshAll();
    } catch (error) {
      showNotice(errorMessage(error), true);
    } finally {
      clearProgress(key);
      clearToolBusy(key);
    }
  }

  function showFleetNotice(successes: number, errors: string[]) {
    if (errors.length > 0) {
      const detail = errors.filter(Boolean).join("; ");
      showNotice(`${t("tools.installPartial", { ready: successes, failed: errors.length })}${detail ? `: ${detail}` : ""}`, true);
      return;
    }
    showNotice(t("tools.installReady", { count: successes }));
  }

  return (
    <div className="page-stack tools-page">
      <p className="subtle-copy">{t("tools.unifiedSubtitle")}</p>
      {(tools.data?.warnings ?? []).map((warning) => <div className="session-notice" key={warning}>{warning}</div>)}
      {tools.error && <div className="surface-body error">{tools.error}</div>}
      {notice && <div className={`session-notice${noticeError ? " error" : ""}`}>{notice}</div>}

      <section className="surface unified-tools-surface">
        <div className="surface-header unified-tools-header">
          <div><h2>{t("tools.installedDirectory")}</h2><span className="pill on">{rows.length}</span></div>
          <button className="action" onClick={() => setInstallerOpen(true)} type="button"><Plus size={15} />{t("tools.installTools")}</button>
        </div>
        <div className="catalog-table-wrap">
          <table className="catalog-table unified-tools-table">
            <thead><tr><th>{t("common.name")}</th><th>{t("common.description")}</th><th>{t("common.actions")}</th></tr></thead>
            <tbody>
              {pagination.pageItems.map((row) => (
                <InstalledToolRows
                  key={row.key}
                  row={row}
                  busy={busy[row.key]}
                  progress={progress[row.key]}
                  check={checks[row.key]}
                  authSession={authSessions[row.key]}
                  onCheck={() => void checkCLIUpdate(row)}
                  onUpdate={() => void runCLIAction(row, "update")}
                  onUninstall={() => row.cli ? requestCLIUninstall(row) : void uninstallSkill(row)}
                  onAuth={() => void startCLIAuth(row)}
                  onCancelAuth={(sessionID) => void cancelCLIAuth(row, sessionID)}
                  t={t}
                />
              ))}
              {rows.length === 0 && <tr><td className="empty-state" colSpan={3}>{t("tools.noInstalled")}</td></tr>}
            </tbody>
          </table>
        </div>
        {pagination.totalPages > 1 && <CatalogPagination page={pagination.page} totalPages={pagination.totalPages} start={pagination.start} end={pagination.end} total={pagination.total} onChange={pagination.setPage} />}
      </section>

      {result?.log && (
        <section className="surface">
          <div className="surface-header"><h2>{t("tools.installLog")}</h2>{result.command && <span className="muted mono">{result.command}</span>}</div>
          <div className="surface-body"><pre className="framework-log">{result.log}</pre></div>
        </section>
      )}

      {installerOpen && (
        <ToolInstallDialog
          allScope={allScope}
          busy={busy}
          candidates={candidates}
          progress={progress}
          selections={installSelections}
          targets={scopedTargets}
          onClose={() => setInstallerOpen(false)}
          onInstall={requestInstall}
          onSelectionChange={(key, ids) => setInstallSelections((current) => ({ ...current, [key]: ids }))}
          t={t}
        />
      )}
      {internalTarget && (
        <InternalOnlyDialog
          name={internalTarget.candidate.name}
          components={internalTarget.candidate.cli?.spec.linked_skills?.map((skill) => skill.name) ?? []}
          onCancel={() => setInternalTarget(null)}
          onConfirm={() => {
            const target = internalTarget;
            setInternalTarget(null);
            void installCandidate(target.candidate, target.targetIDs, true);
          }}
        />
      )}
    </div>
  );
}

function InstalledToolRows({
  row, busy, progress, check, authSession, onCheck, onUpdate, onUninstall, onAuth, onCancelAuth, t,
}: {
  row: InstalledToolRow;
  busy?: ToolBusyAction;
  progress?: OperationProgress;
  check?: CLIUpdateCheck;
  authSession?: CLIAuthSession;
  onCheck: () => void;
  onUpdate: () => void;
  onUninstall: () => void;
  onAuth: () => void;
  onCancelAuth: (sessionID: string) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  const hasUpdate = Boolean(check?.update_available) || row.needsRepair;
  const authActive = Boolean(authSession && !cliAuthSessionTerminal(authSession.state));
  const disabled = Boolean(busy) || authActive;
  const linkedSpecs = row.cli?.spec.linked_skills ?? [];
  return (
    <>
      <tr className="catalog-row unified-tool-row">
        <td className="catalog-primary-cell" data-label={t("common.name")}>
          <span className="provider-icon">{row.cli ? <TerminalSquare size={16} /> : <Bot size={16} />}</span>
          <span className="catalog-primary-copy">
            <strong>{row.name}</strong>
            <span className="tool-meta-tags">
              <span className="pill tool-type-tag">{row.cli ? "CLI" : "Skill"}</span>
              {linkedSpecs.map((linked) => {
                const status = row.linkedSkills.find((item) => item.spec.id === linked.id);
                return <span className="pill tool-type-tag linked" key={linked.id} title={status?.detail || linked.note}>{`Skill · ${linked.name}`}</span>;
              })}
              <TargetBadge target_id={row.targetID} target_name={row.targetName} />
            </span>
          </span>
        </td>
        <td className="catalog-description-cell" data-label={t("common.description")}>{row.description || "—"}</td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
          <div className="tool-row-actions">
            {row.cli && (
              <button className={hasUpdate ? "action" : "ghost-action"} disabled={disabled} onClick={hasUpdate ? onUpdate : onCheck} type="button">
                {hasUpdate ? <Download size={14} /> : <RefreshCw className={busy === "check" ? "spin" : ""} size={14} />}
                {busy === "check" ? t("tools.checkingUpdate") : busy === "update" ? t("frameworks.installing") : hasUpdate ? t("tools.update") : t("tools.checkUpdate")}
              </button>
            )}
            <button className="ghost-action danger-action" disabled={disabled || Boolean(row.cli && !row.cli.spec.uninstall_supported)} onClick={onUninstall} title={row.cli && !row.cli.spec.uninstall_supported ? t("frameworks.uninstallUnsupported") : undefined} type="button">
              <Trash2 size={14} />{busy === "uninstall" ? t("frameworks.uninstalling") : t("frameworks.uninstall")}
            </button>
            {row.cli?.spec.login_supported && <button className="ghost-action" disabled={disabled} onClick={onAuth} type="button"><LogIn className={busy === "auth" ? "spin" : ""} size={14} />{t("tools.authLogin")}</button>}
          </div>
        </td>
      </tr>
      {progress && <tr className="catalog-progress-row"><td colSpan={3}><OperationProgressView progress={progress} /></td></tr>}
      {authSession && <tr className="catalog-progress-row cli-auth-row"><td colSpan={3}><CLIAuthPrompt session={authSession} busy={busy === "auth"} onCancel={() => onCancelAuth(authSession.session_id)} t={t} /></td></tr>}
    </>
  );
}

function ToolInstallDialog({
  candidates, targets, allScope, selections, busy, progress, onSelectionChange, onInstall, onClose, t,
}: {
  candidates: ToolInstallCandidate[];
  targets: MachineTarget[];
  allScope: boolean;
  selections: Record<string, string[]>;
  busy: Record<string, ToolBusyAction>;
  progress: Record<string, OperationProgress>;
  onSelectionChange: (key: string, ids: string[]) => void;
  onInstall: (candidate: ToolInstallCandidate) => void;
  onClose: () => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  const targetByID = new Map(targets.map((target) => [target.id, target]));
  return (
    <div className="meeting-dialog-layer framework-install-dialog-layer">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.close")} onClick={onClose} type="button" />
      <section className="surface meeting-dialog framework-install-dialog tool-install-dialog" role="dialog" aria-modal="true" aria-labelledby="tool-install-title">
        <button className="meeting-dialog-close" aria-label={t("common.close")} onClick={onClose} type="button"><X size={17} /></button>
        <div className="framework-install-dialog-copy">
          <span className="meeting-dialog-icon"><Package size={22} /></span>
          <div><h2 id="tool-install-title">{t("tools.installTools")}</h2><p>{allScope ? t("tools.installDialogFleetHint") : t("tools.installDialogHint")}</p></div>
        </div>
        <div className="framework-install-list tool-install-list">
          {candidates.map((candidate) => {
            const selected = allScope ? selections[candidate.key] ?? [] : candidate.missingTargetIDs.slice(0, 1);
            const candidateBusy = busy[candidate.key] === "install";
            return (
              <div className="framework-install-option tool-install-option" id={candidate.kind === "cli" ? `install-cli-${candidate.id}` : undefined} key={candidate.key}>
                <span className="provider-icon">{candidate.kind === "cli" ? <TerminalSquare size={16} /> : <Bot size={16} />}</span>
                <span className="framework-install-option-copy tool-install-option-copy">
                  <strong>{candidate.name}</strong>
                  <span className="tool-install-description">{candidate.description || "—"}</span>
                  <span className="framework-meta-tags">
                    <span className="pill tool-type-tag">{candidate.kind === "cli" ? "CLI" : "Skill"}</span>
                    {candidate.internalOnly && <span className="status-badge warning internal-only-badge">{t("tools.internalBadge")}</span>}
                  </span>
                  {allScope ? (
                    <span className="tool-target-checklist">
                      {candidate.missingTargetIDs.map((targetID) => {
                        const target = targetByID.get(targetID);
                        return (
                          <label className="tool-target-choice" key={targetID}>
                            <input checked={selected.includes(targetID)} disabled={candidateBusy} onChange={(event) => onSelectionChange(candidate.key, event.target.checked ? [...selected, targetID] : selected.filter((id) => id !== targetID))} type="checkbox" />
                            <span>{targetID === "local" ? t("remote.localMachine") : target?.name || targetID}</span>
                          </label>
                        );
                      })}
                    </span>
                  ) : <span className="target-badge">{candidate.missingTargetIDs[0] === "local" ? t("remote.localMachine") : targetByID.get(candidate.missingTargetIDs[0])?.name || candidate.missingTargetIDs[0]}</span>}
                </span>
                <button className="action" disabled={candidateBusy || selected.length === 0} onClick={() => onInstall(candidate)} type="button"><Plus size={14} />{candidateBusy ? t("frameworks.installing") : t("frameworks.install")}</button>
                {progress[candidate.key] && <OperationProgressView progress={progress[candidate.key]} />}
              </div>
            );
          })}
          {candidates.length === 0 && <div className="empty-state framework-install-empty">{t("tools.allInstalled")}</div>}
        </div>
      </section>
    </div>
  );
}

function CLIAuthPrompt({ session, busy, onCancel, t }: { session: CLIAuthSession; busy: boolean; onCancel: () => void; t: (key: string) => string }) {
  const active = !cliAuthSessionTerminal(session.state);
  const phaseLabel = session.phase === "setup" ? t("tools.authSetupPhase") : t("tools.authLoginPhase");
  return (
    <div className={`cli-auth-prompt ${session.state}`}>
      <div className="cli-auth-prompt-copy">
        <strong>{session.state === "succeeded" ? t("tools.authReady") : session.state === "failed" ? t("tools.authFailed") : session.state === "cancelled" ? t("tools.authCancelled") : phaseLabel}</strong>
        {active && <span>{session.phase === "setup" ? t("tools.authSetupHelp") : t("tools.authLoginHelp")}</span>}
        {session.error && <span className="error">{session.error}</span>}
      </div>
      <div className="cli-auth-prompt-actions">
        {session.login_url && active && <a className="action" href={session.login_url} onClick={(event) => { if (!isDesktopApp()) return; event.preventDefault(); void openExternalURL(session.login_url || ""); }} rel="noreferrer" target="_blank"><ExternalLink size={14} />{t("tools.openAuthLink")}</a>}
        {session.verification_code && active && <span className="cli-auth-code">{t("tools.authCode")} <code>{session.verification_code}</code></span>}
        {active && <button className="ghost-action" disabled={busy} onClick={onCancel} type="button"><X size={14} />{t("tools.authCancel")}</button>}
      </div>
    </div>
  );
}

function resolveAvailableTargets(targets: MachineTarget[], cli: Array<{ target_id?: string; target_name?: string }>, scope: string, t: (key: string) => string) {
  const byID = new Map(targets.map((target) => [target.id, target]));
  cli.forEach((item) => {
    if (!item.target_id || byID.has(item.target_id)) return;
    byID.set(item.target_id, { id: item.target_id, name: item.target_name || item.target_id, kind: item.target_id === "local" ? "local" : "ssh", trusted: true, online: true });
  });
  if (scope !== "all" && !byID.has(scope)) byID.set(scope, fallbackMachine(scope, t));
  return [...byID.values()];
}

function fallbackMachine(scope: string, t: (key: string) => string): MachineTarget {
  const id = scope === "all" ? "local" : scope;
  return { id, name: id === "local" ? t("remote.localMachine") : id, kind: id === "local" ? "local" : "ssh", trusted: true, online: true };
}

function cliAuthSessionTerminal(state: CLIAuthSession["state"]) {
  return state === "succeeded" || state === "failed" || state === "cancelled";
}

function cliInstallTarget() {
  const match = window.location.hash.match(/^#\/?skills\/cli\/([^/?]+)/);
  if (!match) return "";
  try { return decodeURIComponent(match[1]); } catch { return ""; }
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
