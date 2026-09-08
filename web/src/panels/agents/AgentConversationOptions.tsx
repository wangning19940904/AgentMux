import { MessagesSquare } from "lucide-react";
import { useId } from "react";
import type { AgentInstance } from "../../api";

type PrivateMode = NonNullable<AgentInstance["private_chat_mode"]>;
type GroupMode = NonNullable<AgentInstance["group_chat_mode"]>;

export function AgentConversationOptions({
  privateMode, groupMode, readOnly, onPrivateModeChange, onGroupModeChange, t,
}: {
  privateMode: AgentInstance["private_chat_mode"];
  groupMode: AgentInstance["group_chat_mode"];
  readOnly: boolean;
  onPrivateModeChange: (mode: PrivateMode) => void;
  onGroupModeChange: (mode: GroupMode) => void;
  t: (key: string) => string;
}) {
  const id = useId();
  return (
    <section className="agent-section">
      <header><MessagesSquare size={17} /><h3>{t("agents.conversationModes")}</h3></header>
      <p className="subtle-copy">{t("agents.conversationModesHelp")}</p>
      <fieldset className="agent-conversation-modes" disabled={readOnly}>
        <legend>{t("agents.groupChatMode")}</legend>
        {!groupMode && (
          <label className="agent-conversation-option selected">
            <input type="radio" name={`${id}-group`} value="" checked onChange={() => onGroupModeChange("")} />
            <span><strong>{t("agents.conversationModeKeep")}</strong><small>{t("agents.groupChatModeKeepHelp")}</small></span>
          </label>
        )}
        {(["chat-topic", "new-topic", "chat"] as const).map((mode) => (
          <label key={mode} className={`agent-conversation-option${groupMode === mode ? " selected" : ""}`}>
            <input type="radio" name={`${id}-group`} value={mode} checked={groupMode === mode} onChange={() => onGroupModeChange(mode)} />
            <span>
              <strong><code>{mode}</code>{mode === "chat-topic" && <span className="status-badge">{t("agents.conversationModeDefault")}</span>}</strong>
              <small>{t(`agents.groupChatMode.${mode}`)}</small>
            </span>
          </label>
        ))}
      </fieldset>
      <label className="field">
        <span>{t("agents.privateChatMode")}</span>
        <select value={privateMode || ""} disabled={readOnly} onChange={(event) => onPrivateModeChange(event.target.value as PrivateMode)}>
          {!privateMode && <option value="">{t("agents.conversationModeKeep")}</option>}
          <option value="chat">{t("agents.privateChatMode.chatLabel")}</option>
          <option value="thread">{t("agents.privateChatMode.threadLabel")}</option>
          <option value="group">{t("agents.privateChatMode.groupLabel")}</option>
        </select>
        <small>{t(privateMode ? `agents.privateChatMode.${privateMode}` : "agents.privateChatModeKeepHelp")}</small>
      </label>
      <p className="subtle-copy">{t("agents.conversationModesOverride")}</p>
    </section>
  );
}
