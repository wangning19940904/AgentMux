import { RotateCcw } from "lucide-react";
import type { SuperAgentPrompt, SuperAgentPromptPart } from "../../api";
import { useI18n } from "../../i18n";
import { confirmAction } from "../../components/ConfirmDialog";

// What an agent's router, chat brain and workers are told is built from named
// parts. Each part shows its built-in default, and an editable one can be
// changed for this agent: the text is saved only while it differs from the
// default, so a part left alone keeps following the default as it changes.

export type PromptOverrides = Record<string, SuperAgentPrompt>;
export type PromptState = "fixed" | "default" | "custom" | "outdated";

// ownText is the agent's text for a part, when it has one that counts: an
// empty text, or the default itself, means the default.
function ownText(part: SuperAgentPromptPart, own?: SuperAgentPrompt): string | null {
  if (!part.editable || !own) return null;
  const text = own.text.trim();
  return text === "" || text === part.text ? null : text;
}

export function promptState(part: SuperAgentPromptPart, own?: SuperAgentPrompt): PromptState {
  if (!part.editable) return "fixed";
  if (ownText(part, own) === null) return "default";
  return own?.base && own.base !== part.version ? "outdated" : "custom";
}

export function missingPlaceholders(part: SuperAgentPromptPart, own?: SuperAgentPrompt): string[] {
  const text = ownText(part, own);
  return text === null ? [] : (part.placeholders ?? []).filter((placeholder) => !text.includes(placeholder));
}

// promptProblems says whether any part's own text cannot be saved.
export function promptProblems(parts: SuperAgentPromptPart[], prompts?: PromptOverrides): boolean {
  return parts.some((part) => missingPlaceholders(part, prompts?.[part.id]).length > 0);
}

type Props = {
  parts: SuperAgentPromptPart[];
  roles: SuperAgentPromptPart["role"][];
  value?: PromptOverrides;
  onChange: (next: PromptOverrides | undefined) => void;
};

export function SuperAgentPrompts({ parts, roles, value, onChange }: Props) {
  const { t } = useI18n();
  const shown = parts.filter((part) => roles.includes(part.role));
  if (shown.length === 0) return null;

  const put = (id: string, own?: SuperAgentPrompt) => {
    const next = { ...(value ?? {}) };
    if (own) next[id] = own; else delete next[id];
    onChange(Object.keys(next).length > 0 ? next : undefined);
  };
  const title = (part: SuperAgentPromptPart) => {
    const key = `superAgentPrompts.part.${part.id}`;
    const text = t(key);
    return text === key ? part.id : text;
  };

  return (
    <div className="super-agent-prompts">
      {shown.map((part) => {
        const own = value?.[part.id];
        const state = promptState(part, own);
        const missing = missingPlaceholders(part, own);
        const name = title(part);
        return (
          <details key={part.id} className="super-agent-prompt" data-part={part.id}>
            <summary>
              <span className="super-agent-prompt-title">{name}</span>
              <span className={`super-agent-prompt-state ${state}`}>{t(`superAgentPrompts.state.${state}`)}</span>
            </summary>
            <div className="super-agent-prompt-body">
              {part.editable ? (
                <textarea rows={10} aria-label={name} spellCheck={false} placeholder={t("superAgentPrompts.emptyUsesDefault")}
                  value={own ? own.text : part.text}
                  onChange={(event) => put(part.id, event.target.value === part.text ? undefined : { text: event.target.value, base: own?.base ?? part.version })} />
              ) : (
                <textarea rows={8} aria-label={name} spellCheck={false} readOnly value={part.text} />
              )}
              {!part.editable && <small>{t("superAgentPrompts.fixedHelp")}</small>}
              {part.editable && (part.placeholders ?? []).length > 0 && missing.length === 0 && (
                <small>{t("superAgentPrompts.placeholders", { list: (part.placeholders ?? []).join(", ") })}</small>
              )}
              {missing.length > 0 && <small className="super-agent-prompt-problem" role="alert">{t("superAgentPrompts.keepPlaceholder", { list: missing.join(", ") })}</small>}
              {state === "outdated" && own && (
                <div className="super-agent-prompt-update">
                  <small>{t("superAgentPrompts.defaultUpdated")}</small>
                  <details>
                    <summary>{t("superAgentPrompts.currentDefault")}</summary>
                    <pre>{part.text}</pre>
                  </details>
                  <button className="ghost-action" type="button" onClick={() => put(part.id, { text: own.text, base: part.version })}>{t("superAgentPrompts.keepMine")}</button>
                </div>
              )}
              {part.editable && own && (
                <button className="ghost-action super-agent-prompt-reset" type="button" onClick={async () => {
                  if (ownText(part, own) === null || await confirmAction(t("superAgentPrompts.resetConfirm", { part: name }))) put(part.id);
                }}><RotateCcw size={13} /> {t("superAgentPrompts.reset")}</button>
              )}
            </div>
          </details>
        );
      })}
    </div>
  );
}
