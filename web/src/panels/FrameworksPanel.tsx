import { useEffect, useState } from "react";
import { CheckCircle2, Download, Package, RefreshCw, TriangleAlert } from "lucide-react";
import { api, Framework, FrameworkInstallResult, FrameworkUpdateCheck } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const [busy, setBusy] = useState<Record<string, "install" | "update" | "check">>({});
  const [checks, setChecks] = useState<Record<string, FrameworkUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const sortedItems = [...items].sort((left, right) => Number(right.installed) - Number(left.installed));
  const installedCount = items.filter((item) => item.installed).length;

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
    setNotice("");
    setResult(null);
    if (action === "install") forgetCheck(kind);
    try {
      const res = await api.installFramework(kind, action);
      setResult(res);
      setNotice(res.ok ? t("frameworks.installed") : frameworkInstallFailureNotice(res, t));
      await frameworks.reload();
      forgetCheck(kind);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
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
        <div className="surface-body framework-grid">
          {sortedItems.map((item) => (
            <FrameworkCard
              key={item.spec.kind}
              item={item}
              busy={busy[item.spec.kind]}
              check={checks[item.spec.kind]}
              disabled={(item.spec.install_requires_npm && Boolean(nodeMissing)) || !item.spec.supported}
              onCheck={() => checkUpdate(item.spec.kind)}
              onInstall={(action) => install(item.spec.kind, action)}
            />
          ))}
          {sortedItems.length === 0 && <div className="empty-state">{t("frameworks.empty")}</div>}
        </div>
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

function FrameworkCard({
  item,
  busy,
  check,
  disabled,
  onCheck,
  onInstall,
}: {
  item: Framework;
  busy?: "install" | "update" | "check";
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
    <div className={`framework-card${item.installed ? " installed" : ""}`}>
      <div className="framework-card-head">
        <span className="provider-icon">
          <Package size={16} />
        </span>
        <div className="framework-card-title">
          <strong>{spec.display}</strong>
          <span className="muted mono">{spec.kind}</span>
        </div>
        <span className="pill framework-type">{spec.kind_type.toUpperCase()}</span>
      </div>

      {spec.note && <p className="framework-note">{spec.note}</p>}

      {spec.env_required && spec.env_required.length > 0 && (
        <div className="framework-env">
          {spec.env_required.map((env) => (
            <span key={env} className="pill mono">{env}</span>
          ))}
        </div>
      )}

      <div className="framework-card-foot">
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
      </div>
      {cli && !item.installed && spec.bin && (
        <p className="framework-cli-hint muted mono">{spec.bin}</p>
      )}
    </div>
  );
}

function frameworkUpdateStatusLabel(check: FrameworkUpdateCheck | undefined, t: (key: string) => string) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("frameworks.latestVersion")} · v${check.latest_version || "?"}`;
  return t("tools.upToDate");
}
