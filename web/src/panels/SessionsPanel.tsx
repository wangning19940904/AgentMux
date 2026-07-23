import { useEffect, useMemo, useState } from "react";
import { Activity, Clipboard, ExternalLink, Link2, Play, RefreshCw, TerminalSquare, Trash2 } from "lucide-react";
import { AgentSession, ProxyTrace, api } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

const PROVIDERS = [
  { id: "", labelKey: "sessions.allProviders" },
  { id: "claudecode", labelKey: "sessions.claudeCode" },
  { id: "codex", labelKey: "sessions.codex" },
];

const SURFACES = [
  { id: "", labelKey: "sessions.allSurfaces" },
  { id: "cli", labelKey: "sessions.cli" },
  { id: "app-server", labelKey: "sessions.desktopApp" },
];

export function SessionsPanel() {
  const { t, language } = useI18n();
  const [provider, setProvider] = useState("");
  const [surface, setSurface] = useState("");
  const [query, setQuery] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [bindChannelID, setBindChannelID] = useState("");
  const [bindConversationID, setBindConversationID] = useState("");
  const [localAnswers, setLocalAnswers] = useState<Record<string, string>>({});
  const sessions = useAsync(() => api.sessions(provider, surface), [provider, surface]);
  const channels = useAsync(() => api.channels(), []);
  const channelConversations = useAsync(
    () => (bindChannelID ? api.channelConversations(bindChannelID) : Promise.resolve([])),
    [bindChannelID]
  );
  const pendingInteractions = useAsync(
    () => (bindChannelID ? api.channelInteractions(bindChannelID, bindConversationID) : Promise.resolve([])),
    [bindChannelID, bindConversationID]
  );

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (sessions.data ?? []).filter((session) => {
      if (!q) return true;
      return [
        session.title,
        session.summary,
        session.project_dir,
        session.session_id,
        session.provider_id,
        session.surface,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(q));
    });
  }, [sessions.data, query]);

  const selected = useMemo(
    () => filtered.find((session) => keyOf(session) === selectedID) ?? filtered[0],
    [filtered, selectedID]
  );
  const messages = useAsync(() => (selected ? api.sessionMessages(selected) : Promise.resolve([])), [selected?.session_id, selected?.source_path, selected?.surface]);
  const traceTool = selected ? routeToolForSession(selected) : "";
  const sessionTraces = useAsync(
    () => (selected && traceTool ? api.proxyTraces({ tool: traceTool, sessionID: selected.session_id, limit: 20 }) : Promise.resolve([])),
    [traceTool, selected?.session_id]
  );
  const recentTraces = useAsync(
    () => (selected && traceTool ? api.proxyTraces({ tool: traceTool, limit: 10 }) : Promise.resolve([])),
    [traceTool, selected?.session_id]
  );
  const selectedTraces = sessionTraces.data ?? [];
  const fallbackTraces = recentTraces.data ?? [];
  const routeTraces = selectedTraces.length > 0 ? selectedTraces : fallbackTraces;
  const routeTraceFallback = !sessionTraces.loading && selectedTraces.length === 0 && fallbackTraces.length > 0;
  const routeTraceLoading = sessionTraces.loading || (!sessionTraces.loading && selectedTraces.length === 0 && recentTraces.loading);

  useEffect(() => {
    if (selected) setSelectedID(keyOf(selected));
  }, [selected?.session_id, selected?.source_path]);

  async function copy(text: string) {
    if (!text) return;
    await navigator.clipboard.writeText(text);
    setNotice(t("sessions.copied"));
  }

  async function resume(openTerminal: boolean) {
    if (!selected) return;
    setBusy(openTerminal ? "terminal" : "resume");
    try {
      const res = await api.resumeSession(selected, openTerminal);
      if (res.command) await copy(res.command);
      setNotice(res.status_message || (res.thread_id ? `${t("sessions.thread")} ${res.thread_id}` : t("sessions.resumeReady")));
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
      sessions.reload();
      setNotice(t("sessions.deleted"));
    } finally {
      setBusy("");
    }
  }

  async function openCodex() {
    if (!selected?.session_id) return;
    setBusy("open-codex");
    try {
      const result = await api.openCodexThread(selected.session_id);
      if (!result.opened && result.command) await copy(result.command);
      setNotice(result.status_message || result.command || t("sessions.resumeReady"));
    } finally {
      setBusy("");
    }
  }

  async function bindCodex() {
    if (!selected?.session_id || !bindChannelID || !bindConversationID) return;
    setBusy("bind-codex");
    try {
      await api.bindChannelConversation(bindChannelID, bindConversationID, selected.session_id);
      setNotice(t("sessions.channelBound"));
      channelConversations.reload();
    } finally {
      setBusy("");
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
      await api.respondChannelInteraction(interactionID, nonce, decision, answers);
      setNotice(t("sessions.interactionResolved"));
      pendingInteractions.reload();
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="page-stack">
      <p className="subtle-copy">{t("sessions.subtitle")}</p>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("sessions.title")}</h2>
          <div className="control-row">
            <select value={provider} onChange={(event) => setProvider(event.target.value)}>
              {PROVIDERS.map((item) => (
                <option key={item.id} value={item.id}>
                  {t(item.labelKey)}
                </option>
              ))}
            </select>
            <select value={surface} onChange={(event) => setSurface(event.target.value)}>
              {SURFACES.map((item) => (
                <option key={item.id} value={item.id}>
                  {t(item.labelKey)}
                </option>
              ))}
            </select>
            <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("sessions.search")} />
            <button className="ghost-action" onClick={sessions.reload} title={t("sessions.refresh")}>
              <RefreshCw size={15} />
              {t("sessions.refresh")}
            </button>
          </div>
        </div>
        {sessions.error && <div className="surface-body error">{sessions.error}</div>}
        {notice && <div className="surface-body session-notice">{notice}</div>}
        <div className="sessions-layout">
          <div className="session-list" role="list">
            {filtered.map((session) => (
              <button
                key={keyOf(session)}
                className={selected && keyOf(selected) === keyOf(session) ? "active" : ""}
                onClick={() => setSelectedID(keyOf(session))}
              >
                <span className="session-main">
                  <strong>{session.title || session.session_id}</strong>
                  <span>{session.project_dir || session.status_message || session.session_id}</span>
                </span>
                <span className="session-meta">
                  <span className="pill">{session.provider_id}</span>
                  <span className="pill">{session.surface}</span>
                  <span className="muted">{formatDate(session.last_active_at, language)}</span>
                </span>
              </button>
            ))}
            {!sessions.error && filtered.length === 0 && <div className="empty-state">{t("sessions.empty")}</div>}
          </div>

          <div className="session-detail">
            {selected ? (
              <>
                <div className="detail-header">
                  <div>
                    <h3>{selected.title || selected.session_id}</h3>
                    <p className="muted mono">{selected.session_id}</p>
                  </div>
                  <div className="control-row">
                    <a
                      className="ghost-action"
                      href={`#observability/traces?session_id=${encodeURIComponent(selected.session_id)}`}
                      title={t("sessions.viewTraces")}
                    >
                      <Activity size={15} />
                      {t("sessions.viewTraces")}
                    </a>
                    {selected.resume_command && (
                      <button className="ghost-action" onClick={() => copy(selected.resume_command || "")} title={t("sessions.copy")}>
                        <Clipboard size={15} />
                        {t("sessions.copy")}
                      </button>
                    )}
                    <button className="action" disabled={busy === "resume"} onClick={() => resume(false)} title={t("sessions.resume")}>
                      <Play size={15} />
                      {t("sessions.resume")}
                    </button>
                    {selected.provider_id === "codex" && (
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
                    {selected.file_backed && (
                      <button className="ghost-action danger-action" disabled={busy === "delete"} onClick={remove} title={t("common.delete")}>
                        <Trash2 size={15} />
                        {t("common.delete")}
                      </button>
                    )}
                  </div>
                </div>

                <div className="session-facts">
                  <span>{selected.project_dir || t("sessions.noProject")}</span>
                  <span>{formatDate(selected.created_at, language)}</span>
                  <span>{selected.message_count}{selected.messages_partial ? "+" : ""} {t("sessions.messages")}</span>
                </div>

                {selected.provider_id === "codex" && selected.surface === "app-server" && (
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
                      <button
                        className="ghost-action"
                        disabled={!bindConversationID || busy === "bind-codex"}
                        onClick={bindCodex}
                      >
                        {t("sessions.bindChannel")}
                      </button>
                    </div>
                    {bindChannelID && (
                      <div className="session-conversation-list">
                        {(channelConversations.data ?? []).map((conversation) => (
                          <div className="session-conversation-row" key={conversation.id}>
                            <span>
                              <strong>{conversation.thread_title || conversation.title || conversation.conversation_key}</strong>
                              <small>{conversation.conversation_key}</small>
                            </span>
                            <span className="mono">{conversation.native_session_id || t("sessions.unboundThread")}</span>
                            <span className="pill">{conversation.active_task?.status || t("sessions.idle")}</span>
                            <span>{t("sessions.queued")}: {conversation.queued_tasks}</span>
                            {conversation.controller_id && <span>{t("sessions.controller")}: {conversation.controller_id}</span>}
                          </div>
                        ))}
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
                  </div>
                )}

                <RouteTracePanel
                  error={sessionTraces.error || recentTraces.error}
                  fallback={routeTraceFallback}
                  language={language}
                  loading={routeTraceLoading}
                  traces={routeTraces}
                  t={t}
                />

                <div className="transcript">
                  {messages.error && <div className="error">{messages.error}</div>}
                  {(messages.data ?? []).map((message, index) => (
                    <article className={`message ${message.role}`} key={`${message.timestamp ?? ""}-${index}`}>
                      <header>
                        <span>{message.role}</span>
                        {message.kind && <span className="muted">{message.kind}</span>}
                        <time>{formatDate(message.timestamp, language)}</time>
                      </header>
                      <p>{message.content}</p>
                    </article>
                  ))}
                  {!messages.error && messages.data?.length === 0 && <div className="empty-state">{t("sessions.noMessages")}</div>}
                </div>
              </>
            ) : (
              <div className="empty-state">{t("sessions.empty")}</div>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}

function keyOf(session: AgentSession) {
  return `${session.provider_id}:${session.surface}:${session.session_id}:${session.source_path ?? ""}`;
}

function routeToolForSession(session: AgentSession) {
  if (session.provider_id === "claude" || session.provider_id === "claudecode") return "claudecode";
  if (session.provider_id === "codex") return "codex";
  return session.provider_id;
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
