import { Network, Pencil, RefreshCw, Search, Trash2, X } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, type AgentInstance, type SuperAgent } from "../../api";
import { useI18n } from "../../i18n";
import { useCachedResource } from "../../useCachedResource";
import { TargetBadge, targetKey } from "../../components/TargetBadge";
import {
  EMPTY_AGENT,
  agentRegistryMetrics,
  agentSourceClass,
  agentSourceDetailKey,
  agentSourceLabelKey,
  agentsWithoutRow,
  boundChannelsForAgent,
  cliCatalogForAgent,
  copyAgent,
  isAgentPart,
  isReadOnlyAgent,
  readOnlyNoticeKey,
  syncAgentConnectBindings,
  toggleID,
  unifiedAgentFor,
  workPolicy,
  type DrawerMode,
} from "./agentUtils";
import { AgentForm } from "./AgentForm";
import { ChannelLogoGroup } from "./ChannelLogo";
import { AgentAvatar } from "./AgentAvatar";
import { RuntimeBadge } from "./RuntimeBadge";
import { SuperAgentDrawer } from "./SuperAgentForm";
import { newSuperAgent } from "../superAgentModel";
import { confirmAction } from "../../components/ConfirmDialog";

// An agent's workers, triggers and Tickets open over the Agents page.
const AgentWork = lazy(() => import("../SuperAgentsPanel").then((module) => ({ default: module.SuperAgentsPanel })));

export type AgentsPanelProps = {
  createRequested?: boolean;
  onCreateRequestHandled?: () => void;
};

export function AgentsPanel({ createRequested = false, onCreateRequestHandled }: AgentsPanelProps = {}) {
  const { t } = useI18n();
  const agents = useCachedResource("agentInstances", () => api.agentInstances());
  const runtimes = useCachedResource("agents", () => api.agents());
  const channels = useCachedResource("channels", () => api.channels());
  const triggers = useCachedResource("triggers", () => api.triggers());
  const mcpServers = useCachedResource("mcp", () => api.mcp());
  const skills = useCachedResource("skills", () => api.skills());
  const tools = useCachedResource("tools", () => api.tools());
  // Workers and triggers may be unavailable on an older daemon; each agent's
  // own settings still show.
  const unified = useCachedResource("unifiedAgents", () => api.superAgents().then((items) => Array.isArray(items) ? items : []).catch(() => [] as SuperAgent[]));
  const refreshing = [agents, runtimes, channels, triggers, mcpServers, skills, tools, unified]
    .some((resource) => resource.loading);
  const refreshAll = () => Promise.all([
    agents.reload(), runtimes.reload(), channels.reload(),
    triggers.reload(), mcpServers.reload(), skills.reload(), tools.reload(), unified.reload(),
  ]);
  const [workAgentID, setWorkAgentID] = useState("");
  // Every agent created here is a Super Agent; the Agent form only edits
  // agents made before that.
  const [superDraft, setSuperDraft] = useState<SuperAgent | null>(null);
  const [superNotice, setSuperNotice] = useState("");
  const [superBusy, setSuperBusy] = useState(false);
  const [drawerMode, setDrawerMode] = useState<DrawerMode | null>(null);
  const [drawerDraft, setDrawerDraft] = useState<AgentInstance | null>(null);
  const [selectedChannelIDs, setSelectedChannelIDs] = useState<string[]>([]);
  const [selectedTriggerIDs, setSelectedTriggerIDs] = useState<string[]>([]);
  const [notice, setNotice] = useState("");
  const [noticeIsError, setNoticeIsError] = useState(false);
  const [busy, setBusy] = useState("");
  const [rowBusy, setRowBusy] = useState("");
  const [deleteCandidate, setDeleteCandidate] = useState<AgentInstance | null>(null);
  const [searchQuery, setSearchQuery] = useState("");
  const createRequestHandled = useRef(false);

  const allInstances = agents.data ?? [];
  // Router agents and auto workers belong to an agent; they are shown through it.
  const items = useMemo(() => allInstances.filter((item) => !isAgentPart(item)), [allInstances]);
  const unifiedItems = unified.data ?? [];
  const routedAgents = useMemo(() => agentsWithoutRow(unifiedItems, allInstances), [unifiedItems, allInstances]);
  const runtimeOptions = runtimes.data ?? [];
  const channelItems = channels.data ?? [];
  const triggerItems = triggers.data ?? [];
  const bindingsReady = channels.hasValue && triggers.hasValue &&
    !channels.loading && !triggers.loading && !channels.error && !triggers.error;
  const bindingError = channels.error || triggers.error;
  const channelStatusLabel = channels.error ? t("agents.channelsLoadFailed")
    : channels.loading ? t("agents.channelsLoading") : t("agents.noBoundChannels");
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
        memory_scope: current.memory_scope || "agent:auto",
      };
    });
  }, [runtimeOptions]);

  const draftTargetID = draft.target_id;
  const sameTarget = <T extends { target_id?: string }>(item: T) => !draftTargetID || item.target_id === draftTargetID;
  const draftChannels = channelItems.filter(sameTarget);
  const draftTriggers = triggerItems.filter(sameTarget);
  const draftMCPOptions = mcpOptions.filter(sameTarget);
  const draftSkillOptions = skillOptions.filter(sameTarget);
  const draftCLICatalog = cliCatalogForAgent(cliCatalog, draftTargetID);

  const registryMetrics = useMemo(
    () => agentRegistryMetrics(items, channelItems),
    [items, channelItems],
  );
  const visibleItems = useMemo(() => {
    const query = searchQuery.trim().toLocaleLowerCase();
    if (!query) return items;
    return items.filter((item) =>
      [item.name, item.id, item.runtime_id]
        .filter(Boolean)
        .some((value) => String(value).toLocaleLowerCase().includes(query)),
    );
  }, [items, searchQuery]);
  const visibleRouted = useMemo(() => {
    const query = searchQuery.trim().toLocaleLowerCase();
    return query ? routedAgents.filter((agent) => [agent.name, agent.id].some((value) => value.toLocaleLowerCase().includes(query))) : routedAgents;
  }, [routedAgents, searchQuery]);
  const workAgent = unifiedItems.find((agent) => agent.id === workAgentID);

  const startNew = useCallback(() => {
    setSuperDraft(newSuperAgent());
    setSuperNotice("");
    setNotice("");
    setNoticeIsError(false);
  }, []);

  async function saveSuperAgent(prepare: () => Promise<SuperAgent>) {
    setSuperBusy(true);
    setSuperNotice("");
    try {
      await api.saveSuperAgent(await prepare());
      await Promise.all([agents.reload(), unified.reload(), channels.reload()]);
      setSuperDraft(null);
      setNotice(t("agents.saved"));
    } catch (error) {
      setSuperNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setSuperBusy(false);
    }
  }

  useEffect(() => {
    if (!createRequested) {
      createRequestHandled.current = false;
      return;
    }
    // StrictMode or a runtime-list refresh must not reset an open draft.
    if (createRequestHandled.current) return;
    createRequestHandled.current = true;
    startNew();
    onCreateRequestHandled?.();
  }, [createRequested, onCreateRequestHandled, startNew]);

  function editAgent(agent: AgentInstance) {
    if (!bindingsReady) return;
    setDrawerMode("edit");
    setDrawerDraft(copyAgent(agent));
    setSelectedChannelIDs(channelItems.filter((channel) => channel.target_id === agent.target_id && channel.agent_id === agent.id).map((channel) => channel.id));
    setSelectedTriggerIDs(triggerItems.filter((trigger) => trigger.target_id === agent.target_id && trigger.agent_id === agent.id).map((trigger) => trigger.id));
    setNotice("");
    setNoticeIsError(false);
  }

  function closeDrawer() {
    setDrawerMode(null);
    setDrawerDraft(null);
    setSelectedChannelIDs([]);
    setSelectedTriggerIDs([]);
    setBusy("");
    setNotice("");
    setNoticeIsError(false);
  }

  function openCLIInstaller(id: string) {
    closeDrawer();
    window.location.hash = `#skills/cli/${encodeURIComponent(id)}`;
  }

  function update<K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) {
    setDrawerDraft((current) => {
      if (!current) return current;
      const runtimeChanged = key === "runtime_id";
      const desktopRuntime = key === "runtime_id" ? String(value) === "codex-app" : current.runtime_id === "codex-app";
      return {
        ...current,
        desktop_thread_id: key === "runtime_id" ? "" : current.desktop_thread_id,
        workspace_mode: desktopRuntime ? "shared" : current.workspace_mode,
        worktree_base_ref: desktopRuntime ? "" : current.worktree_base_ref,
        session_backend: desktopRuntime ? "structured" : current.session_backend,
        default_model: runtimeChanged ? "" : current.default_model,
        default_reasoning_effort: runtimeChanged ? "" : current.default_reasoning_effort,
        default_service_tier: runtimeChanged ? "" : current.default_service_tier,
        default_approval_mode: key === "runtime_id" ? "" : current.default_approval_mode,
        [key]: value,
      };
    });
  }

  async function save() {
    if (!drawerDraft || !bindingsReady) return;
    setBusy("save");
    setNotice("");
    setNoticeIsError(false);
    try {
      const installedCLIIDs = tools.data
        ? new Set(draftCLICatalog.filter((tool) => tool.installed).map((tool) => tool.spec.id))
        : null;
      const saved = await api.upsertAgentInstance({
        ...drawerDraft,
        clis: installedCLIIDs
          ? (drawerDraft.clis ?? []).filter((id) => installedCLIIDs.has(id))
          : drawerDraft.clis,
        source: drawerDraft.source || (drawerMode === "create" ? "manual" : "console"),
        memory_scope: drawerDraft.memory_scope || `agent:${drawerDraft.id || "new"}`,
      });
      await syncAgentConnectBindings(saved.id, selectedChannelIDs, selectedTriggerIDs, channelItems.filter((item) => item.target_id === saved.target_id), triggerItems.filter((item) => item.target_id === saved.target_id));
      await agents.reload();
      void unified.reload();
      await channels.reload();
      await triggers.reload();
      setDrawerMode(null);
      setDrawerDraft(null);
      setSelectedChannelIDs([]);
      setSelectedTriggerIDs([]);
      setNotice(t("agents.saved"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeIsError(true);
    } finally {
      setBusy("");
    }
  }

  async function remove() {
    if (!drawerDraft?.id || isReadOnlyAgent(drawerDraft)) return;
    setBusy("delete");
    setNotice("");
    setNoticeIsError(false);
    try {
      await api.deleteAgentInstance(drawerDraft.id, drawerDraft.target_id);
      setDrawerMode(null);
      setDrawerDraft(null);
      setNotice(t("agents.deleted"));
      await Promise.all([agents.reload(), channels.reload(), unified.reload()]);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeIsError(true);
    } finally {
      setBusy("");
    }
  }

  async function toggleAgentEnabled(agent: AgentInstance) {
    if (isReadOnlyAgent(agent) || rowBusy) return;
    const operation = `toggle:${targetKey(agent.target_id, agent.id)}`;
    setRowBusy(operation);
    setNotice("");
    setNoticeIsError(false);
    try {
      await api.upsertAgentInstance({ ...copyAgent(agent), enabled: !agent.enabled });
      await Promise.all([agents.reload(), channels.reload(), unified.reload()]);
      setNotice(t(agent.enabled ? "agents.disabled" : "agents.enabled"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeIsError(true);
    } finally {
      setRowBusy("");
    }
  }

  async function removeAgentFromRow(agent: AgentInstance) {
    if (isReadOnlyAgent(agent) || rowBusy) return;
    const operation = `delete:${targetKey(agent.target_id, agent.id)}`;
    setRowBusy(operation);
    setNotice("");
    setNoticeIsError(false);
    try {
      await api.deleteAgentInstance(agent.id, agent.target_id);
      await Promise.all([agents.reload(), channels.reload(), unified.reload()]);
      setDeleteCandidate(null);
      setNotice(t("agents.deleted"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeIsError(true);
    } finally {
      setRowBusy("");
    }
  }

  // An agent that answers with a router agent is deleted with its router.
  async function removeRoutedAgent(agent: SuperAgent) {
    if (rowBusy || !(await confirmAction(t("agents.work.deleteConfirm", { name: agent.name }), { danger: true, title: t("agents.deleteTitle", { name: agent.name }), confirmLabel: t("common.delete") }))) return;
    setRowBusy(`delete:${agent.id}`);
    setNotice("");
    setNoticeIsError(false);
    try {
      await api.deleteSuperAgent(agent.id);
      await Promise.all([agents.reload(), unified.reload()]);
      setNotice(t("agents.deleted"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeIsError(true);
    } finally {
      setRowBusy("");
    }
  }

  const readOnly = isReadOnlyAgent(draft);
  const canSave = Boolean(
    drawerDraft &&
      drawerDraft.name.trim() &&
      drawerDraft.runtime_id &&
      (drawerMode === "edit" || runtimeOptions.includes(drawerDraft.runtime_id)) &&
      (drawerDraft.runtime_id !== "codex-app" || Boolean(drawerDraft.desktop_thread_id)) &&
      !readOnly && bindingsReady
  );
  const noticeClass = noticeIsError ? " error" : "";

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
                <AgentAvatar channels={draftChannels.filter((channel) => selectedChannelIDs.includes(channel.id))} />
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
              {readOnly && <div className="session-notice">{t(readOnlyNoticeKey(draft))}</div>}
              <AgentForm
                canSave={canSave}
                channelOptions={draftChannels}
                cliOptions={draftCLICatalog.map((tool) => ({
                  id: tool.spec.id,
                  name: tool.spec.name,
                  note: tool.spec.note,
                  installed: tool.installed,
                }))}
                draft={draft}
                mcpOptions={draftMCPOptions.map((server) => server.name)}
                readOnly={readOnly}
                runtimeOptions={runtimeOptions}
                selectedChannelIDs={selectedChannelIDs}
                selectedTriggerIDs={selectedTriggerIDs}
                skillOptions={draftSkillOptions.map((skill) => skill.name)}
                triggerOptions={draftTriggers}
                busy={busy}
                drawerMode={drawerMode}
                onDelete={remove}
                onInstallCLI={openCLIInstaller}
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

      {superDraft && (
        <SuperAgentDrawer
          draft={superDraft}
          notice={superNotice}
          busy={superBusy}
          onChange={setSuperDraft}
          onClose={() => { setSuperDraft(null); setSuperNotice(""); }}
          onSave={(prepare) => void saveSuperAgent(prepare)}
        />
      )}

      {workAgent && (
        <div className="provider-drawer-layer">
          <button className="provider-drawer-backdrop" type="button" aria-label={t("common.close")} onClick={() => setWorkAgentID("")} />
          <aside className="provider-drawer agent-drawer agent-work-drawer" role="dialog" aria-modal="true" aria-labelledby="agent-work-title">
            <div className="provider-builder-head">
              <div className="provider-form-title">
                <div>
                  <h2 id="agent-work-title">{workAgent.name}</h2>
                  <span className="muted">{t("agents.work.title")}</span>
                </div>
              </div>
              <div className="table-actions">
                <button className="ghost-action icon-action" onClick={() => setWorkAgentID("")} title={t("common.close")}><X size={15} /></button>
              </div>
            </div>
            <div className="surface-body provider-builder-body agent-drawer-body">
              <Suspense fallback={<div className="empty-state">{t("common.loading")}</div>}>
                <AgentWork agentID={workAgent.id} onChanged={() => void Promise.all([unified.reload(), agents.reload(), channels.reload()])} />
              </Suspense>
            </div>
          </aside>
        </div>
      )}

      {deleteCandidate && (
        <DeleteAgentDialog
          agent={deleteCandidate}
          busy={rowBusy === `delete:${targetKey(deleteCandidate.target_id, deleteCandidate.id)}`}
          error={notice}
          onClose={() => {
            if (!rowBusy) {
              setDeleteCandidate(null);
              setNotice("");
              setNoticeIsError(false);
            }
          }}
          onConfirm={() => removeAgentFromRow(deleteCandidate)}
        />
      )}

      <section className="surface agent-registry-surface">
        <div className="surface-header">
          <label className="agent-registry-search">
            <Search size={15} aria-hidden="true" />
            <input
              type="search"
              value={searchQuery}
              placeholder={t("agents.searchPlaceholder")}
              aria-label={t("agents.searchPlaceholder")}
              onChange={(event) => setSearchQuery(event.target.value)}
            />
          </label>
          <div className="agent-registry-summary" aria-label={t("agents.registrySummaryLabel")}>
            <span className="agent-filter-chip active">{t("agents.metricAgents", { count: registryMetrics.agentCount + routedAgents.length })}</span>
            <span className="agent-filter-chip">{t("agents.metricMachines", { count: registryMetrics.machineCount })}</span>
            <span className="agent-filter-chip">{t("agents.metricChannels", { count: registryMetrics.channelCount })}</span>
          </div>
          <div className="table-actions">
            <button className="ghost-action" onClick={() => void refreshAll()} disabled={refreshing} title={t("common.refresh")}>
              <RefreshCw size={15} />
              {t("common.refresh")}
            </button>
          </div>
        </div>
        {!drawerOpen && notice && <div className={`surface-body session-notice${noticeClass}`}>{notice}</div>}
        {bindingError && <div className="surface-body session-notice error" role="alert">
          {t("agents.bindingsLoadFailed")} {bindingError}
        </div>}
        <div className="surface-body agent-registry-list">
          {agents.loading && agents.data === null && <div className="empty-state">{t("common.loading")}</div>}
          {agents.error && <div className="empty-state error">{String(agents.error)}</div>}
          {!agents.loading && !agents.error && visibleItems.length === 0 && visibleRouted.length === 0 && <div className="empty-state">{t("agents.empty")}</div>}
          {visibleItems.map((item) => {
            const itemChannels = boundChannelsForAgent(item, channelItems);
            // Providers and routing belong to the framework, so an agent
            // shows only the model it asks its framework for.
            const routeModelLabel = t("agents.frameworkModel", { model: item.default_model?.trim() || t("agents.defaultModelShort") });
            const itemReadOnly = isReadOnlyAgent(item);
            const itemUnified = unifiedAgentFor(item, unifiedItems);
            const policy = workPolicy(itemUnified);
            const itemKey = targetKey(item.target_id, item.id);
            const toggleOperation = `toggle:${itemKey}`;
            const deleteOperation = `delete:${itemKey}`;
            return (
              <article
                className="agent-registry-row"
                key={itemKey}
                onDoubleClick={() => editAgent(item)}
              >
                <div className="agent-list-main">
                  <AgentAvatar channels={itemChannels} />
                  <span>
                    <span className="agent-list-title-row">
                      <strong>{item.name}</strong>
                      <button
                        className={`agent-status-switch${item.enabled ? " enabled" : ""}`}
                        type="button"
                        role="switch"
                        aria-checked={item.enabled}
                        aria-label={t(item.enabled ? "agents.disableAction" : "agents.enableAction", { name: item.name })}
                        title={itemReadOnly ? t(readOnlyNoticeKey(item)) : t(item.enabled ? "common.enabled" : "common.disabled")}
                        disabled={itemReadOnly || Boolean(rowBusy)}
                        onClick={() => void toggleAgentEnabled(item)}
                        onDoubleClick={(event) => event.stopPropagation()}
                      >
                        <span className="agent-status-switch-thumb">
                          {rowBusy === toggleOperation && <RefreshCw className="spin" size={9} />}
                        </span>
                      </button>
                      {itemReadOnly && <span className="source-badge config compact">{t(agentSourceLabelKey(item))}</span>}
                    </span>
                    <small className="agent-runtime"><RuntimeBadge runtime={item.runtime_id} emptyLabel={t("agents.noRuntime")} /></small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <TargetBadge target_id={item.target_id} target_name={item.target_name} />
                  <span className="pill agent-route-model-pill">{routeModelLabel}
                    {item.default_reasoning_effort?.trim() && ` · ${item.default_reasoning_effort.trim()}`}
                    {item.default_service_tier?.trim() && ` · ${["priority", "fast"].includes(item.default_service_tier.trim())
                      ? t("agents.fastModeShort") : ["default", "normal", "standard"].includes(item.default_service_tier.trim())
                        ? t("agents.normalModeShort") : item.default_service_tier.trim()}`}
                  </span>
                  <ChannelLogoGroup
                    channels={itemChannels}
                    emptyLabel={channelStatusLabel}
                    label={t("agents.boundChannelsShort")}
                  />
                  {itemUnified && <span className="pill agent-work-pill">{t(policy.key, policy.params)}</span>}
                </div>
                <div className="agent-row-actions">
                  {itemUnified && (
                    <button className="ghost-action" onClick={() => setWorkAgentID(itemUnified.id)} onDoubleClick={(event) => event.stopPropagation()}>
                      <Network size={15} />
                      {t("agents.work.open")}
                    </button>
                  )}
                  <button className="ghost-action" disabled={!bindingsReady} title={!bindingsReady ? (bindingError ? t("agents.bindingsLoadFailed") : t("agents.channelsLoading")) : undefined} onClick={() => editAgent(item)} onDoubleClick={(event) => event.stopPropagation()}>
                    <Pencil size={15} />
                    {t("common.edit")}
                  </button>
                  <button
                    className="ghost-action danger-action"
                    disabled={itemReadOnly || Boolean(rowBusy)}
                    onClick={() => {
                      setNotice("");
                      setNoticeIsError(false);
                      setDeleteCandidate(copyAgent(item));
                    }}
                    onDoubleClick={(event) => event.stopPropagation()}
                    title={itemReadOnly ? t(readOnlyNoticeKey(item)) : t("common.delete")}
                  >
                    {rowBusy === deleteOperation ? <RefreshCw className="spin" size={15} /> : <Trash2 size={15} />}
                    {t("common.delete")}
                  </button>
                </div>
              </article>
            );
          })}
          {visibleRouted.map((agent) => {
            const router = allInstances.find((item) => item.id === agent.router_agent_id);
            const policy = workPolicy(agent);
            return (
              <article className="agent-registry-row" key={`agent:${agent.id}`} onDoubleClick={() => setWorkAgentID(agent.id)}>
                <div className="agent-list-main">
                  <AgentAvatar channels={[]} />
                  <span>
                    <span className="agent-list-title-row">
                      <strong>{agent.name}</strong>
                      <span className={`status-badge status-${agent.enabled ? "running" : "cancelled"}`}>{t(agent.enabled ? "common.enabled" : "common.disabled")}</span>
                    </span>
                    <small className="agent-runtime"><RuntimeBadge runtime={router?.runtime_id ?? ""} emptyLabel={t("agents.noRuntime")} /></small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  {router && <span className="pill">{t("agents.work.routedBy", { name: router.name })}</span>}
                  <span className="pill agent-work-pill">{t(policy.key, policy.params)}</span>
                </div>
                <div className="agent-row-actions">
                  <button className="ghost-action" onClick={() => setWorkAgentID(agent.id)} onDoubleClick={(event) => event.stopPropagation()}>
                    <Network size={15} />
                    {t("agents.work.open")}
                  </button>
                  <button className="ghost-action danger-action" disabled={Boolean(rowBusy)} onClick={() => void removeRoutedAgent(agent)} onDoubleClick={(event) => event.stopPropagation()}>
                    {rowBusy === `delete:${agent.id}` ? <RefreshCw className="spin" size={15} /> : <Trash2 size={15} />}
                    {t("common.delete")}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      </section>
    </div>
  );
}

function DeleteAgentDialog({
  agent,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  agent: AgentInstance;
  busy: boolean;
  error: string;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const { t } = useI18n();
  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button
        aria-label={t("common.cancel")}
        className="meeting-dialog-backdrop internal-dialog-backdrop"
        disabled={busy}
        onClick={onClose}
        type="button"
      />
      <section
        aria-labelledby="agent-delete-title"
        aria-modal="true"
        className="surface meeting-dialog confirm-delete-dialog"
        role="alertdialog"
      >
        <div className="meeting-dialog-icon confirm-delete-dialog-icon">
          <Trash2 size={22} />
        </div>
        <div className="meeting-dialog-heading">
          <h2 id="agent-delete-title">{t("agents.deleteTitle", { name: agent.name })}</h2>
          <p>{t("agents.deleteConfirm", { name: agent.name })}</p>
        </div>
        {error && <div className="session-notice error">{error}</div>}
        <div className="meeting-dialog-actions">
          <button className="ghost-action" disabled={busy} onClick={onClose} type="button">
            {t("common.cancel")}
          </button>
          <button
            className="ghost-action danger-action"
            disabled={busy}
            onClick={() => void onConfirm()}
            type="button"
          >
            {busy ? <RefreshCw className="spin" size={14} /> : <Trash2 size={14} />}
            {busy ? t("agents.deleting") : t("common.delete")}
          </button>
        </div>
      </section>
    </div>
  );
}
