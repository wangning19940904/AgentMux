import { Bot, CheckCircle2, Download, RefreshCw, Search, ShieldCheck, TerminalSquare, TriangleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import {
  api,
  CLIInstallResult,
  CLIManagedTool,
  CLIUpdateCheck,
  MarketplaceSkill,
  Skill,
} from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

type CLIBusyAction = "install" | "update" | "check" | "sync";

export function ToolsPanel() {
  const { t } = useI18n();
  const tools = useAsync(() => api.tools(), []);
  const [marketQuery, setMarketQuery] = useState("");
  const marketplace = useAsync(() => api.skillMarketplace(marketQuery), [marketQuery]);
  const [busy, setBusy] = useState("");
  const [cliBusy, setCliBusy] = useState<Record<string, CLIBusyAction>>({});
  const [cliChecks, setCliChecks] = useState<Record<string, CLIUpdateCheck>>({});
  const [notice, setNotice] = useState("");
  const [result, setResult] = useState<CLIInstallResult | null>(null);

  const data = tools.data;
  const cli = data?.cli ?? [];
  const skills = data?.skills ?? [];
  const mcp = data?.mcp ?? [];
  const market = marketplace.data ?? data?.marketplace ?? [];

  useEffect(() => {
    cli.forEach((item) => {
      const id = item.spec.id;
      if (!item.installed || cliChecks[id] || cliBusy[id]) return;
      void checkCLIUpdate(id, true);
    });
  }, [cli, cliBusy, cliChecks]);

  async function refreshAll() {
    setCliChecks({});
    await Promise.all([tools.reload(), marketplace.reload()]);
  }

  function markCLIBusy(id: string, action: CLIBusyAction) {
    setCliBusy((current) => ({ ...current, [id]: action }));
  }

  function clearCLIBusy(id: string) {
    setCliBusy((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  function forgetCLICheck(id: string) {
    setCliChecks((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  async function checkCLIUpdate(id: string, silent = false) {
    markCLIBusy(id, "check");
    if (!silent) {
      setNotice("");
      setResult(null);
    }
    try {
      const res = await api.checkCLIUpdate(id);
      setCliChecks((current) => ({ ...current, [id]: res }));
      if (!silent) {
        if (res.error) {
          setNotice(`${t("tools.updateCheckFailed")}: ${res.error}`);
        } else if (res.update_available) {
          setNotice(`${t("tools.updateAvailable")}: ${res.current_version || "?"} -> ${res.latest_version || "?"}`);
        } else {
          setNotice(t("tools.upToDate"));
        }
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setCliChecks((current) => ({ ...current, [id]: { id, installed: true, update_available: false, error: message } }));
      if (!silent) setNotice(`${t("tools.updateCheckFailed")}: ${message}`);
    } finally {
      clearCLIBusy(id);
    }
  }

  async function installCLI(id: string, action: "install" | "update") {
    markCLIBusy(id, action);
    setNotice("");
    setResult(null);
    if (action === "install") forgetCLICheck(id);
    try {
      const res = await api.installCLI(id, action);
      setResult(res);
      setNotice(res.ok ? t("tools.cliReady") : res.error || t("tools.cliFailed"));
      await tools.reload();
      forgetCLICheck(id);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIBusy(id);
    }
  }

  async function syncCLISkills(id: string) {
    markCLIBusy(id, "sync");
    setNotice("");
    setResult(null);
    try {
      const res = await api.syncCLISkills(id);
      setResult(res);
      setNotice(res.ok ? t("tools.skillsSynced") : res.error || t("tools.skillsSyncFailed"));
      await tools.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      clearCLIBusy(id);
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
            <CLIManagedCard
              key={item.spec.id}
              item={item}
              busy={cliBusy[item.spec.id]}
              check={cliChecks[item.spec.id]}
              onCheck={checkCLIUpdate}
              onInstall={installCLI}
              onSync={syncCLISkills}
              t={t}
            />
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
  check,
  onCheck,
  onInstall,
  onSync,
  t,
}: {
  item: CLIManagedTool;
  busy?: CLIBusyAction;
  check?: CLIUpdateCheck;
  onCheck: (id: string) => void;
  onInstall: (id: string, action: "install" | "update") => void;
  onSync: (id: string) => void;
  t: (key: string) => string;
}) {
  const hasUpdate = Boolean(check?.update_available);
  const linkedSkills = item.linked_skills ?? [];
  const hasLinkedSkills = linkedSkills.length > 0 || Boolean(item.spec.linked_skills?.length);
  const needsSkillSync = linkedSkills.some((skill) => !skill.installed || !skill.in_sync);
  let action: "install" | "update" | "check" | "sync" = "check";
  if (!item.installed) action = "install";
  else if (hasUpdate) action = "update";
  else if (needsSkillSync) action = "sync";
  const disabled = Boolean(busy);
  let buttonLabel = t("tools.checkUpdate");
  if (busy === "check") buttonLabel = t("tools.checkingUpdate");
  else if (busy === "sync") buttonLabel = t("tools.syncingSkills");
  else if (busy) buttonLabel = t("frameworks.installing");
  else if (action === "install") buttonLabel = hasLinkedSkills ? t("tools.installBundle") : t("frameworks.install");
  else if (action === "update") buttonLabel = t("tools.update");
  else if (action === "sync") buttonLabel = t("tools.syncSkills");
  const updateStatus = updateStatusLabel(check, t);
  const updateStatusClass = check?.error ? "warning" : check?.update_available ? "warning" : "success";
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
        <span className="cli-status-stack">
          <span className={`status-badge ${item.installed ? "success" : ""}`}>
            {item.installed ? <CheckCircle2 size={14} /> : <TriangleAlert size={14} />}
            {item.installed ? item.version || t("frameworks.installed") : t("frameworks.notDetected")}
          </span>
          {item.installed && updateStatus && <span className={`status-badge ${updateStatusClass}`}>{updateStatus}</span>}
          {linkedSkills.map((skill) => (
            <span
              key={skill.spec.id}
              className={`status-badge ${skill.installed && skill.in_sync ? "success" : "warning"}`}
              title={skill.detail || skill.spec.version_policy_label || skill.spec.note}
            >
              <Bot size={14} />
              {skill.spec.name}: {linkedSkillStatusLabel(skill, t)}
            </span>
          ))}
        </span>
        <button
          className="action"
          disabled={disabled}
          onClick={() => {
            if (action === "check") onCheck(item.spec.id);
            else if (action === "sync") onSync(item.spec.id);
            else onInstall(item.spec.id, action);
          }}
        >
          {busy === "check" || action === "check" || action === "sync" ? <RefreshCw size={14} /> : <Download size={14} />}
          {buttonLabel}
        </button>
      </div>
    </article>
  );
}

function linkedSkillStatusLabel(
  skill: { installed: boolean; in_sync: boolean; version?: string },
  t: (key: string) => string,
) {
  if (!skill.installed) return t("tools.skillMissing");
  if (!skill.in_sync) return `${skill.version || "?"} · ${t("tools.skillNeedsSync")}`;
  return `${skill.version || ""} · ${t("tools.skillInSync")}`.trim();
}

function updateStatusLabel(
  check: { error?: string; update_available: boolean; latest_version?: string } | undefined,
  t: (key: string) => string,
) {
  if (!check) return "";
  if (check.error) return t("tools.updateCheckFailed");
  if (check.update_available) return `${t("tools.updateAvailable")} ${check.latest_version || ""}`.trim();
  return t("tools.upToDate");
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
