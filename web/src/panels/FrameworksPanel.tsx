import { useEffect, useState } from "react";
import { CheckCircle2, Download, Package, RefreshCw, TriangleAlert } from "lucide-react";
import { api, Framework, FrameworkInstallResult, FrameworkUpdateCheck, OperationProgress } from "../api";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { OperationProgress as OperationProgressView } from "../components/OperationProgress";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const [busy, setBusy] = useState<Record<string, "install" | "update" | "check">>({});
  const [progress, setProgress] = useState<Record<string, OperationProgress>>({});
  const [checks, setChecks] = useState<Record<string, FrameworkUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const sortedItems = [...items].sort((left, right) => Number(right.installed) - Number(left.installed));
  const installedCount = items.filter((item) => item.installed).length;
  const frameworkPagination = useCatalogPagination(sortedItems);

  useEffect(() => {
    items.forEach((item) => {
      const kind = item.spec.kind;
      if (!item.spec.update_supported || !item.spec.supported || !item.installed || checks[kind] || busy[kind]) return;
      void checkUpdate(kind, true);
    });
  }, [items, busy, checks]);

  function markBusy(kind: string, action: "install" | "update" | "check") {
    setBusy((current) => ({ ...current, [kind]: action }));
  }

  function clearBusy(kind: string) {
    setBusy((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    });
  }

  function forgetCheck(kind: string) {
    setChecks((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    });
  }

  function beginProgress(kind: string, phase: string) {
    setProgress((current) => ({
      ...current,
      [kind]: { phase, percent: 4, started_at: Date.now() },
    }));
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
      const res = await api.checkFrameworkUpdate(kind);
      setChecks((current) => ({ ...current, [kind]: res }));
      if (!silent) {
        if (res.error) setNotice(`${t("tools.updateCheckFailed")}: ${res.error}`);
        else if (res.update_available) setNotice(`${t("tools.updateAvailable")}: ${res.current_version || "?"} -> ${res.latest_version || "?"}`);
        else setNotice(t("tools.upToDate"));
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setChecks((current) => ({
        ...current,
        [kind]: { kind, installed: true, update_available: false, error: message },
      }));
      if (!silent) setNotice(`${t("tools.updateCheckFailed")}: ${message}`);
    } finally {
      clearBusy(kind);
    }
  }

  async function install(kind: string, action: "install" | "update") {
    markBusy(kind, action);
    beginProgress(kind, action === "update" ? "checking" : "preparing");
    setNotice("");
    setResult(null);
    if (action === "install") forgetCheck(kind);
    try {
      const res = await api.installFramework(kind, action, (update) => updateProgress(kind, update));
      setResult(res);
      setNotice(res.ok ? t("frameworks.installed") : frameworkInstallFailureNotice(res, t));
      await frameworks.reload();
      forgetCheck(kind);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
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
            <span className={`status-badge ${prereqs?.node ? "success" : ""}`}>
              <span className="status-dot" />
              node {prereqs?.node ? t("common.enabled") : t("frameworks.missing")}
            </span>
            <span className={`status-badge ${prereqs?.npm ? "success" : ""}`}>
              <span className="status-dot" />
              npm {prereqs?.npm ? t("common.enabled") : t("frameworks.missing")}
            </span>
          </div>
          {nodeMissing && (
            <div className="framework-hint">
              <TriangleAlert size={15} />
              <span>{t("frameworks.nodeHint")}</span>
            </div>
          )}
        </div>
      </section>

      {notice && (
        <div className={`session-notice${result && !result.ok ? " error" : ""}`}>{notice}</div>
      )}

      <section className="surface">
        <div className="surface-header">
          <h2>{t("frameworks.catalogTitle")}</h2>
          <span className="pill on">{items.length}</span>
        </div>
        {frameworks.error && <div className="surface-body error">{frameworks.error}</div>}
        <div className="catalog-table-wrap">
          <table className="catalog-table framework-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("common.type")}</th>
                <th>{t("frameworks.requirements")}</th>
                <th>{t("common.status")}</th>
                <th>{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {frameworkPagination.pageItems.map((item) => (
                <FrameworkTableRows
                  key={item.spec.kind}
                  item={item}
                  busy={busy[item.spec.kind]}
                  progress={progress[item.spec.kind]}
                  check={checks[item.spec.kind]}
                  disabled={(item.spec.install_requires_npm && Boolean(nodeMissing)) || !item.spec.supported}
                  onCheck={() => checkUpdate(item.spec.kind)}
                  onInstall={(action) => install(item.spec.kind, action)}
                />
              ))}
              {sortedItems.length === 0 && (
                <tr>
                  <td className="empty-state" colSpan={5}>{t("frameworks.empty")}</td>
                </tr>
              )}
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
          <div className="surface-header">
            <h2>{t("frameworks.installLog")}</h2>
            {result.command && <span className="muted mono">{result.command}</span>}
          </div>
          <div className="surface-body">
            <pre className="framework-log">{result.log}</pre>
          </div>
        </section>
      )}
    </div>
  );
}

function frameworkInstallFailureNotice(
  result: FrameworkInstallResult,
  t: (key: string) => string,
): string {
  const summary = result.error || t("frameworks.installFailed");
  const lines = (result.log || "")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  const detail = lines[lines.length - 1];
  return detail && !summary.includes(detail) ? `${summary}: ${detail}` : summary;
}

function FrameworkTableRows({
  item,
  busy,
  progress,
  check,
  disabled,
  onCheck,
  onInstall,
}: {
  item: Framework;
  busy?: "install" | "update" | "check";
  progress?: OperationProgress;
  check?: FrameworkUpdateCheck;
  disabled: boolean;
  onCheck?: () => void;
  onInstall?: (action: "install" | "update") => void;
}) {
  const { t } = useI18n();
  const { spec } = item;
  const cli = spec.kind_type === "cli";
  const hasUpdate = Boolean(check?.update_available);
  const action: "install" | "update" | "check" = item.installed ? (hasUpdate ? "update" : "check") : "install";
  const updateStatus = frameworkUpdateStatusLabel(check, t);
  const updateStatusClass = check?.error || check?.update_available ? "warning" : "success";
  const showAction = spec.supported && (item.installed ? spec.update_supported : spec.install_supported);
  const buttonLabel =
    busy === "check"
      ? t("tools.checkingUpdate")
      : busy
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
          <span className="provider-icon">
            <Package size={16} />
          </span>
          <span className="catalog-primary-copy">
            <strong>{spec.display}</strong>
            <small className="mono">{spec.kind}</small>
            {spec.note && <small>{spec.note}</small>}
            {cli && !item.installed && spec.bin && <small className="mono">{spec.bin}</small>}
          </span>
        </td>
        <td data-label={t("common.type")}>
          <span className="pill framework-type">{spec.kind_type.toUpperCase()}</span>
        </td>
        <td data-label={t("frameworks.requirements")}>
          <span className="catalog-badge-list">
            {spec.env_required?.map((env) => (
              <span key={env} className="pill mono">{env}</span>
            ))}
            {!spec.env_required?.length && <span className="muted">—</span>}
          </span>
        </td>
        <td data-label={t("common.status")}>
        <span className="cli-status-stack">
          {item.installed ? (
            <span className="status-badge success">
              <CheckCircle2 size={14} />
              {t("frameworks.installed")}
              {item.registered && <span className="muted"> · {t("frameworks.routable")}</span>}
            </span>
          ) : !spec.supported ? (
            <span className="status-badge">{t("frameworks.comingSoon")}</span>
          ) : (
            <span className="status-badge">
              <span className="status-dot" />
              {t("frameworks.notDetected")}
            </span>
          )}
          {item.installed && item.version && (
            <span className="status-badge version-badge mono">{t("frameworks.currentVersion")} · v{item.version}</span>
          )}
          {item.installed && updateStatus && (
            <span className={`status-badge ${updateStatusClass}`} title={check?.error || undefined}>{updateStatus}</span>
          )}
        </span>
        </td>
        <td className="catalog-action-cell" data-label={t("common.actions")}>
        {showAction && (
          <button
            className="action"
            disabled={disabled || Boolean(busy)}
            onClick={() => (action === "check" ? onCheck?.() : onInstall?.(action))}
          >
            {busy === "check" || action === "check" ? <RefreshCw size={14} /> : <Download size={14} />}
            {buttonLabel}
          </button>
        )}
        </td>
      </tr>
      {progress && (
        <tr className="catalog-progress-row">
          <td colSpan={5}><OperationProgressView progress={progress} /></td>
        </tr>
      )}
    </>
  );
}

function frameworkUpdateStatusLabel(check: FrameworkUpdateCheck | undefined, t: (key: string) => string) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("frameworks.latestVersion")} · v${check.latest_version || "?"}`;
  return t("tools.upToDate");
}
