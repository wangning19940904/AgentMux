import { Bot, FolderOpen, HardDrive, Plus, Save, Server, Trash2, X, Zap } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../api";
import type { SuperAgent, SuperAgentMachine, SuperAgentRouter, SuperAgentSource, SuperAgentSourceKind } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { confirmAction } from "../../components/ConfirmDialog";
import { SourceEditor } from "../superAgentSetup";
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
      const saved = await api.saveSuperAgentRouter({ ...routerDraft, name });
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

  // Machines: this one always; others the operator allows. A machine's
  // directory is set with the working directories.
  const machines = draft.worker_machines ?? [];
  const hostList = hosts.data ?? [];
  const unusedHosts = hostList.filter((host) => !machines.some((machine) => machine.host_id === host.id));
  const hostName = (id: string) => hostList.find((host) => host.id === id)?.name ?? id;
  const setMachines = (list: SuperAgentMachine[]) => onChange({ ...draft, worker_machines: list });
  const updateMachine = (hostID: string, patch: Partial<SuperAgentMachine>) =>
    setMachines(machines.map((machine) => machine.host_id === hostID ? { ...machine, ...patch } : machine));
  const toggleMachine = (hostID: string, checked: boolean) =>
    setMachines(checked ? [...machines, { host_id: hostID }] : machines.filter((machine) => machine.host_id !== hostID));
  const toggleWorkerRuntime = (kind: string, checked: boolean) =>
    onChange(withWorkerRuntimes(draft, checked ? [...workerRuntimes, kind] : workerRuntimes.filter((item) => item !== kind)));
  const [customLocalDir, setCustomLocalDir] = useState(Boolean(draft.auto_worker_dir));

  const updateSource = (index: number, patch: Partial<SuperAgentSource>) =>
    onChange({ ...draft, sources: draft.sources.map((source, position) => position === index ? { ...source, ...patch } : source) });
  const channelSources = draft.sources.filter(isChannelSource);

  const routerCapability = routerDraft ? capability(routerDraft.runtime_id) : undefined;
  const listedApprovals = routerCapability?.approval_modes ?? [];
  const routerApproval = routerDraft ? routerDraft.approval_mode || routerCapability?.default_approval_mode || "" : "";
  const approvalModes = routerApproval && !listedApprovals.includes(routerApproval) ? [...listedApprovals, routerApproval] : listedApprovals;
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
                  <label className="field"><span>{t("superAgents.routerApproval")}</span>
                    <select value={routerApproval} onChange={(event) => setRouterDraft({ ...routerDraft, approval_mode: event.target.value })}>
                      {approvalModes.length === 0 && <option value="">{t("superAgents.routerApprovalDefault")}</option>}
                      {approvalModes.map((mode) => <option key={mode} value={mode}>{t(`superAgents.routerApprovalMode.${mode}`)}</option>)}
                    </select>
                    <small>{routerApproval === "plan" ? t("superAgents.routerApprovalPlanNotice") : t("superAgents.routerApprovalHelp")}</small>
                  </label>
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
              <button className="ghost-action" type="button" disabled={unusedHosts.length === 0} title={hostList.length === 0 ? t("superAgents.noMachines") : undefined}
                onClick={() => setMachines([...machines, { host_id: unusedHosts[0].id }])}><Plus size={14} /> {t("superAgentForm.addRemoteDir")}</button>
            </header>
            <div className="super-agent-card">
              <div className="super-agent-section-head">
                <strong><HardDrive size={14} aria-hidden="true" /> {t("superAgents.thisMachine")}</strong>
                {customLocalDir
                  ? <button className="ghost-action" type="button" onClick={() => { setCustomLocalDir(false); onChange({ ...draft, auto_worker_dir: "" }); }}>{t("superAgentForm.useDefaultDir")}</button>
                  : <button className="ghost-action" type="button" onClick={() => setCustomLocalDir(true)}><Plus size={13} /> {t("superAgentForm.addLocalDir")}</button>}
              </div>
              {customLocalDir ? (
                <label className="field"><span>{t("superAgentForm.localDir")}</span>
                  <input placeholder="~/code/my-repo" value={draft.auto_worker_dir ?? ""} onChange={(event) => onChange({ ...draft, auto_worker_dir: event.target.value })} />
                  <small>{t("superAgentForm.localDirHelp")}</small>
                </label>
              ) : (
                <div className="field"><span>{t("superAgentForm.defaultDir")}</span>
                  <code>{`super-agents/${draft.id || "<id>"}/tickets/<ticket>/work`}</code>
                  <small>{t("superAgentForm.defaultDirHelp")}</small>
                </div>
              )}
            </div>
            {machines.map((machine) => (
              <div className="super-agent-card" key={machine.host_id}>
                <div className="field-grid">
                  <label className="field"><span><Server size={13} aria-hidden="true" /> {t("superAgents.machine")}</span>
                    <select value={machine.host_id} onChange={(event) => setMachines(machines.map((item) => item.host_id === machine.host_id ? { ...item, host_id: event.target.value } : item))}>
                      {!hostList.some((host) => host.id === machine.host_id) && <option value={machine.host_id}>{machine.host_id}</option>}
                      {hostList.filter((host) => host.id === machine.host_id || unusedHosts.includes(host)).map((host) => <option key={host.id} value={host.id}>{host.name}</option>)}
                    </select>
                  </label>
                  <label className="field"><span>{t("superAgents.machineDir")}</span>
                    <input placeholder={t("superAgents.machineDirPlaceholder")} value={machine.dir ?? ""} onChange={(event) => updateMachine(machine.host_id, { dir: event.target.value })} />
                  </label>
                </div>
                <label className="field"><span>{t("superAgents.machineCapabilities")}</span>
                  <input placeholder={t("superAgents.machineCapabilitiesPlaceholder")} value={machine.capabilities ?? ""} onChange={(event) => updateMachine(machine.host_id, { capabilities: event.target.value })} />
                </label>
                <button className="ghost-action danger-action" type="button" onClick={() => toggleMachine(machine.host_id, false)}><Trash2 size={13} /> {t("superAgents.remove")}</button>
              </div>
            ))}
            {hostList.length === 0 && !hosts.loading && <small className="muted">{t("superAgents.noMachines")}</small>}
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
            <div className="field" role="group" aria-labelledby="saf-worker-machines">
              <span id="saf-worker-machines">{t("superAgentForm.workerMachines")}</span>
              <div className="super-agent-chips">
                <label className="super-agent-chip super-agent-toggle"><input type="checkbox" checked disabled /> {t("superAgents.thisMachine")}</label>
                {[...hostList.map((host) => host.id), ...machines.map((machine) => machine.host_id).filter((id) => !hostList.some((host) => host.id === id))].map((id) => (
                  <label key={id} className="super-agent-chip super-agent-toggle">
                    <input type="checkbox" checked={machines.some((machine) => machine.host_id === id)} onChange={(event) => toggleMachine(id, event.target.checked)} />
                    {hostName(id)}
                  </label>
                ))}
              </div>
              <small>{t("superAgentForm.workerMachinesHelp")}</small>
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
