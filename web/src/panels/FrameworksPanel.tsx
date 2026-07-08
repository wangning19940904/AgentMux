import { useState } from "react";
import { Boxes, CheckCircle2, Download, Package, TerminalSquare, TriangleAlert } from "lucide-react";
import { api, Framework, FrameworkInstallResult } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function FrameworksPanel() {
  const { t } = useI18n();
  const frameworks = useAsync(() => api.frameworks(), []);
  const [busyKind, setBusyKind] = useState("");
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<FrameworkInstallResult | null>(null);

  const prereqs = frameworks.data?.prereqs;
  const items = frameworks.data?.frameworks ?? [];
  const sdkItems = items.filter((item) => item.spec.kind_type === "sdk");
  const cliItems = items.filter((item) => item.spec.kind_type === "cli");
  const installedCount = items.filter((item) => item.installed).length;

  async function install(kind: string) {
    setBusyKind(kind);
    setNotice("");
    setResult(null);
    try {
      const res = await api.installFramework(kind);
      setResult(res);
      setNotice(res.ok ? t("frameworks.installed") : res.error || t("frameworks.installFailed"));
      frameworks.reload();
    } catch (err) {
      setNotice(String(err));
    } finally {
      setBusyKind("");
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
          <h2>{t("frameworks.sdkTitle")}</h2>
          <Boxes size={16} />
        </div>
        {frameworks.error && <div className="surface-body error">{frameworks.error}</div>}
        <div className="surface-body framework-grid">
          {sdkItems.map((item) => (
            <FrameworkCard
              key={item.spec.kind}
              item={item}
              busy={busyKind === item.spec.kind}
              disabled={Boolean(nodeMissing) || !item.spec.supported}
              onInstall={() => install(item.spec.kind)}
            />
          ))}
          {sdkItems.length === 0 && <div className="empty-state">{t("frameworks.empty")}</div>}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("frameworks.cliTitle")}</h2>
          <TerminalSquare size={16} />
        </div>
        <div className="surface-body framework-grid">
          {cliItems.map((item) => (
            <FrameworkCard key={item.spec.kind} item={item} busy={false} disabled cli />
          ))}
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

function FrameworkCard({
  item,
  busy,
  disabled,
  cli,
  onInstall,
}: {
  item: Framework;
  busy: boolean;
  disabled: boolean;
  cli?: boolean;
  onInstall?: () => void;
}) {
  const { t } = useI18n();
  const { spec } = item;

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
        {spec.language && <span className="pill">{spec.language}</span>}
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
        {item.installed ? (
          <span className="status-badge success">
            <CheckCircle2 size={14} />
            {item.version ? `v${item.version}` : t("frameworks.installed")}
            {item.registered && <span className="muted"> · {t("frameworks.routable")}</span>}
          </span>
        ) : !spec.supported ? (
          <span className="status-badge">{t("frameworks.comingSoon")}</span>
        ) : cli ? (
          <span className="status-badge">
            <span className="status-dot" />
            {t("frameworks.notDetected")}
          </span>
        ) : (
          <button className="action" disabled={disabled || busy} onClick={onInstall}>
            <Download size={14} />
            {busy ? t("frameworks.installing") : t("frameworks.install")}
          </button>
        )}
      </div>
      {cli && !item.installed && spec.bin && (
        <p className="framework-cli-hint muted mono">{spec.bin}</p>
      )}
    </div>
  );
}
