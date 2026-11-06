import { Bot, Play, Plus, RefreshCw, RotateCcw, Save, Square, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import type { AgentInstance, Channel, SuperAgent, SuperAgentRouter, SuperAgentRouterList, SuperAgentSource, SuperAgentSourceKind, SuperAgentWorker } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { FeishuActorPicker, FeishuChatPicker, useFeishuDirectory } from "./superAgentFeishu";
import { describeModelChoice, ModelPicker } from "./superAgentModelPicker";
import type { ModelChoice } from "./superAgentModelPicker";
import { NEW_ROUTER, SOURCE_FIELDS, SOURCE_KINDS, githubWebhookPath, isFeishuSource, newRouter, newSource, newSuperAgent, newWorker, parseActors, superAgentProblems, withWorkerRuntimes } from "./superAgentModel";

export type SuperAgentsPanelProps = {
  createRequested?: boolean;
  onCreateRequestHandled?: () => void;
};

export function SuperAgentsPanel({ createRequested = false, onCreateRequestHandled }: SuperAgentsPanelProps = {}) {
  const { t, language } = useI18n();
  const list = useAsync(() => api.superAgents(), []);
  const [selectedID, setSelectedID] = useState("");
  const [draft, setDraft] = useState<SuperAgent | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [request, setRequest] = useState("");
  const handledCreate = useRef(false);
  const items = list.data ?? [];
  const selected = items.find((item) => item.id === selectedID) ?? items[0];
  const activity = useAsync(() => selected ? api.superAgentActivity(selected.id) : Promise.resolve(null), [selected?.id]);
  usePolling(activity.reload, 3_000, { enabled: Boolean(selected) });

  useEffect(() => {
    if (!createRequested) {
      handledCreate.current = false;
      return;
    }
    if (handledCreate.current) return;
    handledCreate.current = true;
    setDraft(newSuperAgent());
    onCreateRequestHandled?.();
  }, [createRequested, onCreateRequestHandled]);

  async function run(label: string, action: () => Promise<unknown>) {
    setBusy(label);
    setNotice("");
    try {
      await action();
      await Promise.all([list.reload(), activity.reload()]);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy("");
    }
  }

  const time = (value?: string) => value ? new Date(value).toLocaleString(language === "zh" ? "zh-CN" : "en-US") : "";

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div><h2>{t("superAgents.title")}</h2><p>{t("superAgents.subtitle")}</p></div>
          <div className="control-row">
            <button className="ghost-action" onClick={() => void list.reload()}><RefreshCw size={15} /> {t("common.refresh")}</button>
            <button className="action" onClick={() => setDraft(newSuperAgent())}><Plus size={15} /> {t("agents.newSuper")}</button>
          </div>
        </div>
        {notice && !draft && <div className="surface-body session-notice error" role="alert">{notice}</div>}
        {list.error && <div className="surface-body session-notice error" role="alert">{String(list.error)}</div>}
        <div className="orchestration-layout">
          <div className="orchestration-list">
            {items.map((item) => (
              <button key={item.id} className={selected?.id === item.id ? "active" : ""} onClick={() => setSelectedID(item.id)}>
                <Bot size={16} />
                <span><strong>{item.name}</strong><small>{t(`superAgents.mode.${item.mode}`)} · {item.auto_workers ? t("superAgents.autoWorkersOn") : t("superAgents.workerCount", { count: item.workers.length })}</small></span>
                <span className={`status-badge status-${item.enabled ? "running" : "cancelled"}`}>{t(item.enabled ? "common.enabled" : "common.disabled")}</span>
              </button>
            ))}
            {!list.loading && items.length === 0 && <div className="empty-state">{t("superAgents.empty")}</div>}
          </div>
          <div className="orchestration-detail">
            {selected ? (
              <>
                <header>
                  <div><h3>{selected.name}</h3><p className="muted">{selected.description || t(`superAgents.modeHelp.${selected.mode}`)}</p></div>
                  <div className="control-row">
                    <button className="ghost-action" onClick={() => setDraft(structuredClone(selected))}>{t("common.edit")}</button>
                    <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => {
                      if (window.confirm(t("superAgents.deleteConfirm", { name: selected.name }))) void run("delete", () => api.deleteSuperAgent(selected.id));
                    }}><Trash2 size={14} /> {t("common.delete")}</button>
                  </div>
                </header>

                <h4>{t("superAgents.workers")}</h4>
                <div className="super-agent-chips">
                  {selected.auto_workers && (selected.worker_runtimes ?? []).map((runtime) => (
                    <span key={runtime} className="super-agent-chip"><i className="online" />{runtime}<small>{t("superAgents.autoWorkersOn")}</small></span>
                  ))}
                  {selected.workers.map((worker) => {
                    const status = activity.data?.workers?.find((item) => item.worker_id === worker.id);
                    return (
                      <span key={worker.id} className="super-agent-chip" title={status?.detail}>
                        <i className={status?.available ? "online" : "offline"} />
                        {worker.label || worker.agent_id}
                        <small>{worker.host_id ? t("superAgents.remote") : t("superAgents.local")}</small>
                      </span>
                    );
                  })}
                </div>

                <h4>{t("superAgents.sources")}</h4>
                <div className="orchestration-task-list">
                  {selected.sources.length === 0 && <p className="muted">{t("superAgents.noSources")}</p>}
                  {selected.sources.map((source) => {
                    const health = activity.data?.sources?.[source.id];
                    return (
                      <article key={source.id}>
                        <header><strong>{t(`superAgents.kind.${source.kind}`)}</strong><span className={`status-badge status-${health?.last_error ? "failed" : source.enabled ? "succeeded" : "cancelled"}`}>{t(source.enabled ? "common.enabled" : "common.disabled")}</span></header>
                        {source.kind === "github_webhook" && <code>{window.location.origin}{githubWebhookPath(source)}</code>}
                        {health?.last_error && <div className="error">{health.last_error}</div>}
                        {health?.last_event_at && <time>{t("superAgents.lastEvent")}: {time(health.last_event_at)}</time>}
                      </article>
                    );
                  })}
                </div>

                <h4>{t("superAgents.tryIt")}</h4>
                <div className="control-row super-agent-request">
                  <input aria-label={t("superAgents.tryIt")} placeholder={t("superAgents.tryItPlaceholder")} value={request} onChange={(event) => setRequest(event.target.value)} />
                  <button className="action" disabled={!request.trim() || Boolean(busy)} onClick={() => void run("trigger", async () => {
                    await api.triggerSuperAgent(selected.id, request);
                    setRequest("");
                  })}><Play size={14} /> {t("superAgents.send")}</button>
                </div>

                <h4>{t("superAgents.tickets")}</h4>
                <div className="orchestration-task-list">
                  {(activity.data?.tickets ?? []).length === 0 && <p className="muted">{t("superAgents.noTickets")}</p>}
                  {(activity.data?.tickets ?? []).map((ticket) => (
                    <article key={ticket.id}>
                      <header>
                        <strong>{ticket.title}</strong>
                        <span className="control-row">
                          <span className={`status-badge status-${ticketStatusClass(ticket.status)}`}>{t(`superAgents.ticket.${ticket.status}`)}</span>
                          {["queued", "running"].includes(ticket.status) && <button className="ghost-action danger-action" disabled={Boolean(busy)} onClick={() => void run("stop", () => api.stopSuperAgentTicket(ticket.id))}><Square size={13} /> {t("superAgents.stop")}</button>}
                          {ticket.status === "failed" && <button className="ghost-action" disabled={Boolean(busy)} onClick={() => void run("retry", () => api.retrySuperAgentTicket(ticket.id))}><RotateCcw size={13} /> {t("common.retry")}</button>}
                        </span>
                      </header>
                      <small>{ticket.worker.label || ticket.worker.agent_id} · {ticket.worker.host_id ? t("superAgents.remote") : t("superAgents.local")}{ticket.actor ? ` · ${ticket.actor}` : ""}</small>
                      {ticket.output && <pre>{ticket.output}</pre>}
                      {ticket.error && <div className="error">{ticket.error}</div>}
                      {ticket.reply_error && <div className="error">{t("superAgents.replyFailed")}: {ticket.reply_error}</div>}
                      <time>{time(ticket.updated_at)}</time>
                    </article>
                  ))}
                </div>

                <h4>{t("superAgents.triggers")}</h4>
                <div className="orchestration-task-list">
                  {(activity.data?.triggers ?? []).length === 0 && <p className="muted">{t("superAgents.noTriggers")}</p>}
                  {(activity.data?.triggers ?? []).map((trigger) => (
                    <article key={trigger.id}>
                      <header>
                        <strong>{trigger.title || trigger.text.slice(0, 80)}</strong>
                        <span className={`status-badge status-${triggerStatusClass(trigger.disposition)}`}>{t(`superAgents.disposition.${trigger.disposition}`)}</span>
                      </header>
                      <small>{trigger.source_kind === "console" ? t("superAgents.kind.console") : t(`superAgents.kind.${trigger.source_kind}`)}{trigger.actor ? ` · ${trigger.actor_name || trigger.actor}` : ""}</small>
                      {trigger.reason && <p>{trigger.reason}</p>}
                      <time>{time(trigger.received_at)}</time>
                    </article>
                  ))}
                </div>
              </>
            ) : <div className="empty-state">{t("superAgents.select")}</div>}
          </div>
        </div>
      </section>
      {draft && (
        <SuperAgentDrawer
          draft={draft}
          notice={notice}
          busy={busy === "save"}
          onChange={setDraft}
          onClose={() => { setDraft(null); setNotice(""); }}
          onSave={(prepare) => void run("save", async () => {
            const saved = await api.saveSuperAgent(await prepare());
            setSelectedID(saved.id);
            setDraft(null);
          })}
        />
      )}
    </div>
  );
}

function isLocalTarget(targetID?: string) {
  return !targetID || targetID === "local";
}

// Router agents and auto workers belong to Super Agents, not to the roster.
function isSuperAgentManaged(agent: AgentInstance) {
  return agent.source === "super_agent" || agent.source === "super_agent_router";
}

function ticketStatusClass(status: string) {
  return { queued: "queued", running: "running", ready: "succeeded", failed: "failed", stopped: "cancelled" }[status] ?? status;
}

function triggerStatusClass(disposition: string) {
  return { pending: "queued", accepted: "succeeded", follow_up: "succeeded", observed: "running", failed: "failed" }[disposition] ?? "cancelled";
}

// SourceEditor edits one trigger. Feishu triggers pick groups and senders from
// what the channel's bot can see; members come from the chosen groups, or from
// every group the bot is in when none are chosen.
function SourceEditor({ source, feishuChannels, onChange, onRemove }: {
  source: SuperAgentSource;
  feishuChannels: Channel[];
  onChange: (patch: Partial<SuperAgentSource>) => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  const feishu = isFeishuSource(source.kind);
  const channelID = feishu ? source.config.channel_id ?? "" : "";
  const memberChats = parseActors(source.config[source.kind === "feishu_doc" ? "notify_chat_id" : "chat_ids"] ?? "");
  const directory = useFeishuDirectory(channelID, memberChats, feishu ? source.allowed_actors : []);
  return (
    <div className="super-agent-card">
      <div className="super-agent-section-head">
        <strong>{t(`superAgents.kind.${source.kind}`)}</strong>
        <span className="control-row">
          <label className="super-agent-check"><input type="checkbox" checked={source.enabled} onChange={(event) => onChange({ enabled: event.target.checked })} /> {t("common.enabled")}</label>
          <button className="ghost-action danger-action" onClick={onRemove}><Trash2 size={13} /> {t("superAgents.remove")}</button>
        </span>
      </div>
      <p className="muted">{t(`superAgents.kindHelp.${source.kind}`)}</p>
      {SOURCE_FIELDS[source.kind].map((field) => {
        const value = source.config[field.key] ?? "";
        const setValue = (next: string) => onChange({ config: { ...source.config, [field.key]: next } });
        if (field.chats) {
          return (
            <FeishuChatPicker key={field.key} label={t(field.labelKey)} required={field.required} single={field.chats === "one"}
              value={value} channelID={channelID} directory={directory} onChange={setValue} />
          );
        }
        return (
          <label className="field" key={field.key}>
            <span>{t(field.labelKey)}{field.required ? " *" : ""}</span>
            {field.channel ? (
              <select value={value} onChange={(event) => setValue(event.target.value)}>
                <option value="">{t("superAgents.chooseChannel")}</option>
                {feishuChannels.map((channel) => <option key={channel.id} value={channel.id}>{channel.name}</option>)}
              </select>
            ) : field.multiline ? (
              <textarea rows={3} placeholder={field.placeholder} value={value} onChange={(event) => setValue(event.target.value)} />
            ) : (
              <input type={field.secret ? "password" : "text"} autoComplete="off" placeholder={field.placeholder} value={value} onChange={(event) => setValue(event.target.value)} />
            )}
          </label>
        );
      })}
      {feishu ? (
        <FeishuActorPicker label={t("superAgents.allowedActors")} values={source.allowed_actors} allowEveryone={source.kind === "feishu_message"} scoped={memberChats.length > 0}
          help={t(`superAgents.actorsHelp.${source.kind}`)} channelID={channelID} directory={directory}
          onChange={(allowed_actors) => onChange({ allowed_actors })} />
      ) : (
        <label className="field">
          <span>{t("superAgents.allowedActors")} *</span>
          <textarea rows={2} value={source.allowed_actors.join(", ")} onChange={(event) => onChange({ allowed_actors: parseActors(event.target.value) })} />
          <small>{t(`superAgents.actorsHelp.${source.kind}`)}</small>
        </label>
      )}
    </div>
  );
}

type FrameworkOption = { kind: string; display: string };

// RouterPicker chooses a router agent, or configures a new one. Only router
// agents are offered: the host keeps them read-only, with no tools but the MCP
// servers allowed here. An ordinary Agent saved as the router before router
// agents existed stays selectable for that Super Agent only.
function RouterPicker({ draft, routers, routerDraft, frameworks, legacyName, onChoose, onRouterDraft, onDelete }: {
  draft: SuperAgent;
  routers: SuperAgentRouterList | null;
  routerDraft: SuperAgentRouter | null;
  frameworks: FrameworkOption[];
  legacyName?: string;
  onChoose: (value: string) => void;
  onRouterDraft: (next: SuperAgentRouter | null) => void;
  onDelete: (router: SuperAgentRouter) => void;
}) {
  const { t } = useI18n();
  const list = routers?.routers ?? [];
  const selected = list.find((router) => router.id === draft.router_agent_id);
  const display = (kind: string) => frameworks.find((item) => item.kind === kind)?.display ?? kind;
  const capability = (kind: string) => routers?.frameworks.find((item) => item.runtime_id === kind);
  const creating = Boolean(routerDraft && !routerDraft.id);
  const value = creating ? NEW_ROUTER : draft.router_agent_id;
  const legacy = Boolean(draft.router_agent_id && !selected && routers);
  const others = (router: SuperAgentRouter) => (router.used_by ?? []).filter((user) => user.id !== draft.id);
  const describe = (router: SuperAgentRouter) => [
    display(router.runtime_id),
    describeModelChoice(t, modelChoice(router)),
    router.mcp_servers.length ? t("superAgents.routerToolsWithMCP", { names: router.mcp_servers.join(", ") }) : t("superAgents.routerToolsReadOnly"),
  ].join(" · ");

  return (
    <>
      <label className="field"><span>{t("superAgents.routerAgent")}</span>
        <select value={value} onChange={(event) => onChoose(event.target.value)}>
          {!value && <option value="">{t("superAgents.chooseRouter")}</option>}
          {legacy && <option value={draft.router_agent_id}>{t("superAgents.keptRouter", { name: legacyName || draft.router_agent_id })}</option>}
          {list.map((router) => <option key={router.id} value={router.id}>{router.name} ({display(router.runtime_id)})</option>)}
          <option value={NEW_ROUTER}>{t("superAgents.newRouter")}</option>
        </select>
      </label>
      {routerDraft ? (
        <RouterEditor router={routerDraft} frameworks={frameworks} routers={routers} sharedWith={selected ? others(selected) : []}
          plannedName={routerNameFor(draft, routerDraft.runtime_id)} onChange={onRouterDraft} />
      ) : selected ? (
        <div className="super-agent-card super-agent-router-summary">
          <span>{describe(selected)}</span>
          {others(selected).length > 0 && <small className="muted">{t("superAgents.routerUsedBy", { names: others(selected).map((user) => user.name).join(", ") })}</small>}
          <span className="control-row">
            <button className="ghost-action" type="button" onClick={() => onRouterDraft({ ...selected, ...modelChoice(selected), mcp_servers: [...selected.mcp_servers] })}>{t("superAgents.editRouter")}</button>
            {(selected.used_by ?? []).length === 0 && (
              <button className="ghost-action danger-action" type="button" onClick={() => onDelete(selected)}><Trash2 size={13} /> {t("superAgents.deleteRouter")}</button>
            )}
          </span>
        </div>
      ) : legacy ? (
        <small className="muted">{t("superAgents.keptRouterHelp")}</small>
      ) : null}
      {creating && capability(routerDraft!.runtime_id) === undefined && frameworks.length === 0 && <small className="muted">{t("superAgents.noFramework")}</small>}
    </>
  );
}

function modelChoice(router: SuperAgentRouter): ModelChoice {
  return { model: router.model ?? "", reasoning_effort: router.reasoning_effort ?? "", service_tier: router.service_tier ?? "" };
}

// routerNameFor is the name a router agent takes from the only Super Agent
// that routes with it. The host names an unnamed Super Agent after the framework.
function routerNameFor(agent: SuperAgent, runtime: string) {
  return `${agent.name.trim() || (runtime ? `Super Agent (${runtime})` : "Super Agent")} router`;
}

function RouterEditor({ router, frameworks, routers, sharedWith, plannedName, onChange }: {
  router: SuperAgentRouter;
  frameworks: FrameworkOption[];
  routers: SuperAgentRouterList | null;
  sharedWith: { id: string; name: string }[];
  plannedName: string;
  onChange: (next: SuperAgentRouter | null) => void;
}) {
  const { t } = useI18n();
  const capability = routers?.frameworks.find((item) => item.runtime_id === router.runtime_id);
  const canRoute = (kind: string) => routers?.frameworks.find((item) => item.runtime_id === kind)?.can_route !== false;
  const display = frameworks.find((item) => item.kind === router.runtime_id)?.display ?? router.runtime_id;
  const mcpSupported = capability?.mcp_servers ?? false;
  const available = [...new Set([...(routers?.mcp_servers ?? []), ...router.mcp_servers])];
  const toggleMCP = (name: string, checked: boolean) =>
    onChange({ ...router, mcp_servers: checked ? [...router.mcp_servers, name] : router.mcp_servers.filter((item) => item !== name) });
  return (
    <div className="super-agent-card super-agent-router-editor">
      {router.id ? (
        <div className="field"><span>{t("superAgents.routerFramework")}</span><strong>{display}</strong><small>{t("superAgents.routerFrameworkFixed")}</small></div>
      ) : (
        <label className="field"><span>{t("superAgents.routerFramework")}</span>
          <select value={router.runtime_id} onChange={(event) => onChange({ ...router, runtime_id: event.target.value, mcp_servers: [], model: "", reasoning_effort: "", service_tier: "" })}>
            <option value="">{t("superAgents.chooseFramework")}</option>
            {frameworks.map((item) => (
              <option key={item.kind} value={item.kind} disabled={!canRoute(item.kind)}>
                {item.display}{canRoute(item.kind) ? "" : ` (${t("superAgents.routerCannotRoute")})`}
              </option>
            ))}
          </select>
          {frameworks.length === 0 && <small>{t("superAgents.noFramework")}</small>}
        </label>
      )}
      <div className="field">
        <span id="super-agent-router-model">{t("superAgents.routerModel")}</span>
        {router.runtime_id ? (
          <ModelPicker runtime={router.runtime_id} labelledBy="super-agent-router-model" value={modelChoice(router)}
            onChange={(choice) => onChange({ ...router, ...choice })} />
        ) : <small>{t("superAgents.modelChooseFramework")}</small>}
      </div>
      <div className="field" role="group" aria-labelledby="super-agent-router-tools">
        <span id="super-agent-router-tools">{t("superAgents.routerToolsTitle")}</span>
        <small>{t("superAgents.routerToolsBuiltIn")}</small>
        {router.runtime_id && !mcpSupported ? (
          <small>{t("superAgents.routerToolsMCPUnsupported")}</small>
        ) : available.length === 0 ? (
          <small>{t("superAgents.routerToolsNoMCP")}</small>
        ) : (
          <>
            <div className="super-agent-chips">
              {available.map((name) => (
                <label key={name} className="super-agent-chip super-agent-toggle">
                  <input type="checkbox" checked={router.mcp_servers.includes(name)} onChange={(event) => toggleMCP(name, event.target.checked)} />
                  {name}
                </label>
              ))}
            </div>
            <small>{t("superAgents.routerToolsMCPHelp")}</small>
          </>
        )}
        <small>{t("superAgents.routerToolsNever")}</small>
      </div>
      <small className="muted">
        {router.id
          ? sharedWith.length ? t("superAgents.routerSharedEdit", { names: sharedWith.map((user) => user.name).join(", ") }) : t("superAgents.routerSavedWithForm", { name: plannedName })
          : t("superAgents.routerCreatedOnSave", { name: plannedName })}
      </small>
      {router.id && <button className="ghost-action" type="button" onClick={() => onChange(null)}>{t("superAgents.cancelRouterEdit")}</button>}
    </div>
  );
}

function SuperAgentDrawer({ draft, notice, busy, onChange, onClose, onSave }: {
  draft: SuperAgent;
  notice: string;
  busy: boolean;
  onChange: (next: SuperAgent) => void;
  onClose: () => void;
  // prepare creates or updates the router agent the form describes, and
  // returns the Super Agent to save with it.
  onSave: (prepare: () => Promise<SuperAgent>) => void;
}) {
  const { t } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const hosts = useAsync(() => api.remoteHosts().catch(() => []), []);
  const channels = useAsync(() => api.channels(), []);
  const frameworks = useAsync(() => api.frameworks(), []);
  const routers = useAsync(() => api.superAgentRouters(), []);
  const [remoteAgents, setRemoteAgents] = useState<Record<string, AgentInstance[]>>({});
  const [routerDraft, setRouterDraft] = useState<SuperAgentRouter | null>(null);
  const [routerError, setRouterError] = useState("");
  // In the "all machines" scope the API tags this daemon's resources as "local".
  // Agents a Super Agent made for itself, routers included, are not workers.
  const localAgents = useMemo(() => (agents.data ?? []).filter((agent) => isLocalTarget(agent.target_id) && !isSuperAgentManaged(agent)), [agents.data]);
  const feishuChannels = useMemo(() => (channels.data ?? []).filter((channel) => isLocalTarget(channel.target_id) && (channel.type === "feishu" || channel.type === "lark")), [channels.data]);
  const installedFrameworks = useMemo(() => (frameworks.data?.frameworks ?? []).filter((item) => item.installed && item.spec.supported && item.registered && isLocalTarget(item.target_id)), [frameworks.data]);
  const frameworkOptions = useMemo(() => installedFrameworks.map((item) => ({ kind: item.spec.kind, display: item.spec.display })), [installedFrameworks]);
  const workerRuntimes = draft.worker_runtimes ?? [];
  // A chosen framework stays visible even when it is no longer detected here.
  const workerFrameworks = [
    ...frameworkOptions,
    ...workerRuntimes.filter((kind) => !frameworkOptions.some((item) => item.kind === kind)).map((kind) => ({ kind, display: kind })),
  ];
  const problems = superAgentProblems(draft, routerDraft);

  // Until any worker is chosen, workers start out on the router's framework.
  const withRouterWorkers = (next: SuperAgent, runtime?: string) =>
    runtime && workerRuntimes.length === 0 && draft.workers.length === 0 ? withWorkerRuntimes(next, [runtime]) : next;
  const routableFramework = (routers.data?.frameworks ?? []).length
    ? frameworkOptions.find((item) => routers.data!.frameworks.some((cap) => cap.runtime_id === item.kind && cap.can_route))?.kind ?? ""
    : frameworkOptions[0]?.kind ?? "";
  const chooseRouter = (value: string) => {
    setRouterError("");
    if (value === NEW_ROUTER) {
      setRouterDraft(newRouter(routableFramework));
      onChange(withRouterWorkers({ ...draft, router_agent_id: "", router_runtime: "" }, routableFramework));
      return;
    }
    setRouterDraft(null);
    const runtime = routers.data?.routers.find((router) => router.id === value)?.runtime_id;
    onChange(withRouterWorkers({ ...draft, router_agent_id: value, router_runtime: "" }, runtime));
  };
  const changeRouterDraft = (next: SuperAgentRouter | null) => {
    if (next && !next.id && next.runtime_id !== routerDraft?.runtime_id) onChange(withRouterWorkers(draft, next.runtime_id));
    setRouterDraft(next);
  };

  // A new Super Agent starts with the first router agent, or a new one on the
  // first framework that can route.
  const defaulted = useRef(Boolean(draft.router_agent_id || draft.router_runtime));
  useEffect(() => {
    if (defaulted.current || !routers.data || frameworks.loading) return;
    defaulted.current = true;
    chooseRouter(routers.data.routers[0]?.id ?? NEW_ROUTER);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routers.data, frameworks.loading]);

  // Saves the router agent the form describes, once, and returns the Super
  // Agent that routes with it.
  async function commitRouter(): Promise<SuperAgent> {
    if (!routerDraft) return draft;
    // Saving the Super Agent names its router after it; this only gives a new
    // router that name from the start.
    const name = routerDraft.id ? routerDraft.name : draft.name.trim() ? routerNameFor(draft, routerDraft.runtime_id) : "";
    const saved = await api.saveSuperAgentRouter({ ...routerDraft, name });
    const next = { ...draft, router_agent_id: saved.id, router_runtime: "" };
    setRouterDraft(null);
    onChange(next);
    void routers.reload();
    return next;
  }

  async function deleteRouter(router: SuperAgentRouter) {
    if (!window.confirm(t("superAgents.deleteRouterConfirm", { name: router.name }))) return;
    setRouterError("");
    try {
      await api.deleteSuperAgentRouter(router.id);
      await routers.reload();
      chooseRouter(NEW_ROUTER);
    } catch (error) {
      setRouterError(error instanceof Error ? error.message : String(error));
    }
  }

  // The template fills the routing rules with an example to edit; what was
  // typed is only replaced when the operator agrees.
  const applyRoutingTemplate = () => {
    const text = t("superAgents.routerPromptTemplateText");
    const current = draft.router_prompt ?? "";
    if (!current.trim() || current === text || window.confirm(t("superAgents.replaceRoutingRules"))) onChange({ ...draft, router_prompt: text });
  };
  const loadRemoteAgents = useCallback(async (hostID: string) => {
    if (!hostID) return;
    try {
      const found = await api.remoteAgentInstances(hostID);
      setRemoteAgents((current) => ({ ...current, [hostID]: found ?? [] }));
    } catch {
      setRemoteAgents((current) => ({ ...current, [hostID]: [] }));
    }
  }, []);

  const hostIDs = draft.workers.map((worker) => worker.host_id ?? "").join("|");
  useEffect(() => {
    for (const hostID of new Set(hostIDs.split("|"))) void loadRemoteAgents(hostID);
  }, [hostIDs, loadRemoteAgents]);

  const toggleWorkerRuntime = (kind: string, checked: boolean) =>
    onChange(withWorkerRuntimes(draft, checked ? [...workerRuntimes, kind] : workerRuntimes.filter((item) => item !== kind)));
  const updateWorker = (index: number, patch: Partial<SuperAgentWorker>) =>
    onChange({ ...draft, workers: draft.workers.map((worker, position) => position === index ? { ...worker, ...patch } : worker) });
  const updateSource = (index: number, patch: Partial<SuperAgentSource>) =>
    onChange({ ...draft, sources: draft.sources.map((source, position) => position === index ? { ...source, ...patch } : source) });
  const legacyRouter = (agents.data ?? []).find((agent) => agent.id === draft.router_agent_id);

  return (
    <div className="provider-drawer-layer">
      <button className="provider-drawer-backdrop" type="button" aria-label={t("common.close")} onClick={onClose} />
      <aside className="provider-drawer agent-drawer" role="dialog" aria-modal="true" aria-labelledby="super-agent-drawer-title">
        <div className="provider-builder-head">
          <div className="provider-form-title">
            <div>
              <h2 id="super-agent-drawer-title">{draft.id ? draft.name : t("agents.newSuper")}</h2>
              <span className="muted">{t("superAgents.drawerSubtitle")}</span>
            </div>
          </div>
          <div className="table-actions">
            <button className="ghost-action icon-action" onClick={onClose} title={t("common.close")}><X size={15} /></button>
          </div>
        </div>
        <div className="surface-body provider-builder-body agent-drawer-body super-agent-form">
          <label className="field"><span>{t("superAgents.name")}</span>
            <input placeholder={t("superAgents.namePlaceholder")} value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} />
            <small>{t("superAgents.nameHelp")}</small>
          </label>

          <section className="super-agent-section" aria-labelledby="super-agent-router">
            <header><h3 id="super-agent-router">{t("superAgents.section.router")}</h3><p>{t("superAgents.section.routerHelp")}</p></header>
            <RouterPicker draft={draft} routers={routers.data} routerDraft={routerDraft} frameworks={frameworkOptions}
              legacyName={legacyRouter?.name} onChoose={chooseRouter} onRouterDraft={changeRouterDraft} onDelete={(router) => void deleteRouter(router)} />
            {(routerError || routers.error) && <div className="session-notice error" role="alert">{routerError || String(routers.error)}</div>}
            <label className="field"><span>{t("superAgents.routerPrompt")}</span>
              <textarea rows={4} placeholder={t("superAgents.routerPromptPlaceholder")} value={draft.router_prompt ?? ""} onChange={(event) => onChange({ ...draft, router_prompt: event.target.value })} />
              <small>{t("superAgents.routerPromptHelp")}</small>
            </label>
            <button className="ghost-action super-agent-template-action" type="button" onClick={applyRoutingTemplate}>{t("superAgents.routerPromptTemplate")}</button>
          </section>

          <section className="super-agent-section" aria-labelledby="super-agent-workers">
            <header><h3 id="super-agent-workers">{t("superAgents.workers")}</h3><p>{t("superAgents.section.workersHelp")}</p></header>
            <div className="field" role="group" aria-labelledby="super-agent-worker-frameworks">
              <span id="super-agent-worker-frameworks">{t("superAgents.workerFrameworks")}</span>
              <div className="super-agent-chips">
                {workerFrameworks.map((item) => (
                  <label key={item.kind} className="super-agent-chip super-agent-toggle">
                    <input type="checkbox" checked={workerRuntimes.includes(item.kind)} onChange={(event) => toggleWorkerRuntime(item.kind, event.target.checked)} />
                    {item.display}
                  </label>
                ))}
              </div>
              <small>{t("superAgents.workerFrameworksHelp")}</small>
            </div>
            {draft.auto_workers && (
              <div className="field-grid">
                <label className="field"><span>{t("superAgents.autoWorkerDir")}</span><input placeholder={t("superAgents.autoWorkerDirPlaceholder")} value={draft.auto_worker_dir ?? ""} onChange={(event) => onChange({ ...draft, auto_worker_dir: event.target.value })} /></label>
                <label className="field"><span>{t("superAgents.autoWorkerApproval")}</span>
                  <select value={draft.auto_worker_approval_mode ?? ""} onChange={(event) => onChange({ ...draft, auto_worker_approval_mode: event.target.value })}>
                    <option value="">{t("superAgents.autoWorkerApprovalDefault")}</option>
                    <option value="auto_edit">auto_edit</option>
                    <option value="auto">auto</option>
                    <option value="yolo">yolo</option>
                  </select>
                  <small>{t("superAgents.autoWorkerApprovalHelp")}</small>
                </label>
              </div>
            )}
            <div className="super-agent-section-head">
              <h4>{t("superAgents.rosterWorkers")}</h4>
              <button className="ghost-action" onClick={() => onChange({ ...draft, workers: [...draft.workers, newWorker()] })}><Plus size={14} /> {t("superAgents.addWorker")}</button>
            </div>
            <small className="muted">{t("superAgents.workersHelp")}</small>
            {draft.workers.map((worker, index) => {
              const options = worker.host_id ? remoteAgents[worker.host_id] ?? [] : localAgents;
              return (
                <div className="super-agent-card" key={worker.id || `new-${index}`}>
                  <div className="field-grid">
                    <label className="field"><span>{t("superAgents.machine")}</span>
                      <select value={worker.host_id ?? ""} onChange={(event) => updateWorker(index, { host_id: event.target.value, agent_id: "" })}>
                        <option value="">{t("superAgents.thisMachine")}</option>
                        {(hosts.data ?? []).map((host) => <option key={host.id} value={host.id}>{host.name}</option>)}
                      </select>
                    </label>
                    <label className="field"><span>{t("superAgents.agent")}</span>
                      <select value={worker.agent_id} onChange={(event) => updateWorker(index, { agent_id: event.target.value })}>
                        <option value="">{t("superAgents.chooseAgent")}</option>
                        {worker.agent_id && !options.some((agent) => agent.id === worker.agent_id) && <option value={worker.agent_id}>{worker.agent_id}</option>}
                        {options.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} ({agent.runtime_id})</option>)}
                      </select>
                    </label>
                  </div>
                  <label className="field"><span>{t("superAgents.capabilities")}</span><input placeholder={t("superAgents.capabilitiesPlaceholder")} value={worker.capabilities ?? ""} onChange={(event) => updateWorker(index, { capabilities: event.target.value })} /></label>
                  <button className="ghost-action danger-action" onClick={() => onChange({ ...draft, workers: draft.workers.filter((_, position) => position !== index) })}><Trash2 size={13} /> {t("superAgents.remove")}</button>
                </div>
              );
            })}
          </section>

          <section className="super-agent-section" aria-labelledby="super-agent-triggers">
            <header className="super-agent-section-head">
              <div><h3 id="super-agent-triggers">{t("superAgents.sources")}</h3><p>{t("superAgents.section.triggersHelp")}</p></div>
              <select aria-label={t("superAgents.addSource")} value="" onChange={(event) => {
                if (event.target.value) onChange({ ...draft, sources: [...draft.sources, newSource(event.target.value as SuperAgentSourceKind)] });
              }}>
                <option value="">{t("superAgents.addSource")}</option>
                {SOURCE_KINDS.map((kind) => <option key={kind} value={kind}>{t(`superAgents.kind.${kind}`)}</option>)}
              </select>
            </header>
            {draft.sources.map((source, index) => (
              <SourceEditor
                key={source.id || `new-${index}`}
                source={source}
                feishuChannels={feishuChannels}
                onChange={(patch) => updateSource(index, patch)}
                onRemove={() => onChange({ ...draft, sources: draft.sources.filter((_, position) => position !== index) })}
              />
            ))}
          </section>

          <details className="super-agent-advanced">
            <summary>{t("superAgents.advanced")}</summary>
            <div className="super-agent-form">
              <div className="field-grid">
                <label className="field"><span>{t("superAgents.modeLabel")}</span>
                  <select value={draft.mode} onChange={(event) => onChange({ ...draft, mode: event.target.value as SuperAgent["mode"] })}>
                    <option value="observe">{t("superAgents.mode.observe")}</option>
                    <option value="act">{t("superAgents.mode.act")}</option>
                  </select>
                  <small>{t(`superAgents.modeHelp.${draft.mode}`)}</small>
                </label>
                <label className="field"><span>{t("superAgents.concurrency")}</span><input type="number" min={1} max={32} value={draft.max_concurrency} onChange={(event) => onChange({ ...draft, max_concurrency: Number(event.target.value) })} /></label>
              </div>
              <label className="super-agent-check"><input type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ ...draft, enabled: event.target.checked })} /> {t("common.enabled")}</label>
            </div>
          </details>
          {problems.length > 0 && <ul className="super-agent-problems">{problems.map((problem) => <li key={problem}>{t(problem)}</li>)}</ul>}
          <div className="agent-drawer-actions">
            {notice && <div className="session-notice error super-agent-save-error" role="alert">{notice}</div>}
            <button className="ghost-action" onClick={onClose}>{t("common.cancel")}</button>
            <button className="action" disabled={busy || problems.length > 0} onClick={() => onSave(commitRouter)}><Save size={15} /> {t("common.save")}</button>
          </div>
        </div>
      </aside>
    </div>
  );
}
