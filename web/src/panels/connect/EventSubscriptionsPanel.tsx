import { useState } from "react";
import {
  api,
  fleetMode,
  type EventDelivery,
  type EventSubscription,
  type EventSubscriptionInput,
} from "../../api";
import { useAsync } from "../../useAsync";
import { usePolling } from "../../hooks/usePolling";
import { useI18n } from "../../i18n";

const FILTERS = ["chat_id", "file_token", "table_id", "agent_id"] as const;
export function splitEventValues(value: string): string[] {
  return [
    ...new Set(
      value
        .split(/[,\n]/)
        .map((v) => v.trim())
        .filter(Boolean),
    ),
  ];
}
export function EventSubscriptionsPanel() {
  const { t } = useI18n();
  if (fleetMode())
    return (
      <section className="surface">
        <div className="surface-body">
          <h2>{t("nav.subscriptions")}</h2>
          <p>{t("events.chooseMachine")}</p>
        </div>
      </section>
    );
  return <Subscriptions />;
}
function Subscriptions() {
  const { t } = useI18n();
  const sources = useAsync(() => api.eventSources(), []);
  const subscriptions = useAsync(() => api.eventSubscriptions(), []);
  const [subscriptionID, setSubscriptionID] = useState("");
  const [status, setStatus] = useState("");
  const [offset, setOffset] = useState(0);
  const deliveries = useAsync(
    () => api.eventDeliveries(subscriptionID, status, offset),
    [subscriptionID, status, offset],
  );
  const [draft, setDraft] = useState<EventSubscriptionInput | null>(null);
  const [typesText, setTypesText] = useState("");
  const [filterText, setFilterText] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [noticeError, setNoticeError] = useState(false);
  const [secret, setSecret] = useState("");
  const [detail, setDetail] = useState<EventDelivery | null>(null);
  const refresh = () =>
    Promise.all([
      sources.reload(),
      subscriptions.reload(),
      deliveries.reload(),
    ]);
  usePolling(() => {
    void refresh();
  }, 10_000);
  async function run(action: () => Promise<unknown>) {
    setBusy(true);
    setNotice("");
    setNoticeError(false);
    try {
      await action();
      await refresh();
    } catch (error) {
      setNoticeError(true);
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  }
  function edit(sub?: EventSubscription) {
    const next: EventSubscriptionInput = sub
      ? {
          ...sub,
          source_refs: [...sub.source_refs],
          filters: { ...sub.filters },
        }
      : {
          key: "",
          name: "",
          source: "feishu",
          source_refs: [],
          event_types: ["drive.file.bitable_record_changed_v1"],
          callback_url:
            "http://127.0.0.1:8000/api/v1/integrations/agentmux/events",
          paused: false,
        };
    setDraft(next);
    setTypesText(next.event_types.join("\n"));
    setFilterText(
      Object.fromEntries(
        FILTERS.map((key) => [key, (next.filters?.[key] ?? []).join(", ")]),
      ),
    );
    setSecret("");
  }
  const visibleSources = (sources.data ?? []).filter(
    (item) => !draft || item.sources.includes(draft.source),
  );
  const error =
    notice || sources.error || subscriptions.error || deliveries.error;
  const hasError = notice ? noticeError : !!error;
  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("nav.subscriptions")}</h2>
            <p>{t("events.help")}</p>
          </div>
          <div className="table-actions">
            <button
              className="ghost-action"
              disabled={busy}
              onClick={() => void refresh()}
            >
              {t("common.refresh")}
            </button>
            <button className="action" disabled={busy} onClick={() => edit()}>
              {t("events.new")}
            </button>
          </div>
        </div>
        <div className="surface-body">
          <p className="subtle-copy">
            {t("events.platformHint")}{" "}
            <a
              href="https://open.feishu.cn/document/docs/bitable-v1/events/bitable_record_changed"
              target="_blank"
              rel="noreferrer"
            >
              {t("events.platformDocs")}
            </a>
          </p>
          {error && (
            <div
              className={`session-notice${hasError ? " error" : ""}`}
              role={hasError ? "alert" : "status"}
            >
              {String(error)}
            </div>
          )}
          {secret && (
            <div className="session-notice" role="status">
              <strong>{t("events.secretOnce")}</strong>
              <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                {secret}
              </pre>
              <button onClick={() => setSecret("")}>
                {t("events.dismissSecret")}
              </button>
            </div>
          )}
          {draft && (
            <form
              className="event-subscription-form"
              onSubmit={(event) => {
                event.preventDefault();
                void run(async () => {
                  const result = await api.saveEventSubscription({
                    ...draft,
                    event_types: splitEventValues(typesText),
                    filters: Object.fromEntries(
                      FILTERS.map((key) => [
                        key,
                        splitEventValues(filterText[key] ?? ""),
                      ]).filter(([, values]) => values.length > 0),
                    ),
                  });
                  setDraft(null);
                  if (result.signing_secret) setSecret(result.signing_secret);
                  setNotice(t("events.saved"));
                });
              }}
            >
              <div className="form-grid">
                <label>
                  {t("events.key")}
                  <input
                    required
                    value={draft.key}
                    disabled={!!draft.id}
                    onChange={(e) =>
                      setDraft({ ...draft, key: e.target.value })
                    }
                  />
                </label>
                <label>
                  {t("events.name")}
                  <input
                    required
                    value={draft.name}
                    onChange={(e) =>
                      setDraft({ ...draft, name: e.target.value })
                    }
                  />
                </label>
                <label>
                  {t("events.source")}
                  <select
                    value={draft.source}
                    onChange={(e) => {
                      const source = e.target
                        .value as EventSubscriptionInput["source"];
                      setDraft({ ...draft, source, source_refs: [] });
                      setTypesText(
                        source === "agentmux"
                          ? "task.completed"
                          : "drive.file.bitable_record_changed_v1",
                      );
                    }}
                  >
                    <option value="feishu">Feishu</option>
                    <option value="lark">Lark</option>
                    <option value="agentmux">AgentMux</option>
                  </select>
                </label>
                <label>
                  {t("events.callback")}
                  <input
                    required
                    type="url"
                    value={draft.callback_url}
                    onChange={(e) =>
                      setDraft({ ...draft, callback_url: e.target.value })
                    }
                  />
                </label>
              </div>
              <fieldset>
                <legend>{t("events.resources")}</legend>
                {visibleSources.length === 0 && <p>{t("events.noSources")}</p>}
                {visibleSources.map((source) => (
                  <label className="event-source-choice" key={source.ref}>
                    <input
                      type="checkbox"
                      checked={draft.source_refs.includes(source.ref)}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          source_refs: e.target.checked
                            ? [...draft.source_refs, source.ref]
                            : draft.source_refs.filter(
                                (ref) => ref !== source.ref,
                              ),
                        })
                      }
                    />
                    {source.name} <small>{source.ref}</small>
                  </label>
                ))}
              </fieldset>
              <label>
                {t("events.types")}
                <textarea
                  required
                  rows={3}
                  value={typesText}
                  onChange={(e) => setTypesText(e.target.value)}
                />
              </label>
              <div className="form-grid">
                {FILTERS.map((key) => (
                  <label key={key}>
                    {key}
                    <input
                      value={filterText[key] ?? ""}
                      placeholder={t("events.optionalFilter")}
                      onChange={(e) => {
                        setFilterText({ ...filterText, [key]: e.target.value });
                      }}
                    />
                  </label>
                ))}
              </div>
              <div className="table-actions">
                <button
                  className="action"
                  disabled={busy || draft.source_refs.length === 0}
                >
                  {t("common.save")}
                </button>
                <button
                  type="button"
                  className="ghost-action"
                  onClick={() => setDraft(null)}
                >
                  {t("common.cancel")}
                </button>
              </div>
            </form>
          )}
          {subscriptions.loading && <p>{t("common.loading")}</p>}
          {!subscriptions.loading && subscriptions.data?.length === 0 && (
            <p className="empty-state">{t("events.empty")}</p>
          )}
          {(subscriptions.data ?? []).map((sub) => (
            <article className="surface event-subscription-card" key={sub.id}>
              <div className="surface-header">
                <div>
                  <h3>
                    {sub.name} <small>{sub.key}</small>
                  </h3>
                  <p>{sub.callback_url}</p>
                </div>
                <span className="pill">
                  {t(sub.paused ? "events.paused" : "events.active")}
                </span>
              </div>
              <div className="surface-body">
                <p>
                  {sub.source} · {sub.event_types.join(", ")}
                </p>
                {sub.source_refs.map((ref) => {
                  const source = sources.data?.find((item) => item.ref === ref);
                  return (
                    <p className="subtle-copy" key={ref}>
                      {source?.name ?? ref} ·{" "}
                      {source
                        ? source.connected
                          ? t("events.connected")
                          : t("events.pendingSource")
                        : t("events.noPermission")}
                      {sub.source !== "agentmux"
                        ? ` · ${t("events.unverified")}`
                        : ""}
                      {!!source?.ingestion.failures &&
                        ` · ${t("events.ingestionFailures")}: ${source.ingestion.failures} (${source.ingestion.last_failure_at})`}
                    </p>
                  );
                })}
                <p>
                  {t("events.pending")}: {sub.pending} · {t("events.dead")}:{" "}
                  {sub.dead} · {t("events.lastSuccess")}:{" "}
                  {sub.last_success_at
                    ? new Date(sub.last_success_at).toLocaleString()
                    : "—"}
                </p>
                {sub.last_error && (
                  <p className="session-notice error">{sub.last_error}</p>
                )}
                <div className="table-actions">
                  <button disabled={busy} onClick={() => edit(sub)}>
                    {t("common.edit")}
                  </button>
                  <button
                    disabled={busy}
                    onClick={() =>
                      void run(() =>
                        api.saveEventSubscription({
                          ...sub,
                          paused: !sub.paused,
                        }),
                      )
                    }
                  >
                    {t(sub.paused ? "events.resume" : "events.pause")}
                  </button>
                  <button
                    disabled={busy || sub.paused}
                    onClick={() =>
                      void run(async () => {
                        await api.testEventSubscription(sub.id);
                        setNotice(t("events.testQueued"));
                      })
                    }
                  >
                    {t("events.test")}
                  </button>
                  <button
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        const result = await api.rotateEventSecret(sub.id);
                        setSecret(result.signing_secret);
                      })
                    }
                  >
                    {t("events.rotate")}
                  </button>
                  <button
                    className="danger"
                    disabled={busy}
                    onClick={() =>
                      void run(() => api.deleteEventSubscription(sub.id))
                    }
                  >
                    {t("common.delete")}
                  </button>
                </div>
              </div>
            </article>
          ))}
        </div>
      </section>
      <section className="surface">
        <div className="surface-header">
          <h2>{t("events.deliveries")}</h2>
          <div className="control-row">
            <select
              aria-label={t("events.subscriptionFilter")}
              value={subscriptionID}
              onChange={(e) => {
                setSubscriptionID(e.target.value);
                setOffset(0);
                setDetail(null);
              }}
            >
              <option value="">{t("events.allSubscriptions")}</option>
              {(subscriptions.data ?? []).map((sub) => (
                <option key={sub.id} value={sub.id}>
                  {sub.name}
                </option>
              ))}
            </select>
            <select
              aria-label={t("events.statusFilter")}
              value={status}
              onChange={(e) => {
                setStatus(e.target.value);
                setOffset(0);
              }}
            >
              <option value="">{t("events.allStatuses")}</option>
              {[
                "pending",
                "retry",
                "delivering",
                "sent",
                "dead",
                "blocked",
                "cancelled",
              ].map((value) => (
                <option key={value}>{value}</option>
              ))}
            </select>
          </div>
        </div>
        <div className="surface-body">
          {(deliveries.data?.items ?? []).map((delivery) => (
            <div className="agent-registry-row" key={delivery.id}>
              <div>
                <strong>{delivery.event?.type ?? delivery.event_id}</strong>
                <p className="subtle-copy">
                  {delivery.status} · {t("events.attempts")}:{" "}
                  {delivery.attempts} ·{" "}
                  {delivery.last_error ||
                    new Date(delivery.updated_at).toLocaleString()}
                </p>
              </div>
              <div className="table-actions">
                <button
                  disabled={busy || !delivery.event}
                  onClick={() =>
                    void run(async () =>
                      setDetail(await api.eventDelivery(delivery.id)),
                    )
                  }
                >
                  {t("events.details")}
                </button>
                {["dead", "retry", "blocked"].includes(delivery.status) && (
                  <button
                    disabled={busy || !delivery.event}
                    onClick={() =>
                      void run(() => api.retryEventDelivery(delivery.id))
                    }
                  >
                    {t("events.retry")}
                  </button>
                )}
              </div>
            </div>
          ))}
          {deliveries.data?.items.length === 0 && (
            <p className="empty-state">{t("events.noDeliveries")}</p>
          )}
          <div className="table-actions">
            <button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 25))}
            >
              {t("events.previous")}
            </button>
            <button
              disabled={!deliveries.data?.has_more}
              onClick={() => setOffset(offset + 25)}
            >
              {t("events.next")}
            </button>
          </div>
          {detail && (
            <details open>
              <summary>{detail.id}</summary>
              <pre
                style={{
                  whiteSpace: "pre-wrap",
                  overflowWrap: "anywhere",
                  maxHeight: 420,
                  overflow: "auto",
                }}
              >
                {JSON.stringify(detail, null, 2)}
              </pre>
            </details>
          )}
        </div>
      </section>
    </div>
  );
}
