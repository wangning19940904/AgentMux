import { MessageSquareText, RefreshCw, Save } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type ChannelFeedback } from "../api";
import { useI18n } from "../i18n";
import { usePolling } from "../hooks/usePolling";
import { useAsync } from "../useAsync";

export function FeedbackPanel() {
  const { t, language } = useI18n();
  const [semantic, setSemantic] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [reason, setReason] = useState("");
  const [comment, setComment] = useState("");
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState("");
  const report = useAsync(() => api.feedback(), []);
  usePolling(report.reload, 15_000);

  const items = useMemo(
    () => (report.data?.items ?? []).filter((item) => !semantic || item.semantic === semantic),
    [report.data, semantic],
  );
  const selected = (report.data?.items ?? []).find((item) => item.id === selectedID);

  function select(item: ChannelFeedback) {
    setSelectedID(item.id);
    setReason(item.reason ?? "");
    setComment(item.comment ?? "");
    setNotice("");
  }

  async function saveDetail() {
    if (!selected || saving) return;
    setSaving(true);
    setNotice("");
    try {
      await api.updateFeedbackDetail({ id: selected.id, reason, comment });
      await report.reload();
      setNotice(t("feedback.saved"));
    } catch (error) {
      setNotice(String(error));
    } finally {
      setSaving(false);
    }
  }

  const counts = report.data?.counts ?? { positive: 0, progress: 0, negative: 0 };
  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("feedback.title")}</h2>
            <p>{t("feedback.subtitle")}</p>
          </div>
          <button className="ghost-action" onClick={report.reload}><RefreshCw size={15} /> {t("common.refresh")}</button>
        </div>
        <div className="feedback-kpis">
          <FeedbackKPI label={t("feedback.positive")} value={counts.positive} tone="positive" />
          <FeedbackKPI label={t("feedback.progress")} value={counts.progress} tone="progress" />
          <FeedbackKPI label={t("feedback.negative")} value={counts.negative} tone="negative" />
          <FeedbackKPI label={t("feedback.total")} value={report.data?.total ?? 0} tone="total" />
        </div>
      </section>
      <section className="surface feedback-workspace">
        <div className="surface-header">
          <div className="control-row">
            <MessageSquareText size={17} />
            <select value={semantic} onChange={(event) => setSemantic(event.target.value)} aria-label={t("feedback.filter")}>
              <option value="">{t("feedback.all")}</option>
              <option value="positive">{t("feedback.positive")}</option>
              <option value="progress">{t("feedback.progress")}</option>
              <option value="negative">{t("feedback.negative")}</option>
            </select>
          </div>
        </div>
        {report.error && <div className="surface-body error">{report.error}</div>}
        <div className="feedback-layout">
          <div className="feedback-list">
            {items.map((item) => (
              <button key={item.id} className={selectedID === item.id ? "active" : ""} onClick={() => select(item)}>
                <span className={`feedback-semantic ${item.semantic}`}>{feedbackLabel(item.semantic, t)}</span>
                <strong>{item.task_id}</strong>
                <span>{item.channel_id} · {item.user_id}</span>
                <time>{new Date(item.updated_at).toLocaleString(language === "zh" ? "zh-CN" : "en-US")}</time>
              </button>
            ))}
            {!report.loading && items.length === 0 && <div className="empty-state">{t("feedback.empty")}</div>}
          </div>
          <div className="feedback-detail">
            {selected ? (
              <>
                <h3>{feedbackLabel(selected.semantic, t)}</h3>
                <p className="muted">{selected.task_id}</p>
                <label className="field">
                  <span>{t("feedback.reason")}</span>
                  <input value={reason} onChange={(event) => setReason(event.target.value)} maxLength={200} />
                </label>
                <label className="field">
                  <span>{t("feedback.comment")}</span>
                  <textarea value={comment} onChange={(event) => setComment(event.target.value)} rows={8} maxLength={1000} />
                </label>
                <button className="action" disabled={saving} onClick={saveDetail}><Save size={15} /> {t("common.save")}</button>
                {notice && <p className="muted">{notice}</p>}
              </>
            ) : <div className="empty-state">{t("feedback.select")}</div>}
          </div>
        </div>
      </section>
    </div>
  );
}

function FeedbackKPI({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <article className={`feedback-kpi ${tone}`}><span>{label}</span><strong>{value}</strong></article>;
}

function feedbackLabel(value: string, t: (key: string) => string) {
  if (value === "positive") return t("feedback.positive");
  if (value === "progress") return t("feedback.progress");
  return t("feedback.negative");
}
