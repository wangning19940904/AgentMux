import { Bot, CheckCircle2, Download, Package, RefreshCw, Search, ShieldCheck, TerminalSquare, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { api, CLIInstallResult, CLIManagedTool, Framework, MarketplaceSkill, Skill } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function ToolsPanel() {
  const { t } = useI18n();
  const tools = useAsync(() => api.tools(), []);
  const [marketQuery, setMarketQuery] = useState("");
  const marketplace = useAsync(() => api.skillMarketplace(marketQuery), [marketQuery]);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<CLIInstallResult | null>(null);

  const data = tools.data;
  const cli = data?.cli ?? [];
  const frameworks = data?.frameworks ?? [];
  const skills = data?.skills ?? [];
  const mcp = data?.mcp ?? [];
  const market = marketplace.data ?? data?.marketplace ?? [];

  async function refreshAll() {
    await Promise.all([tools.reload(), marketplace.reload()]);
  }

  async function installCLI(id: string, action: "install" | "update") {
    setBusy(`cli:${id}`);
    setNotice("");
    setResult(null);
    try {
      const res = await api.installCLI(id, action);
      setResult(res);
      setNotice(res.ok ? t("tools.cliReady") : res.error || t("tools.cliFailed"));
      await tools.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function installSkill(skill: MarketplaceSkill) {
    setBusy(`skill:${skill.name}`);
    setNotice("");
    setResult(null);
    try {
      await api.installSkill({ repo: skill.repo, path: skill.path, name: skill.name });
      setNotice(t("tools.skillInstalled"));
      await refreshAll();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function toggleSkill(skill: Skill) {
    setBusy(`toggle:${skill.name}`);
    try {
      await api.toggleSkill(skill.name, !skill.enabled);
      await refreshAll();
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="page-stack tools-page">
      <p className="subtle-copy">{t("tools.subtitle")}</p>
      {tools.error && <div className="surface-body error">{tools.error}</div>}
      {notice && <div className={`session-notice${result && !result.ok ? " error" : ""}`}>{notice}</div>}

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.cliTitle")}</h2>
            <p className="subtle-copy">{t("tools.cliSubtitle")}</p>
          </div>
          <button className="ghost-action" onClick={refreshAll}>
            <RefreshCw size={15} />
            {t("common.refresh")}
          </button>
        </div>
        <div className="surface-body tools-grid">
          {cli.map((item) => (
            <CLIManagedCard key={item.spec.id} item={item} busy={busy === `cli:${item.spec.id}`} onInstall={installCLI} t={t} />
          ))}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.agentRuntimeTitle")}</h2>
            <p className="subtle-copy">{t("tools.agentRuntimeSubtitle")}</p>
          </div>
          <TerminalSquare size={16} />
        </div>
        <div className="surface-body tools-grid">
          {frameworks.map((item) => (
            <FrameworkToolCard key={item.spec.kind} item={item} t={t} />
          ))}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tools.marketTitle")}</h2>
            <p className="subtle-copy">{t("tools.marketSubtitle")}</p>
          </div>
          <label className="tools-search">
            <Search size={15} />
            <input value={marketQuery} onChange={(event) => setMarketQuery(event.target.value)} placeholder={t("tools.searchSkills")} />
          </label>
        </div>
        <div className="surface-body tools-grid">
          {market.map((skill) => (
            <MarketplaceCard key={`${skill.repo}:${skill.path}`} skill={skill} busy={busy === `skill:${skill.name}`} onInstall={installSkill} t={t} />
          ))}
          {!marketplace.loading && market.length === 0 && <div className="empty-state">{t("tools.marketEmpty")}</div>}
        </div>
      </section>

      <section className="tools-two-column">
        <div className="surface">
          <div className="surface-header">
            <h2>{t("tools.installedSkills")}</h2>
            <span className="pill on">{skills.length}</span>
          </div>
          <div className="surface-body tools-list">
            {skills.map((skill) => (
              <button key={skill.name} className="tools-list-row" onClick={() => toggleSkill(skill)} disabled={busy === `toggle:${skill.name}`}>
                <span>
                  <strong>{skill.name}</strong>
                  <small>{skill.description || t("common.description")}</small>
                </span>
                <span className={`status-badge ${skill.enabled ? "success" : ""}`}>
                  <span className="status-dot" />
                  {skill.enabled ? t("common.enabled") : t("common.disabled")}
                </span>
              </button>
            ))}
            {skills.length === 0 && <div className="empty-state">{t("skills.empty")}</div>}
          </div>
        </div>

        <div className="surface">
          <div className="surface-header">
            <h2>{t("tools.mcpTitle")}</h2>
            <span className="pill on">{mcp.length}</span>
          </div>
          <div className="surface-body tools-list">
            {mcp.map((server) => (
              <div key={server.name} className="tools-list-row static">
                <span>
                  <strong>{server.name}</strong>
                  <small>{server.command || server.url || server.transport}</small>
                </span>
                <span className={`status-badge ${server.enabled ? "success" : ""}`}>
                  <span className="status-dot" />
                  {server.enabled ? t("common.enabled") : t("common.disabled")}
                </span>
              </div>
            ))}
            {mcp.length === 0 && <div className="empty-state">{t("mcp.empty")}</div>}
          </div>
        </div>
      </section>

      {result?.log && (
        <section className="surface">
          <div className="surface-header">
            <h2>{t("tools.installLog")}</h2>
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

function CLIManagedCard({
  item,
  busy,
  onInstall,
  t,
}: {
  item: CLIManagedTool;
  busy: boolean;
  onInstall: (id: string, action: "install" | "update") => void;
  t: (key: string) => string;
}) {
  const action = item.installed ? "update" : "install";
  return (
    <article className="tool-card">
      <div className="tool-card-head">
        <span className="provider-icon">
          <TerminalSquare size={16} />
        </span>
        <span>
          <strong>{item.spec.name}</strong>
          <small className="mono">{item.spec.bin}</small>
        </span>
      </div>
      <p>{item.spec.note}</p>
      <div className="tool-card-foot">
        <span className={`status-badge ${item.installed ? "success" : ""}`}>
          {item.installed ? <CheckCircle2 size={14} /> : <TriangleAlert size={14} />}
          {item.installed ? item.version || t("frameworks.installed") : t("frameworks.notDetected")}
        </span>
        <button className="action" disabled={busy} onClick={() => onInstall(item.spec.id, action)}>
          <Download size={14} />
          {busy ? t("frameworks.installing") : item.installed ? t("tools.update") : t("frameworks.install")}
        </button>
      </div>
    </article>
  );
}

function FrameworkToolCard({ item, t }: { item: Framework; t: (key: string) => string }) {
  return (
    <article className="tool-card">
      <div className="tool-card-head">
        <span className="provider-icon">
          <Package size={16} />
        </span>
        <span>
          <strong>{item.spec.display}</strong>
          <small className="mono">{item.spec.kind}</small>
        </span>
      </div>
      <p>{item.spec.note || item.spec.bin || item.spec.language}</p>
      <div className="tool-card-foot">
        <span className={`status-badge ${item.installed ? "success" : ""}`}>
          <span className="status-dot" />
          {item.installed ? item.version || t("frameworks.installed") : t("frameworks.notDetected")}
        </span>
        {item.registered && <span className="pill">{t("frameworks.routable")}</span>}
      </div>
    </article>
  );
}

function MarketplaceCard({
  skill,
  busy,
  onInstall,
  t,
}: {
  skill: MarketplaceSkill;
  busy: boolean;
  onInstall: (skill: MarketplaceSkill) => void;
  t: (key: string) => string;
}) {
  return (
    <article className="tool-card">
      <div className="tool-card-head">
        <span className="provider-icon">
          <Bot size={16} />
        </span>
        <span>
          <strong>{skill.name}</strong>
          <small>{skill.category || skill.source}</small>
        </span>
      </div>
      <p>{skill.description}</p>
      <div className="tool-card-foot">
        <span className={`status-badge ${skill.trusted ? "success" : ""}`}>
          {skill.trusted ? <ShieldCheck size={14} /> : <TriangleAlert size={14} />}
          {skill.trusted ? t("tools.trusted") : t("tools.community")}
        </span>
        {skill.installed ? (
          <span className="status-badge success">
            <CheckCircle2 size={14} />
            {t("frameworks.installed")}
          </span>
        ) : (
          <button className="action" disabled={busy} onClick={() => onInstall(skill)}>
            <Download size={14} />
            {busy ? t("frameworks.installing") : t("frameworks.install")}
          </button>
        )}
      </div>
    </article>
  );
}
