import { Pencil } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { useI18n } from "../../i18n";

export function ToolDescriptionEditor({ name, description, kind, disabled, onSave }: {
  name: string;
  description: string;
  kind: "cli" | "skill";
  disabled: boolean;
  onSave: (description: string) => Promise<string>;
}) {
  const { t } = useI18n();
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const saving = useRef(false);
  const [value, setValue] = useState(description);
  const [draft, setDraft] = useState(description);
  const [editing, setEditing] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { setValue(description); }, [description]);

  function close() {
    setEditing(false);
    setError("");
    window.requestAnimationFrame(() => trigger.current?.focus());
  }

  async function save() {
    if (saving.current || disabled) return;
    saving.current = true;
    setPending(true);
    setError("");
    try {
      setValue(await onSave(draft));
      close();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      saving.current = false;
      setPending(false);
    }
  }

  if (!editing) return (
    <div className="tool-description">
      <span>{value || "—"}</span>
      <button ref={trigger} className="ghost-action tool-description-edit" type="button" disabled={disabled}
        aria-label={t("tools.editDescription", { name })} title={t("tools.editDescription", { name })}
        onClick={() => { setDraft(value); setEditing(true); }}><Pencil size={13} /></button>
    </div>
  );

  return (
    <form className="tool-description-editor" onSubmit={(event) => { event.preventDefault(); void save(); }}
      onKeyDown={(event) => {
        if (event.key === "Escape" && !pending) { event.preventDefault(); event.stopPropagation(); close(); }
      }}>
      <textarea autoFocus rows={4} maxLength={4000} value={draft} disabled={pending}
        aria-label={t("tools.editDescription", { name })} aria-describedby={`${id}-hint`} aria-invalid={Boolean(error)}
        onChange={(event) => setDraft(event.target.value)} />
      <small id={`${id}-hint`}>{t(kind === "cli" ? "tools.cliDescriptionHint" : "tools.skillDescriptionHint")}</small>
      {error && <span className="error" role="alert">{error}</span>}
      <div className="tool-description-actions">
        <button className="action" type="submit" disabled={pending || disabled}>{t(pending ? "tools.descriptionSaving" : "common.save")}</button>
        <button className="ghost-action" type="button" disabled={pending} onClick={close}>{t("common.cancel")}</button>
      </div>
    </form>
  );
}
