import { useEffect, useMemo, useRef, useState } from "react";
import { Package, Plus, Trash2, TriangleAlert, X } from "lucide-react";
import {
  activeMachineScope,
  activeRemoteID,
  api,
  Framework,
  FrameworkInstallResult,
  FrameworkUpdateCheck,
  OperationProgress,
} from "../api";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { InternalOnlyDialog } from "../components/InternalOnlyDialog";
import { targetKey } from "../components/TargetBadge";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { FrameworkBusyAction, FrameworkTableRows } from "./frameworks/FrameworkTableRows";
import { frameworkCompanyName } from "./frameworks/frameworkPresentation";
import { useFrameworkAuth } from "./frameworks/useFrameworkAuth";
import { frameworkUpdateCandidates } from "./frameworks/frameworkBulkUpdate";
import { BulkUpdateButton, BulkUpdateProgress, BulkUpdateResults } from "../components/BulkUpdate";
import { BulkUpdateResult, runBulkUpdates } from "../components/bulkUpdateModel";

type FrameworkAction = "install" | "update" | "uninstall";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const remoteHosts = useAsync(() => api.remoteHosts(), []);
  const [busy, setBusy] = useState<Record<string, FrameworkBusyAction>>({});
  const [progress, setProgress] = useState<Record<string, OperationProgress>>({});
  const [checks, setChecks] = useState<Record<string, FrameworkUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);
  const [confirming, setConfirming] = useState<Framework | null>(null);
  const [uninstallCandidate, setUninstallCandidate] = useState<Framework | null>(null);
  const [installerOpen, setInstallerOpen] = useState(false);
  const [bulkProgress, setBulkProgress] = useState<BulkUpdateProgress | null>(null);
  const [bulkResults, setBulkResults] = useState<BulkUpdateResult<Framework>[]>([]);
  const bulkRunning = useRef(false);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const installedItems = useMemo(() => items
    .filter((item) => item.installed)
    .sort((left, right) => left.spec.display.localeCompare(right.spec.display)), [items]);
  const installableItems = useMemo(() => items
    .filter((item) => !item.installed && item.spec.supported && item.spec.install_supported)
    .sort((left, right) => left.spec.display.localeCompare(right.spec.display)), [items]);
  const frameworkPagination = useCatalogPagination(installedItems);
  const selectedRemoteID = activeRemoteID();
  const currentMachine = activeMachineScope() === "all"
    ? t("remote.allMachines")
    : selectedRemoteID
      ? remoteHosts.data?.find((host) => host.id === selectedRemoteID)?.name || t("remote.currentMachine")
      : t("remote.localMachine");
  const nodeMissing = Boolean(prereqs && (!prereqs.node || !prereqs.npm));

  function markBusy(key: string, action: FrameworkBusyAction) {
    setBusy((current) => ({ ...current, [key]: action }));
  }

  function clearBusy(key: string) {
    setBusy((current) => {
      const next = { ...current };
      delete next[key];
      return next;
    });
  }

  const frameworkAuth = useFrameworkAuth({
    items: installedItems,
    currentMachine,
    markBusy,
    clearBusy,
    setNotice: (value, error = false) => {
      if (typeof value === "string") setNoticeError(error);
      setNotice(value);
    },
  });

  const updateCandidates = frameworkUpdateCandidates(installedItems, checks);
  const updateAllBlocked = frameworks.loading || Boolean(frameworks.error) || Boolean(bulkProgress)
    || Object.keys(busy).length > 0
    || Object.values(frameworkAuth.loginFlows).some((flow) => flow.state === "waiting");

  useEffect(() => {
    if (bulkRunning.current) return;
    installedItems.forEach((item) => {
      const key = targetKey(item.target_id, item.spec.kind);
      if (!item.spec.update_supported || checks[key] || busy[key]) return;
      void checkUpdate(item, true);
    });
  }, [installedItems, busy, checks, bulkProgress]);

  function forgetCheck(key: string) {
    setChecks((current) => {
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

  async function checkUpdate(item: Framework, silent = false) {
    const key = targetKey(item.target_id, item.spec.kind);
    markBusy(key, "check");
    if (!silent) {
      setNotice("");
      setNoticeError(false);
      setResult(null);
      setBulkResults([]);
    }
    try {
      const response = await api.checkFrameworkUpdate(item.spec.kind, item.target_id);
      setChecks((current) => ({ ...current, [key]: response }));
      if (!silent) {
        setNoticeError(Boolean(response.error));
        if (response.error) setNotice(`${t("tools.updateCheckFailed")}: ${response.error}`);
        else if (response.update_available) setNotice(`${t("tools.updateAvailable")}: ${response.current_version || "?"} -> ${response.latest_version || "?"}`);
        else setNotice(t("tools.upToDate"));
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setChecks((current) => ({ ...current, [key]: { kind: item.spec.kind, installed: true, update_available: false, error: message } }));
      if (!silent) { setNoticeError(true); setNotice(`${t("tools.updateCheckFailed")}: ${message}`); }
    } finally {
      clearBusy(key);
    }
  }

  async function runFrameworkAction(item: Framework, action: FrameworkAction, acknowledgeInternal = false) {
    const key = targetKey(item.target_id, item.spec.kind);
    markBusy(key, action);
    beginProgress(key, action === "update" ? "checking" : "preparing");
    setNotice("");
    setNoticeError(false);
    setResult(null);
    setBulkResults([]);
    if (action !== "update") forgetCheck(key);
    try {
      const response = await api.installFramework(item.spec.kind, action, (update) => updateProgress(key, update), acknowledgeInternal, item.target_id);
      setResult(response);
      setNoticeError(!response.ok);
      setNotice(response.ok ? frameworkActionSuccessNotice(action, t) : frameworkInstallFailureNotice(response, t));
      await frameworks.reload();
      forgetCheck(key);
      return response.ok;
    } catch (error) {
      setNoticeError(true);
      setNotice(error instanceof Error ? error.message : String(error));
      return false;
    } finally {
      clearProgress(key);
      clearBusy(key);
    }
  }

  async function updateAllFrameworks() {
    if (bulkRunning.current || updateAllBlocked || updateCandidates.length === 0) return;
    // Capture the selection before starting: later scope changes must never
    // redirect a queued update to a different machine (or to the whole fleet).
    const targetID = selectedRemoteID || "local";
    const candidates = [...updateCandidates];
    bulkRunning.current = true;
    setBulkProgress({ completed: 0, total: candidates.length });
    setBulkResults([]);
    setNotice("");
    setNoticeError(false);
    setResult(null);
    try {
      await runBulkUpdates(candidates, async (item) => {
        const key = targetKey(item.target_id, item.spec.kind);
        markBusy(key, "update");
        beginProgress(key, "checking");
        try {
          return await api.installFramework(item.spec.kind, "update", (update) => updateProgress(key, update), false, item.target_id || targetID);
        } finally {
          clearProgress(key);
          clearBusy(key);
          forgetCheck(key);
        }
      }, (entry, completed) => {
        setBulkResults((current) => [...current, entry]);
        setBulkProgress({ completed, total: candidates.length });
      });
      await frameworks.reload();
    } finally {
      bulkRunning.current = false;
      setBulkProgress(null);
    }
  }

  function requestInstall(item: Framework) {
    if (item.spec.internal_only) {
      setConfirming(item);
      return;
    }
    void runFrameworkAction(item, "install").then((ok) => {
      if (ok) setInstallerOpen(false);
    });
  }

  function requestUninstall(item: Framework) {
    setNotice("");
    setResult(null);
    setBulkResults([]);
    setUninstallCandidate(item);
  }

  return (
    <div className="page-stack framework-page">
      {notice && <div className={`session-notice${noticeError || result && !result.ok ? " error" : ""}`} role="status">{notice}</div>}

      <BulkUpdateResults progress={bulkProgress} results={bulkResults.map(({ item, result }) => ({
        key: targetKey(item.target_id, item.spec.kind),
        label: `${item.spec.display} · ${item.target_name || item.target_id || currentMachine}`,
        result,
      }))} />

      <section className="surface framework-catalog-surface">
        <div className="surface-header framework-catalog-header">
          <div>
            <h2>{t("frameworks.catalogTitle")}</h2>
            <span className="pill on">{installedItems.length}</span>
          </div>
          <div className="catalog-bulk-actions">
            <BulkUpdateButton
              count={updateCandidates.length}
              progress={bulkProgress}
              disabled={updateAllBlocked}
              onClick={() => void updateAllFrameworks()}
              hint={t("frameworks.updateAllHint", { machine: currentMachine })}
            />
            <button className="action" disabled={Boolean(bulkProgress)} onClick={() => setInstallerOpen(true)} type="button">
              <Plus size={15} />{t("frameworks.installFramework")}
            </button>
          </div>
        </div>
        {activeMachineScope() !== "all" && (frameworks.data?.warnings ?? []).map((warning) => <div className="session-notice warning framework-warning" key={warning}>{warning}</div>)}
        {frameworks.error && <div className="surface-body error">{frameworks.error}</div>}
        <div className="catalog-table-wrap">
          <table className="catalog-table framework-table">
            <thead><tr>
              <th>{t("frameworks.framework")}</th>
              <th>{t("frameworks.versionAndUpdate")}</th>
              <th>{t("common.actions")}</th>
            </tr></thead>
            <tbody>
              {frameworkPagination.pageItems.map((item) => {
                const key = targetKey(item.target_id, item.spec.kind);
                return <FrameworkTableRows
                  key={key}
                  item={item}
                  busy={busy[key]}
                  progress={progress[key]}
                  check={checks[key]}
                  auth={frameworkAuth.auth[key]}
                  loginFlow={frameworkAuth.loginFlows[key]}
                  loginCode={frameworkAuth.loginCodes[key] ?? ""}
                  copiedCode={frameworkAuth.copiedCode === key}
                  currentMachine={item.target_name || item.target_id || currentMachine}
                  disabled={!item.spec.supported || Boolean(bulkProgress)}
                  onCheck={() => void checkUpdate(item)}
                  onInstall={(action) => void runFrameworkAction(item, action)}
                  onUninstall={() => requestUninstall(item)}
                  onAuth={() => void frameworkAuth.startAuth(item)}
                  onLogout={() => void frameworkAuth.logout(item)}
                  onLoginCodeChange={(code) => frameworkAuth.setLoginCode(item, code)}
                  onCompleteAuth={(sessionID) => void frameworkAuth.completeAuth(item, sessionID)}
                  onCancelAuth={(sessionID) => void frameworkAuth.cancelAuth(item, sessionID)}
                  onDismissAuth={() => frameworkAuth.dismissAuth(item)}
                  onCopyCode={(code) => void frameworkAuth.copyCode(item, code)}
                />
              })}
              {installedItems.length === 0 && <tr><td className="empty-state" colSpan={3}>{t("frameworks.noInstalled")}</td></tr>}
            </tbody>
          </table>
        </div>
        {frameworkPagination.totalPages > 1 && (
          <CatalogPagination
            page={frameworkPagination.page}
            totalPages={frameworkPagination.totalPages}
            start={frameworkPagination.start}
            end={frameworkPagination.end}
            total={frameworkPagination.total}
            onChange={frameworkPagination.setPage}
          />
        )}
      </section>

      {result?.log && (
        <section className="surface">
          <div className="surface-header"><h2>{t("frameworks.installLog")}</h2>{result.command && <span className="muted mono">{result.command}</span>}</div>
          <div className="surface-body"><pre className="framework-log">{result.log}</pre></div>
        </section>
      )}

      {installerOpen && (
        <FrameworkInstallDialog
          busy={busy}
          items={installableItems}
          nodeMissing={nodeMissing}
          onClose={() => setInstallerOpen(false)}
          onInstall={requestInstall}
          t={t}
        />
      )}
      {confirming && (
        <InternalOnlyDialog
          name={confirming.spec.display}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const item = confirming;
            setConfirming(null);
            void runFrameworkAction(item, "install", true).then((ok) => {
              if (ok) setInstallerOpen(false);
            });
          }}
        />
      )}
      {uninstallCandidate && (
        <FrameworkUninstallDialog
          busy={busy[targetKey(uninstallCandidate.target_id, uninstallCandidate.spec.kind)] === "uninstall"}
          error={notice}
          item={uninstallCandidate}
          onClose={() => {
            if (!busy[targetKey(uninstallCandidate.target_id, uninstallCandidate.spec.kind)]) {
              setUninstallCandidate(null);
              setNotice("");
            }
          }}
          onConfirm={() => void runFrameworkAction(uninstallCandidate, "uninstall").then((ok) => {
            if (ok) setUninstallCandidate(null);
          })}
        />
      )}
    </div>
  );
}

function FrameworkInstallDialog({
  items,
  busy,
  nodeMissing,
  onClose,
  onInstall,
  t,
}: {
  items: Framework[];
  busy: Record<string, FrameworkBusyAction>;
  nodeMissing: boolean;
  onClose: () => void;
  onInstall: (item: Framework) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  return (
    <div className="meeting-dialog-layer framework-install-dialog-layer">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.close")} onClick={onClose} type="button" />
      <section className="surface meeting-dialog framework-install-dialog" role="dialog" aria-modal="true" aria-labelledby="framework-install-title">
        <button className="meeting-dialog-close" aria-label={t("common.close")} onClick={onClose} type="button"><X size={17} /></button>
        <div className="framework-install-dialog-copy">
          <span className="meeting-dialog-icon"><Package size={22} /></span>
          <div>
            <h2 id="framework-install-title">{t("frameworks.installFramework")}</h2>
            <p>{t("frameworks.installDialogHint")}</p>
          </div>
        </div>
        {nodeMissing && (
          <div className="framework-hint"><TriangleAlert size={15} /><span>{t("frameworks.nodeHint")}</span></div>
        )}
        <div className="framework-install-list">
          {items.map((item) => {
            const installDisabled = item.spec.install_requires_npm && nodeMissing;
            return (
              <div className="framework-install-option" key={targetKey(item.target_id, item.spec.kind)}>
                <span className="provider-icon"><Package size={16} /></span>
                <span className="framework-install-option-copy">
                  <strong>{item.spec.display}</strong>
                  <span className="framework-meta-tags">
                    <span className="pill framework-company">{frameworkCompanyName(item.spec)}</span>
                    <span className="pill framework-type">{item.spec.kind_type.toUpperCase()}</span>
                  </span>
                </span>
                <button className="action" disabled={installDisabled || Boolean(busy[targetKey(item.target_id, item.spec.kind)])} onClick={() => onInstall(item)} type="button">
                  <Plus size={14} />{busy[targetKey(item.target_id, item.spec.kind)] === "install" ? t("frameworks.installing") : t("frameworks.install")}
                </button>
              </div>
            );
          })}
          {items.length === 0 && <div className="empty-state framework-install-empty">{t("frameworks.allInstalled")}</div>}
        </div>
      </section>
    </div>
  );
}

function FrameworkUninstallDialog({
  item,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  item: Framework;
  busy: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button
        aria-label={t("common.cancel")}
        className="meeting-dialog-backdrop internal-dialog-backdrop"
        disabled={busy}
        onClick={onClose}
        type="button"
      />
      <section aria-labelledby="framework-uninstall-title" aria-modal="true" className="surface meeting-dialog tenant-delete-dialog" role="alertdialog">
        <div className="meeting-dialog-icon tenant-delete-dialog-icon"><Trash2 size={22} /></div>
        <div className="meeting-dialog-heading">
          <h2 id="framework-uninstall-title">{t("frameworks.uninstallTitle", { framework: item.spec.display })}</h2>
          <p>{t("frameworks.uninstallConfirm", { framework: item.spec.display })}</p>
        </div>
        {error && <div className="session-notice error">{error}</div>}
        <div className="meeting-dialog-actions">
          <button className="ghost-action" disabled={busy} onClick={onClose} type="button">{t("common.cancel")}</button>
          <button className="ghost-action danger-action" disabled={busy} onClick={onConfirm} type="button">
            <Trash2 size={14} />{busy ? t("frameworks.uninstalling") : t("frameworks.uninstall")}
          </button>
        </div>
      </section>
    </div>
  );
}

function frameworkActionSuccessNotice(action: FrameworkAction, t: (key: string) => string) {
  if (action === "uninstall") return t("frameworks.uninstalled");
  if (action === "update") return t("frameworks.updated");
  return t("frameworks.installed");
}

function frameworkInstallFailureNotice(result: FrameworkInstallResult, t: (key: string) => string): string {
  const summary = result.error || t("frameworks.installFailed");
  const lines = (result.log || "").split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const detail = lines[lines.length - 1];
  return detail && !summary.includes(detail) ? `${summary}: ${detail}` : summary;
}
