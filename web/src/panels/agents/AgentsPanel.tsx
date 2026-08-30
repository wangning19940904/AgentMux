import { Bot, Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { api, type AgentInstance } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { TargetBadge, targetKey } from "../../components/TargetBadge";
import {
  EMPTY_AGENT,
  agentRegistryMetrics,
  agentRouteModel,
  agentSourceClass,
  agentSourceDetailKey,
  agentSourceLabelKey,
  boundChannelsForAgent,
  cliCatalogForAgent,
  copyAgent,
  isConfigManaged,
  newAgent,
  ownerBadge,
  routeToolForRuntime,
  runtimeLabel,
  syncAgentConnectBindings,
  toggleID,
  type DrawerMode,
} from "./agentUtils";
import { AgentForm } from "./AgentForm";
import { ChannelLogoGroup } from "./ChannelLogo";


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
  const [rowBusy, setRowBusy] = useState("");
  const [deleteCandidate, setDeleteCandidate] = useState<AgentInstance | null>(null);

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
  const draftTargetID = draft.target_id;
  const sameTarget = <T extends { target_id?: string }>(item: T) => !draftTargetID || item.target_id === draftTargetID;
  const compatibleProviders = providerOptions.filter(sameTarget);
  const draftRoutes = activeRouteItems.filter(sameTarget);
  const draftChannels = channelItems.filter(sameTarget);
  const draftTriggers = triggerItems.filter(sameTarget);
  const draftMCPOptions = mcpOptions.filter(sameTarget);
  const draftSkillOptions = skillOptions.filter(sameTarget);
  const draftCLICatalog = cliCatalogForAgent(cliCatalog, draftTargetID);

  const registryMetrics = useMemo(
    () => agentRegistryMetrics(items, activeRouteItems, providerOptions, channelItems),
    [items, activeRouteItems, providerOptions, channelItems],
  );

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
    setSelectedChannelIDs(channelItems.filter((channel) => channel.target_id === agent.target_id && channel.agent_id === agent.id).map((channel) => channel.id));
    setSelectedTriggerIDs(triggerItems.filter((trigger) => trigger.target_id === agent.target_id && trigger.agent_id === agent.id).map((trigger) => trigger.id));
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

  function openCLIInstaller(id: string) {
    closeDrawer();
    window.location.hash = `#skills/cli/${encodeURIComponent(id)}`;
  }

  function update<K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) {
    setDrawerDraft((current) => {
      if (!current) return current;
      const routeChanged = key === "runtime_id" || key === "provider_tool" || key === "provider_id";
      const nextRuntimeRouteTool = key === "runtime_id" ? routeToolForRuntime(String(value)) : current.provider_tool;
      const desktopRuntime = key === "runtime_id" ? String(value) === "codex-app" : current.runtime_id === "codex-app";
      return {
        ...current,
        provider_tool: nextRuntimeRouteTool,
        provider_id: key === "runtime_id" ? "" : current.provider_id,
        desktop_thread_id: key === "runtime_id" ? "" : current.desktop_thread_id,
        workspace_mode: desktopRuntime ? "shared" : current.workspace_mode,
        worktree_base_ref: desktopRuntime ? "" : current.worktree_base_ref,
        session_backend: desktopRuntime ? "structured" : current.session_backend,
        default_model: routeChanged ? "" : current.default_model,
        default_reasoning_effort: routeChanged ? "" : current.default_reasoning_effort,
        default_service_tier: routeChanged ? "" : current.default_service_tier,
        default_approval_mode: key === "runtime_id" ? "" : current.default_approval_mode,
        [key]: value,
      };
    });
  }

  async function save() {
    if (!drawerDraft) return;
    setBusy("save");
    setNotice("");
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
        provider_tool: drawerDraft.provider_tool || drawerDraft.runtime_id,
        memory_scope: drawerDraft.memory_scope || `agent:${drawerDraft.id || "new"}`,
      });
      await syncAgentConnectBindings(saved.id, selectedChannelIDs, selectedTriggerIDs, channelItems.filter((item) => item.target_id === saved.target_id), triggerItems.filter((item) => item.target_id === saved.target_id));
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
      await api.deleteAgentInstance(drawerDraft.id, drawerDraft.target_id);
      setDrawerMode(null);
      setDrawerDraft(null);
      setNotice(t("agents.deleted"));
      await Promise.all([agents.reload(), channels.reload()]);
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function toggleAgentEnabled(agent: AgentInstance) {
    if (isConfigManaged(agent) || rowBusy) return;
    const operation = `toggle:${targetKey(agent.target_id, agent.id)}`;
    setRowBusy(operation);
    setNotice("");
    try {
      await api.upsertAgentInstance({ ...copyAgent(agent), enabled: !agent.enabled });
      await Promise.all([agents.reload(), channels.reload()]);
      setNotice(t(agent.enabled ? "agents.disabled" : "agents.enabled"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setRowBusy("");
    }
  }

  async function removeAgentFromRow(agent: AgentInstance) {
    if (isConfigManaged(agent) || rowBusy) return;
    const operation = `delete:${targetKey(agent.target_id, agent.id)}`;
    setRowBusy(operation);
    setNotice("");
    try {
      await api.deleteAgentInstance(agent.id, agent.target_id);
      await Promise.all([agents.reload(), channels.reload()]);
      setDeleteCandidate(null);
      setNotice(t("agents.deleted"));
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setRowBusy("");
    }
  }

  const readOnly = isConfigManaged(draft);
  const canSave = Boolean(
    drawerDraft &&
      drawerDraft.name.trim() &&
      drawerDraft.runtime_id &&
      (drawerMode === "edit" || runtimeOptions.includes(drawerDraft.runtime_id)) &&
      (drawerDraft.runtime_id !== "codex-app" || Boolean(drawerDraft.desktop_thread_id)) &&
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
                activeRoutes={draftRoutes}
                channelOptions={draftChannels}
                cliOptions={draftCLICatalog.map((tool) => ({
                  id: tool.spec.id,
                  name: tool.spec.name,
                  note: tool.spec.note,
                  installed: tool.installed,
                }))}
                compatibleProviders={compatibleProviders}
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

      {deleteCandidate && (
        <DeleteAgentDialog
          agent={deleteCandidate}
          busy={rowBusy === `delete:${targetKey(deleteCandidate.target_id, deleteCandidate.id)}`}
          error={notice}
          onClose={() => {
            if (!rowBusy) {
              setDeleteCandidate(null);
              setNotice("");
            }
          }}
          onConfirm={() => removeAgentFromRow(deleteCandidate)}
        />
      )}

      <section className="surface agent-registry-surface">
        <div className="surface-header">
          <div className="agent-registry-heading">
            <h2>{t("agents.registry")}</h2>
            <div className="agent-registry-summary" aria-label={t("agents.registrySummaryLabel")}>
              <span>{t("agents.metricAgents", { count: registryMetrics.agentCount })}</span>
              <span className="agent-registry-summary-separator" aria-hidden="true">·</span>
              <span>{t("agents.metricProviders", { count: registryMetrics.providerCount })}</span>
              <span className="agent-registry-summary-separator" aria-hidden="true">·</span>
              <span>{t("agents.metricMachines", { count: registryMetrics.machineCount })}</span>
              <span className="agent-registry-summary-separator" aria-hidden="true">·</span>
              <span>{t("agents.metricChannels", { count: registryMetrics.channelCount })}</span>
            </div>
          </div>
          <div className="table-actions">
            <button className="ghost-action" onClick={agents.reload} title={t("common.refresh")}>
              <RefreshCw size={15} />
              {t("common.refresh")}
            </button>
            <button className="action" onClick={startNew}>
              <Plus size={16} />
              {t("agents.newShort")}
            </button>
          </div>
        </div>
        {!drawerOpen && notice && <div className={`surface-body session-notice${noticeClass}`}>{notice}</div>}
        <div className="surface-body agent-registry-list">
          {agents.loading && <div className="empty-state">{t("common.loading")}</div>}
          {agents.error && <div className="empty-state error">{String(agents.error)}</div>}
          {!agents.loading && !agents.error && items.length === 0 && <div className="empty-state">{t("agents.empty")}</div>}
          {items.map((item) => {
            const itemChannels = boundChannelsForAgent(item, channelItems);
            const routeModel = agentRouteModel(item, activeRouteItems, providerOptions);
            const model = routeModel.model || t("agents.defaultModelShort");
            const routeModelLabel = t(
              routeModel.mode === "provider" ? "agents.customRouteModel" : "agents.accountLoginModel",
              { model },
            );
            const itemReadOnly = isConfigManaged(item);
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
                  <span className="provider-icon">
                    <Bot size={15} />
                  </span>
                  <span>
                    <span className="agent-list-title-row">
                      <strong>{item.name}</strong>
                      <button
                        className={`agent-status-switch${item.enabled ? " enabled" : ""}`}
                        type="button"
                        role="switch"
                        aria-checked={item.enabled}
                        aria-label={t(item.enabled ? "agents.disableAction" : "agents.enableAction", { name: item.name })}
                        title={itemReadOnly ? t("agents.readOnlyNotice") : t(item.enabled ? "common.enabled" : "common.disabled")}
                        disabled={itemReadOnly || Boolean(rowBusy)}
                        onClick={() => void toggleAgentEnabled(item)}
                        onDoubleClick={(event) => event.stopPropagation()}
                      >
                        <span className="agent-status-switch-thumb">
                          {rowBusy === toggleOperation && <RefreshCw className="spin" size={9} />}
                        </span>
                      </button>
                      {itemReadOnly && <span className="source-badge config compact">{t("agents.sourceConfig")}</span>}
                    </span>
                    <small>{item.runtime_id ? runtimeLabel(item.runtime_id) : t("agents.noRuntime")}</small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <TargetBadge target_id={item.target_id} target_name={item.target_name} />
                  <OwnerBadge resource={item} />
                  <span className="pill agent-route-model-pill">{routeModelLabel}</span>
                  <ChannelLogoGroup
                    channels={itemChannels}
                    emptyLabel={t("agents.noBoundChannels")}
                    label={t("agents.boundChannelsShort")}
                  />
                </div>
                <div className="agent-row-actions">
                  <button className="ghost-action" onClick={() => editAgent(item)} onDoubleClick={(event) => event.stopPropagation()}>
                    <Pencil size={15} />
                    {t("common.edit")}
                  </button>
                  <button
                    className="ghost-action danger-action"
                    disabled={itemReadOnly || Boolean(rowBusy)}
                    onClick={() => {
                      setNotice("");
                      setDeleteCandidate(copyAgent(item));
                    }}
                    onDoubleClick={(event) => event.stopPropagation()}
                    title={itemReadOnly ? t("agents.readOnlyNotice") : t("common.delete")}
                  >
                    {rowBusy === deleteOperation ? <RefreshCw className="spin" size={15} /> : <Trash2 size={15} />}
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
        className="surface meeting-dialog tenant-delete-dialog"
        role="alertdialog"
      >
        <div className="meeting-dialog-icon tenant-delete-dialog-icon">
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

// OwnerBadge is rendered only when ownership is meaningful: a tenant-scoped
// Console sees just its own resources, so labelling every row would be noise.
export function OwnerBadge({
  resource,
}: {
  resource: { owner_tenant_name?: string; owner_tenant_id?: string; visibility?: string };
}) {
  const { t } = useI18n();
  const badge = ownerBadge(resource);
  if (!badge || badge.className === "unassigned") return null;
  return <span className={`owner-badge ${badge.className}`}>{t(badge.labelKey, badge.params)}</span>;
}
