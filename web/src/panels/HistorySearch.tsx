import { useState } from "react";
import { api } from "../api";
import type { HistoryMatch, HistorySearchRequest, HistorySearchResult } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

// historyRequest turns the form into a request: one wording per non-empty
// line, and a time hint only when both days are given. Days are local and
// cover the whole of the second one.
export function historyRequest(agentID: string, text: string, from: string, to: string): HistorySearchRequest {
  const queries = text.split("\n").map((line) => line.trim()).filter(Boolean);
  const request: HistorySearchRequest = { queries };
  if (agentID) request.agent_id = agentID;
  if (from && to) {
    const start = new Date(`${from}T00:00:00`);
    const end = new Date(`${to}T23:59:59.999`);
    if (!Number.isNaN(start.getTime()) && !Number.isNaN(end.getTime()) && start <= end) {
      request.around = { from: start.toISOString(), to: end.toISOString() };
    }
  }
  return request;
}

// HistorySearch is the Console's search over memory and past turns, for one
// agent or all of them.
export function HistorySearch() {
  const { t, language } = useI18n();
  const agents = useAsync(() => api.agentInstances(), []);
  const [agentID, setAgentID] = useState("");
  const [text, setText] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<HistorySearchResult | null>(null);
  const request = historyRequest(agentID, text, from, to);
  const agentNames = new Map((agents.data ?? []).map((agent) => [agent.id, agent.name]));

  async function search() {
    if (request.queries.length === 0) return;
    setBusy(true);
    setError("");
    try {
      setResult(await api.searchHistory(request));
    } catch (err) {
      setResult(null);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const when = (time: string) => new Date(time).toLocaleString(language === "zh" ? "zh-CN" : "en-US");
  const list = (title: string, hint: string, matches: HistoryMatch[]) => (
    <div className="history-results">
      <h3>{title} <span className="muted">· {hint}</span></h3>
      {matches.length === 0 && <p className="empty-state">{t("history.none")}</p>}
      <ol>
        {matches.map((match) => (
          <li key={match.id}>
            <div className="muted">
              <span className="pill">{t(`history.kind.${match.kind}`)}</span>{" "}
              {when(match.time)}
              {match.agent_id && ` · ${agentNames.get(match.agent_id) ?? match.agent_id}`}
              {match.memory_scope && ` · ${match.memory_scope}`}
              {match.user_name && ` · ${match.user_name}`}
              {request.queries.length > 1 && ` · ${t("history.matchedBy", { count: match.matched_queries.length })}`}
              {match.in_time_range && <> · <strong>{t("history.inRange")}</strong></>}
            </div>
            <div>{match.snippet}</div>
          </li>
        ))}
      </ol>
    </div>
  );

  return (
    <section className="surface">
      <div className="surface-header">
        <div>
          <h2>{t("history.title")}</h2>
          <p className="subtle-copy">{t("history.subtitle")}</p>
        </div>
      </div>
      <form className="inline-editor" onSubmit={(event) => { event.preventDefault(); void search(); }}>
        <div className="control-row">
          <label className="field">
            <span>{t("history.agent")}</span>
            <select value={agentID} onChange={(event) => setAgentID(event.target.value)}>
              <option value="">{t("history.allAgents")}</option>
              {(agents.data ?? []).map((agent) => <option key={agent.id} value={agent.id}>{agent.name || agent.id}</option>)}
            </select>
          </label>
          <label className="field">
            <span>{t("history.from")}</span>
            <input type="date" value={from} onChange={(event) => setFrom(event.target.value)} />
          </label>
          <label className="field">
            <span>{t("history.to")}</span>
            <input type="date" value={to} onChange={(event) => setTo(event.target.value)} />
          </label>
        </div>
        <label className="field">
          <span>{t("history.queries")}</span>
          <textarea rows={3} value={text} placeholder={t("history.queriesPlaceholder")} onChange={(event) => setText(event.target.value)} />
        </label>
        {(from || to) && <p className="subtle-copy">{t("history.timeHint")}</p>}
        <div className="control-row">
          <button className="action" type="submit" disabled={busy || request.queries.length === 0}>
            {busy ? t("history.searching") : t("history.search")}
          </button>
        </div>
      </form>
      {error && <div className="surface-body error">{error}</div>}
      {result && (
        <div className="surface-body">
          {list(t("history.memory"), t("history.memoryHint"), result.memory ?? [])}
          {list(t("history.originals"), t("history.originalsHint"), result.originals ?? [])}
          {(result.notes ?? []).map((note) => <p key={note} className="subtle-copy">{note}</p>)}
        </div>
      )}
    </section>
  );
}
