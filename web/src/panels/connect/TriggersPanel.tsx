import { Bot, Pencil, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { api, setMachineOverride, type SuperAgent, type SuperAgentSource } from "../../api";
import { useI18n } from "../../i18n";
import { useAsync } from "../../useAsync";
import { TargetBadge, targetKey } from "../../components/TargetBadge";
import { resourceTargetID } from "../agents/agentUtils";
import { SuperAgentDrawer } from "../agents/SuperAgentForm";
import { EventSubscriptionsSection } from "./EventSubscriptionsPanel";
import { TriggerList } from "./TriggerList";

// agentTriggers lists every Super Agent's triggers with the agent they feed.
export function agentTriggers(agents: SuperAgent[]): Array<{ agent: SuperAgent; source: SuperAgentSource }> {
  return agents.flatMap((agent) => (agent.sources ?? []).map((source) => ({ agent, source })));
}

// TriggersPanel is every way work starts or events leave, in one place:
// inbound are Connect webhooks and Super Agent triggers (Feishu, custom
// callbacks such as a Meego poller); outbound are event actions (shell or HTTP
// to any address) and event subscriptions (signed, retried, loopback only).
export function TriggersPanel() {
  const { t } = useI18n();
  const triggers = useAsync(() => api.triggers(), []);
  const channels = useAsync(() => api.channels(), []);
  const agents = useAsync(() => api.agentInstances(), []);
  const superAgents = useAsync(
    () => api.superAgents().then((items) => Array.isArray(items) ? items : []).catch(() => [] as SuperAgent[]),
    [],
  );
  const [editing, setEditing] = useState<{ draft: SuperAgent; machine: string } | null>(null);
  const [editNotice, setEditNotice] = useState("");
  const [editBusy, setEditBusy] = useState(false);

  // An open agent's requests go to its machine; the override ends with it.
  useEffect(() => () => setMachineOverride(null), []);

  const triggerItems = triggers.data ?? [];
  const channelItems = channels.data ?? [];
  const agentOptions = (agents.data ?? [])
    .filter((agent) => !agent.id.startsWith("config:"))
    .map((agent) => ({ id: agent.id, name: agent.name }));
  const agentRows = agentTriggers(superAgents.data ?? []);
  const reloadTriggers = () => Promise.all([triggers.reload(), channels.reload()]);

  function openAgent(agent: SuperAgent) {
    const machine = resourceTargetID(agent);
    // Set before the drawer mounts, so its first requests already go there.
    setMachineOverride(machine);
    const draft = structuredClone(agent);
    delete draft.target_id;
    delete draft.target_name;
    setEditing({ draft, machine });
    setEditNotice("");
  }

  function closeAgent() {
    setMachineOverride(null);
    setEditing(null);
    setEditNotice("");
  }

  async function saveAgent(prepare: () => Promise<SuperAgent>) {
    setEditBusy(true);
    setEditNotice("");
    try {
      await api.saveSuperAgent(await prepare());
      closeAgent();
      await superAgents.reload();
    } catch (error) {
      setEditNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setEditBusy(false);
    }
  }

  return (
    <div className="page-stack connect-page">
      <div className="surface connect-summary">
        <div className="surface-header">
          <p className="subtle-copy">{t("connect.triggersHelp")}</p>
          <button
            className="ghost-action"
            onClick={() => Promise.all([reloadTriggers(), superAgents.reload()])}
          >
            <RefreshCw size={15} />
            {t("common.refresh")}
          </button>
        </div>
      </div>

      <TriggerList
        triggers={triggerItems}
        loading={triggers.loading}
        channels={channelItems}
        agents={agentOptions}
        kinds={["webhook"]}
        title={t("triggers.inbound")}
        help={t("triggers.inboundHelp")}
        newLabel={t("triggers.newWebhook")}
        emptyLabel={t("triggers.noInbound")}
        onChanged={reloadTriggers}
      >
        {agentRows.length > 0 && (
          <>
            <h3 className="trigger-subheading">{t("triggers.agentTriggers")}</h3>
            {agentRows.map(({ agent, source }) => (
              <AgentTriggerRow
                key={`${targetKey(agent.target_id, agent.id)}/${source.id}`}
                agent={agent}
                source={source}
                onEdit={() => openAgent(agent)}
              />
            ))}
          </>
        )}
      </TriggerList>

      <TriggerList
        triggers={triggerItems}
        loading={triggers.loading}
        channels={channelItems}
        agents={agentOptions}
        kinds={["event"]}
        title={t("triggers.outbound")}
        help={t("triggers.outboundHelp")}
        newLabel={t("triggers.newEventAction")}
        emptyLabel={t("triggers.noEventActions")}
        onChanged={reloadTriggers}
      />

      <EventSubscriptionsSection />

      {editing && (
        <SuperAgentDrawer
          key={editing.machine}
          draft={editing.draft}
          notice={editNotice}
          busy={editBusy}
          machine={editing.machine}
          onChange={(draft) => setEditing((current) => current && { ...current, draft })}
          onClose={closeAgent}
          onSave={(prepare) => void saveAgent(prepare)}
        />
      )}
    </div>
  );
}

function AgentTriggerRow({ agent, source, onEdit }: { agent: SuperAgent; source: SuperAgentSource; onEdit: () => void }) {
  const { t } = useI18n();
  const kind = t(`superAgents.kind.${source.kind}`);
  const name = source.name?.trim() || source.id;
  return (
    <div className="connect-row agent-trigger-row">
      <div className="connect-row-summary">
        <div className="agent-list-main">
          <span className="provider-icon"><Bot size={16} aria-hidden="true" /></span>
          <span>
            <strong>{name}</strong>
            <small>{kind} → {agent.name}</small>
          </span>
          <span className={`status-badge ${source.enabled ? "success" : ""}`}>
            <span className="status-dot" />
            {source.enabled ? t("common.enabled") : t("common.disabled")}
          </span>
          <TargetBadge target_id={agent.target_id} target_name={agent.target_name} />
          <button className="ghost-action" type="button" onClick={onEdit}>
            <Pencil size={14} aria-hidden="true" />
            {t("triggers.editInAgent")}
          </button>
        </div>
      </div>
    </div>
  );
}
