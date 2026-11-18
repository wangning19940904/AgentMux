import { AlarmClock, Bot, FileText, GitBranch, Inbox, Mail, MessagesSquare, Search, Send, SquareTerminal, Users, X } from "lucide-react";
import type { ReactNode } from "react";
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { api } from "../api";
import type { SuperAgent, SuperAgentTrigger } from "../api";
import { useI18n } from "../i18n";
import { PlanCard, TicketCard, timerStatusClass, triggerStatusClass } from "./superAgentCards";
import type { CardContext } from "./superAgentCards";
import { CONSOLE_CONVERSATION, buildConversations, previewText } from "./superAgentConversations";
import type { ChatItem, Conversation } from "./superAgentConversations";
import { WorkerFace, workerPersona } from "./agents/avatars";

export type SuperAgentChatProps = {
  agent: SuperAgent;
  context: CardContext;
  declineNotes: Record<string, string>;
  onDeclineNote: (planID: string, value: string) => void;
  // The agent's portrait, as the Agents page shows it.
  avatar?: ReactNode;
};

// SuperAgentChat shows an agent's history the way a chat app does: each
// group, chat or trigger source it hears from is a contact, and opening one
// shows what people asked, what the agent answered and the work it did.
export function SuperAgentChat({ agent, context, declineNotes, onDeclineNote, avatar }: SuperAgentChatProps) {
  const { t, language } = useI18n();
  const { activity, busy, run } = context;
  const conversations = useMemo(() => buildConversations(activity), [activity]);
  const [selectedKey, setSelectedKey] = useState("");
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [chatNames, setChatNames] = useState<Record<string, string>>({});
  const messagesRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);

  // Open the most recent conversation that has something in it.
  const fallback = conversations.find((item) => item.kind !== "console" && item.items.length > 0) ?? conversations[0];
  const selected = conversations.find((item) => item.key === selectedKey) ?? fallback;

  // Feishu groups are named by the bot's chat list; a chat not on it is a
  // direct chat, named after the person in it.
  const channelIDs = useMemo(() => [...new Set(conversations.map((item) => item.channelID).filter((id): id is string => Boolean(id)))], [conversations]);
  useEffect(() => {
    let cancelled = false;
    for (const channelID of channelIDs) {
      api.feishuChats(channelID).then((list) => {
        if (cancelled) return;
        setChatNames((current) => {
          const next = { ...current };
          for (const chat of list.items ?? []) if (chat.name) next[chat.chat_id] = chat.name;
          return next;
        });
      }).catch(() => undefined);
    }
    return () => { cancelled = true; };
  }, [channelIDs.join("\n")]);

  const kindLabel = (kind: string) => kind ? t(`superAgents.kind.${kind}`) : "";
  const sourceFor = (conversation: Conversation) => agent.sources.find((source) => source.id === conversation.sourceID);
  const title = (conversation: Conversation) => {
    if (conversation.kind === "console") return t("superAgents.chat.console");
    if (conversation.kind === "chat") {
      const name = conversation.chatID ? chatNames[conversation.chatID] : "";
      if (name) return name;
      if (conversation.participants.length > 0) return conversation.participants.slice(0, 3).join(t("superAgents.chat.nameSeparator"));
      return t("superAgents.chat.unnamedChat");
    }
    if (conversation.key.startsWith("thread:mail:")) return conversation.key.slice("thread:mail:".length);
    const source = sourceFor(conversation);
    return source?.name || kindLabel(conversation.sourceKind) || t("superAgents.chat.other");
  };
  const subtitle = (conversation: Conversation) => {
    if (conversation.kind === "console") return t("superAgents.chat.consoleHelp");
    return [
      kindLabel(conversation.sourceKind),
      conversation.participants.length > 0 ? t("superAgents.chat.participants", { count: conversation.participants.length }) : "",
    ].filter(Boolean).join(" · ");
  };

  const locale = language === "zh" ? "zh-CN" : "en-US";
  const clock = (value?: string) => value ? new Date(value).toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" }) : "";
  const day = (value?: string) => value ? new Date(value).toLocaleDateString(locale, { year: "numeric", month: "short", day: "numeric", weekday: "short" }) : "";
  const listTime = (value?: string) => {
    if (!value) return "";
    const date = new Date(value);
    return date.toDateString() === new Date().toDateString() ? clock(value) : date.toLocaleDateString(locale, { month: "numeric", day: "numeric" });
  };

  const visible = conversations.filter((conversation) => {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return true;
    return [title(conversation), subtitle(conversation), ...conversation.participants].some((value) => value.toLocaleLowerCase().includes(needle));
  });

  const itemCount = selected?.items.length ?? 0;
  useEffect(() => { stickToBottom.current = true; }, [selected?.key]);
  useEffect(() => {
    const element = messagesRef.current;
    if (element && stickToBottom.current) element.scrollTop = element.scrollHeight;
  }, [selected?.key, itemCount, activity]);

  const send = () => {
    const text = draft.trim();
    if (!text) return;
    run("trigger", async () => {
      await api.triggerSuperAgent(agent.id, text);
      setDraft("");
      setSelectedKey(CONSOLE_CONVERSATION);
      stickToBottom.current = true;
    });
  };

  const agentFace = <span className="super-agent-chat-face agent" aria-hidden="true">{avatar ?? <Bot size={15} />}</span>;
  const health = selected?.sourceID ? activity?.sources?.[selected.sourceID] : undefined;

  return (
    <div className="super-agent-chat">
      <aside className="super-agent-chat-contacts" aria-label={t("superAgents.chat.contacts")}>
        <label className="super-agent-chat-search">
          <Search size={14} aria-hidden="true" />
          <input type="search" value={query} placeholder={t("superAgents.chat.search")} aria-label={t("superAgents.chat.search")} onChange={(event) => setQuery(event.target.value)} />
        </label>
        <div className="super-agent-chat-contact-list">
          {visible.map((conversation) => {
            const last = conversation.items[conversation.items.length - 1];
            return (
              <button
                key={conversation.key}
                type="button"
                className={`super-agent-chat-contact${conversation.key === selected?.key ? " active" : ""}`}
                aria-current={conversation.key === selected?.key ? "true" : undefined}
                onClick={() => setSelectedKey(conversation.key)}
              >
                <ContactAvatar conversation={conversation} />
                <span className="super-agent-chat-contact-body">
                  <span className="super-agent-chat-contact-top">
                    <strong>{title(conversation)}</strong>
                    <time>{listTime(conversation.lastAt)}</time>
                  </span>
                  <span className="super-agent-chat-contact-bottom">
                    <small>{previewText(last) || subtitle(conversation)}</small>
                    {conversation.attention ? <span className="super-agent-chat-badge attention" title={t("superAgents.chat.needsYou")}>!</span>
                      : conversation.active > 0 ? <span className="super-agent-chat-badge" title={t("superAgents.chat.inProgress")}>{conversation.active}</span> : null}
                  </span>
                </span>
              </button>
            );
          })}
          {visible.length === 0 && <p className="muted super-agent-chat-empty">{t("superAgents.chat.noMatch")}</p>}
        </div>
      </aside>

      {selected && (
        <section className="super-agent-chat-thread" aria-label={title(selected)}>
          <header className="super-agent-chat-thread-head">
            <ContactAvatar conversation={selected} />
            <div>
              <strong>{title(selected)}</strong>
              <small>{subtitle(selected)}</small>
            </div>
          </header>
          {health?.last_error && <div className="error super-agent-chat-banner">{health.last_error}</div>}
          {health?.warning && <div className="warning super-agent-chat-banner" role="status">{health.warning}</div>}
          <div
            className="super-agent-chat-messages"
            ref={messagesRef}
            onScroll={(event) => {
              const element = event.currentTarget;
              stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 48;
            }}
          >
            {selected.items.length === 0 && (
              <div className="super-agent-chat-empty-thread">
                {agentFace}
                <p>{selected.kind === "console" ? t("superAgents.chat.consoleEmpty", { name: agent.name }) : t("superAgents.chat.empty")}</p>
              </div>
            )}
            {selected.items.map((item, index) => {
              const previous = selected.items[index - 1];
              const newDay = !previous || new Date(previous.at).toDateString() !== new Date(item.at).toDateString();
              return (
                <Fragment key={`${item.type}:${itemID(item)}`}>
                  {newDay && <div className="super-agent-chat-day"><span>{day(item.at)}</span></div>}
                  <ChatEntry
                    item={item}
                    conversation={selected}
                    context={context}
                    agentName={agent.name}
                    agentFace={agentFace}
                    clock={clock}
                    declineNote={item.type === "plan" ? declineNotes[item.plan.id] ?? "" : ""}
                    onDeclineNote={(value) => item.type === "plan" && onDeclineNote(item.plan.id, value)}
                  />
                </Fragment>
              );
            })}
          </div>
          {selected.kind === "console" ? (
            <form className="super-agent-chat-composer" onSubmit={(event) => { event.preventDefault(); send(); }}>
              <textarea
                aria-label={t("superAgents.tryIt")}
                placeholder={t("superAgents.chat.composerPlaceholder", { name: agent.name })}
                value={draft}
                rows={1}
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    send();
                  }
                }}
              />
              <button className="action" type="submit" disabled={!draft.trim() || Boolean(busy)}><Send size={14} /> {t("superAgents.send")}</button>
            </form>
          ) : (
            <footer className="super-agent-chat-readonly">
              <span>{t("superAgents.chat.replyElsewhere", { source: kindLabel(selected.sourceKind) || title(selected) })}</span>
              <button type="button" className="ghost-action" onClick={() => setSelectedKey(CONSOLE_CONVERSATION)}><SquareTerminal size={14} /> {t("superAgents.chat.openConsole")}</button>
            </footer>
          )}
        </section>
      )}
    </div>
  );
}

function itemID(item: ChatItem) {
  switch (item.type) {
    case "ticket": return item.ticket.id;
    case "plan": return item.plan.id;
    case "timer": return item.timer.id;
    default: return item.trigger.id;
  }
}

function ContactAvatar({ conversation }: { conversation: Conversation }) {
  // A direct chat shows the person in it.
  const person = conversation.kind === "chat" && conversation.participants.length === 1 ? conversation.participants[0] : "";
  if (person && conversation.avatars[person]) return <PersonFace name={person} avatar={conversation.avatars[person]} />;
  const icon = conversation.kind === "console" ? <SquareTerminal size={16} />
    : conversation.kind === "chat" ? (conversation.participants.length > 1 ? <Users size={16} /> : <MessagesSquare size={16} />)
    : conversation.key.startsWith("thread:mail:") ? <Inbox size={16} />
    : conversation.sourceKind === "github_webhook" ? <GitBranch size={16} />
    : conversation.sourceKind === "email" ? <Mail size={16} />
    : conversation.sourceKind === "feishu_doc" ? <FileText size={16} />
    : <MessagesSquare size={16} />;
  return <span className={`super-agent-chat-face contact kind-${conversation.kind}`} aria-hidden="true">{icon}</span>;
}

function PersonFace({ name, avatar }: { name: string; avatar?: string }) {
  // A portrait that fails to load, such as one Feishu has since replaced,
  // falls back to the initial.
  const [failed, setFailed] = useState("");
  if (avatar && failed !== avatar) {
    return (
      <span className="super-agent-chat-face person photo" aria-hidden="true">
        <img src={avatar} alt="" referrerPolicy="no-referrer" onError={() => setFailed(avatar)} />
      </span>
    );
  }
  const initial = Array.from(name.trim())[0]?.toUpperCase() ?? "?";
  let hash = 0;
  for (const char of name) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return <span className={`super-agent-chat-face person tone-${hash % 6}`} aria-hidden="true">{initial}</span>;
}

// Dispositions worth saying under a request: what happened when the agent
// did not simply answer or take the work.
const quietDispositions = new Set(["accepted", "answered", "follow_up"]);

function ChatEntry({ item, conversation, context, agentName, agentFace, clock, declineNote, onDeclineNote }: {
  item: ChatItem;
  conversation: Conversation;
  context: CardContext;
  agentName: string;
  agentFace: ReactNode;
  clock: (value?: string) => string;
  declineNote: string;
  onDeclineNote: (value: string) => void;
}) {
  const { t } = useI18n();
  const fromAgent = (body: ReactNode, at?: string, label?: string) => (
    <div className="super-agent-chat-row from-agent">
      {agentFace}
      <div className="super-agent-chat-stack">
        <span className="super-agent-chat-author">{label ? `${agentName} · ${label}` : agentName}<time>{clock(at)}</time></span>
        {body}
      </div>
    </div>
  );
  switch (item.type) {
    case "request": {
      const trigger = item.trigger;
      const name = conversation.kind === "console" ? t("superAgents.chat.you") : trigger.actor_name || trigger.actor || t("superAgents.chat.someone");
      return (
        <>
          <div className="super-agent-chat-row from-person">
            <div className="super-agent-chat-stack">
              <span className="super-agent-chat-author">{name}<time>{clock(trigger.received_at)}</time></span>
              <div className="super-agent-chat-bubble person">
                {trigger.title && <strong>{trigger.title}</strong>}
                <span>{trigger.text}</span>
              </div>
              {!quietDispositions.has(trigger.disposition) && trigger.disposition !== "pending" && (
                <span className="super-agent-chat-note">
                  <span className={`status-badge status-${triggerStatusClass(trigger.disposition)}`}>{t(`superAgents.disposition.${trigger.disposition}`)}</span>
                  {trigger.reason && <span title={trigger.reason}>{trigger.reason}</span>}
                </span>
              )}
            </div>
            <PersonFace name={name} avatar={conversation.kind === "console" ? undefined : trigger.actor_avatar || conversation.avatars[name]} />
          </div>
          {trigger.disposition === "pending" && fromAgent(<div className="session-typing"><span /><span /><span />{t("superAgents.disposition.pending")}</div>)}
        </>
      );
    }
    case "answer":
      return fromAgent(<AnswerBubble trigger={item.trigger} />, item.trigger.updated_at || item.at);
    case "event":
      return fromAgent(<AnswerBubble trigger={item.trigger} />, item.at, t(`superAgents.chat.event.${item.trigger.kind}`));
    case "ticket": {
      // A Ticket is the worker's message: it speaks as itself, for the agent.
      const worker = item.ticket.worker;
      return (
        <div className="super-agent-chat-row from-agent from-worker">
          <span className="super-agent-chat-face worker" aria-hidden="true"><WorkerFace worker={worker} /></span>
          <div className="super-agent-chat-stack">
            <span className="super-agent-chat-author">{t("superAgents.chat.workerAuthor", { name: workerPersona(worker).name, agent: agentName })}<time>{clock(item.at)}</time></span>
            <TicketCard ticket={item.ticket} context={context} variant="chat" />
          </div>
        </div>
      );
    }
    case "plan":
      return fromAgent(<PlanCard plan={item.plan} context={context} declineNote={declineNote} onDeclineNote={onDeclineNote} variant="chat" />, item.at);
    case "timer": {
      const timer = item.timer;
      const when = timer.status === "active" ? timer.due_at : timer.status === "done" ? timer.fired_at || timer.updated_at : timer.updated_at;
      return (
        <div className="super-agent-chat-system">
          <AlarmClock size={13} aria-hidden="true" />
          <span>{t("superAgents.chat.timer", { note: timer.note })}</span>
          <span className={`status-badge status-${timerStatusClass(timer.status)}`}>{t(`superAgents.timer.${timer.status}`)}</span>
          <time>{context.time(when)}</time>
          {timer.status === "active" && (
            <button type="button" className="ghost-action danger-action" disabled={Boolean(context.busy)} onClick={() => context.run("cancel-timer", () => api.cancelSuperAgentTimer(timer.id))}>
              <X size={12} /> {t("common.cancel")}
            </button>
          )}
        </div>
      );
    }
  }
}

function AnswerBubble({ trigger }: { trigger: SuperAgentTrigger }) {
  return (
    <div className="super-agent-chat-bubble agent message-content">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{trigger.answer ?? ""}</ReactMarkdown>
    </div>
  );
}
