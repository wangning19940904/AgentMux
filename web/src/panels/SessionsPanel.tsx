import { useEffect, useMemo, useState } from "react";
import { Clipboard, Play, RefreshCw, TerminalSquare, Trash2 } from "lucide-react";
import { AgentSession, api } from "../api";
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
  const sessions = useAsync(() => api.sessions(provider, surface), [provider, surface]);

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

function formatDate(value: string | undefined, language: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString(language === "zh" ? "zh-CN" : "en-US");
}
