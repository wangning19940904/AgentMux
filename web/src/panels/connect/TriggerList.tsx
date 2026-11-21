import { Plus } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Channel, Trigger, api } from "../../api";
import { useI18n } from "../../i18n";
import { targetKey } from "../../components/TargetBadge";
import { EMPTY_TRIGGER } from "./connectShared";
import { TriggerCard, TriggerEditor } from "./TriggerEditor";

type TriggerKind = "cron" | "webhook" | "event";

// TriggerList is one surface of Connect triggers of the given kinds: its
// editor, its rows and their actions. Rows of other kinds belong to another
// list; children add rows of their own after these.
export function TriggerList({
  triggers,
  loading,
  channels,
  agents,
  kinds,
  title,
  help,
  newLabel,
  emptyLabel,
  onChanged,
  children,
}: {
  triggers: Trigger[];
  loading: boolean;
  channels: Channel[];
  agents: Array<{ id: string; name: string }>;
  kinds: TriggerKind[];
  title: string;
  help?: string;
  newLabel: string;
  // Shown when there is nothing to list, children included.
  emptyLabel?: string;
  onChanged: () => Promise<unknown>;
  children?: ReactNode;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<Partial<Trigger> | null>(null);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const items = triggers.filter((trigger) => (kinds as string[]).includes(trigger.kind));

  async function run(action: string, fn: () => Promise<unknown>, doneMessage: string) {
    setBusy(action);
    setNotice("");
    setNoticeError(false);
    try {
      await fn();
      if (doneMessage) setNotice(doneMessage);
      await onChanged();
    } catch (err) {
      setNotice(err instanceof Error ? err.message : String(err));
      setNoticeError(true);
    } finally {
      setBusy("");
    }
  }

  const save = (next: Partial<Trigger>) =>
    run("save-trigger", async () => {
      await api.upsertTrigger(next);
      setDraft(null);
    }, t("connect.triggerSaved"));

  return (
    <div className="surface">
      <div className="surface-header">
        <div>
          <h2>{title}</h2>
          {help && <p className="subtle-copy">{help}</p>}
        </div>
        <button
          className="action"
          onClick={() => setDraft(kinds[0] === "cron"
            ? { ...EMPTY_TRIGGER }
            : { ...EMPTY_TRIGGER, kind: kinds[0], cron_expr: undefined })}
        >
          <Plus size={16} />
          {newLabel}
        </button>
      </div>
      <div className="surface-body agent-mini-list">
        {notice && <div className={`session-notice${noticeError ? " error" : ""}`} role={noticeError ? "alert" : "status"}>{notice}</div>}
        {draft && (
          <TriggerEditor
            draft={draft}
            setDraft={setDraft}
            channels={channels}
            agents={agents}
            allowedKinds={kinds}
            busy={busy === "save-trigger"}
            onSave={() => draft && save(draft)}
            onCancel={() => setDraft(null)}
          />
        )}
        {loading && <div className="empty-state">{t("common.loading")}</div>}
        {!loading && items.length === 0 && !children && !draft && emptyLabel && (
          <div className="empty-state">{emptyLabel}</div>
        )}
        {items.map((tr) => (
          <TriggerCard
            key={targetKey(tr.target_id, tr.id)}
            trigger={tr}
            busy={busy}
            onEdit={() => setDraft({ ...tr })}
            onRun={() => run(`run-${tr.id}`, () => api.runTrigger(tr.id, tr.target_id), t("connect.triggerStarted"))}
            onToggle={() => run(`toggle-${tr.id}`, () => api.upsertTrigger({ ...tr, enabled: !tr.enabled }), "")}
            onDelete={() => run(`delete-${tr.id}`, () => api.deleteTrigger(tr.id, tr.target_id), t("connect.triggerDeleted"))}
          />
        ))}
        {children}
      </div>
    </div>
  );
}
