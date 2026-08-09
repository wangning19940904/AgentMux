import { Bot, LoaderCircle, MessageSquareText, Send, Users, Video, Volume2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api } from "../api";
import type { ActiveMeeting, MeetingDetail, MeetingResponseMode, MeetingTimelineItem, MeetingTurn } from "../api";
import { useMeetings } from "../MeetingContext";
import { useI18n } from "../i18n";

function meetingKey(meeting: ActiveMeeting) {
  return `${meeting.target_id ?? ""}::${meeting.channel_id}::${meeting.id}`;
}

type TimelineEntry =
  | { type: "item"; at: string; item: MeetingTimelineItem }
  | { type: "turn"; at: string; turn: MeetingTurn };

export function MeetingsPanel() {
  const { t, language } = useI18n();
  const { overview, lastEvent, refresh } = useMeetings();
  const [selectedKey, setSelectedKey] = useState("");
  const [detail, setDetail] = useState<MeetingDetail | null>(null);
  const [mode, setMode] = useState<"direct" | "agent">("direct");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [settingBusy, setSettingBusy] = useState(false);
  const [error, setError] = useState("");
  const timelineRef = useRef<HTMLDivElement>(null);

  const selected = useMemo(
    () => overview.meetings.find((meeting) => meetingKey(meeting) === selectedKey) ?? overview.meetings[0],
    [overview.meetings, selectedKey],
  );

  async function load(meeting = selected) {
    if (!meeting) { setDetail(null); return; }
    try {
      const result = await api.meetingActivity(meeting.target_id ?? "", meeting.channel_id, meeting.id);
      setDetail(result);
      setError("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    }
  }

  useEffect(() => {
    if (!selected) { setSelectedKey(""); setDetail(null); return; }
    if (meetingKey(selected) !== selectedKey) setSelectedKey(meetingKey(selected));
    void load(selected);
  }, [selectedKey, selected?.id, selected?.target_id]);

  useEffect(() => {
    if (!selected || !lastEvent || lastEvent.meeting_id !== selected.id) return;
    if ((lastEvent.channel_id ?? selected.channel_id) !== selected.channel_id) return;
    if ((lastEvent.target_id ?? "") !== (selected.target_id ?? "")) return;
    void load(selected);
  }, [lastEvent]);

  useEffect(() => {
    const node = timelineRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [detail?.items?.length, detail?.turns?.length]);

  const entries = useMemo<TimelineEntry[]>(() => {
    if (!detail) return [];
    return [
      ...(detail.items ?? []).map((item) => ({ type: "item" as const, at: item.event_time, item })),
      ...(detail.turns ?? []).map((turn) => ({ type: "turn" as const, at: turn.created_at, turn })),
    ].sort((left, right) => new Date(left.at).getTime() - new Date(right.at).getTime());
  }, [detail]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const content = text.trim();
    if (!selected || !content || busy) return;
    setBusy(true); setError("");
    try {
      if (mode === "direct") {
        await api.sendMeetingMessage(selected.target_id ?? "", selected.channel_id, selected.id, content);
      } else {
        await api.askMeeting(selected.target_id ?? "", selected.channel_id, selected.id, content);
      }
      setText("");
      await load(selected);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy(false);
    }
  }

  async function changeResponseMode(nextMode: MeetingResponseMode) {
    if (!selected || settingBusy || nextMode === (selected.response_mode ?? "stream_text")) return;
    setSettingBusy(true); setError("");
    try {
      await api.setMeetingResponseMode(selected.target_id ?? "", selected.channel_id, nextMode);
      await refresh(false);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setSettingBusy(false);
    }
  }

  if (overview.meetings.length === 0) {
    return (
      <section className="surface meeting-panel-empty">
        <div className="meeting-empty-icon"><Video size={28} /></div>
        <h2>{t("meetings.emptyTitle")}</h2>
        <p>{t("meetings.emptyHint")}</p>
        <button className="action" type="button" onClick={() => document.querySelector<HTMLButtonElement>(".meeting-launch-button")?.click()}>
          <Video size={16} />{t("meeting.joinButton")}
        </button>
      </section>
    );
  }

  return (
    <section className="meeting-workspace">
      <aside className="surface meeting-list-pane">
        <header><div><span className="eyebrow">LIVE</span><h2>{t("meetings.active")}</h2></div><span className="count-badge">{overview.meetings.length}</span></header>
        <div className="meeting-list">
          {overview.meetings.map((meeting) => {
            const active = meetingKey(meeting) === meetingKey(selected);
            return (
              <button className={`meeting-list-item${active ? " active" : ""}`} key={meetingKey(meeting)} onClick={() => setSelectedKey(meetingKey(meeting))} type="button">
                <span className="meeting-list-live"><span />{meeting.meeting_number || meeting.id}</span>
                <strong>{meeting.topic || t("meeting.untitled")}</strong>
                <small>{meeting.target_name || t("meeting.localMachine")} · {meeting.channel_name}</small>
                <span className="meeting-list-meta"><Users size={13} />{meeting.participant_count ?? 0}<span>{meeting.agent_name || meeting.bot_name}</span></span>
              </button>
            );
          })}
        </div>
      </aside>

      <div className="surface meeting-conversation-pane">
        <header className="meeting-conversation-header">
          <div><span className="eyebrow">{selected.meeting_number}</span><h2>{selected.topic || t("meeting.untitled")}</h2><p>{selected.bot_name || t("meeting.botNameUnavailable")} · {selected.agent_name || t("meeting.agentUnavailable")}</p></div>
          <div className="meeting-header-actions">
            <label className="meeting-response-mode">
              <Volume2 size={15} />
              <span>{t("meetings.responseMode")}</span>
              <select
                aria-label={t("meetings.responseMode")}
                disabled={settingBusy}
                onChange={(event) => void changeResponseMode(event.target.value as MeetingResponseMode)}
                value={selected.response_mode ?? "stream_text"}
              >
                <option value="stream_text">{t("meetings.responseModeStreamText")}</option>
                <option value="final_text">{t("meetings.responseModeFinalText")}</option>
                <option value="text_voice">{t("meetings.responseModeTextVoice")}</option>
                <option value="voice">{t("meetings.responseModeVoice")}</option>
              </select>
              {settingBusy && <LoaderCircle className="spin" size={14} />}
            </label>
            <span className="meeting-live-pill"><span />{t("meetings.live")}</span>
          </div>
        </header>
        <div className="meeting-timeline" ref={timelineRef}>
          {entries.length === 0 && <div className="meeting-timeline-empty">{t("meetings.noActivity")}</div>}
          {entries.map((entry) => entry.type === "turn"
            ? <TurnEntry key={`turn-${entry.turn.id}`} turn={entry.turn} />
            : <ActivityEntry key={`item-${entry.item.id}`} item={entry.item} language={language} />)}
        </div>
        <form className="meeting-composer" onSubmit={submit}>
          <div className="meeting-composer-modes">
            <button className={mode === "direct" ? "active" : ""} onClick={() => setMode("direct")} type="button"><MessageSquareText size={15} />{t("meetings.direct")}</button>
            <button className={mode === "agent" ? "active" : ""} onClick={() => setMode("agent")} type="button"><Bot size={15} />{t("meetings.askAgent")}</button>
          </div>
          {mode === "agent" && <p className="meeting-composer-hint">{t("meetings.agentHint")}</p>}
          <div className="meeting-composer-row">
            <textarea aria-label={t("meetings.messagePlaceholder")} disabled={busy} onChange={(event) => setText(event.target.value)} placeholder={mode === "agent" ? t("meetings.questionPlaceholder") : t("meetings.messagePlaceholder")} rows={2} value={text} />
            <button className="action" disabled={busy || !text.trim()} type="submit">{busy ? <LoaderCircle className="spin" size={17} /> : <Send size={17} />}</button>
          </div>
          {error && <div className="session-notice error">{error}</div>}
        </form>
      </div>
    </section>
  );
}

function ActivityEntry({ item, language }: { item: MeetingTimelineItem; language: string }) {
  const time = new Date(item.event_time).toLocaleTimeString(language === "zh" ? "zh-CN" : "en-US", { hour: "2-digit", minute: "2-digit" });
  if (["participant_joined", "participant_left", "share_started", "share_ended"].includes(item.kind)) {
    const action = item.kind === "participant_joined" ? "加入了会议" : item.kind === "participant_left" ? "离开了会议" : item.kind === "share_started" ? `开始共享 ${item.share_title || "内容"}` : "结束共享";
    return <div className="meeting-system-entry"><span>{time}</span>{item.actor?.name || "参会者"} {action}{item.share_url && <a href={item.share_url} target="_blank" rel="noreferrer">查看</a>}</div>;
  }
  if (item.kind === "reaction") return <div className="meeting-reaction-entry"><span>{item.actor?.name || "参会者"}</span><strong>{item.text}</strong><time>{time}</time></div>;
  const bot = item.kind === "bot" || item.actor?.participant_type === "bot";
  return (
    <div className={`meeting-message-entry${bot ? " bot" : ""}${item.kind === "transcript" ? " transcript" : ""}`}>
      <div className="meeting-message-avatar">{bot ? <Bot size={16} /> : (item.actor?.name || "?").slice(0, 1)}</div>
      <div><header><strong>{item.actor?.name || (bot ? "Bot" : "参会者")}</strong><span>{item.kind === "transcript" ? "字幕" : "聊天"}</span><time>{time}</time></header><p>{item.text}</p></div>
    </div>
  );
}

function TurnEntry({ turn }: { turn: MeetingTurn }) {
  return <div className="meeting-app-question"><span>仅 App 可见</span><Bot size={15} /><p>{turn.question}</p><small>{turn.status === "running" ? "智能体正在回答…" : turn.status === "failed" ? `回答失败：${turn.error ?? "未知错误"}` : "答案已发送到会议"}</small></div>;
}
