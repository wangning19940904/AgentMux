import { Bot, Pencil, Plus, RefreshCw, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, type AgentInstance } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import {
  EMPTY_AGENT,
  agentProviderSummary,
  agentSourceClass,
  agentSourceDetailKey,
  agentSourceLabelKey,
  copyAgent,
  isConfigManaged,
  newAgent,
  routeToolForRuntime,
  runtimeLabel,
  syncAgentConnectBindings,
  toggleID,
  type DrawerMode,
} from "./agentUtils";
import { Summary } from "./widgets";
import { AgentForm } from "./AgentForm";


export function AgentsPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const runtimes = useAsync(() => api.agents(), []);
  const providers = useAsync(() => api.providers(), []);
  const activeRoutes = useAsync(() => api.activeRoutes(), []);
  const channels = useAsync(() => api.channels(), []);
  const triggers = useAsync(() => api.triggers(), []);
  const mcpServers = useAsync(() => api.mcp(), []);
  const skills = useAsync(() => api.skills(), []);
  const tools = useAsync(() => api.tools(), []);
  const [drawerMode, setDrawerMode] = useState<DrawerMode | null>(null);
  const [drawerDraft, setDrawerDraft] = useState<AgentInstance | null>(null);
  const [selectedChannelIDs, setSelectedChannelIDs] = useState<string[]>([]);
  const [selectedTriggerIDs, setSelectedTriggerIDs] = useState<string[]>([]);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const items = agents.data ?? [];
  const runtimeOptions = runtimes.data ?? [];
  const providerOptions = providers.data ?? [];
  const activeRouteItems = activeRoutes.data ?? [];
  const channelItems = channels.data ?? [];
  const triggerItems = triggers.data ?? [];
  const mcpOptions = mcpServers.data ?? [];
  const skillOptions = skills.data ?? [];
  const cliCatalog = tools.data?.cli ?? [];
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

  function startNew() {
    setDrawerMode("create");
    setDrawerDraft(newAgent(runtimeOptions));
    setSelectedChannelIDs([]);
    setSelectedTriggerIDs([]);
    setNotice("");
  }

  function editAgent(agent: AgentInstance) {
    setDrawerMode("edit");
    setDrawerDraft(copyAgent(agent));
    setSelectedChannelIDs(channelItems.filter((channel) => channel.agent_id === agent.id).map((channel) => channel.id));
    setSelectedTriggerIDs(triggerItems.filter((trigger) => trigger.agent_id === agent.id).map((trigger) => trigger.id));
    setNotice("");
  }

  function closeDrawer() {
    setDrawerMode(null);
    setDrawerDraft(null);
    setSelectedChannelIDs([]);
    setSelectedTriggerIDs([]);
    setBusy("");
    setNotice("");
  }

  function update<K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) {
    setDrawerDraft((current) => {
      if (!current) return current;
      const routeChanged = key === "runtime_id" || key === "provider_tool" || key === "provider_id";
      const nextRuntimeRouteTool = key === "runtime_id" ? routeToolForRuntime(String(value)) : current.provider_tool;
      return {
        ...current,
        [key]: value,
        provider_tool: nextRuntimeRouteTool,
        provider_id: key === "runtime_id" ? "" : current.provider_id,
        default_model: routeChanged ? "" : current.default_model,
        default_reasoning_effort: routeChanged ? "" : current.default_reasoning_effort,
        default_service_tier: routeChanged ? "" : current.default_service_tier,
        default_approval_mode: key === "runtime_id" ? "" : current.default_approval_mode,
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
      await syncAgentConnectBindings(saved.id, selectedChannelIDs, selectedTriggerIDs, channelItems, triggerItems);
      await agents.reload();
      await activeRoutes.reload();
      await channels.reload();
      await triggers.reload();
      setDrawerMode(null);
      setDrawerDraft(null);
      setSelectedChannelIDs([]);
      setSelectedTriggerIDs([]);
      setNotice(t("agents.saved"));
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
  const canSave = Boolean(
    drawerDraft &&
      drawerDraft.name.trim() &&
      drawerDraft.runtime_id &&
      (drawerMode === "edit" || runtimeOptions.includes(drawerDraft.runtime_id)) &&
      !readOnly
  );
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
                canSave={canSave}
                activeRoutes={activeRouteItems}
                channelOptions={channelItems}
                cliOptions={cliCatalog.map((tool) => ({ id: tool.spec.id, name: tool.spec.name, note: tool.spec.note }))}
                compatibleProviders={compatibleProviders}
                draft={draft}
                mcpOptions={mcpOptions.map((server) => server.name)}
                readOnly={readOnly}
                runtimeOptions={runtimeOptions}
                selectedChannelIDs={selectedChannelIDs}
                selectedTriggerIDs={selectedTriggerIDs}
                skillOptions={skillOptions.map((skill) => skill.name)}
                triggerOptions={triggerItems}
                busy={busy}
                drawerMode={drawerMode}
                onDelete={remove}
                onSave={save}
                onToggleChannel={(id) => setSelectedChannelIDs((current) => toggleID(current, id))}
                onToggleTrigger={(id) => setSelectedTriggerIDs((current) => toggleID(current, id))}
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
              <article
                className="agent-registry-row"
                key={item.id}
                onDoubleClick={() => editAgent(item)}
                title={t("agents.doubleClickToEdit")}
              >
                <div className="agent-list-main">
                  <span className="provider-icon">
                    <Bot size={15} />
                  </span>
                  <span>
                    <strong>{item.name}</strong>
                    <small>{item.runtime_id ? runtimeLabel(item.runtime_id) : t("agents.noRuntime")}</small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <span className={`status-badge ${item.enabled ? "success" : "warning"}`}>
                    <span className="status-dot" />
                    {item.enabled ? t("common.enabled") : t("common.disabled")}
                  </span>
                  <span className={`source-badge ${agentSourceClass(item)}`}>{t(agentSourceLabelKey(item))}</span>
                  <span className="pill">{agentProviderSummary(item, activeRouteItems, t)}</span>
                  {item.default_model && <span className="pill">{item.default_model}</span>}
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
