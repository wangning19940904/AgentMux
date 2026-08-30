import { KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Activity,
  Bot,
  CheckCircle2,
  ChevronDown,
  Clipboard,
  Clock3,
  ExternalLink,
  Link2,
  MessageSquareText,
  Play,
  RefreshCw,
  Send,
	Square,
  TerminalSquare,
  Trash2,
  Wrench,
  X,
  XCircle,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { ChannelAvatar } from "../ChannelAvatar";
import { AgentSession, ProxyTrace, SessionMessage, api } from "../api";
import { useI18n } from "../i18n";
import { usePolling } from "../hooks/usePolling";
import { useAsync } from "../useAsync";
import { TargetBadge } from "../components/TargetBadge";
import { CatalogPagination, useCatalogPagination } from "../components/CatalogPagination";
import { Drawer } from "../components/ui";

const PROVIDERS = [
  { id: "", labelKey: "sessions.allProviders" },
  { id: "claudecode", labelKey: "sessions.claudeCode" },
  { id: "codex", labelKey: "sessions.codex" },
];

const SOURCES = [
  { id: "", labelKey: "sessions.allSources" },
  { id: "channel", labelKey: "sessions.channelSessions" },
  { id: "local", labelKey: "sessions.localSessions" },
  { id: "cli", labelKey: "sessions.cli" },
  { id: "desktop", labelKey: "sessions.desktopApp" },
  { id: "app-server", labelKey: "sessions.appServer" },
];

export function SessionsPanel() {
  const { t, language } = useI18n();
  const [provider, setProvider] = useState("");
  const [source, setSource] = useState("");
  const [agentID, setAgentID] = useState("");
  const [channelID, setChannelID] = useState("");
  const [query, setQuery] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [busy, setBusy] = useState("");
	const [stopping, setStopping] = useState(false);
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [draft, setDraft] = useState("");
  const [optimisticMessages, setOptimisticMessages] = useState<SessionMessage[]>([]);
  const [bindChannelID, setBindChannelID] = useState("");
  const [bindConversationID, setBindConversationID] = useState("");
  const [localAnswers, setLocalAnswers] = useState<Record<string, string>>({});
  const [terminalVisible, setTerminalVisible] = useState(false);
  const [terminalDraft, setTerminalDraft] = useState("");
  const [terminalBusy, setTerminalBusy] = useState(false);
  const transcriptRef = useRef<HTMLDivElement>(null);
	const stopRequestedRef = useRef(false);

  const sessions = useAsync(() => api.sessions(), []);
	usePolling(sessions.reload, 10_000);
  const channels = useAsync(() => api.channels(), []);
  const channelByID = useMemo(
    () => new Map((channels.data ?? []).map((channel) => [`${channel.target_id || "local"}::${channel.id}`, channel])),
    [channels.data]
  );
  const availableAgents = useMemo(() => {
    const agents = new Map<string, string>();
    for (const session of sessions.data ?? []) {
      if (session.agent_id) agents.set(session.agent_id, session.agent_name || session.agent_id);
    }
    return [...agents.entries()].sort((left, right) => left[1].localeCompare(right[1]));
  }, [sessions.data]);
  const availableChannels = useMemo(() => {
    const items = new Map<string, string>();
    for (const session of sessions.data ?? []) {
      if (session.channel_id) items.set(session.channel_id, session.channel_name || session.channel_id);
    }
    return [...items.entries()].sort((left, right) => left[1].localeCompare(right[1]));
  }, [sessions.data]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (sessions.data ?? []).filter((session) => {
      if (provider && normalizeProvider(session.provider_id) !== normalizeProvider(provider)) return false;
      if (source === "channel" && session.origin !== "channel") return false;
      if (source === "local" && session.origin === "channel") return false;
      if ((source === "cli" || source === "desktop" || source === "app-server") && session.surface !== source) return false;
      if (agentID && session.agent_id !== agentID) return false;
      if (channelID && session.channel_id !== channelID) return false;
      if (!q) return true;
      return [
        session.title,
        session.summary,
        session.project_dir,
        session.session_id,
        session.native_session_id,
        session.provider_id,
        session.surface,
        session.agent_name,
        session.channel_name,
        session.channel_type,
        session.conversation_key,
        session.chat_id,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(q));
    });
  }, [agentID, channelID, provider, query, sessions.data, source]);

  const pagination = useCatalogPagination(filtered, [agentID, channelID, provider, query, source].join("\u0000"));

  const selected = useMemo(
    () => (sessions.data ?? []).find((session) => keyOf(session) === selectedID),
    [selectedID, sessions.data]
  );
  const selectedKey = selected ? keyOf(selected) : "";
  const messages = useAsync(
    () => (selected ? api.sessionMessages(selected) : Promise.resolve([])),
    [selected?.conversation_id, selected?.session_id, selected?.source_path, selected?.surface, selected?.target_id]
  );
  const traceSessionID = selected?.native_session_id || (selected?.origin !== "channel" ? selected?.session_id : "");
  const traceTool = selected && traceSessionID ? routeToolForSession(selected) : "";
  const sessionTraces = useAsync(
    () => (traceTool && traceSessionID ? api.proxyTraces({ tool: traceTool, sessionID: traceSessionID, limit: 20 }, selected?.target_id) : Promise.resolve([])),
    [traceTool, traceSessionID, selected?.target_id]
  );
  const recentTraces = useAsync(
    () => (traceTool ? api.proxyTraces({ tool: traceTool, limit: 10 }, selected?.target_id) : Promise.resolve([])),
    [traceTool, selected?.target_id]
  );
  const channelConversations = useAsync(
    () => (bindChannelID ? api.channelConversations(bindChannelID) : Promise.resolve([])),
    [bindChannelID]
  );
  const interactionChannelID = selected?.channel_id || bindChannelID;
  const interactionConversationID = selected?.conversation_id || bindConversationID;
  const pendingInteractions = useAsync(
    () => (interactionChannelID ? api.channelInteractions(interactionChannelID, interactionConversationID, selected?.target_id) : Promise.resolve([])),
    [interactionChannelID, interactionConversationID, selected?.target_id]
  );
  const terminal = useAsync(
    () => terminalVisible && selected?.terminal_backend && selected.channel_id && selected.conversation_id
      ? api.terminalSession(selected)
      : Promise.resolve(null),
    [terminalVisible, selected?.channel_id, selected?.conversation_id, selected?.terminal_backend, selected?.target_id]
  );
  usePolling(terminal.reload, 750, { enabled: terminalVisible && Boolean(selected?.terminal_backend) });
  const visibleMessages = [...(messages.data ?? []), ...optimisticMessages];
  const transcriptMessages = visibleMessages.filter((message) =>
    message.role === "user" || message.role === "assistant" || message.role === "tool"
  );
  const selectedTraces = sessionTraces.data ?? [];
  const fallbackTraces = recentTraces.data ?? [];
  const routeTraces = selectedTraces.length > 0 ? selectedTraces : fallbackTraces;
  const routeTraceFallback = !sessionTraces.loading && selectedTraces.length === 0 && fallbackTraces.length > 0;
  const routeTraceLoading = sessionTraces.loading || (!sessionTraces.loading && selectedTraces.length === 0 && recentTraces.loading);
	const selectedRunStatus = stopping
		? "stopping"
		: busy === "message"
			? "running"
			: selected?.run_status || "idle";
	const selectedCanStop = Boolean(
		selectedRunStatus !== "stopping" && selected?.channel_id && selected?.conversation_id &&
		(selected.can_stop || (selected.can_chat && busy === "message"))
	);

  const closeDrawer = useCallback(() => setSelectedID(""), []);

  useEffect(() => {
    setOptimisticMessages([]);
    setDraft("");
    setTerminalVisible(false);
    setTerminalDraft("");
    if (selected?.channel_id) {
      setBindChannelID(selected.channel_id);
      setBindConversationID(selected.conversation_id || "");
    }
  }, [selectedKey]);

  async function sendTerminalInput() {
    if (!selected || !terminalDraft || terminalBusy) return;
    setTerminalBusy(true);
    try {
      await api.writeTerminal(selected, terminalDraft, true);
      setTerminalDraft("");
      await terminal.reload();
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setTerminalBusy(false);
    }
  }

  async function interruptTerminal() {
    if (!selected || terminalBusy) return;
    setTerminalBusy(true);
    try {
      // ETX is interpreted by tmux-backed sessions as Ctrl+C.
      await api.writeTerminal(selected, "\u0003", false);
      await terminal.reload();
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setTerminalBusy(false);
    }
  }

  useEffect(() => {
    const node = transcriptRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [messages.data, optimisticMessages]);

  function showNotice(text: string, error = false) {
    setNotice(text);
    setNoticeError(error);
  }

  async function copy(text: string) {
    if (!text) return;
    await navigator.clipboard.writeText(text);
    showNotice(t("sessions.copied"));
  }

  async function resume(openTerminal: boolean) {
    if (!selected) return;
    setBusy(openTerminal ? "terminal" : "resume");
    try {
      const res = await api.resumeSession(selected, openTerminal);
      if (res.command) await copy(res.command);
      showNotice(res.status_message || (res.thread_id ? `${t("sessions.thread")} ${res.thread_id}` : t("sessions.resumeReady")));
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  async function remove() {
    if (!selected || !selected.file_backed) return;
    setBusy("delete");
    try {
      await api.deleteSession(selected);
      setSelectedID("");
      await sessions.reload();
      showNotice(t("sessions.deleted"));
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

	async function stopConversation() {
		if (!selected?.channel_id || !selected.conversation_id || !selectedCanStop || stopping) return;
		if (!window.confirm(t("sessions.stopConfirm"))) return;
		const stoppingOwnMessage = busy === "message";
		stopRequestedRef.current = true;
		setStopping(true);
		try {
			await api.stopSession(selected);
			showNotice(t("sessions.stopRequested"));
			await Promise.all([sessions.reload(), messages.reload()]);
		} catch (error) {
			stopRequestedRef.current = false;
			showNotice(String(error), true);
		} finally {
			if (!stoppingOwnMessage) stopRequestedRef.current = false;
			setStopping(false);
		}
	}

  async function openCodex() {
    const threadID = selected?.native_session_id || selected?.session_id;
    if (!threadID) return;
    setBusy("open-codex");
    try {
      const result = await api.openCodexThread(threadID, selected?.target_id);
      if (!result.opened && result.command) await copy(result.command);
      showNotice(result.status_message || result.command || t("sessions.resumeReady"));
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  async function bindCodex() {
    const threadID = selected?.native_session_id || selected?.session_id;
    if (!threadID || !bindChannelID || !bindConversationID) return;
    setBusy("bind-codex");
    try {
      await api.bindChannelConversation(bindChannelID, bindConversationID, threadID, selected?.target_id);
      showNotice(t("sessions.channelBound"));
      await Promise.all([channelConversations.reload(), sessions.reload()]);
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  async function sendMessage() {
    const text = draft.trim();
    if (!selected?.can_chat || !selected.channel_id || !selected.conversation_id || !text || busy === "message") return;
    const sentAt = new Date().toISOString();
    setDraft("");
    setBusy("message");
    setOptimisticMessages([{ role: "user", content: text, timestamp: sentAt }]);
    try {
      const result = await api.sendSessionMessage(selected, text);
      if (result.answer) {
        setOptimisticMessages((current) => [
          ...current,
          { role: "assistant", content: result.answer, timestamp: new Date().toISOString() },
        ]);
      }
      await Promise.all([messages.reload(), sessions.reload(), pendingInteractions.reload()]);
      setOptimisticMessages([]);
      showNotice(t("sessions.messageSent"));
    } catch (error) {
      setDraft(text);
      setOptimisticMessages([]);
		if (stopRequestedRef.current) {
			showNotice(t("sessions.stopped"));
		} else {
			showNotice(String(error), true);
		}
    } finally {
		stopRequestedRef.current = false;
      setBusy("");
    }
  }

  function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void sendMessage();
    }
  }

  async function respondInteraction(interactionID: string, nonce: string, decision: string, questionIDs: string[]) {
    if (decision === "acceptForSession" && !window.confirm(t("sessions.allowSessionConfirm"))) return;
    setBusy(`interaction-${interactionID}`);
    try {
      const answers: Record<string, string[]> = {};
      for (const questionID of questionIDs) {
        answers[questionID] = [localAnswers[`${interactionID}:${questionID}`] ?? ""];
      }
      await api.respondChannelInteraction(interactionID, nonce, decision, answers, selected?.target_id);
      showNotice(t("sessions.interactionResolved"));
      await pendingInteractions.reload();
    } catch (error) {
      showNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="page-stack sessions-page">
      <section className="surface">
        <div className="surface-header sessions-toolbar">
          <div>
            <h2>{t("sessions.title")}</h2>
            <span className="muted">{t("sessions.resultCount", { count: filtered.length })}</span>
          </div>
          <div className="control-row">
            <select value={source} onChange={(event) => setSource(event.target.value)} aria-label={t("sessions.source")}>
              {SOURCES.map((item) => <option key={item.id} value={item.id}>{t(item.labelKey)}</option>)}
            </select>
            <select value={agentID} onChange={(event) => setAgentID(event.target.value)} aria-label={t("sessions.agent")}>
              <option value="">{t("sessions.allAgents")}</option>
              {availableAgents.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
            </select>
            <select value={channelID} onChange={(event) => setChannelID(event.target.value)} aria-label={t("sessions.channel")}>
              <option value="">{t("sessions.allChannels")}</option>
              {availableChannels.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
            </select>
            <select value={provider} onChange={(event) => setProvider(event.target.value)} aria-label={t("sessions.provider")}>
              {PROVIDERS.map((item) => <option key={item.id} value={item.id}>{t(item.labelKey)}</option>)}
            </select>
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("sessions.search")} />
            <button className="ghost-action" onClick={sessions.reload} title={t("sessions.refresh")}>
              <RefreshCw size={15} />
              {t("sessions.refresh")}
            </button>
          </div>
        </div>
        {sessions.error && <div className="surface-body error">{sessions.error}</div>}
        {notice && !selected && <div className={`surface-body session-notice${noticeError ? " error" : ""}`}>{notice}</div>}
        <div className="sessions-layout">
          <div className="session-list" role="list">
            {pagination.pageItems.map((session) => {
              const channel = session.channel_id ? channelByID.get(`${session.target_id || "local"}::${session.channel_id}`) : undefined;
				const sessionRunStatus = selected && keyOf(selected) === keyOf(session) && busy === "message"
					? "running"
					: session.run_status || "idle";
              return (
                <button
                  key={keyOf(session)}
                  className={selected && keyOf(selected) === keyOf(session) ? "active" : ""}
                  onClick={() => setSelectedID(keyOf(session))}
                >
                  <span className="session-list-icon">
                    {channel ? (
                      <ChannelAvatar channel={channel} />
                    ) : session.surface === "cli" ? (
                      <TerminalSquare size={18} />
                    ) : (
                      <Bot size={18} />
                    )}
                  </span>
                  <span className="session-main">
                    <span className="session-title-line">
                      <strong>{session.title || session.conversation_key || session.session_id}</strong>
                      <span className="session-meta">
                        <SessionStatusBadge status={sessionRunStatus} t={t} />
                        {session.origin === "channel" && <span className="pill accent">{t("sessions.channelSession")}</span>}
                        {session.channel_type && <span className="pill">{channelTypeLabel(session.channel_type)}</span>}
                        <span className="pill">{providerLabel(session.provider_id)}</span>
                        <TargetBadge target_id={session.target_id} target_name={session.target_name} />
                      </span>
                      <time className="session-list-time">{formatDate(session.last_active_at, language)}</time>
                    </span>
                  </span>
                </button>
              );
            })}
            {!sessions.error && filtered.length === 0 && (
              <div className="empty-state">{sessions.loading ? t("common.loading") : t("sessions.empty")}</div>
            )}
          </div>

          <CatalogPagination
            page={pagination.page}
            totalPages={pagination.totalPages}
            start={pagination.start}
            end={pagination.end}
            total={pagination.total}
            onChange={pagination.setPage}
          />
        </div>
      </section>

      <Drawer open={Boolean(selected)} onClose={closeDrawer} className="session-drawer">
          <div className="session-detail" role="dialog" aria-modal="true" aria-labelledby="session-drawer-title">
            {selected ? (
              <>
                <div className="detail-header">
                  <div>
                    <span className="session-eyebrow">
                      {selected.origin === "channel" ? t("sessions.channelSession") : t("sessions.localSession")}
                    </span>
					<div className="session-title-row">
						<h3 id="session-drawer-title">{selected.title || selected.conversation_key || selected.session_id}</h3>
						<SessionStatusBadge status={selectedRunStatus} t={t} />
					</div>
                  </div>
                  <div className="control-row">
                    {traceSessionID && (
                      <a
                        className="ghost-action"
                        href={`#observability/traces?session_id=${encodeURIComponent(traceSessionID)}`}
                        title={t("sessions.viewTraces")}
                      >
                        <Activity size={15} />
                        {t("sessions.viewTraces")}
                      </a>
                    )}
                    {selected.resume_command && (
                      <button className="ghost-action" onClick={() => copy(selected.resume_command || "")} title={t("sessions.copy")}>
                        <Clipboard size={15} />
                        {t("sessions.copy")}
                      </button>
                    )}
					{selectedCanStop && (
						<button
							className="ghost-action danger-action session-stop-action"
							disabled={stopping}
							onClick={stopConversation}
							title={t("sessions.stop")}
						>
							<Square size={14} fill="currentColor" />
							{stopping ? t("sessions.stopping") : t("sessions.stop")}
						</button>
					)}
                    {selected.surface !== "channel" && selected.available && (
                      <button className="action" disabled={busy === "resume"} onClick={() => resume(false)} title={t("sessions.resume")}>
                        <Play size={15} />
                        {t("sessions.resume")}
                      </button>
                    )}
                    {selected.provider_id === "codex" && (selected.native_session_id || selected.surface === "app-server") && (
                      <button className="ghost-action" disabled={busy === "open-codex"} onClick={openCodex} title={t("sessions.openCodex")}>
                        <ExternalLink size={15} />
                        {t("sessions.openCodex")}
                      </button>
                    )}
                    {selected.file_backed && (
                      <button className="ghost-action" disabled={busy === "terminal"} onClick={() => resume(true)} title={t("sessions.terminal")}>
                        <TerminalSquare size={15} />
                        {t("sessions.terminal")}
                      </button>
                    )}
                    {selected.terminal_backend && (
                      <button
                        className={terminalVisible ? "action" : "ghost-action"}
                        onClick={() => setTerminalVisible((current) => !current)}
                        title={t("sessions.webTerminal")}
                      >
                        <TerminalSquare size={15} />
                        {t("sessions.webTerminal")}
                      </button>
                    )}
                    {selected.file_backed && (
                      <button className="ghost-action danger-action" disabled={busy === "delete"} onClick={remove} title={t("common.delete")}>
                        <Trash2 size={15} />
                        {t("common.delete")}
                      </button>
                    )}
                    <button className="ghost-action icon-action" onClick={closeDrawer} title={t("common.close")} aria-label={t("common.close")}>
                      <X size={16} />
                    </button>
                  </div>
                </div>

                {notice && <div className={`session-notice${noticeError ? " error" : ""}`}>{notice}</div>}

                <div className="session-facts">
                  {selected.agent_name && <span><Bot size={13} /> {selected.agent_name}</span>}
                  {selected.channel_name && <span><MessageSquareText size={13} /> {selected.channel_name}</span>}
                  {selected.channel_type && <span>{channelTypeLabel(selected.channel_type)}</span>}
                  <span>{providerLabel(selected.provider_id)} · {surfaceLabel(selected.surface, t)}</span>
                  <TargetBadge target_id={selected.target_id} target_name={selected.target_name} />
                  <span>{selected.message_count}{selected.messages_partial ? "+" : ""} {t("sessions.messages")}</span>
                  <span>{formatDate(selected.last_active_at || selected.created_at, language)}</span>
                </div>

                <div className="session-chat">
                  {terminalVisible && selected.terminal_backend && (
                    <section className="session-terminal" aria-label={t("sessions.webTerminal")}>
                      <header>
                        <div>
                          <strong>{t("sessions.webTerminal")}</strong>
                          <span>{terminal.data?.info.backend || selected.terminal_backend}</span>
                        </div>
                        <div className="control-row">
                          <button className="ghost-action" disabled={terminalBusy} onClick={interruptTerminal}>Ctrl+C</button>
                          {terminal.data?.info.attach_command && (
                            <button className="ghost-action" onClick={() => copy(terminal.data?.info.attach_command || "")}>
                              <Clipboard size={14} /> {t("sessions.copyAttach")}
                            </button>
                          )}
                          <button className="ghost-action" onClick={terminal.reload}>
                            <RefreshCw size={14} /> {t("sessions.refresh")}
                          </button>
                        </div>
                      </header>
                      {terminal.error && <div className="error">{terminal.error}</div>}
                      <pre className="session-terminal-screen">
                        {terminal.data?.snapshot || (terminal.loading ? t("common.loading") : t("sessions.terminalUnavailable"))}
                      </pre>
                      <div className="session-terminal-composer">
                        <textarea
                          rows={2}
                          value={terminalDraft}
                          onChange={(event) => setTerminalDraft(event.target.value)}
                          onKeyDown={(event) => {
                            if (event.key === "Enter" && !event.shiftKey) {
                              event.preventDefault();
                              void sendTerminalInput();
                            }
                          }}
                          placeholder={t("sessions.terminalInputPlaceholder")}
                        />
                        <button className="action" disabled={!terminalDraft || terminalBusy} onClick={sendTerminalInput}>
                          <Send size={14} /> {t("sessions.send")}
                        </button>
                      </div>
                    </section>
                  )}
                  <div className="transcript" ref={transcriptRef} aria-live="polite">
                    {messages.loading && !messages.data && <div className="empty-state compact">{t("common.loading")}</div>}
                    {messages.error && <div className="error">{messages.error}</div>}
                    {transcriptMessages.map((message, index) => (
                      message.role === "tool" || message.tool_name || message.tool_input || message.tool_output ? (
                        <SessionToolMessage
                          key={message.tool_call_id || `${message.timestamp ?? ""}-${index}`}
                          language={language}
                          message={message}
                          onCopy={copy}
                          t={t}
                        />
                      ) : (
                        <article className={`message ${message.role}`} key={`${message.timestamp ?? ""}-${index}`}>
                          <header>
                            <span>{roleLabel(message.role, t)}</span>
                            {message.kind && message.kind !== "message" && <span className="muted">{message.kind}</span>}
                            <time>{formatDate(message.timestamp, language)}</time>
                          </header>
                          <div className="message-content">
                            <ReactMarkdown remarkPlugins={[remarkGfm]}>{message.content}</ReactMarkdown>
                          </div>
                        </article>
                      )
                    ))}
                    {!messages.loading && !messages.error && transcriptMessages.length === 0 && (
                      <div className="empty-state">{t("sessions.noMessages")}</div>
                    )}
                    {busy === "message" && (
                      <div className="session-typing">
                        <span />
                        <span />
                        <span />
                        {t("sessions.agentThinking")}
                      </div>
                    )}
                  </div>

                  {selected.can_chat ? (
                    <div className="session-composer">
                      <textarea
                        value={draft}
                        onChange={(event) => setDraft(event.target.value)}
                        onKeyDown={handleComposerKeyDown}
                        placeholder={t("sessions.messagePlaceholder")}
                        rows={3}
                        disabled={busy === "message"}
                      />
                      <div className="session-composer-footer">
                        <span>{t("sessions.consoleOnlyHint")}</span>
                        <button className="action" onClick={sendMessage} disabled={!draft.trim() || busy === "message"}>
                          <Send size={15} />
                          {busy === "message" ? t("sessions.sending") : t("sessions.send")}
                        </button>
                      </div>
                    </div>
                  ) : (
                    <div className="session-chat-unavailable">
                      <MessageSquareText size={16} />
                      <span>{selected.origin === "channel" ? t("sessions.channelOffline") : t("sessions.localReadOnly")}</span>
                    </div>
                  )}
                </div>

                {(selected.provider_id === "codex" || traceTool || pendingInteractions.data?.length) && (
                  <details className="session-advanced">
                    <summary>{t("sessions.advanced")}</summary>
                    {selected.provider_id === "codex" && selected.surface === "desktop" && selected.origin !== "channel" && (
                      <div className="session-channel-control">
                        <div className="control-row session-channel-bind">
                          <Link2 size={15} />
                          <select
                            value={bindChannelID}
                            onChange={(event) => {
                              setBindChannelID(event.target.value);
                              setBindConversationID("");
                            }}
                          >
                            <option value="">{t("sessions.selectChannel")}</option>
                            {(channels.data ?? [])
                              .filter((channel) => channel.config?.codex_control_enabled === "true")
                              .map((channel) => (
                                <option key={channel.id} value={channel.id}>{channel.bot_name || channel.name}</option>
                              ))}
                          </select>
                          <select value={bindConversationID} onChange={(event) => setBindConversationID(event.target.value)}>
                            <option value="">{t("sessions.selectConversation")}</option>
                            {(channelConversations.data ?? []).map((conversation) => (
                              <option key={conversation.id} value={conversation.id}>
                                {conversation.title || conversation.conversation_key}
                              </option>
                            ))}
                          </select>
                          <button className="ghost-action" disabled={!bindConversationID || busy === "bind-codex"} onClick={bindCodex}>
                            {t("sessions.bindChannel")}
                          </button>
                        </div>
                      </div>
                    )}

                    {(pendingInteractions.data ?? []).map((interaction) => {
                      const questions = interaction.request.questions ?? [];
                      return (
                        <div className="session-interaction" key={interaction.id}>
                          <strong>{interaction.request.title || t("sessions.pendingInteraction")}</strong>
                          {interaction.request.command && <pre>{interaction.request.command}</pre>}
                          {interaction.request.reason && <p>{interaction.request.reason}</p>}
                          {questions.map((question) => (
                            <label className="field" key={question.id}>
                              <span>{question.header || question.question}</span>
                              {question.options?.length ? (
                                <select
                                  value={localAnswers[`${interaction.id}:${question.id}`] ?? ""}
                                  onChange={(event) => setLocalAnswers((current) => ({
                                    ...current,
                                    [`${interaction.id}:${question.id}`]: event.target.value,
                                  }))}
                                >
                                  <option value="">{question.question}</option>
                                  {question.options.map((option) => <option key={option.label} value={option.label}>{option.label}</option>)}
                                </select>
                              ) : (
                                <input
                                  type={question.secret ? "password" : "text"}
                                  autoComplete={question.secret ? "new-password" : undefined}
                                  value={localAnswers[`${interaction.id}:${question.id}`] ?? ""}
                                  onChange={(event) => setLocalAnswers((current) => ({
                                    ...current,
                                    [`${interaction.id}:${question.id}`]: event.target.value,
                                  }))}
                                />
                              )}
                            </label>
                          ))}
                          <div className="control-row">
                            {questions.length > 0 ? (
                              <button
                                className="action"
                                disabled={busy === `interaction-${interaction.id}`}
                                onClick={() => respondInteraction(interaction.id, interaction.nonce, "", questions.map((question) => question.id))}
                              >
                                {t("common.save")}
                              </button>
                            ) : (
                              <>
                                <button
                                  className="action"
                                  disabled={busy === `interaction-${interaction.id}`}
                                  onClick={() => respondInteraction(interaction.id, interaction.nonce, "accept", [])}
                                >
                                  {t("sessions.allowOnce")}
                                </button>
                                {!interaction.request.high_risk && (
                                  <button
                                    className="ghost-action"
                                    disabled={busy === `interaction-${interaction.id}`}
                                    onClick={() => respondInteraction(interaction.id, interaction.nonce, "acceptForSession", [])}
                                  >
                                    {t("sessions.allowSession")}
                                  </button>
                                )}
                                <button
                                  className="ghost-action danger-action"
                                  disabled={busy === `interaction-${interaction.id}`}
                                  onClick={() => respondInteraction(interaction.id, interaction.nonce, "decline", [])}
                                >
                                  {t("sessions.decline")}
                                </button>
                              </>
                            )}
                          </div>
                        </div>
                      );
                    })}

                    {traceTool && (
                      <RouteTracePanel
                        error={sessionTraces.error || recentTraces.error}
                        fallback={routeTraceFallback}
                        language={language}
                        loading={routeTraceLoading}
                        traces={routeTraces}
                        t={t}
                      />
                    )}
                  </details>
                )}
              </>
            ) : (
              <div className="empty-state">{sessions.loading ? t("common.loading") : t("sessions.empty")}</div>
            )}
          </div>
      </Drawer>
    </div>
  );
}

function keyOf(session: AgentSession) {
  const target = session.target_id || "local";
  if (session.conversation_id) return `${target}:conversation:${session.conversation_id}`;
  return `${target}:${session.provider_id}:${session.surface}:${session.session_id}:${session.source_path ?? ""}`;
}

function SessionStatusBadge({ status, t }: { status?: string; t: (key: string) => string }) {
	const normalized = normalizeSessionRunStatus(status);
	return (
		<span className={`status-badge session-status-badge status-${normalized} ${sessionStatusTone(normalized)}`}>
			<span className="status-dot" />
			{sessionStatusLabel(normalized, t)}
		</span>
	);
}

function normalizeSessionRunStatus(status?: string) {
	return (status || "idle").trim().toLowerCase().replace(/-/g, "_");
}

function sessionStatusTone(status: string) {
	if (["running", "succeeded"].includes(status)) return "success";
	if (["queued", "waiting_input", "stopping", "interrupted", "cancelled", "canceled"].includes(status)) return "warning";
	if (["failed", "error"].includes(status)) return "danger";
	return "";
}

function sessionStatusLabel(status: string, t: (key: string) => string) {
	switch (status) {
		case "running": return t("sessions.statusRunning");
		case "queued": return t("sessions.statusQueued");
		case "waiting_input": return t("sessions.statusWaitingInput");
		case "stopping": return t("sessions.statusStopping");
		case "succeeded": return t("sessions.statusSucceeded");
		case "failed":
		case "error": return t("sessions.statusFailed");
		case "cancelled":
		case "canceled": return t("sessions.statusCancelled");
		case "interrupted": return t("sessions.statusInterrupted");
		case "offline": return t("sessions.statusOffline");
		default: return t("sessions.statusIdle");
	}
}

function routeToolForSession(session: AgentSession) {
  if (session.provider_id === "claude" || session.provider_id === "claudecode") return "claudecode";
  if (session.provider_id === "codex") return "codex";
  return session.provider_id;
}

function normalizeProvider(provider: string) {
  return provider === "claude" ? "claudecode" : provider;
}

function providerLabel(provider: string) {
  if (provider === "claude" || provider === "claudecode") return "Claude Code";
  if (provider === "codex") return "Codex";
  return provider || "Agent";
}

function channelTypeLabel(channelType: string) {
  if (channelType === "feishu" || channelType === "lark") return "Feishu";
  if (channelType === "dingtalk") return "DingTalk";
  if (channelType === "telegram") return "Telegram";
  if (channelType === "discord") return "Discord";
  if (channelType === "slack") return "Slack";
  if (channelType === "webhook") return "Webhook";
  return channelType;
}

function surfaceLabel(surface: string, t: (key: string) => string) {
  if (surface === "cli") return t("sessions.cli");
  if (surface === "desktop") return t("sessions.desktopApp");
  if (surface === "app-server") return t("sessions.appServer");
  if (surface === "channel") return t("sessions.channelSession");
  return surface;
}

function roleLabel(role: string, t: (key: string) => string) {
  if (role === "user") return t("sessions.you");
  if (role === "assistant") return t("sessions.agentReply");
  return role;
}

function SessionToolMessage({
  language,
  message,
  onCopy,
  t,
}: {
  language: string;
  message: SessionMessage;
  onCopy: (text: string) => Promise<void>;
  t: (key: string) => string;
}) {
  const [open, setOpen] = useState(false);
  const tone = toolStatusTone(message.tool_status);
  const StatusIcon = tone === "success" ? CheckCircle2 : tone === "error" ? XCircle : Clock3;
  const name = message.tool_name || message.kind || t("sessions.toolCall");
  return (
    <article className={`tool-message ${tone}${open ? " open" : ""}`}>
      <button
        aria-expanded={open}
        className="tool-message-summary"
        onClick={() => setOpen((current) => !current)}
        type="button"
      >
        <span className="tool-message-summary-grid">
          <span className="tool-message-heading">
            <span className="tool-message-icon"><Wrench size={16} /></span>
            <span>
              <strong>{name}</strong>
              <small>{t("sessions.toolCall")}</small>
            </span>
          </span>
          <span className={`tool-status ${tone}`}>
            <StatusIcon size={13} />
            {toolStatusLabel(message.tool_status, t)}
          </span>
          <time>{formatDate(message.timestamp, language)}</time>
          <ChevronDown className="tool-message-chevron" size={16} />
        </span>
      </button>
      {open && (
        <div className="tool-message-body">
          {message.tool_input && (
            <ToolPayload title={t("sessions.toolInput")} value={message.tool_input} onCopy={onCopy} copyLabel={t("sessions.copy")} />
          )}
          {message.tool_output && (
            <ToolPayload title={t("sessions.toolOutput")} value={message.tool_output} onCopy={onCopy} copyLabel={t("sessions.copy")} />
          )}
          {!message.tool_input && !message.tool_output && message.content && (
            <ToolPayload title={t("sessions.toolDetails")} value={message.content} onCopy={onCopy} copyLabel={t("sessions.copy")} />
          )}
          {message.tool_call_id && <span className="tool-call-id">ID: {message.tool_call_id}</span>}
        </div>
      )}
    </article>
  );
}

function ToolPayload({
  copyLabel,
  onCopy,
  title,
  value,
}: {
  copyLabel: string;
  onCopy: (text: string) => Promise<void>;
  title: string;
  value: string;
}) {
  return (
    <section className="tool-payload">
      <header>
        <strong>{title}</strong>
        <button className="tool-copy" onClick={() => void onCopy(value)} title={copyLabel} type="button">
          <Clipboard size={13} />
          {copyLabel}
        </button>
      </header>
      <pre><code>{value}</code></pre>
    </section>
  );
}

function toolStatusTone(status = "") {
  const normalized = status.toLowerCase().replace(/_/g, "");
  if (["completed", "success", "succeeded", "done"].includes(normalized)) return "success";
  if (["failed", "error", "declined", "cancelled", "canceled"].includes(normalized)) return "error";
  return "pending";
}

function toolStatusLabel(status: string | undefined, t: (key: string) => string) {
  const tone = toolStatusTone(status);
  if (tone === "success") return t("sessions.toolCompleted");
  if (tone === "error") return t("sessions.toolFailed");
  return t("sessions.toolCalled");
}

function RouteTracePanel({
  error,
  fallback,
  language,
  loading,
  traces,
  t,
}: {
  error: string | null;
  fallback: boolean;
  language: string;
  loading: boolean;
  traces: ProxyTrace[];
  t: (key: string) => string;
}) {
  return (
    <section className="route-trace-panel">
      <div className="route-trace-head">
        <strong>{t("sessions.routeTrace")}</strong>
        {fallback && <span className="muted">{t("sessions.routeTraceFallback")}</span>}
      </div>
      {error && <div className="error">{error}</div>}
      {loading && <div className="muted">{t("common.loading")}</div>}
      {!error && !loading && traces.length === 0 && <div className="empty-state compact">{t("sessions.noRouteTrace")}</div>}
      {!loading && traces.length > 0 && (
        <div className="route-trace-list">
          {traces.map((trace) => (
            <article className="route-trace-item" key={trace.id}>
              <div>
                <strong>
                  {toolLabel(trace.tool)} -&gt; {trace.provider_name || trace.provider_id || "-"}
                </strong>
                <span className="mono">
                  {trace.client_model || "-"} -&gt; {trace.upstream_model || "-"}
                </span>
              </div>
              <div>
                <span className={trace.success ? "status-badge success" : "status-badge warning"}>
                  <span className="status-dot" />
                  {trace.success ? t("common.healthy") : t("common.degraded")}
                </span>
                <span className="muted">
                  {protocolLabel(trace.client_protocol)} -&gt; {protocolLabel(trace.upstream_protocol)}
                </span>
                <time>{formatDate(trace.timestamp, language)}</time>
              </div>
              {trace.error && <small className="error">{trace.error}</small>}
            </article>
          ))}
        </div>
      )}
    </section>
  );
}

function toolLabel(tool: string) {
  if (tool === "claudecode") return "Claude Code CLI";
  if (tool === "claude-desktop") return "Claude Desktop";
  if (tool === "codex") return "Codex CLI";
  if (tool === "gemini") return "Gemini CLI";
  return tool || "-";
}

function protocolLabel(protocol: string) {
  if (protocol === "anthropic") return "Anthropic";
  if (protocol === "openai_chat") return "OpenAI Chat";
  if (protocol === "openai_responses") return "OpenAI Responses";
  if (protocol === "gemini") return "Gemini";
  return protocol || "-";
}

function formatDate(value: string | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString(language === "zh" ? "zh-CN" : "en-US");
}
