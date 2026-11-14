import { useEffect, useMemo, useRef, useState } from "react";
import { Package, Play, Plus, Settings2, Trash2, TriangleAlert, X } from "lucide-react";
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
import { MachineInstances } from "../components/MachineInstances";
import { groupResources, selectedGroupMember } from "../components/resourceGroups";
import { targetKey } from "../components/TargetBadge";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { FrameworkBusyAction, FrameworkInstallingRow, FrameworkTableRows } from "./frameworks/FrameworkTableRows";
import { frameworkCompanyName } from "./frameworks/frameworkPresentation";
import { useFrameworkAuth } from "./frameworks/useFrameworkAuth";
import { latestHealthResult, useFrameworkHealth } from "./frameworks/useFrameworkHealth";
import { FrameworkModelsDialog } from "./frameworks/FrameworkModelsDialog";
import { frameworkUpdateCandidates } from "./frameworks/frameworkBulkUpdate";
import { BulkUpdateButton, BulkUpdateProgress, BulkUpdateResults } from "../components/BulkUpdate";
import { BulkUpdateResult, runBulkUpdates } from "../components/bulkUpdateModel";
import {
  FrameworkAction,
  FrameworkOperation,
  finishFrameworkOperation,
  startFrameworkOperation,
  useFrameworkOperations,
} from "./frameworks/frameworkOperations";
import { CustomCommandConfirmDialog, CustomCommandEntry, FrameworkInstallSettingsDialog } from "./frameworks/FrameworkInstallSettingsDialog";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const remoteHosts = useAsync(() => api.remoteHosts(), []);
  // Install, update and uninstall run in a module-level store so they survive
  // navigating away; local state covers short-lived row actions and bulk updates.
  const operations = useFrameworkOperations();
  const [localBusy, setBusy] = useState<Record<string, FrameworkBusyAction>>({});
  const [localProgress, setProgress] = useState<Record<string, OperationProgress>>({});
  const busy = useMemo(() => ({
    ...Object.fromEntries(Object.values(operations).map((operation) => [operation.key, operation.action as FrameworkBusyAction])),
    ...localBusy,
  }), [operations, localBusy]);
  const progress = useMemo(() => ({
    ...localProgress,
    ...Object.fromEntries(Object.values(operations).map((operation) => [operation.key, operation.progress])),
  }), [operations, localProgress]);
  const handledOutcomes = useRef(new Set<FrameworkOperation>());
  const [checks, setChecks] = useState<Record<string, FrameworkUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);
  const [confirming, setConfirming] = useState<Framework | null>(null);
  const [uninstallCandidate, setUninstallCandidate] = useState<Framework | null>(null);
  const [installerOpen, setInstallerOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [pendingCustom, setPendingCustom] = useState<{ entries: CustomCommandEntry[]; run: () => void } | null>(null);
  const [bulkProgress, setBulkProgress] = useState<BulkUpdateProgress | null>(null);
  const [bulkResults, setBulkResults] = useState<BulkUpdateResult<Framework>[]>([]);
  const [modelsFor, setModelsFor] = useState<Framework | null>(null);
  const bulkRunning = useRef(false);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const installedItems = useMemo(() => items
    .filter((item) => item.installed)
    .sort((left, right) => left.spec.display.localeCompare(right.spec.display)), [items]);
  const installableItems = useMemo(() => items
    .filter((item) => !item.installed && item.spec.supported && item.spec.install_supported)
    .sort((left, right) => left.spec.display.localeCompare(right.spec.display)), [items]);
  const installingItems = installableItems.filter((item) => operations[targetKey(item.target_id, item.spec.kind)]?.action === "install");
  const [selectedInstances, setSelectedInstances] = useState<Record<string, string>>({});
  const installedGroups = groupResources(installedItems, (item) => item.spec.kind, (item) => targetKey(item.target_id, item.spec.kind), activeMachineScope() === "all");
  const frameworkPagination = useCatalogPagination(installedGroups);
  const selectedRemoteID = activeRemoteID();
  const currentMachine = activeMachineScope() === "all"
    ? t("remote.allMachines")
    : selectedRemoteID
      ? remoteHosts.data?.find((host) => host.id === selectedRemoteID)?.name || t("remote.currentMachine")
      : t("remote.localMachine");
  const nodeMissing = Boolean(prereqs && (!prereqs.node || !prereqs.npm));
  // Install settings belong to one machine and are edited on that machine.
  const settingsEditable = activeMachineScope() === "local";

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

  const health = useFrameworkHealth(installedItems);
  // Test all covers the instances shown: one per framework, on its selected machine.
  const testCandidates = frameworkPagination.pageItems
    .map((group) => selectedGroupMember(group, selectedInstances[group.key], (item) => targetKey(item.target_id, item.spec.kind)))
    .filter((item) => item.registered && item.spec.supported);

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

  function runFrameworkAction(item: Framework, action: FrameworkAction, acknowledgeInternal = false, confirmedCommand?: string) {
    const key = targetKey(item.target_id, item.spec.kind);
    setNotice("");
    setNoticeError(false);
    setResult(null);
    setBulkResults([]);
    if (action !== "update") forgetCheck(key);
    return startFrameworkOperation(item, action, acknowledgeInternal, confirmedCommand);
  }

  // Custom commands from a machine's install settings only run after the user
  // sees them; `run` receives the confirmed text per framework instance.
  function confirmCustomCommands(requests: { item: Framework; action: "install" | "update" }[], run: (confirmed: Record<string, string>) => void) {
    const entries = requests.flatMap(({ item, action }) => {
      const plan = item.commands?.[action];
      if (!plan?.custom) return [];
      const machine = item.target_name || item.target_id;
      return [{ key: targetKey(item.target_id, item.spec.kind), name: machine ? `${item.spec.display} · ${machine}` : item.spec.display, action, command: plan.command }];
    });
    const confirmed = Object.fromEntries(entries.map((entry) => [entry.key, entry.command]));
    if (entries.length === 0) {
      run(confirmed);
      return;
    }
    setPendingCustom({ entries, run: () => run(confirmed) });
  }

  // Report operations that settled, including ones that finished while this
  // page was not mounted, then refresh the catalog before dropping their rows.
  useEffect(() => {
    const settled = Object.values(operations).filter((operation) => operation.outcome && !handledOutcomes.current.has(operation));
    if (settled.length === 0) return;
    settled.forEach((operation) => handledOutcomes.current.add(operation));
    const { action, outcome } = settled[settled.length - 1];
    if (outcome?.result) {
      setResult(outcome.result);
      setNoticeError(!outcome.result.ok);
      setNotice(outcome.result.ok ? frameworkActionSuccessNotice(action, t) : frameworkInstallFailureNotice(outcome.result, t));
    } else {
      setResult(null);
      setNoticeError(true);
      setNotice(outcome?.error || t("frameworks.installFailed"));
    }
    void frameworks.reload().finally(() => {
      settled.forEach((operation) => {
        forgetCheck(operation.key);
        finishFrameworkOperation(operation.key);
      });
    });
  }, [operations]);

  function requestUpdateAll() {
    if (bulkRunning.current || updateAllBlocked || updateCandidates.length === 0) return;
    // Capture the selection before starting: later scope changes must never
    // redirect a queued update to a different machine (or to the whole fleet).
    const targetID = selectedRemoteID || "local";
    const candidates = [...updateCandidates];
    confirmCustomCommands(candidates.map((item) => ({ item, action: "update" })), (confirmed) => {
      void updateAllFrameworks(candidates, targetID, confirmed);
    });
  }

  async function updateAllFrameworks(candidates: Framework[], targetID: string, confirmed: Record<string, string>) {
    if (bulkRunning.current) return;
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
          return await api.installFramework(item.spec.kind, "update", (update) => updateProgress(key, update), false, item.target_id || targetID, confirmed[key]);
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

  // Installs can take minutes, so the dialog closes as soon as one starts and
  // its progress continues in the catalog table.
  function startInstall(item: Framework, acknowledgeInternal = false) {
    setInstallerOpen(false);
    const key = targetKey(item.target_id, item.spec.kind);
    confirmCustomCommands([{ item, action: "install" }], (confirmed) => {
      void runFrameworkAction(item, "install", acknowledgeInternal, confirmed[key]);
    });
  }

  function requestUpdate(item: Framework) {
    const key = targetKey(item.target_id, item.spec.kind);
    confirmCustomCommands([{ item, action: "update" }], (confirmed) => {
      void runFrameworkAction(item, "update", false, confirmed[key]);
    });
  }

  function requestInstall(item: Framework) {
    if (item.spec.internal_only) {
      setConfirming(item);
      return;
    }
    startInstall(item);
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
            <span className="pill on">{installedGroups.length}</span>
          </div>
          <div className="catalog-bulk-actions">
            <button
              className="ghost-action"
              disabled={health.testingAll || testCandidates.length === 0 || Boolean(bulkProgress)}
              onClick={() => void health.testAll(testCandidates)}
              title={t("frameworks.health.testAllHint", { machine: currentMachine })}
              type="button"
            >
              <Play size={15} />{health.testingAll ? t("frameworks.health.testing") : t("frameworks.health.testAll")}
            </button>
            <BulkUpdateButton
              count={updateCandidates.length}
              progress={bulkProgress}
              disabled={updateAllBlocked}
              onClick={requestUpdateAll}
              hint={t("frameworks.updateAllHint", { machine: currentMachine })}
            />
            <button
              className="ghost-action"
              disabled={!settingsEditable}
              onClick={() => setSettingsOpen(true)}
              title={settingsEditable ? undefined : t("frameworks.installSettingsLocalOnly")}
              type="button"
            >
              <Settings2 size={15} />{t("frameworks.installSettings")}
            </button>
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
              <th>{t("frameworks.availability")}</th>
              <th>{t("common.actions")}</th>
            </tr></thead>
            <tbody>
              {installingItems.map((item) => {
                const key = targetKey(item.target_id, item.spec.kind);
                return <FrameworkInstallingRow key={`installing:${key}`} item={item} progress={progress[key]} />;
              })}
              {frameworkPagination.pageItems.map((group) => {
                const item = selectedGroupMember(group, selectedInstances[group.key], (item) => targetKey(item.target_id, item.spec.kind));
                const key = targetKey(item.target_id, item.spec.kind);
                return <FrameworkTableRows
                  key={group.key}
                  machineInstances={<MachineInstances selected={key}
                    instances={group.members.map((item) => ({ key: targetKey(item.target_id, item.spec.kind), targetID: item.target_id, name: item.target_name, detail: item.version ? `v${item.version}` : "—" }))}
                    onSelect={(key) => setSelectedInstances((current) => ({ ...current, [group.key]: key }))} />}
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
                  health={latestHealthResult(health.results[key])}
                  testing={Object.keys(health.testing[key] ?? {}).length > 0}
                  onTest={() => void health.test(item)}
                  onModels={() => setModelsFor(item)}
                  onCheck={() => void checkUpdate(item)}
                  onInstall={(action) => (action === "update" ? requestUpdate(item) : startInstall(item))}
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
              {installedItems.length === 0 && installingItems.length === 0 && <tr><td className="empty-state" colSpan={4}>{t("frameworks.noInstalled")}</td></tr>}
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

      {modelsFor && (
        <FrameworkModelsDialog
          item={modelsFor}
          results={health.results[targetKey(modelsFor.target_id, modelsFor.spec.kind)] ?? {}}
          testing={health.testing[targetKey(modelsFor.target_id, modelsFor.spec.kind)] ?? {}}
          onTest={(model) => void health.test(modelsFor, model)}
          onClose={() => setModelsFor(null)}
        />
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
      {settingsOpen && (
        <FrameworkInstallSettingsDialog
          onClose={() => setSettingsOpen(false)}
          onSaved={() => {
            setSettingsOpen(false);
            setChecks({});
            setResult(null);
            setNoticeError(false);
            setNotice(t("frameworks.settingsSaved"));
            void frameworks.reload();
          }}
        />
      )}
      {pendingCustom && (
        <CustomCommandConfirmDialog
          entries={pendingCustom.entries}
          onCancel={() => setPendingCustom(null)}
          onConfirm={() => {
            const { run } = pendingCustom;
            setPendingCustom(null);
            run();
          }}
        />
      )}
      {confirming && (
        <InternalOnlyDialog
          name={confirming.spec.display}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const item = confirming;
            setConfirming(null);
            startInstall(item, true);
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
      <section aria-labelledby="framework-uninstall-title" aria-modal="true" className="surface meeting-dialog confirm-delete-dialog" role="alertdialog">
        <div className="meeting-dialog-icon confirm-delete-dialog-icon"><Trash2 size={22} /></div>
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
