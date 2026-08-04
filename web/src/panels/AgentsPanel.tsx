import { Bot, Cable, Eye, FolderOpen, FolderPlus, Link2, Pencil, Plus, RefreshCw, Save, Trash2, Workflow, X, Zap } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { api, type AgentInstance, type Channel, type Provider, type ProviderRoute, type Trigger } from "../api";
import { ChannelAvatar } from "../ChannelAvatar";
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
  default_model: "",
  default_reasoning_effort: "",
  default_service_tier: "",
  memory_scope: "",
  channel_bindings: [],
  schedules: [],
  mcp_servers: [],
  skills: [],
  clis: [],
  enabled: true,
  source: "manual",
};

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

type CLIOption = { id: string; name: string; note?: string };

function AgentForm({
  busy,
  canSave,
  activeRoutes,
  channelOptions,
  cliOptions,
  compatibleProviders,
  draft,
  drawerMode,
  mcpOptions,
  onDelete,
  onSave,
  onToggleChannel,
  onToggleTrigger,
  onUpdate,
  readOnly,
  runtimeOptions,
  selectedChannelIDs,
  selectedTriggerIDs,
  skillOptions,
  triggerOptions,
  t,
}: {
  busy: string;
  canSave: boolean;
  activeRoutes: ProviderRoute[];
  channelOptions: Channel[];
  cliOptions: CLIOption[];
  compatibleProviders: Provider[];
  draft: AgentInstance;
  drawerMode: DrawerMode | null;
  mcpOptions: string[];
  onDelete: () => void;
  onSave: () => void;
  onToggleChannel: (id: string) => void;
  onToggleTrigger: (id: string) => void;
  onUpdate: <K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) => void;
  readOnly: boolean;
  runtimeOptions: string[];
  selectedChannelIDs: string[];
  selectedTriggerIDs: string[];
  skillOptions: string[];
  triggerOptions: Trigger[];
  t: (key: string) => string;
}) {
  const [directoryBusy, setDirectoryBusy] = useState("");
  const [directoryNotice, setDirectoryNotice] = useState("");
  const [promptView, setPromptView] = useState<"edit" | "preview">(readOnly ? "preview" : "edit");
  const injectedPrompt = useMemo(() => {
    const logPaths = selectedChannelIDs.map((id) => `~/.agentmux/logs/channels/${id}.jsonl`);
    const clis = (draft.clis ?? [])
      .map((id) => cliOptions.find((option) => option.id === id))
      .filter((option): option is CLIOption => Boolean(option))
      .map((option) => ({ name: option.name, note: option.note ?? "" }));
    return composeInjectedPrompt(draft.system_prompt ?? "", logPaths, clis);
  }, [draft.system_prompt, draft.clis, selectedChannelIDs, cliOptions]);
  const selectedRouteTool = draft.provider_tool || routeToolForRuntime(draft.runtime_id);
  const routeToolOptions = routeToolOptionsForRuntime(draft.runtime_id);
  const activeRoute = activeRouteForTool(activeRoutes, selectedRouteTool);
  const activeRouteProvider = activeRoute?.configured ? activeRoute.provider_name || activeRoute.provider_id || "" : "";
  const overrideProvider = compatibleProviders.find((provider) => provider.id === draft.provider_id);
  const routeProvider = activeRoute?.configured
    ? compatibleProviders.find((provider) => provider.id === activeRoute.provider_id)
    : undefined;
  const modelProvider = overrideProvider ?? routeProvider;
  const modelOptions = providerModelOptions(modelProvider);
	const reasoningOptions = runtimeProviderOptions(modelProvider, "supported_reasoning_efforts");
	const serviceTierOptions = runtimeProviderOptions(modelProvider, "supported_service_tiers");
  const usingLocalLogin = !draft.provider_id && !activeRouteProvider;
  const providerStatus = draft.provider_id
    ? `${t("agents.providerOverrideActive")} ${overrideProvider?.name || draft.provider_id}`
    : activeRouteProvider
      ? `${t("agents.activeRouteProvider")} ${runtimeLabel(selectedRouteTool)} -> ${activeRouteProvider}`
      : `${t("agents.noActiveRouteProvider")} ${runtimeLabel(selectedRouteTool)}. ${t("agents.localLoginActive")}`;
  const defaultModelStatus =
    modelOptions.length > 0
      ? t("agents.defaultModelHelp")
      : modelProvider
        ? t("agents.defaultModelUnavailable")
        : usingLocalLogin
          ? t("agents.defaultModelLocalLogin")
          : t("agents.defaultModelNoProvider");

  useEffect(() => {
    if (readOnly || !draft.runtime_id) return;
    const nextRouteTool = routeToolForRuntime(draft.runtime_id);
    if (draft.provider_tool !== nextRouteTool) onUpdate("provider_tool", nextRouteTool);
  }, [draft.provider_tool, draft.runtime_id, onUpdate, readOnly]);

  useEffect(() => {
    if (readOnly || !draft.default_model) return;
    if (modelOptions.length === 0 || !modelOptions.includes(draft.default_model)) {
      onUpdate("default_model", "");
    }
  }, [draft.default_model, modelOptions.join("\u0000"), onUpdate, readOnly]);

  async function selectWorkDir() {
    setDirectoryBusy("select");
    setDirectoryNotice("");
    try {
      const selected = await api.selectDirectory(draft.work_dir ?? "");
      if (selected.path) {
        onUpdate("work_dir", selected.path);
        setDirectoryNotice(t("agents.workDirSelected"));
      }
    } catch (err) {
      setDirectoryNotice(workDirErrorMessage(err, t));
    } finally {
      setDirectoryBusy("");
    }
  }

  async function createWorkDir() {
    setDirectoryBusy("create");
    setDirectoryNotice("");
    try {
      const created = await api.ensureDirectory(draft.work_dir ?? "");
      onUpdate("work_dir", created.path);
      setDirectoryNotice(t("agents.workDirCreated"));
    } catch (err) {
      setDirectoryNotice(workDirErrorMessage(err, t));
    } finally {
      setDirectoryBusy("");
    }
  }

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
                  {runtimeLabel(runtime)}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>{t("agents.workDir")}</span>
            <div className="directory-input-row">
              <input disabled={readOnly} value={draft.work_dir ?? ""} onChange={(event) => onUpdate("work_dir", event.target.value)} />
              <button
                className="ghost-action icon-action"
                disabled={readOnly || directoryBusy === "select"}
                onClick={selectWorkDir}
                title={t("agents.selectWorkDir")}
                type="button"
                aria-label={t("agents.selectWorkDir")}
              >
                <FolderOpen size={15} />
              </button>
              <button
                className="ghost-action icon-action"
                disabled={readOnly || directoryBusy === "create"}
                onClick={createWorkDir}
                title={t("agents.createWorkDir")}
                type="button"
                aria-label={t("agents.createWorkDir")}
              >
                <FolderPlus size={15} />
              </button>
            </div>
            {directoryNotice && <small className="directory-notice">{directoryNotice}</small>}
          </label>
          <label className="field">
            <span>{t("agents.memoryScope")}</span>
            <input
              disabled={readOnly}
              value={draft.memory_scope ?? ""}
              onChange={(event) => onUpdate("memory_scope", event.target.value)}
            />
          </label>
          <div className="field wide agent-prompt-field">
            <div className="field-label-row">
              <span>{t("agents.systemPrompt")}</span>
              <div className="markdown-mode-toggle" role="group" aria-label={t("agents.markdownViewMode")}>
                <button
                  className={promptView === "edit" ? "active" : ""}
                  type="button"
                  aria-pressed={promptView === "edit"}
                  onClick={() => setPromptView("edit")}
                >
                  <Pencil size={13} />
                  {t("agents.markdownEdit")}
                </button>
                <button
                  className={promptView === "preview" ? "active" : ""}
                  type="button"
                  aria-pressed={promptView === "preview"}
                  onClick={() => setPromptView("preview")}
                >
                  <Eye size={14} />
                  {t("agents.markdownPreview")}
                </button>
              </div>
            </div>
            {promptView === "edit" ? (
              <textarea
                className="markdown-editor"
                disabled={readOnly}
                rows={8}
                value={draft.system_prompt ?? ""}
                onChange={(event) => onUpdate("system_prompt", event.target.value)}
                placeholder={t("agents.markdownPlaceholder")}
              />
            ) : (
              <MarkdownPreview content={draft.system_prompt ?? ""} empty={t("agents.markdownPreviewEmpty")} />
            )}
            <small>{t("agents.markdownHelp")}</small>
          </div>
          <div className="field wide">
            <span>{t("agents.injectedPrompt")}</span>
            <MarkdownPreview
              className="injected-prompt-preview"
              content={injectedPrompt}
              empty={t("agents.injectedPromptEmpty")}
            />
            <small>{t("agents.injectedPromptHelp")}</small>
          </div>
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
              disabled={readOnly || routeToolOptions.length <= 1}
              value={selectedRouteTool}
              onChange={(event) => onUpdate("provider_tool", event.target.value)}
            >
              {routeToolOptions.map((tool) => (
                <option key={tool} value={tool}>
                  {runtimeLabel(tool)}
                </option>
              ))}
            </select>
            <small>{t("agents.routeToolFollowsRuntime")}</small>
          </label>
          <label className="field">
            <span>{t("agents.providerOverride")}</span>
            <select disabled={readOnly} value={draft.provider_id ?? ""} onChange={(event) => onUpdate("provider_id", event.target.value)}>
              <option value="">{activeRouteProvider ? t("agents.followActiveRoute") : t("agents.useLocalLogin")}</option>
              {compatibleProviders.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name}
                </option>
              ))}
            </select>
            <small>{providerStatus}</small>
          </label>
          {usingLocalLogin && (
            <div className="agent-route-notice">
              <strong>
                {t("agents.routeMissingTitle")} {runtimeLabel(selectedRouteTool)}
              </strong>
              <span>{t("agents.routeMissingLocalLogin")}</span>
            </div>
          )}
          <label className="field">
            <span>{t("agents.defaultModel")}</span>
            <select
              disabled={readOnly || modelOptions.length === 0}
              value={draft.default_model ?? ""}
              onChange={(event) => onUpdate("default_model", event.target.value)}
            >
              <option value="">{t("agents.defaultModelPlaceholder")}</option>
              {modelOptions.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </select>
            <small>{defaultModelStatus}</small>
          </label>
		  <label className="field">
			<span>{t("agents.defaultReasoningEffort")}</span>
			<select disabled={readOnly || reasoningOptions.length === 0} value={draft.default_reasoning_effort ?? ""} onChange={(event) => onUpdate("default_reasoning_effort", event.target.value)}>
			  <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
			  {reasoningOptions.map((value) => <option key={value} value={value}>{value}</option>)}
			</select>
			<small>{t("agents.defaultReasoningEffortHelp")}</small>
		  </label>
		  <label className="field">
			<span>{t("agents.defaultServiceTier")}</span>
			<select disabled={readOnly || serviceTierOptions.length === 0} value={draft.default_service_tier ?? ""} onChange={(event) => onUpdate("default_service_tier", event.target.value)}>
			  <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
			  {serviceTierOptions.map((value) => <option key={value} value={value}>{serviceTierLabel(value)}</option>)}
			</select>
			<small>{t("agents.defaultServiceTierHelp")}</small>
		  </label>
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Link2 size={17} />
          <h3>{t("nav.connect")}</h3>
        </header>
        <p className="subtle-copy">{t("agents.connectBindingHelp")}</p>
        <div className="agent-picker-grid">
          <div className="mapping-card">
            <strong>
              <Cable size={13} /> {t("agents.boundChannels")} ({selectedChannelIDs.length})
            </strong>
            {channelOptions.length === 0 && <span className="muted">{t("connect.noChannels")}</span>}
            <div className="provider-chip-row agent-channel-options">
              {channelOptions.map((channel) => {
                const selected = selectedChannelIDs.includes(channel.id);
                const displayName = channel.bot_name || channel.name;
                const subtitle = [
                  channel.bot_name && channel.name !== channel.bot_name ? channel.name : "",
                  channel.type,
                  (channel.agent_name || channel.agent_id) && !selected ? channel.agent_name || channel.agent_id : "",
                ]
                  .filter(Boolean)
                  .join(" · ");
                return (
                  <button
                    key={channel.id}
                    className={`agent-channel-option ${selected ? "selected" : ""}`}
                    disabled={readOnly}
                    onClick={() => onToggleChannel(channel.id)}
                    title={subtitle ? `${displayName} · ${subtitle}` : displayName}
                    type="button"
                  >
                    <ChannelAvatar channel={channel} size="small" />
                    <span className="agent-channel-copy">
                      <strong>{displayName}</strong>
                      {subtitle && <small>{subtitle}</small>}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
          <div className="mapping-card">
            <strong>
              <Zap size={13} /> {t("agents.boundTriggers")} ({selectedTriggerIDs.length})
            </strong>
            {triggerOptions.length === 0 && <span className="muted">{t("connect.noTriggers")}</span>}
            <div className="provider-chip-row">
              {triggerOptions.map((trigger) => (
                <button
                  key={trigger.id}
                  className={`status-badge ${selectedTriggerIDs.includes(trigger.id) ? "success" : ""}`}
                  disabled={readOnly}
                  onClick={() => onToggleTrigger(trigger.id)}
                  type="button"
                >
                  {trigger.name} · {trigger.kind}
                  {(trigger.agent_name || trigger.agent_id) && !selectedTriggerIDs.includes(trigger.id)
                    ? ` · ${trigger.agent_name || trigger.agent_id}`
                    : ""}
                </button>
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
          <Picker
            title={t("agents.clis")}
            items={cliOptions.map((option) => option.id)}
            labels={Object.fromEntries(cliOptions.map((option) => [option.id, option.name]))}
            selected={draft.clis ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("clis", next)}
            empty={t("agents.clisEmpty")}
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

function MarkdownPreview({
  className = "",
  content,
  empty,
}: {
  className?: string;
  content: string;
  empty: string;
}) {
  if (!content.trim()) {
    return <div className={`markdown-preview markdown-preview-empty ${className}`.trim()}>{empty}</div>;
  }

  return (
    <div className={`markdown-preview ${className}`.trim()}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml>
        {content}
      </ReactMarkdown>
    </div>
  );
}

function Picker({
  title,
  items,
  labels,
  selected,
  readOnly,
  onChange,
  empty,
}: {
  title: string;
  items: string[];
  labels?: Record<string, string>;
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
              {labels?.[item] ?? item}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// composeInjectedPrompt mirrors core.ComposeSystemPrompt so the agent form can
// preview the exact prompt injected at runtime.
function composeInjectedPrompt(
  base: string,
  logPaths: string[],
  clis: { name: string; note: string }[]
): string {
  const sections: string[] = [];
  const trimmedBase = base.replace(/\n+$/, "");

  if (logPaths.length > 0) {
    sections.push(["绑定的事件回调日志路径为：", ...logPaths.map((path) => `- ${path}`)].join("\n"));
  }
  if (clis.length > 0) {
    const lines = clis
      .filter((cli) => cli.name.trim())
      .map((cli) => (cli.note.trim() ? `- ${cli.name}：${cli.note}` : `- ${cli.name}`));
    sections.push(["已启用以下 CLI 工具：", ...lines].join("\n"));
  }

  return [trimmedBase, ...sections].filter((part) => part !== "").join("\n\n");
}

async function syncAgentConnectBindings(
  agentID: string,
  selectedChannelIDs: string[],
  selectedTriggerIDs: string[],
  channels: Channel[],
  triggers: Trigger[]
) {
  const selectedChannels = new Set(selectedChannelIDs);
  const selectedTriggers = new Set(selectedTriggerIDs);
  const channelUpdates = channels
    .map((channel) => {
      const currentAgentID = channel.agent_id ?? "";
      const nextAgentID = selectedChannels.has(channel.id) ? agentID : currentAgentID === agentID ? "" : currentAgentID;
      return nextAgentID === currentAgentID ? null : api.upsertChannel({ ...channel, agent_id: nextAgentID });
    })
    .filter((update): update is Promise<Channel> => Boolean(update));
  const triggerUpdates = triggers
    .map((trigger) => {
      const currentAgentID = trigger.agent_id ?? "";
      const nextAgentID = selectedTriggers.has(trigger.id) ? agentID : currentAgentID === agentID ? "" : currentAgentID;
      return nextAgentID === currentAgentID ? null : api.upsertTrigger({ ...trigger, agent_id: nextAgentID });
    })
    .filter((update): update is Promise<Trigger> => Boolean(update));
  await Promise.all([...channelUpdates, ...triggerUpdates]);
}

function toggleID(items: string[], id: string): string[] {
  return items.includes(id) ? items.filter((item) => item !== id) : [...items, id];
}

function newAgent(runtimeOptions: string[]): AgentInstance {
  const runtime = runtimeOptions[0] ?? "codex";
  return copyAgent({
    ...EMPTY_AGENT,
    runtime_id: runtime,
    provider_tool: routeToolForRuntime(runtime),
    memory_scope: "agent:new",
    source: "manual",
  });
}

function routeToolForRuntime(runtime: string): string {
  return normalizeTool(runtime);
}

function routeToolOptionsForRuntime(runtime: string): string[] {
  const tool = routeToolForRuntime(runtime);
  return tool ? [tool] : [];
}

function runtimeLabel(runtime: string): string {
  switch (runtime) {
    case "claudecode":
      return "Claude Code CLI";
    case "codex":
      return "Codex CLI";
    case "cursor":
      return "Cursor Agent CLI";
    case "gemini":
      return "Gemini CLI";
    case "iflow":
      return "iFlow CLI";
    case "kimi":
      return "Kimi CLI";
    case "opencode":
      return "OpenCode CLI";
    case "qoder":
      return "Qoder CLI";
    case "claude-desktop":
      return "Claude Desktop";
    case "codex-app":
      return "Codex Desktop";
    default:
      return runtime;
  }
}

function activeRouteForTool(routes: ProviderRoute[], tool: string): ProviderRoute | undefined {
  const normalized = normalizeTool(tool);
  return routes.find((route) => route.tool === tool || normalizeTool(route.tool) === normalized);
}

function agentProviderSummary(agent: AgentInstance, activeRoutes: ProviderRoute[], t: (key: string) => string): string {
  if (agent.provider_name || agent.provider_id) return `${t("agents.providerOverrideShort")}: ${agent.provider_name || agent.provider_id}`;
  const route = activeRouteForTool(activeRoutes, agent.provider_tool || agent.runtime_id);
  const provider = route?.configured ? route.provider_name || route.provider_id : "";
  return provider ? `${t("agents.followRouteShort")}: ${provider}` : t("agents.followRouteShort");
}

function normalizeTool(tool: string): string {
  switch (tool.trim()) {
    case "claude":
    case "claudecode":
    case "claudecode-cli":
    case "claude-code-cli":
      return "claudecode";
    case "claude-desktop":
    case "claudecode-desktop":
    case "claude-code-desktop":
      return "claude-desktop";
    case "codex":
    case "codex-cli":
    case "codex-app":
    case "codex-desktop":
    case "codex-app-server":
      return "codex";
    default:
      return tool.trim();
  }
}

function providerModelOptions(provider: Provider | undefined): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const add = (model: unknown) => {
    if (typeof model !== "string") return;
    const trimmed = model.trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    out.push(trimmed);
  };
  add(provider?.model);
  const supported = provider?.meta?.supported_models;
  if (Array.isArray(supported)) supported.forEach(add);
  return out;
}

function runtimeProviderOptions(provider: Provider | undefined, key: "supported_reasoning_efforts" | "supported_service_tiers"): string[] {
	const values = provider?.meta?.[key];
	if (!Array.isArray(values)) return [];
	return values.filter((value): value is string => typeof value === "string" && value.trim().length > 0).map((value) => value.trim());
}

function serviceTierLabel(value: string): string {
	if (value === "priority" || value === "fast") return `${value} (${value === "priority" ? "快速" : "fast"})`;
	if (value === "default" || value === "normal" || value === "standard") return `${value} (普通)`;
	return value;
}

function workDirErrorMessage(err: unknown, t: (key: string) => string): string {
  const message = err instanceof Error ? err.message : String(err);
  if (message.includes("desktop directory picker unavailable")) return t("agents.workDirPickerUnavailable");
  if (message.includes("directory path is required")) return t("agents.workDirRequired");
  if (message.includes("path is not a directory")) return t("agents.workDirNotDirectory");
  return message;
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
