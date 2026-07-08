import { Bot, Cable, Link2, Pencil, Plus, RefreshCw, Save, Trash2, Workflow, X, Zap } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { AgentInstance, api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

type DrawerMode = "create" | "edit";

const EMPTY_AGENT: AgentInstance = {
  id: "",
  name: "",
  runtime_id: "",
  work_dir: "",
  system_prompt: "",
  provider_tool: "",
  provider_id: "",
  memory_scope: "",
  channel_bindings: [],
  schedules: [],
  mcp_servers: [],
  skills: [],
  enabled: true,
  source: "manual",
};

export function AgentsPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const runtimes = useAsync(() => api.agents(), []);
  const providers = useAsync(() => api.providers(), []);
  const channels = useAsync(() => api.channels(), []);
  const triggers = useAsync(() => api.triggers(), []);
  const mcpServers = useAsync(() => api.mcp(), []);
  const skills = useAsync(() => api.skills(), []);
  const [drawerMode, setDrawerMode] = useState<DrawerMode | null>(null);
  const [drawerDraft, setDrawerDraft] = useState<AgentInstance | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const items = agents.data ?? [];
  const runtimeOptions = runtimes.data ?? [];
  const providerOptions = providers.data ?? [];
  const channelItems = channels.data ?? [];
  const triggerItems = triggers.data ?? [];
  const mcpOptions = mcpServers.data ?? [];
  const skillOptions = skills.data ?? [];
  const draft = drawerDraft ?? EMPTY_AGENT;
  const drawerOpen = drawerMode !== null && drawerDraft !== null;

  useEffect(() => {
    if (runtimeOptions.length === 0) return;
    setDrawerDraft((current) => {
      if (!current || current.runtime_id) return current;
      const runtime = runtimeOptions[0];
      return {
        ...current,
        runtime_id: runtime,
        provider_tool: current.provider_tool || runtime,
        memory_scope: current.memory_scope || "agent:auto",
      };
    });
  }, [runtimeOptions]);

  // The unified translation layer lets any provider back any tool, so every
  // provider is a compatible choice regardless of the selected runtime.
  const compatibleProviders = providerOptions;

  const metrics = useMemo(() => {
    const manualCount = items.filter((item) => item.source === "manual").length;
    const configCount = items.filter((item) => item.source === "config.toml" || item.id.startsWith("config:")).length;
    const consoleCount = items.length - manualCount - configCount;
    const channelCount = channelItems.length;
    const scheduleCount = triggerItems.length;
    return { channelCount, consoleCount, manualCount, scheduleCount };
  }, [items, channelItems, triggerItems]);

  const boundChannels = useMemo(
    () => channelItems.filter((ch) => ch.agent_id && ch.agent_id === draft.id),
    [channelItems, draft.id]
  );
  const boundTriggers = useMemo(
    () =>
      triggerItems.filter(
        (tr) =>
          (tr.agent_id && tr.agent_id === draft.id) ||
          (tr.channel_id && boundChannels.some((ch) => ch.id === tr.channel_id))
      ),
    [triggerItems, draft.id, boundChannels]
  );

  function startNew() {
    setDrawerMode("create");
    setDrawerDraft(newAgent(runtimeOptions));
    setNotice("");
  }

  function editAgent(agent: AgentInstance) {
    setDrawerMode("edit");
    setDrawerDraft(copyAgent(agent));
    setNotice("");
  }

  function closeDrawer() {
    setDrawerMode(null);
    setDrawerDraft(null);
    setBusy("");
    setNotice("");
  }

  function update<K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) {
    setDrawerDraft((current) => {
      if (!current) return current;
      return {
        ...current,
        [key]: value,
        provider_tool: key === "runtime_id" ? String(value) : current.provider_tool,
        provider_id: key === "runtime_id" ? "" : current.provider_id,
      };
    });
  }

  async function save() {
    if (!drawerDraft) return;
    setBusy("save");
    setNotice("");
    try {
      const saved = await api.upsertAgentInstance({
        ...drawerDraft,
        source: drawerDraft.source || (drawerMode === "create" ? "manual" : "console"),
        provider_tool: drawerDraft.provider_tool || drawerDraft.runtime_id,
        memory_scope: drawerDraft.memory_scope || `agent:${drawerDraft.id || "new"}`,
      });
      setDrawerMode("edit");
      setDrawerDraft(copyAgent(saved));
      setNotice(t("agents.saved"));
      await agents.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function remove() {
    if (!drawerDraft?.id || isConfigManaged(drawerDraft)) return;
    setBusy("delete");
    setNotice("");
    try {
      await api.deleteAgentInstance(drawerDraft.id);
      setDrawerMode(null);
      setDrawerDraft(null);
      setNotice(t("agents.deleted"));
      await agents.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  const readOnly = isConfigManaged(draft);
  const canSave = Boolean(drawerDraft && drawerDraft.name.trim() && drawerDraft.runtime_id && !readOnly);
  const noticeClass = notice.toLowerCase().includes("failed") || notice.toLowerCase().includes("error") ? " error" : "";

  return (
    <div className="page-stack agents-page">
      {drawerOpen && (
        <div className="provider-drawer-layer">
          <button
            className="provider-drawer-backdrop"
            type="button"
            aria-label={t("common.close")}
            onClick={closeDrawer}
          />
          <aside className="provider-drawer agent-drawer" role="dialog" aria-modal="true" aria-labelledby="agent-drawer-title">
            <div className="provider-builder-head">
              <div className="provider-form-title">
                <span className="provider-icon">
                  <Bot size={20} />
                </span>
                <div>
                  <h2 id="agent-drawer-title">
                    {drawerMode === "create" ? t("agents.newDrawerTitle") : draft.name || t("agents.editDrawerTitle")}
                  </h2>
                  <span className="muted">
                    {drawerMode === "create" ? t("agents.newDrawerSubtitle") : t(agentSourceDetailKey(draft))}
                  </span>
                </div>
              </div>
              <div className="table-actions">
                <span className={`source-badge ${agentSourceClass(draft)}`}>{t(agentSourceLabelKey(draft))}</span>
                <button className="ghost-action icon-action" onClick={closeDrawer} title={t("common.close")}>
                  <X size={15} />
                </button>
              </div>
            </div>

            <div className="surface-body provider-builder-body agent-drawer-body">
              {notice && <div className={`session-notice${noticeClass}`}>{notice}</div>}
              {readOnly && <div className="session-notice">{t("agents.readOnlyNotice")}</div>}
              <AgentForm
                boundChannels={boundChannels}
                boundTriggers={boundTriggers}
                canSave={canSave}
                compatibleProviders={compatibleProviders}
                draft={draft}
                mcpOptions={mcpOptions.map((server) => server.name)}
                readOnly={readOnly}
                runtimeOptions={runtimeOptions}
                skillOptions={skillOptions.map((skill) => skill.name)}
                busy={busy}
                drawerMode={drawerMode}
                onDelete={remove}
                onSave={save}
                onUpdate={update}
                t={t}
              />
            </div>
          </aside>
        </div>
      )}

      <section className="surface agent-registry-surface">
        <div className="surface-header">
          <div>
            <h2>{t("agents.registry")}</h2>
            <p className="subtle-copy">{t("agents.registrySubtitle")}</p>
          </div>
          <div className="table-actions">
            <span className="pill">
              {items.length} {t("agents.registryCount")}
            </span>
            <button className="ghost-action" onClick={agents.reload} title={t("common.refresh")}>
              <RefreshCw size={15} />
              {t("common.refresh")}
            </button>
            <button className="action" onClick={startNew}>
              <Plus size={16} />
              {t("agents.new")}
            </button>
          </div>
        </div>
        {!drawerOpen && notice && <div className={`surface-body session-notice${noticeClass}`}>{notice}</div>}
        <div className="surface-body agent-registry-list">
          {agents.loading && <div className="empty-state">{t("common.loading")}</div>}
          {agents.error && <div className="empty-state error">{String(agents.error)}</div>}
          {!agents.loading && !agents.error && items.length === 0 && <div className="empty-state">{t("agents.empty")}</div>}
          {items.map((item) => {
            const channelCount = channelItems.filter((ch) => ch.agent_id === item.id).length;
            return (
              <article className="agent-registry-row" key={item.id}>
                <div className="agent-list-main">
                  <span className="provider-icon">
                    <Bot size={15} />
                  </span>
                  <span>
                    <strong>{item.name}</strong>
                    <small>{item.runtime_id || t("agents.noRuntime")}</small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <span className={`status-badge ${item.enabled ? "success" : "warning"}`}>
                    <span className="status-dot" />
                    {item.enabled ? t("common.enabled") : t("common.disabled")}
                  </span>
                  <span className={`source-badge ${agentSourceClass(item)}`}>{t(agentSourceLabelKey(item))}</span>
                  <span className="pill">{item.provider_name || item.provider_id || t("agents.noProvider")}</span>
                  <span className="pill">
                    {channelCount} {t("agents.channelCount")}
                  </span>
                </div>
                <button className="ghost-action" onClick={() => editAgent(item)}>
                  <Pencil size={15} />
                  {t("common.edit")}
                </button>
              </article>
            );
          })}
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("agents.title")}</h2>
            <p className="subtle-copy">{t("agents.subtitle")}</p>
          </div>
        </div>
        <div className="surface-body agent-metrics">
          <Summary label={t("agents.total")} value={items.length} />
          <Summary label={t("agents.manualManaged")} value={metrics.manualCount} />
          <Summary label={t("agents.consoleManaged")} value={metrics.consoleCount} />
          <Summary label={t("agents.runtimes")} value={runtimeOptions.length} />
          <Summary label={t("agents.channels")} value={metrics.channelCount} />
          <Summary label={t("agents.schedules")} value={metrics.scheduleCount} />
        </div>
      </section>
    </div>
  );
}

function AgentForm({
  boundChannels,
  boundTriggers,
  busy,
  canSave,
  compatibleProviders,
  draft,
  drawerMode,
  mcpOptions,
  onDelete,
  onSave,
  onUpdate,
  readOnly,
  runtimeOptions,
  skillOptions,
  t,
}: {
  boundChannels: { id: string; name: string; type: string; state?: string }[];
  boundTriggers: { id: string; name: string; kind: string; enabled: boolean }[];
  busy: string;
  canSave: boolean;
  compatibleProviders: { id: string; name: string }[];
  draft: AgentInstance;
  drawerMode: DrawerMode | null;
  mcpOptions: string[];
  onDelete: () => void;
  onSave: () => void;
  onUpdate: <K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) => void;
  readOnly: boolean;
  runtimeOptions: string[];
  skillOptions: string[];
  t: (key: string) => string;
}) {
  return (
    <>
      <section className="agent-section">
        <header>
          <Workflow size={17} />
          <h3>{t("agents.identity")}</h3>
        </header>
        <div className="field-grid">
          <label className="field">
            <span>{t("agents.name")}</span>
            <input disabled={readOnly} value={draft.name} onChange={(event) => onUpdate("name", event.target.value)} />
          </label>
          <label className="field">
            <span>{t("agents.runtime")}</span>
            <select disabled={readOnly} value={draft.runtime_id} onChange={(event) => onUpdate("runtime_id", event.target.value)}>
              {runtimeOptions.map((runtime) => (
                <option key={runtime} value={runtime}>
                  {runtime}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>{t("agents.workDir")}</span>
            <input disabled={readOnly} value={draft.work_dir ?? ""} onChange={(event) => onUpdate("work_dir", event.target.value)} />
          </label>
          <label className="field">
            <span>{t("agents.memoryScope")}</span>
            <input
              disabled={readOnly}
              value={draft.memory_scope ?? ""}
              onChange={(event) => onUpdate("memory_scope", event.target.value)}
            />
          </label>
          <label className="field wide">
            <span>{t("agents.systemPrompt")}</span>
            <textarea
              disabled={readOnly}
              rows={3}
              value={draft.system_prompt ?? ""}
              onChange={(event) => onUpdate("system_prompt", event.target.value)}
            />
          </label>
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Link2 size={17} />
          <h3>{t("agents.routing")}</h3>
        </header>
        <div className="field-grid">
          <label className="field">
            <span>{t("agents.routeTool")}</span>
            <select
              disabled={readOnly}
              value={draft.provider_tool || draft.runtime_id}
              onChange={(event) => onUpdate("provider_tool", event.target.value)}
            >
              {runtimeOptions.map((runtime) => (
                <option key={runtime} value={runtime}>
                  {runtime}
                </option>
              ))}
              <option value="claude-desktop">claude-desktop</option>
            </select>
          </label>
          <label className="field">
            <span>{t("agents.defaultProvider")}</span>
            <select disabled={readOnly} value={draft.provider_id ?? ""} onChange={(event) => onUpdate("provider_id", event.target.value)}>
              <option value="">{t("agents.noProvider")}</option>
              {compatibleProviders.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name}
                </option>
              ))}
            </select>
          </label>
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Link2 size={17} />
          <h3>{t("nav.connect")}</h3>
        </header>
        <p className="subtle-copy">{t("agents.connectMoved")}</p>
        <div className="agent-picker-grid">
          <div className="mapping-card">
            <strong>
              <Cable size={13} /> {t("agents.boundChannels")} ({boundChannels.length})
            </strong>
            {boundChannels.length === 0 && <span className="muted">{t("connect.noChannels")}</span>}
            <div className="provider-chip-row">
              {boundChannels.map((ch) => (
                <span key={ch.id} className={`status-badge ${ch.state === "running" ? "success" : ""}`}>
                  {ch.name} · {ch.type}
                </span>
              ))}
            </div>
          </div>
          <div className="mapping-card">
            <strong>
              <Zap size={13} /> {t("agents.boundTriggers")} ({boundTriggers.length})
            </strong>
            {boundTriggers.length === 0 && <span className="muted">{t("connect.noTriggers")}</span>}
            <div className="provider-chip-row">
              {boundTriggers.map((tr) => (
                <span key={tr.id} className={`status-badge ${tr.enabled ? "success" : ""}`}>
                  {tr.name} · {tr.kind}
                </span>
              ))}
            </div>
          </div>
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Bot size={17} />
          <h3>{t("agents.tools")}</h3>
        </header>
        <div className="agent-picker-grid">
          <Picker
            title={t("agents.mcpServers")}
            items={mcpOptions}
            selected={draft.mcp_servers ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("mcp_servers", next)}
            empty={t("mcp.empty")}
          />
          <Picker
            title={t("agents.skills")}
            items={skillOptions}
            selected={draft.skills ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("skills", next)}
            empty={t("skills.empty")}
          />
        </div>
      </section>

      <div className="agent-drawer-actions">
        <button className="ghost-action danger-action" disabled={!draft.id || readOnly || busy === "delete"} onClick={onDelete}>
          <Trash2 size={15} />
          {t("common.delete")}
        </button>
        <button className="action" disabled={!canSave || busy === "save"} onClick={onSave}>
          <Save size={15} />
          {drawerMode === "create" ? t("agents.create") : t("common.save")}
        </button>
      </div>
    </>
  );
}

function Summary({ label, value }: { label: string; value: number }) {
  return (
    <div className="summary-stat">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function Picker({
  title,
  items,
  selected,
  readOnly,
  onChange,
  empty,
}: {
  title: string;
  items: string[];
  selected: string[];
  readOnly: boolean;
  onChange: (next: string[]) => void;
  empty: string;
}) {
  return (
    <div className="mapping-card">
      <strong>{title}</strong>
      {items.length === 0 && <span className="muted">{empty}</span>}
      <div className="provider-chip-row">
        {items.map((item) => {
          const active = selected.includes(item);
          return (
            <button
              key={item}
              className={`status-badge ${active ? "success" : ""}`}
              disabled={readOnly}
              onClick={() => onChange(active ? selected.filter((value) => value !== item) : [...selected, item])}
            >
              {item}
            </button>
          );
        })}
      </div>
    </div>
  );
}

function newAgent(runtimeOptions: string[]): AgentInstance {
  const runtime = runtimeOptions[0] ?? "codex";
  return copyAgent({
    ...EMPTY_AGENT,
    runtime_id: runtime,
    provider_tool: runtime,
    memory_scope: "agent:new",
    source: "manual",
  });
}

function isConfigManaged(agent: AgentInstance): boolean {
  return agent.source === "config.toml" || agent.id.startsWith("config:");
}

function agentSourceLabelKey(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "agents.sourceConfig";
  if (agent.source === "manual") return "agents.sourceManual";
  if (agent.source === "console" || !agent.source) return "agents.sourceConsole";
  return "agents.sourceUnknown";
}

function agentSourceDetailKey(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "agents.configManaged";
  if (agent.source === "manual") return "agents.manualManagedDetail";
  return "agents.consoleManagedDetail";
}

function agentSourceClass(agent: AgentInstance): string {
  if (isConfigManaged(agent)) return "config";
  if (agent.source === "manual") return "manual";
  return "console";
}

function copyAgent(agent: AgentInstance): AgentInstance {
  return {
    ...agent,
    env: { ...(agent.env ?? {}) },
    channel_bindings: (agent.channel_bindings ?? []).map((channel) => ({ ...channel, config: { ...(channel.config ?? {}) } })),
    schedules: (agent.schedules ?? []).map((schedule) => ({ ...schedule })),
    mcp_servers: [...(agent.mcp_servers ?? [])],
    skills: [...(agent.skills ?? [])],
  };
}
