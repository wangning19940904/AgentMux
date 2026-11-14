import { Bot, FolderOpen, HardDrive, Plus, Save, Server, Trash2, X, Zap } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../api";
import { isDesktopApp } from "../../api/desktop";
import type { SuperAgent, SuperAgentMachine, SuperAgentRouter, SuperAgentSource, SuperAgentSourceKind } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { confirmAction } from "../../components/ConfirmDialog";
import { SourceEditor } from "../superAgentSetup";
import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";
import { workDirErrorMessage } from "./agentUtils";
import { ModelPicker } from "../superAgentModelPicker";
import type { ModelChoice } from "../superAgentModelPicker";
import { isReachableOrigin } from "../superAgentSetupModel";
import { SOURCE_KINDS, isChannelSource, newRouter, newSource, ownsBrain, superAgentProblems, withWorkerRuntimes } from "../superAgentModel";

// Every agent a person creates is a Super Agent: a router agent of its own reads
// each request and starts workers for the work. This form is the whole agent:
// who it is, how its router thinks, where and with what its workers run, and
// what wakes it up. There is no Provider to pick: the router and workers use
// each framework's own login or active route.

type FrameworkOption = { kind: string; display: string };

export type SuperAgentFormProps = {
  draft: SuperAgent;
  notice: string;
  busy: boolean;
  onChange: (next: SuperAgent) => void;
  onClose: () => void;
  // prepare saves the agent's router and returns the Super Agent to save.
  onSave: (prepare: () => Promise<SuperAgent>) => void;
};

const isLocal = (item: { target_id?: string }) => !item.target_id || item.target_id === "local";

// routerNameFor is the name a router takes from its agent. The host names an
// unnamed agent after the framework.
export function routerNameFor(agent: SuperAgent, runtime: string) {
  return `${agent.name.trim() || (runtime ? `Super Agent (${runtime})` : "Super Agent")} router`;
}

function modelChoice(router: SuperAgentRouter): ModelChoice {
  return { model: router.model ?? "", reasoning_effort: router.reasoning_effort ?? "", service_tier: router.service_tier ?? "" };
}

export function SuperAgentDrawer({ draft, notice, busy, onChange, onClose, onSave }: SuperAgentFormProps) {
  const { t } = useI18n();
  const hosts = useAsync(() => api.remoteHosts().catch(() => []), []);
  const channels = useAsync(() => api.channels(), []);
  const frameworks = useAsync(() => api.frameworks(), []);
  const routers = useAsync(() => api.superAgentRouters(), []);
  const agents = useAsync(() => api.agentInstances().catch(() => []), []);
  const [routerDraft, setRouterDraft] = useState<SuperAgentRouter | null>(null);
  const [routerError, setRouterError] = useState("");

  const feishuChannels = useMemo(() => (channels.data ?? []).filter((channel) => isLocal(channel) && (channel.type === "feishu" || channel.type === "lark")), [channels.data]);
  const frameworkOptions: FrameworkOption[] = useMemo(() => (frameworks.data?.frameworks ?? [])
    .filter((item) => item.installed && item.spec.supported && item.registered && isLocal(item))
    .map((item) => ({ kind: item.spec.kind, display: item.spec.display })), [frameworks.data]);
  const display = (kind: string) => frameworkOptions.find((item) => item.kind === kind)?.display ?? kind;
  const capability = (kind: string) => routers.data?.frameworks.find((item) => item.runtime_id === kind);
  const canRoute = (kind: string) => capability(kind)?.can_route !== false;
  const routableFramework = frameworkOptions.find((item) => canRoute(item.kind))?.kind ?? "";

  // The router this agent routes with. An agent made before routers existed may
  // answer with an ordinary Agent; that one is shown, not edited, here.
  const savedRouter = routers.data?.routers.find((router) => router.id === draft.router_agent_id);
  const legacyBrain = Boolean(draft.router_agent_id && routers.data && !savedRouter);
  const legacyAgent = (agents.data ?? []).find((agent) => agent.id === draft.router_agent_id);

  const workerRuntimes = draft.worker_runtimes ?? [];
  const workerFrameworks = [
    ...frameworkOptions,
    ...workerRuntimes.filter((kind) => !frameworkOptions.some((item) => item.kind === kind)).map((kind) => ({ kind, display: kind })),
  ];

  // Each agent has exactly one router, made with it. A new agent starts one on
  // the first framework that can route, and its workers on the same framework.
  const started = useRef(false);
  useEffect(() => {
    if (started.current || !routers.data || frameworks.loading) return;
    started.current = true;
    if (savedRouter) {
      setRouterDraft({ ...savedRouter, ...modelChoice(savedRouter), approval_mode: savedRouter.approval_mode ?? "", mcp_servers: [...savedRouter.mcp_servers] });
    } else if (!draft.router_agent_id) {
      setRouterDraft(newRouter(routableFramework));
      if (routableFramework && workerRuntimes.length === 0 && draft.workers.length === 0) onChange(withWorkerRuntimes(draft, [routableFramework]));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routers.data, frameworks.loading]);

  const changeRouterFramework = (runtime: string) => {
    if (!routerDraft) return;
    setRouterDraft({ ...routerDraft, runtime_id: runtime, model: "", reasoning_effort: "", service_tier: "", approval_mode: "", mcp_servers: [] });
    // Workers follow the router's framework until someone picks their own.
    const onlyRouters = workerRuntimes.length === 0 || (workerRuntimes.length === 1 && workerRuntimes[0] === routerDraft.runtime_id);
    if (runtime && onlyRouters) onChange(withWorkerRuntimes(draft, [runtime]));
  };

  async function prepare(): Promise<SuperAgent> {
    if (!routerDraft) return draft;
    const name = routerDraft.id ? routerDraft.name : draft.name.trim() ? routerNameFor(draft, routerDraft.runtime_id) : "";
    try {
      const saved = await api.saveSuperAgentRouter({ ...routerDraft, name, approval_mode: routerApproval });
      const next = { ...draft, router_agent_id: saved.id, router_runtime: "" };
      setRouterDraft({ ...saved, ...modelChoice(saved), approval_mode: saved.approval_mode ?? "", mcp_servers: [...saved.mcp_servers] });
      onChange(next);
      return next;
    } catch (error) {
      setRouterError(error instanceof Error ? error.message : String(error));
      throw error;
    }
  }

  const applyRoutingTemplate = async () => {
    const text = t("superAgents.routerPromptTemplateText");
    const current = draft.router_prompt ?? "";
    if (!current.trim() || current === text || await confirmAction(t("superAgents.replaceRoutingRules"))) onChange({ ...draft, router_prompt: text });
  };

  // Working directories: this machine's default one always, plus any paths
  // the operator lists, here or on other machines. A path chosen before there
  // could be several (auto_worker_dir) is shown, and saved, as the first one.
  const hostList = hosts.data ?? [];
  const paths: SuperAgentMachine[] = [
    ...(draft.auto_worker_dir ? [{ host_id: "", dir: draft.auto_worker_dir }] : []),
    ...(draft.worker_machines ?? []),
  ];
  const setPaths = (list: SuperAgentMachine[]) => onChange({ ...draft, auto_worker_dir: "", worker_machines: list });
  const updatePath = (index: number, patch: Partial<SuperAgentMachine>) =>
    setPaths(paths.map((path, position) => position === index ? { ...path, ...patch } : path));
  const [picking, setPicking] = useState<{ index: number; target: string } | null>(null);
  const [pathNotice, setPathNotice] = useState<{ index: number; text: string } | null>(null);
  // choosePath opens the system's folder dialog for this machine in the
  // desktop app, and otherwise browses the machine's folders here.
  const choosePath = async (index: number) => {
    const path = paths[index];
    const target = path.host_id || "local";
    setPathNotice(null);
    if (target !== "local" || !isDesktopApp()) {
      setPicking({ index, target });
      return;
    }
    try {
      const selected = await api.selectDirectory(path.dir ?? "");
      if (selected.path) updatePath(index, { dir: selected.path });
    } catch (error) {
      setPathNotice({ index, text: workDirErrorMessage(error, t) });
    }
  };
  const toggleWorkerRuntime = (kind: string, checked: boolean) =>
    onChange(withWorkerRuntimes(draft, checked ? [...workerRuntimes, kind] : workerRuntimes.filter((item) => item !== kind)));

  const updateSource = (index: number, patch: Partial<SuperAgentSource>) =>
    onChange({ ...draft, sources: draft.sources.map((source, position) => position === index ? { ...source, ...patch } : source) });
  const channelSources = draft.sources.filter(isChannelSource);

  const routerCapability = routerDraft ? capability(routerDraft.runtime_id) : undefined;
  // Routers always run in their framework's default mode, Auto where it has
  // one; saving moves an older router onto it.
  const routerApproval = routerCapability?.default_approval_mode ?? "";
  const availableMCP = [...new Set([...(routers.data?.mcp_servers ?? []), ...(routerDraft?.mcp_servers ?? [])])];
  const toggleMCP = (name: string, checked: boolean) => routerDraft &&
    setRouterDraft({ ...routerDraft, mcp_servers: checked ? [...routerDraft.mcp_servers, name] : routerDraft.mcp_servers.filter((item) => item !== name) });

  const problems = superAgentProblems(draft, routerDraft);
  if (!draft.name.trim()) problems.unshift("superAgentForm.problem.name");

  return (
    <div className="provider-drawer-layer">
      <button className="provider-drawer-backdrop" type="button" aria-label={t("common.close")} onClick={onClose} />
      <aside className="provider-drawer agent-drawer" role="dialog" aria-modal="true" aria-labelledby="super-agent-drawer-title">
        <div className="provider-builder-head">
          <div className="provider-form-title">
            <div>
              <h2 id="super-agent-drawer-title">{draft.id ? draft.name : t("superAgentForm.newTitle")}</h2>
              <span className="muted">{t("superAgentForm.subtitle")}</span>
            </div>
          </div>
          <div className="table-actions">
            <button className="ghost-action icon-action" onClick={onClose} title={t("common.close")}><X size={15} /></button>
          </div>
        </div>
        <div className="surface-body provider-builder-body agent-drawer-body super-agent-form">

          <section className="super-agent-section" aria-labelledby="saf-identity">
            <header><h3 id="saf-identity">{t("superAgentForm.section.identity")}</h3></header>
            <label className="field"><span>{t("superAgentForm.name")}</span>
              <input value={draft.name} placeholder={t("superAgentForm.namePlaceholder")} onChange={(event) => onChange({ ...draft, name: event.target.value })} />
              <small>{t("superAgentForm.nameHelp")}</small>
            </label>
            <label className="field"><span>{t("superAgentForm.description")}</span>
              <input value={draft.description ?? ""} placeholder={t("superAgentForm.descriptionPlaceholder")} onChange={(event) => onChange({ ...draft, description: event.target.value })} />
              <small>{t("superAgentForm.descriptionHelp")}</small>
            </label>
          </section>

          <section className="super-agent-section" aria-labelledby="saf-router">
            <header><h3 id="saf-router"><Bot size={15} aria-hidden="true" /> {t("superAgentForm.section.router")}</h3><p>{t("superAgentForm.section.routerHelp")}</p></header>
            {legacyBrain ? (
              <div className="super-agent-card super-agent-router-summary">
                <span>{[legacyAgent?.name, legacyAgent?.runtime_id, legacyAgent?.default_model].filter(Boolean).join(" · ") || draft.router_agent_id}</span>
                <small className="muted">{t(ownsBrain(draft) ? "superAgentForm.legacyOwnBrain" : "superAgentForm.legacyRouter")}</small>
              </div>
            ) : routerDraft ? (
              <>
                <div className="field-grid">
                  {routerDraft.id ? (
                    <div className="field"><span>{t("superAgentForm.routerFramework")}</span><strong>{display(routerDraft.runtime_id)}</strong><small>{t("superAgentForm.routerFrameworkFixed")}</small></div>
                  ) : (
                    <label className="field"><span>{t("superAgentForm.routerFramework")}</span>
                      <select value={routerDraft.runtime_id} onChange={(event) => changeRouterFramework(event.target.value)}>
                        <option value="">{t("superAgents.chooseFramework")}</option>
                        {frameworkOptions.map((item) => (
                          <option key={item.kind} value={item.kind} disabled={!canRoute(item.kind)}>
                            {item.display}{canRoute(item.kind) ? "" : ` (${t("superAgents.routerCannotRoute")})`}
                          </option>
                        ))}
                      </select>
                      <small>{frameworkOptions.length === 0 ? t("superAgents.noFramework") : t("superAgentForm.routerFrameworkHelp")}</small>
                    </label>
                  )}
                  <div className="field">
                    <span id="saf-router-model">{t("superAgentForm.routerModel")}</span>
                    {routerDraft.runtime_id ? (
                      <ModelPicker runtime={routerDraft.runtime_id} labelledBy="saf-router-model" value={modelChoice(routerDraft)}
                        onChange={(choice) => setRouterDraft({ ...routerDraft, ...choice })} />
                    ) : <small>{t("superAgents.modelChooseFramework")}</small>}
                  </div>
                </div>
              </>
            ) : <div className="empty-state">{t("common.loading")}</div>}
            {(routerError || routers.error) && <div className="session-notice error" role="alert">{routerError || String(routers.error)}</div>}
            <label className="field"><span>{t("superAgentForm.routerPrompt")}</span>
              <textarea rows={5} placeholder={t("superAgents.routerPromptPlaceholder")} value={draft.router_prompt ?? ""} onChange={(event) => onChange({ ...draft, router_prompt: event.target.value })} />
              <small>{t("superAgents.routerPromptHelp")}</small>
            </label>
            <button className="ghost-action super-agent-template-action" type="button" onClick={applyRoutingTemplate}>{t("superAgents.routerPromptTemplate")}</button>
            {routerDraft && !legacyBrain && (
              <details className="super-agent-advanced">
                <summary>{t("superAgentForm.routerAdvanced")}</summary>
                <div className="super-agent-form">
                  {routerApproval && (
                    <div className="field">
                      <span>{t("superAgents.routerApproval")}</span>
                      <small>{routerApproval === "auto" ? t("superAgents.routerApprovalAuto")
                        : t("superAgents.routerApprovalFallback", { mode: t(`superAgents.routerApprovalShort.${routerApproval}`) })}</small>
                    </div>
                  )}
                  <div className="field" role="group" aria-labelledby="saf-router-tools">
                    <span id="saf-router-tools">{t("superAgents.routerToolsTitle")}</span>
                    {routerDraft.runtime_id && !routerCapability?.mcp_servers ? (
                      <small>{t("superAgents.routerToolsMCPUnsupported")}</small>
                    ) : availableMCP.length === 0 ? (
                      <small>{t("superAgents.routerToolsNoMCP")}</small>
                    ) : (
                      <div className="super-agent-chips">
                        {availableMCP.map((name) => (
                          <label key={name} className="super-agent-chip super-agent-toggle">
                            <input type="checkbox" checked={routerDraft.mcp_servers.includes(name)} onChange={(event) => toggleMCP(name, event.target.checked)} />
                            {name}
                          </label>
                        ))}
                      </div>
                    )}
                    <small>{t("superAgents.routerToolsNever")}</small>
                  </div>
                </div>
              </details>
            )}
          </section>

          <section className="super-agent-section" aria-labelledby="saf-dirs">
            <header className="super-agent-section-head">
              <div><h3 id="saf-dirs"><FolderOpen size={15} aria-hidden="true" /> {t("superAgentForm.section.dirs")}</h3><p>{t("superAgentForm.section.dirsHelp")}</p></div>
              <button className="ghost-action" type="button" onClick={() => setPaths([...paths, { host_id: "" }])}><Plus size={14} /> {t("superAgentForm.addDir")}</button>
            </header>
            <div className="super-agent-card">
              <strong><HardDrive size={14} aria-hidden="true" /> {t("superAgents.thisMachine")}</strong>
              <div className="field"><span>{t("superAgentForm.defaultDir")}</span>
                <code>{`super-agents/${draft.id || "<id>"}/tickets/<ticket>/work`}</code>
                <small>{t("superAgentForm.defaultDirHelp")}</small>
              </div>
            </div>
            {paths.map((path, index) => {
              const local = !path.host_id;
              return (
                <div className="super-agent-card" key={path.id || `path-${index}`}>
                  <div className="field-grid super-agent-path-grid">
                    <label className="field"><span>{local ? <HardDrive size={13} aria-hidden="true" /> : <Server size={13} aria-hidden="true" />} {t("superAgents.machine")}</span>
                      <select value={path.host_id} onChange={(event) => updatePath(index, { host_id: event.target.value, id: undefined })}>
                        <option value="">{t("superAgents.thisMachine")}</option>
                        {path.host_id && !hostList.some((host) => host.id === path.host_id) && <option value={path.host_id}>{path.host_id}</option>}
                        {hostList.map((host) => <option key={host.id} value={host.id}>{host.name}</option>)}
                      </select>
                    </label>
                    <label className="field"><span>{t("superAgentForm.pathDir")}</span>
                      <div className="directory-input-row">
                        <input placeholder={local ? t("superAgentForm.localPathPlaceholder") : t("superAgents.machineDirPlaceholder")} value={path.dir ?? ""}
                          onChange={(event) => updatePath(index, { dir: event.target.value })} />
                        <button className="ghost-action icon-action" type="button" title={t("superAgentForm.chooseDir")} aria-label={t("superAgentForm.chooseDir")}
                          onClick={() => void choosePath(index)}><FolderOpen size={15} /></button>
                      </div>
                      {pathNotice?.index === index ? <small className="directory-notice" role="status">{pathNotice.text}</small>
                        : <small>{local ? t("superAgentForm.localPathHelp") : t("superAgentForm.remotePathHelp")}</small>}
                    </label>
                  </div>
                  <label className="field"><span>{t("superAgents.machineCapabilities")}</span>
                    <input placeholder={t("superAgents.machineCapabilitiesPlaceholder")} value={path.capabilities ?? ""} onChange={(event) => updatePath(index, { capabilities: event.target.value })} />
                  </label>
                  <button className="ghost-action danger-action" type="button" onClick={() => setPaths(paths.filter((_, position) => position !== index))}><Trash2 size={13} /> {t("superAgents.remove")}</button>
                </div>
              );
            })}
            {picking && (
              <RemoteDirectoryPicker
                initialPath={paths[picking.index]?.dir ?? ""}
                targetID={picking.target}
                onClose={() => setPicking(null)}
                onSelect={(dir) => { updatePath(picking.index, { dir }); setPicking(null); }}
                t={t}
              />
            )}
          </section>

          <section className="super-agent-section" aria-labelledby="saf-workers">
            <header><h3 id="saf-workers"><Zap size={15} aria-hidden="true" /> {t("superAgentForm.section.workers")}</h3><p>{t("superAgentForm.section.workersHelp")}</p></header>
            <div className="field" role="group" aria-labelledby="saf-worker-frameworks">
              <span id="saf-worker-frameworks">{t("superAgentForm.workerFrameworks")}</span>
              <div className="super-agent-chips">
                {workerFrameworks.map((item) => (
                  <label key={item.kind} className="super-agent-chip super-agent-toggle">
                    <input type="checkbox" checked={workerRuntimes.includes(item.kind)} onChange={(event) => toggleWorkerRuntime(item.kind, event.target.checked)} />
                    {item.display}
                  </label>
                ))}
              </div>
              <small>{t("superAgentForm.workerFrameworksHelp")}</small>
            </div>
            <div className="field-grid">
              <label className="field"><span>{t("superAgentForm.workerApproval")}</span>
                <select value={draft.auto_worker_approval_mode ?? ""} onChange={(event) => onChange({ ...draft, auto_worker_approval_mode: event.target.value })}>
                  <option value="">{t("superAgents.autoWorkerApprovalDefault")}</option>
                  <option value="auto_edit">{t("approval.auto_edit")}</option>
                  <option value="auto">{t("approval.auto")}</option>
                  <option value="yolo">{t("approval.yolo")}</option>
                </select>
                <small>{t("superAgents.autoWorkerApprovalHelp")}</small>
              </label>
              <label className="field"><span>{t("superAgents.concurrency")}</span>
                <input type="number" min={1} max={32} value={draft.max_concurrency} onChange={(event) => onChange({ ...draft, max_concurrency: Number(event.target.value) })} />
                <small>{t("superAgentForm.concurrencyHelp")}</small>
              </label>
            </div>
            {draft.workers.length > 0 && (
              <div className="field">
                <span>{t("superAgentForm.legacyWorkers")}</span>
                <div className="super-agent-chips">
                  {draft.workers.map((worker, index) => (
                    <span key={worker.id || index} className="super-agent-chip">
                      {worker.label || worker.agent_id}
                      <button className="ghost-action icon-action" type="button" title={t("superAgents.remove")} onClick={() => onChange({ ...draft, workers: draft.workers.filter((_, position) => position !== index) })}><X size={12} /></button>
                    </span>
                  ))}
                </div>
                <small>{t("superAgentForm.legacyWorkersHelp")}</small>
              </div>
            )}
          </section>

          <section className="super-agent-section" aria-labelledby="saf-triggers">
            <header className="super-agent-section-head">
              <div><h3 id="saf-triggers">{t("superAgentForm.section.triggers")}</h3><p>{t("superAgents.section.triggersHelp")}</p></div>
              <select aria-label={t("superAgents.addSource")} value="" onChange={(event) => {
                if (event.target.value) onChange({ ...draft, sources: [...draft.sources, newSource(event.target.value as SuperAgentSourceKind, isReachableOrigin(window.location.origin))] });
              }}>
                <option value="">{t("superAgents.addSource")}</option>
                {SOURCE_KINDS.map((kind) => <option key={kind} value={kind}>{t(`superAgents.kind.${kind}`)}</option>)}
              </select>
            </header>
            {channelSources.length > 0 && (
              <div className="field">
                <span>{t("superAgents.kind.channel")}</span>
                <div className="super-agent-chips">
                  {channelSources.map((source) => <span key={source.id} className="super-agent-chip"><i className={source.enabled ? "online" : "offline"} />{source.name || source.config.channel_id}</span>)}
                </div>
                <small>{t("agents.work.channelSourceHelp")}</small>
              </div>
            )}
            {draft.sources.map((source, index) => isChannelSource(source) ? null : (
              <SourceEditor
                key={source.id || `new-${index}`}
                source={source}
                superAgentID={draft.id}
                feishuChannels={feishuChannels}
                onFeishuChannelCreated={async () => { await channels.reload(); }}
                onChange={(patch) => updateSource(index, patch)}
                onRemove={() => onChange({ ...draft, sources: draft.sources.filter((_, position) => position !== index) })}
              />
            ))}
          </section>

          <details className="super-agent-advanced">
            <summary>{t("superAgents.advanced")}</summary>
            <div className="super-agent-form">
              <label className="field"><span>{t("superAgents.modeLabel")}</span>
                <select value={draft.mode} onChange={(event) => onChange({ ...draft, mode: event.target.value as SuperAgent["mode"] })}>
                  <option value="observe">{t("superAgents.mode.observe")}</option>
                  <option value="act">{t("superAgents.mode.act")}</option>
                </select>
                <small>{t(`superAgents.modeHelp.${draft.mode}`)}</small>
              </label>
              <label className="super-agent-check"><input type="checkbox" checked={draft.enabled} onChange={(event) => onChange({ ...draft, enabled: event.target.checked })} /> {t("common.enabled")}</label>
            </div>
          </details>

          {problems.length > 0 && <ul className="super-agent-problems">{problems.map((problem) => <li key={problem}>{t(problem)}</li>)}</ul>}
          <div className="agent-drawer-actions">
            {notice && <div className="session-notice error super-agent-save-error" role="alert">{notice}</div>}
            <button className="ghost-action" onClick={onClose}>{t("common.cancel")}</button>
            <button className="action" disabled={busy || problems.length > 0} onClick={() => onSave(prepare)}><Save size={15} /> {t("common.save")}</button>
          </div>
        </div>
      </aside>
    </div>
  );
}
