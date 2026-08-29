import { useEffect, useState } from "react";
import { LogIn, TriangleAlert } from "lucide-react";
import {
  activeRemoteID,
  api,
  Framework,
  FrameworkInstallResult,
  FrameworkUpdateCheck,
  OperationProgress,
} from "../api";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { InternalOnlyDialog } from "../components/InternalOnlyDialog";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { FrameworkBusyAction, FrameworkTableRows } from "./frameworks/FrameworkTableRows";
import { useFrameworkAuth } from "./frameworks/useFrameworkAuth";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const remoteHosts = useAsync(() => api.remoteHosts(), []);
  const [busy, setBusy] = useState<Record<string, FrameworkBusyAction>>({});
  const [progress, setProgress] = useState<Record<string, OperationProgress>>({});
  const [checks, setChecks] = useState<Record<string, FrameworkUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);
  const [confirming, setConfirming] = useState<Framework | null>(null);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const sortedItems = [...items].sort((left, right) => Number(right.installed) - Number(left.installed));
  const installedCount = items.filter((item) => item.installed).length;
  const frameworkPagination = useCatalogPagination(sortedItems);
  const selectedRemoteID = activeRemoteID();
  const currentMachine = selectedRemoteID
    ? remoteHosts.data?.find((host) => host.id === selectedRemoteID)?.name || t("remote.currentMachine")
    : t("remote.localMachine");

  function markBusy(kind: string, action: FrameworkBusyAction) {
    setBusy((current) => ({ ...current, [kind]: action }));
  }

  function clearBusy(kind: string) {
    setBusy((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    });
  }

  const frameworkAuth = useFrameworkAuth({
    items,
    targetID: selectedRemoteID,
    currentMachine,
    markBusy,
    clearBusy,
    setNotice,
  });

  useEffect(() => {
    items.forEach((item) => {
      const kind = item.spec.kind;
      if (!item.spec.update_supported || !item.spec.supported || !item.installed || checks[kind] || busy[kind]) return;
      void checkUpdate(kind, true);
    });
  }, [items, busy, checks]);

  function forgetCheck(kind: string) {
    setChecks((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    });
  }

  function beginProgress(kind: string, phase: string) {
    setProgress((current) => ({ ...current, [kind]: { phase, percent: 4, started_at: Date.now() } }));
  }

  function updateProgress(kind: string, update: OperationProgress) {
    setProgress((current) => ({
      ...current,
      [kind]: { ...update, started_at: current[kind]?.started_at ?? Date.now() },
    }));
  }

  function clearProgress(kind: string) {
    setProgress((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    });
  }

  async function checkUpdate(kind: string, silent = false) {
    markBusy(kind, "check");
    if (!silent) {
      setNotice("");
      setResult(null);
    }
    try {
      const response = await api.checkFrameworkUpdate(kind);
      setChecks((current) => ({ ...current, [kind]: response }));
      if (!silent) {
        if (response.error) setNotice(`${t("tools.updateCheckFailed")}: ${response.error}`);
        else if (response.update_available) setNotice(`${t("tools.updateAvailable")}: ${response.current_version || "?"} -> ${response.latest_version || "?"}`);
        else setNotice(t("tools.upToDate"));
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setChecks((current) => ({ ...current, [kind]: { kind, installed: true, update_available: false, error: message } }));
      if (!silent) setNotice(`${t("tools.updateCheckFailed")}: ${message}`);
    } finally {
      clearBusy(kind);
    }
  }

  async function install(kind: string, action: "install" | "update", acknowledgeInternal = false) {
    markBusy(kind, action);
    beginProgress(kind, action === "update" ? "checking" : "preparing");
    setNotice("");
    setResult(null);
    if (action === "install") forgetCheck(kind);
    try {
      const response = await api.installFramework(kind, action, (update) => updateProgress(kind, update), acknowledgeInternal);
      setResult(response);
      setNotice(response.ok ? t("frameworks.installed") : frameworkInstallFailureNotice(response, t));
      await frameworks.reload();
      forgetCheck(kind);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      clearProgress(kind);
      clearBusy(kind);
    }
  }

  const nodeMissing = prereqs && (!prereqs.node || !prereqs.npm);

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("frameworks.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("frameworks.prereqTitle")}</h2>
          <span className="pill on">{installedCount} / {items.length}</span>
        </div>
        <div className="surface-body">
          <div className="framework-prereqs">
            <span className={`status-badge ${prereqs?.node ? "success" : ""}`}><span className="status-dot" />node {prereqs?.node ? t("common.enabled") : t("frameworks.missing")}</span>
            <span className={`status-badge ${prereqs?.npm ? "success" : ""}`}><span className="status-dot" />npm {prereqs?.npm ? t("common.enabled") : t("frameworks.missing")}</span>
          </div>
          {nodeMissing && <div className="framework-hint"><TriangleAlert size={15} /><span>{t("frameworks.nodeHint")}</span></div>}
        </div>
      </section>

      {notice && <div className={`session-notice${result && !result.ok ? " error" : ""}`}>{notice}</div>}

      <section className="surface">
        <div className="surface-header">
          <h2>{t("frameworks.catalogTitle")}</h2>
          <div className="table-actions">
            {frameworkAuth.relevantAuth.length > 0 && (
              <span className={`pill ${frameworkAuth.readyAuthCount === frameworkAuth.relevantAuth.length ? "on" : ""}`}>
                <LogIn size={13} />
                {t("frameworks.authSummary", { ready: frameworkAuth.readyAuthCount, total: frameworkAuth.relevantAuth.length })}
              </span>
            )}
            <span className="pill on">{items.length}</span>
          </div>
        </div>
        {frameworks.error && <div className="surface-body error">{frameworks.error}</div>}
        <div className="catalog-table-wrap">
          <table className="catalog-table framework-table">
            <thead><tr>
              <th>{t("common.name")}</th><th>{t("common.type")}</th><th>{t("frameworks.requirements")}</th>
              <th>{t("common.status")}</th><th>{t("common.actions")}</th>
            </tr></thead>
            <tbody>
              {frameworkPagination.pageItems.map((item) => (
                <FrameworkTableRows
                  key={item.spec.kind}
                  item={item}
                  busy={busy[item.spec.kind]}
                  progress={progress[item.spec.kind]}
                  check={checks[item.spec.kind]}
                  auth={frameworkAuth.auth[item.spec.kind]}
                  loginFlow={frameworkAuth.loginFlows[item.spec.kind]}
                  loginCode={frameworkAuth.loginCodes[item.spec.kind] ?? ""}
                  copiedCode={frameworkAuth.copiedCode === item.spec.kind}
                  currentMachine={currentMachine}
                  disabled={(item.spec.install_requires_npm && Boolean(nodeMissing)) || !item.spec.supported}
                  onCheck={() => void checkUpdate(item.spec.kind)}
                  onInstall={(action) => {
                    if (action === "install" && item.spec.internal_only) {
                      setConfirming(item);
                      return;
                    }
                    void install(item.spec.kind, action);
                  }}
                  onAuth={() => void frameworkAuth.startAuth(item.spec.kind)}
                  onConfigureCredentials={() => { window.location.hash = "#providers"; }}
                  onLoginCodeChange={(code) => frameworkAuth.setLoginCode(item.spec.kind, code)}
                  onCompleteAuth={(sessionID) => void frameworkAuth.completeAuth(item.spec.kind, sessionID)}
                  onCancelAuth={(sessionID) => void frameworkAuth.cancelAuth(item.spec.kind, sessionID)}
                  onDismissAuth={() => frameworkAuth.dismissAuth(item.spec.kind)}
                  onCopyCode={(code) => void frameworkAuth.copyCode(item.spec.kind, code)}
                />
              ))}
              {sortedItems.length === 0 && <tr><td className="empty-state" colSpan={5}>{t("frameworks.empty")}</td></tr>}
            </tbody>
          </table>
        </div>
        <CatalogPagination
          page={frameworkPagination.page}
          totalPages={frameworkPagination.totalPages}
          start={frameworkPagination.start}
          end={frameworkPagination.end}
          total={frameworkPagination.total}
          onChange={frameworkPagination.setPage}
        />
      </section>

      {result?.log && (
        <section className="surface">
          <div className="surface-header"><h2>{t("frameworks.installLog")}</h2>{result.command && <span className="muted mono">{result.command}</span>}</div>
          <div className="surface-body"><pre className="framework-log">{result.log}</pre></div>
        </section>
      )}
      {confirming && (
        <InternalOnlyDialog
          name={confirming.spec.display}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const kind = confirming.spec.kind;
            setConfirming(null);
            void install(kind, "install", true);
          }}
        />
      )}
    </div>
  );
}

function frameworkInstallFailureNotice(result: FrameworkInstallResult, t: (key: string) => string): string {
  const summary = result.error || t("frameworks.installFailed");
  const lines = (result.log || "").split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const detail = lines[lines.length - 1];
  return detail && !summary.includes(detail) ? `${summary}: ${detail}` : summary;
}
