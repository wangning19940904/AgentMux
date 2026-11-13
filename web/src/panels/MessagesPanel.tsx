import { ArrowLeft, Check, Eye, Plus, RefreshCw, Send, SquarePen, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent } from "react";
import { mailboxAPI, notifyMailboxUnread } from "../api";
import type { MailContact, MailConversation, MailMessage, MailParty } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { contactState, conversationThread, messageState, partyFromAddress, relativeTime, sortParties, threadKey } from "./messagesModel";
import type { OpenThread } from "./messagesModel";
import { confirmAction } from "../components/ConfirmDialog";

// Messages are the direct messages between people and agents, and between
// agents. The page lists the viewer's own conversations and the ones between
// agents they may watch, opens one as a chat, and manages contacts: the
// connections that let parties of different owners write to each other.

const POLL_INTERVAL_MS = 5_000;
// Who exists, who is online and which contacts wait change less often.
const SLOW_POLL_INTERVAL_MS = 30_000;
// The recipient list gets a filter once it is long enough to need one.
const FILTER_FROM = 8;

type View = "conversations" | "contacts";
type Translate = ReturnType<typeof useI18n>["t"];

function errorText(error: unknown) {
  return (error instanceof Error ? error.message : String(error)).replace(/^\w*Error:\s*/, "");
}

function partyDetail(party: MailParty, t: Translate) {
  return [
    t(`messages.kind.${party.kind}`),
    party.machine,
    party.owner_name && party.owner_name !== party.name ? t("messages.ownedBy", { name: party.owner_name }) : "",
    party.kind === "agent" && !party.online ? t("messages.offline") : "",
  ].filter(Boolean).join(" · ");
}

export function MessagesPanel() {
  const { t } = useI18n();
  const overview = useAsync(() => mailboxAPI.overview(), []);
  const directory = useAsync(() => mailboxAPI.directory(), []);
  const contacts = useAsync(() => mailboxAPI.contacts(), []);
  const [view, setView] = useState<View>("conversations");
  const [open, setOpen] = useState<OpenThread | null>(null);
  const [picking, setPicking] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<{ key: string; text: string } | null>(null);
  // Messages sent from here show at once, until the thread reloads with them.
  const [sent, setSent] = useState<{ key: string; messages: MailMessage[] }>({ key: "", messages: [] });

  const me = overview.data?.me;
  const mine = overview.data?.conversations ?? [];
  const conversationsRef = useRef(mine);
  conversationsRef.current = mine;

  const openKey = threadKey(open);
  const thread = useAsync(async () => {
    if (!open) return { key: "", messages: [] as MailMessage[] };
    if (open.watched) return { key: openKey, messages: await mailboxAPI.messages(open.self, open.peer) };
    const messages = await mailboxAPI.messages(open.peer);
    // Reading the viewer's own conversation marks it read on the server, so
    // the list is reloaded when it still counts those messages as unread.
    if (conversationsRef.current.some((conversation) => conversation.peer.address === open.peer && conversation.unread > 0)) void overview.reload();
    return { key: openKey, messages };
  }, [openKey]);
  const loaded = thread.data?.key === openKey ? thread.data.messages : null;
  const watched = overview.data?.agent_conversations ?? [];
  const controlled = overview.data?.controlled ?? [];
  const parties = directory.data?.parties ?? [];
  const reachable = directory.data?.reachable ?? [];

  // Everyone this page can name, by address.
  const known = useMemo(() => {
    const index = new Map<string, MailParty>();
    for (const conversation of [...mine, ...watched]) {
      index.set(conversation.self.address, conversation.self);
      index.set(conversation.peer.address, conversation.peer);
    }
    for (const party of parties) index.set(party.address, party);
    if (me) index.set(me.address, me);
    return index;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [overview.data, directory.data]);
  const partyOf = useCallback((address: string, name = "") => known.get(address) ?? partyFromAddress(address, name), [known]);

  const recipients = useMemo(
    () => sortParties(parties.filter((party) => reachable.includes(party.address))),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [directory.data],
  );
  const pendingForMe = (contacts.data ?? []).filter((contact) => contactState(contact, controlled) === "accept").length;

  const refresh = useCallback(() => {
    void overview.reload();
    if (openKey) void thread.reload();
    if (view === "contacts") {
      void contacts.reload();
      void directory.reload();
    }
  }, [overview.reload, thread.reload, contacts.reload, directory.reload, openKey, view]);
  usePolling(refresh, POLL_INTERVAL_MS);
  const refreshPeople = useCallback(() => {
    void contacts.reload();
    void directory.reload();
  }, [contacts.reload, directory.reload]);
  usePolling(refreshPeople, SLOW_POLL_INTERVAL_MS, { enabled: view !== "contacts" });

  // The shell's navigation badge follows what this page knows.
  const unread = overview.data?.unread;
  useEffect(() => {
    if (unread !== undefined) notifyMailboxUnread(unread);
  }, [unread]);

  function openThread(next: OpenThread) {
    setOpen(next);
    setPicking(false);
    setView("conversations");
  }

  function closePane() {
    setOpen(null);
    setPicking(false);
  }

  function startNewMessage() {
    setView("conversations");
    setOpen(null);
    setPicking(true);
    void directory.reload();
  }

  async function send() {
    if (!open || open.watched || sending) return;
    const key = openKey;
    const text = (drafts[key] ?? "").trim();
    if (!text) return;
    setSending(true);
    setSendError(null);
    try {
      const message = await mailboxAPI.send(open.peer, text);
      setDrafts((current) => ({ ...current, [key]: "" }));
      setSent((current) => ({ key, messages: [...(current.key === key ? current.messages : []), message] }));
      await Promise.all([thread.reload(), overview.reload()]);
    } catch (error) {
      // The draft stays, so nothing typed is lost.
      setSendError({ key, text: errorText(error) });
    } finally {
      setSending(false);
    }
  }

  const messages = useMemo(() => {
    if (!loaded) return null;
    const ids = new Set(loaded.map((message) => message.id));
    return [...loaded, ...(sent.key === openKey ? sent.messages.filter((message) => !ids.has(message.id)) : [])];
  }, [loaded, sent, openKey]);

  const drilled = view === "conversations" && (picking || Boolean(open));
  const nothingYet = mine.length === 0 && watched.length === 0;
  const peer = open ? partyOf(open.peer) : null;
  // The directory knows who the viewer may write to. A party it does not
  // list lives elsewhere, and only sending can tell.
  const blocked = Boolean(open && !open.watched && directory.data && parties.some((party) => party.address === open.peer) && !reachable.includes(open.peer));

  return (
    <div className="page-stack">
      <section className={`surface mail-surface${drilled ? " mail-drilled" : ""}`}>
        <div className="surface-header">
          <div>
            <h2>{t("messages.title")}</h2>
            <p>{t("messages.subtitle")}</p>
          </div>
          <div className="control-row">
            <div className="segmented mail-views" role="group" aria-label={t("messages.title")}>
              <button type="button" className={view === "conversations" ? "active" : ""} aria-pressed={view === "conversations"} onClick={() => setView("conversations")}>
                {t("messages.conversations")}
              </button>
              <button type="button" className={view === "contacts" ? "active" : ""} aria-pressed={view === "contacts"} onClick={() => setView("contacts")}>
                {t("messages.contacts")}
                {pendingForMe > 0 && <span className="mail-unread" aria-label={t("messages.contacts.pendingCount", { count: pendingForMe })}>{pendingForMe}</span>}
              </button>
            </div>
            <button className="ghost-action" type="button" onClick={() => { refresh(); if (view !== "contacts") refreshPeople(); }}>
              <RefreshCw size={15} /> {t("common.refresh")}
            </button>
            {view === "conversations" && (
              <button className="action" type="button" onClick={startNewMessage}>
                <SquarePen size={15} /> {t("messages.new")}
              </button>
            )}
          </div>
        </div>
        {overview.error && <div className="surface-body session-notice error" role="alert">{t("messages.loadFailed")} {errorText(overview.error)}</div>}

        {view === "conversations" ? (
          <div className="mail-layout">
            <div className="mail-list">
              {!overview.data && overview.loading && <div className="empty-state">{t("common.loading")}</div>}
              {overview.data && nothingYet && (
                <div className="empty-state mail-empty">
                  <p>{t(recipients.length > 0 || !directory.data ? "messages.empty" : "messages.emptyNoRecipients")}</p>
                  {recipients.length > 0 && (
                    <button className="action" type="button" onClick={startNewMessage}><SquarePen size={15} /> {t("messages.new")}</button>
                  )}
                </div>
              )}
              {overview.data && !nothingYet && (
                <>
                  <section aria-labelledby="mail-group-mine">
                    <h3 id="mail-group-mine" className="mail-group-title">{t("messages.group.mine")}</h3>
                    {mine.length === 0 && <p className="mail-group-empty">{t("messages.group.mineEmpty")}</p>}
                    {mine.map((conversation) => {
                      const key = threadKey(conversationThread(conversation, false));
                      return (
                        <ConversationRow key={key} conversation={conversation} watched={false} active={key === openKey} me={me?.address ?? ""}
                          onOpen={() => openThread(conversationThread(conversation, false))} />
                      );
                    })}
                  </section>
                  {watched.length > 0 && (
                    <section aria-labelledby="mail-group-agents">
                      <h3 id="mail-group-agents" className="mail-group-title">{t("messages.group.agents")}<small>{t("messages.group.agentsHint")}</small></h3>
                      {watched.map((conversation) => {
                        const key = threadKey(conversationThread(conversation, true));
                        return (
                          <ConversationRow key={key} conversation={conversation} watched active={key === openKey} me={me?.address ?? ""}
                            onOpen={() => openThread(conversationThread(conversation, true))} />
                        );
                      })}
                    </section>
                  )}
                </>
              )}
            </div>

            <div className="mail-pane">
              {picking ? (
                <RecipientPicker recipients={recipients} loading={directory.loading && !directory.data} error={directory.error ? errorText(directory.error) : ""}
                  onPick={(party) => openThread({ watched: false, peer: party.address })} onBack={closePane} onContacts={() => { closePane(); setView("contacts"); }} />
              ) : open && peer ? (
                <Thread
                  key={openKey}
                  self={open.watched ? partyOf(open.self) : me ?? partyFromAddress("")}
                  peer={peer}
                  watched={open.watched}
                  messages={messages}
                  error={thread.error ? errorText(thread.error) : ""}
                  blocked={blocked}
                  draft={drafts[openKey] ?? ""}
                  sending={sending}
                  sendError={sendError?.key === openKey ? sendError.text : ""}
                  onDraft={(value) => {
                    setDrafts((current) => ({ ...current, [openKey]: value }));
                    setSendError(null);
                  }}
                  onSend={() => void send()}
                  onBack={closePane}
                  onContacts={() => { closePane(); setView("contacts"); }}
                />
              ) : (
                <div className="empty-state mail-pane-empty">{t("messages.choose")}</div>
              )}
            </div>
          </div>
        ) : (
          <Contacts
            contacts={contacts.data}
            error={contacts.error ? errorText(contacts.error) : ""}
            controlled={controlled}
            admin={overview.data?.admin ?? false}
            everyone={me ? [me, ...parties] : parties}
            partyOf={partyOf}
            onChanged={async () => { await Promise.all([contacts.reload(), directory.reload()]); }}
          />
        )}
      </section>
    </div>
  );
}

// PartyName is a party's name with a dot that says whether an agent's
// machine is online. People have no dot.
function PartyName({ party }: { party: MailParty }) {
  const { t } = useI18n();
  const state = t(party.online ? "messages.online" : "messages.offline");
  return (
    <span className="mail-party">
      {party.kind === "agent" && <i className={`mail-dot ${party.online ? "online" : "offline"}`} role="img" aria-label={state} title={state} />}
      <span>{party.name}</span>
    </span>
  );
}

function useTimes() {
  const { t, language } = useI18n();
  const locale = language === "zh" ? "zh-CN" : "en-US";
  return {
    relative(value?: string) {
      const time = relativeTime(value);
      if (!time) return "";
      if ("date" in time) return time.date.toLocaleDateString(locale, { month: "short", day: "numeric" });
      return t(time.key, time.count === undefined ? undefined : { count: time.count });
    },
    exact(value?: string) {
      return value ? new Date(value).toLocaleString(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "";
    },
  };
}

function ConversationRow({ conversation, watched, active, me, onOpen }: {
  conversation: MailConversation;
  watched: boolean;
  active: boolean;
  me: string;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const times = useTimes();
  const { self, peer, last } = conversation;
  // The open conversation is being read, whatever the last count said.
  const unread = active ? 0 : conversation.unread;
  const machines = (watched ? [self.machine, peer.machine] : [peer.machine]).filter(Boolean).join(" · ");
  const author = watched
    ? last.from_name || (last.from === self.address ? self.name : peer.name)
    : last.from === me ? t("messages.you") : "";
  const preview = last.text.replace(/\s+/g, " ").trim();
  return (
    <button type="button" className={`mail-row${active ? " active" : ""}${unread > 0 ? " unread" : ""}`} aria-current={active ? "true" : undefined} onClick={onOpen}>
      <span className="mail-row-main">
        <span className="mail-row-title">
          {watched && <><PartyName party={self} />{" ↔ "}</>}
          <PartyName party={peer} />
          {machines && <small>{machines}</small>}
        </span>
        <span className="mail-row-preview">
          {messageState(last) === "failed" && <em>{t("messages.state.failed")} · </em>}
          {author ? t("messages.preview", { name: author, text: preview }) : preview}
        </span>
      </span>
      <span className="mail-row-side">
        <time dateTime={last.created_at} title={times.exact(last.created_at)}>{times.relative(last.created_at)}</time>
        {unread > 0 && <span className="mail-unread" aria-label={t("messages.unreadCount", { count: unread })}>{unread}</span>}
      </span>
    </button>
  );
}

function Thread({ self, peer, watched, messages, error, blocked, draft, sending, sendError, onDraft, onSend, onBack, onContacts }: {
  // self is the viewer in their own conversation, and the party shown on the
  // viewer's side in a watched one.
  self: MailParty;
  peer: MailParty;
  watched: boolean;
  messages: MailMessage[] | null;
  error: string;
  blocked: boolean;
  draft: string;
  sending: boolean;
  sendError: string;
  onDraft: (value: string) => void;
  onSend: () => void;
  onBack: () => void;
  onContacts: () => void;
}) {
  const { t } = useI18n();
  const times = useTimes();
  const scroller = useRef<HTMLDivElement>(null);
  const count = messages?.length ?? 0;

  // Keep the newest message in view when the thread opens or grows, and when
  // a refusal under the composer takes some of its room.
  useEffect(() => {
    const element = scroller.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [count, sendError]);

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    // Enter sends and Shift+Enter starts a new line, as in Session History.
    // Enter that confirms an input method's candidate is not a send.
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      onSend();
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    onSend();
  }

  const detail = [peer.machine, peer.description].filter(Boolean).join(" · ");
  return (
    <div className="mail-thread">
      <header className="mail-thread-header">
        <button className="ghost-action icon-action mail-back" type="button" onClick={onBack} title={t("messages.back")} aria-label={t("messages.back")}>
          <ArrowLeft size={16} />
        </button>
        <div>
          <h3>
            {watched && <><PartyName party={self} />{" ↔ "}</>}
            <PartyName party={peer} />
          </h3>
          <small>{watched ? [self.machine, peer.machine].filter(Boolean).join(" · ") || t("messages.group.agentsHint") : detail || peer.address}</small>
        </div>
      </header>

      <div className="mail-messages" ref={scroller} aria-live="polite">
        {error && <div className="session-notice error" role="alert">{error}</div>}
        {!messages && !error && <div className="empty-state compact">{t("common.loading")}</div>}
        {messages && messages.length === 0 && <div className="empty-state compact">{t(watched ? "messages.threadEmptyWatched" : "messages.threadEmpty", { name: peer.name })}</div>}
        {(messages ?? []).map((message) => {
          const own = message.from === self.address;
          const state = messageState(message);
          return (
            <article key={message.id} className={`mail-message ${own ? "end" : "start"}${state === "delivered" ? "" : ` ${state}`}`}>
              {watched && <header>{message.from_name || (own ? self.name : peer.name)}</header>}
              <div className="mail-bubble">{message.text}</div>
              <footer>
                <time dateTime={message.created_at}>{times.exact(message.created_at)}</time>
                {state === "waiting" && <span className="mail-state">{t("messages.state.waiting")}</span>}
                {state === "failed" && <span className="mail-state failed">{t("messages.state.failed")}{message.error ? `: ${message.error}` : ""}</span>}
              </footer>
            </article>
          );
        })}
      </div>

      {watched ? (
        <div className="mail-readonly" role="note">
          <Eye size={15} aria-hidden="true" />
          <span>{t("messages.readOnly", { a: self.name, b: peer.name })}</span>
        </div>
      ) : blocked ? (
        <div className="mail-readonly" role="note">
          <span>{t("messages.blocked", { name: peer.name })}</span>
          <button className="ghost-action" type="button" onClick={onContacts}>{t("messages.contacts")}</button>
        </div>
      ) : (
        <form className="mail-composer" onSubmit={submit}>
          {peer.kind === "agent" && !peer.online && <small className="mail-composer-note">{t("messages.offlineHint", { name: peer.name })}</small>}
          <textarea
            aria-label={t("messages.write")}
            placeholder={t("messages.writeTo", { name: peer.name })}
            rows={2}
            value={draft}
            onChange={(event) => onDraft(event.target.value)}
            onKeyDown={handleKeyDown}
          />
          {sendError && <div className="mail-inline-error" role="alert">{sendError}</div>}
          <div className="mail-composer-footer">
            <span>{t("messages.composerHint")}</span>
            <button className="action" type="submit" disabled={!draft.trim() || sending}>
              <Send size={15} /> {t(sending ? "messages.sending" : "messages.send")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

function RecipientPicker({ recipients, loading, error, onPick, onBack, onContacts }: {
  recipients: MailParty[];
  loading: boolean;
  error: string;
  onPick: (party: MailParty) => void;
  onBack: () => void;
  onContacts: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const needle = query.trim().toLocaleLowerCase();
  const shown = needle
    ? recipients.filter((party) => [party.name, party.machine, party.owner_name, party.description].some((value) => value?.toLocaleLowerCase().includes(needle)))
    : recipients;
  return (
    <div className="mail-thread">
      <header className="mail-thread-header">
        <button className="ghost-action icon-action mail-back" type="button" onClick={onBack} title={t("messages.back")} aria-label={t("messages.back")}>
          <ArrowLeft size={16} />
        </button>
        <div>
          <h3>{t("messages.new")}</h3>
          <small>{t("messages.pickRecipient")}</small>
        </div>
      </header>
      {recipients.length >= FILTER_FROM && (
        <div className="mail-picker-filter">
          <input type="search" aria-label={t("messages.findRecipient")} placeholder={t("messages.findRecipient")} value={query} onChange={(event) => setQuery(event.target.value)} />
        </div>
      )}
      <div className="mail-picker-list">
        {error && <div className="session-notice error" role="alert">{error}</div>}
        {loading && <div className="empty-state compact">{t("common.loading")}</div>}
        {!loading && !error && recipients.length === 0 && (
          <div className="empty-state mail-empty">
            <p>{t("messages.emptyNoRecipients")}</p>
            <button className="ghost-action" type="button" onClick={onContacts}>{t("messages.contacts")}</button>
          </div>
        )}
        {recipients.length > 0 && shown.length === 0 && <div className="empty-state compact">{t("messages.noMatch")}</div>}
        {shown.map((party) => (
          <button key={party.address} type="button" className="mail-row" onClick={() => onPick(party)}>
            <span className="mail-row-main">
              <span className="mail-row-title"><PartyName party={party} /></span>
              <span className="mail-row-preview">{[partyDetail(party, t), party.description].filter(Boolean).join(" · ")}</span>
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

function Contacts({ contacts, error, controlled, admin, everyone, partyOf, onChanged }: {
  contacts: MailContact[] | null;
  error: string;
  controlled: string[];
  admin: boolean;
  // Every party the viewer can name, themselves included.
  everyone: MailParty[];
  partyOf: (address: string, name?: string) => MailParty;
  onChanged: () => Promise<void>;
}) {
  const { t } = useI18n();
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [problem, setProblem] = useState("");

  async function run(id: string, action: () => Promise<unknown>) {
    setBusy(id);
    setProblem("");
    setNotice("");
    try {
      await action();
      await onChanged();
    } catch (failure) {
      setProblem(errorText(failure));
    } finally {
      setBusy("");
    }
  }

  const items = contacts ?? [];
  return (
    <div className="surface-body mail-contacts">
      <div className="mail-contacts-intro">
        <p className="subtle-copy">{t("messages.contacts.rule")}</p>
        {!adding && (
          <button className="action" type="button" onClick={() => { setAdding(true); setNotice(""); }}>
            <Plus size={15} /> {t("messages.contacts.add")}
          </button>
        )}
      </div>

      {adding && (
        <ContactForm
          controlled={controlled}
          admin={admin}
          everyone={everyone}
          onCancel={() => setAdding(false)}
          onAdded={async (contact) => {
            setAdding(false);
            const waitingFor = contact.waiting_for ? partyOf(contact.waiting_for) : null;
            setNotice(contact.status === "accepted"
              ? t("messages.contacts.added", { a: partyOf(contact.a, contact.a_name).name, b: partyOf(contact.b, contact.b_name).name })
              : t("messages.contacts.requested", { name: waitingFor?.owner_name || waitingFor?.name || t("messages.contacts.otherSide") }));
            await onChanged();
          }}
        />
      )}

      {notice && <div className="session-notice" role="status">{notice}</div>}
      {(problem || error) && <div className="session-notice error" role="alert">{problem || error}</div>}

      {!contacts && !error && <div className="empty-state compact">{t("common.loading")}</div>}
      {contacts && items.length === 0 && !adding && <div className="empty-state">{t("messages.contacts.empty")}</div>}
      {items.length > 0 && (
        <ul className="mail-contact-list">
          {items.map((contact) => {
            const a = partyOf(contact.a, contact.a_name);
            const b = partyOf(contact.b, contact.b_name);
            const state = contactState(contact, controlled);
            const waitingFor = contact.waiting_for ? partyOf(contact.waiting_for) : null;
            const working = busy === contact.id;
            return (
              <li key={contact.id}>
                <div className="mail-contact-parties">
                  <ContactSide party={a} />
                  <span aria-hidden="true">↔</span>
                  <ContactSide party={b} />
                </div>
                <div className="mail-contact-state">
                  <span className={`status-badge status-${state === "accepted" ? "succeeded" : "queued"}`}>
                    {t(state === "accepted" ? "messages.contacts.accepted" : "messages.contacts.pending")}
                  </span>
                  {state === "accept" && <small>{t("messages.contacts.waitingForYou", { name: waitingFor?.name ?? "" })}</small>}
                  {state === "waiting" && (
                    <small>{waitingFor?.owner_name ? t("messages.contacts.waitingForOwner", { name: waitingFor.owner_name }) : t("messages.contacts.waitingForOther")}</small>
                  )}
                </div>
                <div className="control-row">
                  {state === "accept" && (
                    <button className="action" type="button" disabled={working} onClick={() => void run(contact.id, () => mailboxAPI.acceptContact(contact.id))}>
                      <Check size={14} /> {t("messages.contacts.accept")}
                    </button>
                  )}
                  <button className="ghost-action danger-action" type="button" disabled={working} onClick={async () => {
                    if (state !== "accepted" || await confirmAction(t("messages.contacts.removeConfirm", { a: a.name, b: b.name }), { danger: true })) {
                      void run(contact.id, () => mailboxAPI.removeContact(contact.id));
                    }
                  }}>
                    {state === "accepted" ? <Trash2 size={14} /> : <X size={14} />}
                    {" "}{t(state === "accepted" ? "messages.contacts.remove" : state === "accept" ? "messages.contacts.decline" : "messages.contacts.withdraw")}
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function ContactSide({ party }: { party: MailParty }) {
  const { t } = useI18n();
  return (
    <span className="mail-contact-side">
      <strong><PartyName party={party} /></strong>
      <small title={party.address}>{partyDetail(party, t)}</small>
    </span>
  );
}

function ContactForm({ controlled, admin, everyone, onCancel, onAdded }: {
  controlled: string[];
  admin: boolean;
  everyone: MailParty[];
  onCancel: () => void;
  onAdded: (contact: MailContact) => Promise<void>;
}) {
  const { t } = useI18n();
  const [first, setFirst] = useState("");
  const [second, setSecond] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");

  // One side is a party the viewer answers for: an administrator answers for
  // everyone. The other side is anyone else a contact could connect it to:
  // not a party of the same owner, and not two people.
  const mine = sortParties(everyone.filter((party) => admin || controlled.includes(party.address)));
  const chosen = everyone.find((party) => party.address === first);
  const others = sortParties(everyone.filter((party) =>
    party.address !== first
    && (admin || !controlled.includes(party.address))
    && !(chosen?.kind === "person" && party.kind === "person")));
  // A choice the first side rules out is no longer a choice.
  const other = others.some((party) => party.address === second) ? second : "";

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!first || !other || busy) return;
    setBusy(true);
    setProblem("");
    try {
      await onAdded(await mailboxAPI.addContact(first, other));
    } catch (failure) {
      setProblem(errorText(failure));
    } finally {
      setBusy(false);
    }
  }

  const options = (list: MailParty[]) => (["agent", "person"] as const).map((kind) => {
    const group = list.filter((party) => party.kind === kind);
    return group.length > 0 && (
      <optgroup key={kind} label={t(`messages.kinds.${kind}`)}>
        {group.map((party) => (
          <option key={party.address} value={party.address}>
            {[party.name, party.machine, party.owner_name && party.owner_name !== party.name ? party.owner_name : ""].filter(Boolean).join(" · ")}
          </option>
        ))}
      </optgroup>
    );
  });

  return (
    <form className="mail-contact-form" onSubmit={(event) => void submit(event)}>
      <div className="field-grid">
        <label className="field"><span>{t(admin ? "messages.contacts.first" : "messages.contacts.yours")}</span>
          <select value={first} onChange={(event) => setFirst(event.target.value)}>
            <option value="">{t("messages.contacts.choose")}</option>
            {options(mine)}
          </select>
        </label>
        <label className="field"><span>{t("messages.contacts.other")}</span>
          <select value={other} onChange={(event) => setSecond(event.target.value)}>
            <option value="">{t("messages.contacts.choose")}</option>
            {options(others)}
          </select>
        </label>
      </div>
      {mine.length > 0 && others.length === 0 && <small className="muted">{t("messages.contacts.noOthers")}</small>}
      {problem && <div className="mail-inline-error" role="alert">{problem}</div>}
      <div className="control-row">
        <button className="action" type="submit" disabled={!first || !other || busy}>{t("messages.contacts.add")}</button>
        <button className="ghost-action" type="button" onClick={onCancel}>{t("common.cancel")}</button>
      </div>
    </form>
  );
}
