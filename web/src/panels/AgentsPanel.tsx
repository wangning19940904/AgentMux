import { Bot, CalendarClock, Link2, Plus, Save, Trash2, Workflow } from "lucide-react";
import type { Dispatch, ReactNode, SetStateAction } from "react";
import { useEffect, useMemo, useState } from "react";
import { AgentChannelBinding, AgentInstance, AgentSchedule, api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

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
  source: "console",
};

export function AgentsPanel() {
  const { t } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const runtimes = useAsync(() => api.agents(), []);
  const providers = useAsync(() => api.providers(), []);
  const platforms = useAsync(() => api.platforms(), []);
  const mcpServers = useAsync(() => api.mcp(), []);
  const skills = useAsync(() => api.skills(), []);
  const [selectedID, setSelectedID] = useState("");
  const [draft, setDraft] = useState<AgentInstance>(EMPTY_AGENT);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const items = agents.data ?? [];
  const runtimeOptions = runtimes.data ?? [];
  const providerOptions = providers.data ?? [];
  const platformOptions = platforms.data ?? [];
  const mcpOptions = mcpServers.data ?? [];
  const skillOptions = skills.data ?? [];
  const selected = items.find((item) => item.id === selectedID);

  useEffect(() => {
    if (!selectedID && items.length > 0) {
      setSelectedID(items[0].id);
      setDraft(copyAgent(items[0]));
    }
  }, [items, selectedID]);

  useEffect(() => {
    if (selected) {
      setDraft(copyAgent(selected));
    }
  }, [selected?.id]);

  useEffect(() => {
    if (!draft.runtime_id && runtimeOptions.length > 0) {
      setDraft((current) => ({
        ...current,
        runtime_id: runtimeOptions[0],
        provider_tool: runtimeOptions[0],
        memory_scope: current.memory_scope || "agent:auto",
      }));
    }
  }, [draft.runtime_id, runtimeOptions]);

  const compatibleProviders = useMemo(() => {
    const tool = draft.provider_tool || draft.runtime_id;
    return providerOptions.filter((provider) => !tool || provider.tools.includes(tool));
  }, [draft.provider_tool, draft.runtime_id, providerOptions]);

  const metrics = useMemo(() => {
    const channelCount = items.reduce((sum, item) => sum + (item.channel_bindings?.length ?? 0), 0);
    const scheduleCount = items.reduce((sum, item) => sum + (item.schedules?.length ?? 0), 0);
    const consoleCount = items.filter((item) => item.source !== "config.toml").length;
    return { channelCount, scheduleCount, consoleCount };
  }, [items]);

  function startNew() {
    const runtime = runtimeOptions[0] ?? "codex";
    const next = copyAgent({
      ...EMPTY_AGENT,
      runtime_id: runtime,
      provider_tool: runtime,
      memory_scope: "agent:new",
    });
    setSelectedID("");
    setDraft(next);
    setNotice("");
  }

  function update<K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) {
    setDraft((current) => ({
      ...current,
      [key]: value,
      provider_tool: key === "runtime_id" ? String(value) : current.provider_tool,
      provider_id: key === "runtime_id" ? "" : current.provider_id,
    }));
  }

  async function save() {
    setBusy("save");
    setNotice("");
    try {
      const saved = await api.upsertAgentInstance({
        ...draft,
        provider_tool: draft.provider_tool || draft.runtime_id,
        memory_scope: draft.memory_scope || `agent:${draft.id || "new"}`,
      });
      setSelectedID(saved.id);
      setDraft(copyAgent(saved));
      setNotice(t("agents.saved"));
      await agents.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function remove() {
    if (!draft.id || draft.source === "config.toml") return;
    setBusy("delete");
    setNotice("");
    try {
      await api.deleteAgentInstance(draft.id);
      setSelectedID("");
      setDraft(EMPTY_AGENT);
      setNotice(t("agents.deleted"));
      await agents.reload();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  const readOnly = draft.source === "config.toml";
  const canSave = Boolean(draft.name.trim() && draft.runtime_id && !readOnly);

  return (
    <div className="page-stack agents-page">
      <div className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("agents.title")}</h2>
            <p className="subtle-copy">{t("agents.subtitle")}</p>
          </div>
          <button className="action" onClick={startNew}>
            <Plus size={16} />
            {t("agents.new")}
          </button>
        </div>
        <div className="surface-body agent-metrics">
          <Summary label={t("agents.total")} value={items.length} />
          <Summary label={t("agents.consoleManaged")} value={metrics.consoleCount} />
          <Summary label={t("agents.runtimes")} value={runtimeOptions.length} />
          <Summary label={t("agents.channels")} value={metrics.channelCount} />
          <Summary label={t("agents.schedules")} value={metrics.scheduleCount} />
        </div>
      </div>

      <div className="agent-workspace">
        <div className="surface">
          <div className="surface-header">
            <h2>{t("agents.registry")}</h2>
            <button className="ghost-action" onClick={agents.reload}>
              {t("common.refresh")}
            </button>
          </div>
          <div className="agent-list">
            {agents.loading && <div className="empty-state">{t("common.loading")}</div>}
            {agents.error && <div className="empty-state error">{String(agents.error)}</div>}
            {!agents.loading && !agents.error && items.length === 0 && (
              <div className="empty-state">{t("agents.empty")}</div>
            )}
            {items.map((item) => (
              <button
                key={item.id}
                className={item.id === draft.id ? "active" : ""}
                onClick={() => {
                  setSelectedID(item.id);
                  setDraft(copyAgent(item));
                  setNotice("");
                }}
              >
                <div className="agent-list-main">
                  <span className="provider-icon">
                    <Bot size={14} />
                  </span>
                  <span>
                    <strong>{item.name}</strong>
                    <small>{item.runtime_id}</small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <span className={`status-badge ${item.enabled ? "success" : "warning"}`}>
                    <span className="status-dot" />
                    {item.enabled ? t("common.enabled") : t("common.disabled")}
                  </span>
                  <span className="pill">{item.source || "console"}</span>
                </div>
                <small className="muted">
                  {item.provider_name || item.provider_id || t("agents.noProvider")} ·{" "}
                  {item.channel_bindings?.length ?? 0} {t("agents.channelCount")}
                </small>
              </button>
            ))}
          </div>
        </div>

        <div className="surface">
          <div className="surface-header">
            <div>
              <h2>{draft.id ? draft.name || t("agents.detail") : t("agents.new")}</h2>
              <p className="subtle-copy">
                {readOnly ? t("agents.configManaged") : t("agents.consoleManagedDetail")}
              </p>
            </div>
            <div className="table-actions">
              <button className="ghost-action danger-action" disabled={!draft.id || readOnly || busy === "delete"} onClick={remove}>
                <Trash2 size={15} />
                {t("common.delete")}
              </button>
              <button className="action" disabled={!canSave || busy === "save"} onClick={save}>
                <Save size={15} />
                {t("common.save")}
              </button>
            </div>
          </div>
          {notice && <div className={`session-notice ${notice.includes("failed") ? "error" : ""}`}>{notice}</div>}
          <div className="surface-body agent-detail-stack">
            <section className="agent-section">
              <header>
                <Workflow size={17} />
                <h3>{t("agents.identity")}</h3>
              </header>
              <div className="field-grid">
                <label className="field">
                  <span>{t("agents.name")}</span>
                  <input disabled={readOnly} value={draft.name} onChange={(event) => update("name", event.target.value)} />
                </label>
                <label className="field">
                  <span>{t("agents.runtime")}</span>
                  <select
                    disabled={readOnly}
                    value={draft.runtime_id}
                    onChange={(event) => update("runtime_id", event.target.value)}
                  >
                    {runtimeOptions.map((runtime) => (
                      <option key={runtime} value={runtime}>
                        {runtime}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="field">
                  <span>{t("agents.workDir")}</span>
                  <input disabled={readOnly} value={draft.work_dir ?? ""} onChange={(event) => update("work_dir", event.target.value)} />
                </label>
                <label className="field">
                  <span>{t("agents.memoryScope")}</span>
                  <input
                    disabled={readOnly}
                    value={draft.memory_scope ?? ""}
                    onChange={(event) => update("memory_scope", event.target.value)}
                  />
                </label>
                <label className="field wide">
                  <span>{t("agents.systemPrompt")}</span>
                  <textarea
                    disabled={readOnly}
                    rows={3}
                    value={draft.system_prompt ?? ""}
                    onChange={(event) => update("system_prompt", event.target.value)}
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
                    onChange={(event) => update("provider_tool", event.target.value)}
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
                  <select
                    disabled={readOnly}
                    value={draft.provider_id ?? ""}
                    onChange={(event) => update("provider_id", event.target.value)}
                  >
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

            <SectionList
              title={t("agents.channelBindings")}
              icon={<Link2 size={17} />}
              addLabel={t("agents.addChannel")}
              disabled={readOnly}
              onAdd={() => setDraft((current) => ({ ...current, channel_bindings: [...(current.channel_bindings ?? []), newChannel(platformOptions[0] ?? "webhook")] }))}
            >
              <div className="agent-mini-list">
                {(draft.channel_bindings ?? []).map((channel, index) => (
                  <ChannelEditor
                    key={channel.id || index}
                    channel={channel}
                    platforms={platformOptions}
                    readOnly={readOnly}
                    onChange={(next) => replaceChannel(index, next, setDraft)}
                    onRemove={() => removeChannel(index, setDraft)}
                  />
                ))}
                {(draft.channel_bindings ?? []).length === 0 && <div className="empty-card">{t("agents.noChannels")}</div>}
              </div>
            </SectionList>

            <SectionList
              title={t("agents.automations")}
              icon={<CalendarClock size={17} />}
              addLabel={t("agents.addSchedule")}
              disabled={readOnly}
              onAdd={() => setDraft((current) => ({ ...current, schedules: [...(current.schedules ?? []), newSchedule()] }))}
            >
              <div className="agent-mini-list">
                {(draft.schedules ?? []).map((schedule, index) => (
                  <ScheduleEditor
                    key={schedule.id || index}
                    schedule={schedule}
                    readOnly={readOnly}
                    onChange={(next) => replaceSchedule(index, next, setDraft)}
                    onRemove={() => removeSchedule(index, setDraft)}
                  />
                ))}
                {(draft.schedules ?? []).length === 0 && <div className="empty-card">{t("agents.noSchedules")}</div>}
              </div>
            </SectionList>

            <section className="agent-section">
              <header>
                <Bot size={17} />
                <h3>{t("agents.tools")}</h3>
              </header>
              <div className="agent-picker-grid">
                <Picker
                  title={t("agents.mcpServers")}
                  items={mcpOptions.map((server) => server.name)}
                  selected={draft.mcp_servers ?? []}
                  readOnly={readOnly}
                  onChange={(next) => update("mcp_servers", next)}
                  empty={t("mcp.empty")}
                />
                <Picker
                  title={t("agents.skills")}
                  items={skillOptions.map((skill) => skill.name)}
                  selected={draft.skills ?? []}
                  readOnly={readOnly}
                  onChange={(next) => update("skills", next)}
                  empty={t("skills.empty")}
                />
              </div>
            </section>
          </div>
        </div>
      </div>
    </div>
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

function SectionList({
  title,
  icon,
  addLabel,
  disabled,
  onAdd,
  children,
}: {
  title: string;
  icon: ReactNode;
  addLabel: string;
  disabled?: boolean;
  onAdd: () => void;
  children: ReactNode;
}) {
  return (
    <section className="agent-section">
      <header>
        {icon}
        <h3>{title}</h3>
        <button className="ghost-action" disabled={disabled} onClick={onAdd}>
          <Plus size={14} />
          {addLabel}
        </button>
      </header>
      {children}
    </section>
  );
}

function ChannelEditor({
  channel,
  platforms,
  readOnly,
  onChange,
  onRemove,
}: {
  channel: AgentChannelBinding;
  platforms: string[];
  readOnly: boolean;
  onChange: (next: AgentChannelBinding) => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="route-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("agents.channelType")}</span>
          <select disabled={readOnly} value={channel.type} onChange={(event) => onChange({ ...channel, type: event.target.value })}>
            {platforms.map((platform) => (
              <option key={platform} value={platform}>
                {platform}
              </option>
            ))}
            <option value="webhook">webhook</option>
          </select>
        </label>
        <label className="field">
          <span>{t("agents.channelName")}</span>
          <input disabled={readOnly} value={channel.name ?? ""} onChange={(event) => onChange({ ...channel, name: event.target.value })} />
        </label>
        <label className="field">
          <span>{t("agents.chatId")}</span>
          <input disabled={readOnly} value={channel.chat_id ?? ""} onChange={(event) => onChange({ ...channel, chat_id: event.target.value })} />
        </label>
        <label className="field">
          <span>{t("common.status")}</span>
          <select disabled={readOnly} value={channel.status ?? "draft"} onChange={(event) => onChange({ ...channel, status: event.target.value })}>
            <option value="draft">draft</option>
            <option value="configured">configured</option>
            <option value="healthy">healthy</option>
          </select>
        </label>
      </div>
      {!readOnly && (
        <div className="table-actions">
          <button className="ghost-action danger-action" onClick={onRemove}>
            <Trash2 size={14} />
            {t("common.delete")}
          </button>
        </div>
      )}
    </div>
  );
}

function ScheduleEditor({
  schedule,
  readOnly,
  onChange,
  onRemove,
}: {
  schedule: AgentSchedule;
  readOnly: boolean;
  onChange: (next: AgentSchedule) => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="route-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("agents.scheduleName")}</span>
          <input disabled={readOnly} value={schedule.name} onChange={(event) => onChange({ ...schedule, name: event.target.value })} />
        </label>
        <label className="field">
          <span>{t("agents.cron")}</span>
          <input disabled={readOnly} value={schedule.cron} onChange={(event) => onChange({ ...schedule, cron: event.target.value })} />
        </label>
        <label className="field wide">
          <span>{t("agents.prompt")}</span>
          <textarea disabled={readOnly} rows={2} value={schedule.prompt} onChange={(event) => onChange({ ...schedule, prompt: event.target.value })} />
        </label>
      </div>
      {!readOnly && (
        <div className="table-actions">
          <label className="switch-row">
            <span>
              <strong>{t("common.enabled")}</strong>
            </span>
            <input type="checkbox" checked={schedule.enabled} onChange={(event) => onChange({ ...schedule, enabled: event.target.checked })} />
          </label>
          <button className="ghost-action danger-action" onClick={onRemove}>
            <Trash2 size={14} />
            {t("common.delete")}
          </button>
        </div>
      )}
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

function newChannel(type: string): AgentChannelBinding {
  return { id: `channel-${Date.now()}`, type, name: type, status: "draft" };
}

function newSchedule(): AgentSchedule {
  return { id: `schedule-${Date.now()}`, name: "daily check", cron: "0 9 * * *", prompt: "", enabled: true };
}

function replaceChannel(index: number, next: AgentChannelBinding, setDraft: Dispatch<SetStateAction<AgentInstance>>) {
  setDraft((current) => {
    const items = [...(current.channel_bindings ?? [])];
    items[index] = next;
    return { ...current, channel_bindings: items };
  });
}

function removeChannel(index: number, setDraft: Dispatch<SetStateAction<AgentInstance>>) {
  setDraft((current) => ({
    ...current,
    channel_bindings: (current.channel_bindings ?? []).filter((_, itemIndex) => itemIndex !== index),
  }));
}

function replaceSchedule(index: number, next: AgentSchedule, setDraft: Dispatch<SetStateAction<AgentInstance>>) {
  setDraft((current) => {
    const items = [...(current.schedules ?? [])];
    items[index] = next;
    return { ...current, schedules: items };
  });
}

function removeSchedule(index: number, setDraft: Dispatch<SetStateAction<AgentInstance>>) {
  setDraft((current) => ({
    ...current,
    schedules: (current.schedules ?? []).filter((_, itemIndex) => itemIndex !== index),
  }));
}
