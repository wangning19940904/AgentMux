import { AlertTriangle, Bot, CheckCircle2, LoaderCircle, Video, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { createPortal } from "react-dom";
import { api } from "./api";
import type { MeetingChannel, MeetingInvitation } from "./api";
import { useMeetings } from "./MeetingContext";
import { useI18n } from "./i18n";

function channelKey(channel: Pick<MeetingChannel, "channel_id" | "target_id">) {
  return `${channel.target_id ?? ""}::${channel.channel_id}`;
}

export function MeetingControls() {
  const { t } = useI18n();
  const { overview, refresh } = useMeetings();
  const [joinOpen, setJoinOpen] = useState(false);
  const [meetingNumber, setMeetingNumber] = useState("");
  const [selectedChannelKey, setSelectedChannelKey] = useState("");
  const [busy, setBusy] = useState<"join" | "accept" | "reject" | "">("");
  const [dialogError, setDialogError] = useState("");
  const [toast, setToast] = useState("");

  const pendingInvitation = overview.invitations[0];
  const connectedChannels = useMemo(
    () => overview.channels.filter((channel) => channel.connected),
    [overview.channels],
  );
  const selectedChannel = useMemo(
    () => connectedChannels.find((channel) => channelKey(channel) === selectedChannelKey),
    [connectedChannels, selectedChannelKey],
  );
  const invitationChannel = useMemo(
    () => pendingInvitation
      ? overview.channels.find((channel) => channelKey(channel) === channelKey(pendingInvitation))
      : undefined,
    [overview.channels, pendingInvitation],
  );

  useEffect(() => {
    if (selectedChannel && selectedChannel.connected) return;
    setSelectedChannelKey(connectedChannels[0] ? channelKey(connectedChannels[0]) : "");
  }, [connectedChannels, selectedChannel]);

  useEffect(() => {
    if (!toast) return;
    const timer = window.setTimeout(() => setToast(""), 6_000);
    return () => window.clearTimeout(timer);
  }, [toast]);

  async function respondToInvitation(invitation: MeetingInvitation, decision: "join" | "reject") {
    setBusy(decision === "join" ? "accept" : "reject");
    setDialogError("");
    try {
      const result = await api.respondMeetingInvitation(invitation, decision);
      await refresh();
      if (decision === "reject") {
        setToast(t("meeting.invitationRejected"));
      } else if (result.greeting_warning) {
        setToast(t("meeting.joinedGreetingWarning", { error: result.greeting_warning }));
      } else {
        setToast(t("meeting.joinedAndGreeted"));
      }
    } catch (error) {
      setDialogError(error instanceof Error ? error.message : String(error));
      await refresh();
    } finally {
      setBusy("");
    }
  }

  async function submitJoin(event: FormEvent) {
    event.preventDefault();
    if (!selectedChannel || !/^\d{9}$/.test(meetingNumber)) return;
    setBusy("join");
    setDialogError("");
    try {
      const result = await api.joinMeeting(
        selectedChannel.target_id ?? "",
        selectedChannel.channel_id,
        meetingNumber,
      );
      setJoinOpen(false);
      setMeetingNumber("");
      if (result.greeting_warning) {
        setToast(t("meeting.joinedGreetingWarning", { error: result.greeting_warning }));
      } else {
        setToast(t("meeting.joinedAndGreeted"));
      }
      await refresh();
    } catch (error) {
      setDialogError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy("");
    }
  }

  function openJoinDialog() {
    setDialogError("");
    setJoinOpen(true);
    void refresh(false);
  }

  return (
    <>
      <button className="ghost-action meeting-launch-button" type="button" onClick={openJoinDialog}>
        <Video size={16} />
        <span>{t("meeting.joinButton")}</span>
        {overview.invitations.length > 0 && (
          <span className="meeting-invite-count" aria-label={t("meeting.pendingInvitations")}>
            {overview.invitations.length}
          </span>
        )}
      </button>

      {createPortal(<>
        {toast && (
          <div className="meeting-toast" role="status">
            <CheckCircle2 size={17} />
            <span>{toast}</span>
          </div>
        )}

        {pendingInvitation ? (
        <div className="meeting-dialog-layer" role="presentation">
          <div aria-hidden="true" className="meeting-dialog-backdrop" />
          <section
            aria-labelledby="meeting-invitation-title"
            aria-modal="true"
            className="surface meeting-dialog"
            role="dialog"
          >
            <div className="meeting-dialog-icon invite">
              <Video size={24} />
            </div>
            <div className="meeting-dialog-heading">
              <span>{t("meeting.incomingInvite")}</span>
              <h2 id="meeting-invitation-title">{pendingInvitation.topic || t("meeting.untitled")}</h2>
              <p>{t("meeting.invitedBy", { name: pendingInvitation.inviter_name })}</p>
            </div>
            <dl className="meeting-details">
              <div>
                <dt>{t("meeting.number")}</dt>
                <dd>{pendingInvitation.meeting_number}</dd>
              </div>
              <div>
                <dt>{t("meeting.channel")}</dt>
                <dd>{pendingInvitation.channel_name}</dd>
              </div>
              <div>
                <dt>{t("meeting.botName")}</dt>
                <dd>{invitationChannel?.bot_name || t("meeting.botNameUnavailable")}</dd>
              </div>
              <div>
                <dt>{t("meeting.boundAgent")}</dt>
                <dd>{invitationChannel?.agent_name || t("meeting.agentUnavailable")}</dd>
              </div>
              <div>
                <dt>{t("meeting.location")}</dt>
                <dd>{pendingInvitation.target_name || t("meeting.localMachine")}</dd>
              </div>
            </dl>
            {pendingInvitation.last_error && (
              <div className="session-notice error">{pendingInvitation.last_error}</div>
            )}
            {dialogError && <div className="session-notice error">{dialogError}</div>}
            <p className="meeting-dialog-hint">{t("meeting.greetingHint")}</p>
            <div className="meeting-dialog-actions">
              <button
                className="ghost-action danger-action"
                disabled={busy !== ""}
                onClick={() => respondToInvitation(pendingInvitation, "reject")}
                type="button"
              >
                {busy === "reject" && <LoaderCircle className="spin" size={15} />}
                {t("meeting.reject")}
              </button>
              <button
                className="action"
                disabled={busy !== ""}
                onClick={() => respondToInvitation(pendingInvitation, "join")}
                type="button"
              >
                {busy === "accept" ? <LoaderCircle className="spin" size={16} /> : <Video size={16} />}
                {t("meeting.acceptAndJoin")}
              </button>
            </div>
          </section>
        </div>
      ) : joinOpen ? (
        <div className="meeting-dialog-layer" role="presentation">
          <div aria-hidden="true" className="meeting-dialog-backdrop" />
          <section
            aria-labelledby="meeting-join-title"
            aria-modal="true"
            className="surface meeting-dialog"
            role="dialog"
          >
            <button
              aria-label={t("common.close")}
              autoFocus
              className="meeting-dialog-close"
              disabled={busy !== ""}
              onClick={() => setJoinOpen(false)}
              type="button"
            >
              <X size={18} />
            </button>
            <div className="meeting-dialog-icon">
              <Bot size={24} />
            </div>
            <div className="meeting-dialog-heading">
              <span>{t("meeting.manualJoinEyebrow")}</span>
              <h2 id="meeting-join-title">{t("meeting.manualJoinTitle")}</h2>
              <p>{t("meeting.manualJoinHint")}</p>
            </div>
            <form className="meeting-join-form" onSubmit={submitJoin}>
              <fieldset className="meeting-channel-fieldset" disabled={busy !== "" || connectedChannels.length === 0}>
                <legend>{t("meeting.channel")}</legend>
                <div className="meeting-channel-options">
                  {connectedChannels.map((channel) => {
                    const key = channelKey(channel);
                    const selected = key === selectedChannelKey;
                    return (
                      <label className={`meeting-channel-option${selected ? " selected" : ""}`} key={key}>
                        <input
                          checked={selected}
                          name="meeting-channel"
                          onChange={() => setSelectedChannelKey(key)}
                          type="radio"
                          value={key}
                        />
                        <span className="meeting-channel-option-heading">
                          <span className="meeting-channel-platform">{channel.platform === "lark" ? "Lark" : "Feishu"}</span>
                          {channel.target_name && <span className="meeting-channel-target">{channel.target_name}</span>}
                          {selected && <CheckCircle2 aria-hidden="true" size={17} />}
                        </span>
                        <dl className="meeting-channel-facts">
                          <div>
                            <dt>{t("meeting.channelName")}</dt>
                            <dd>{channel.channel_name}</dd>
                          </div>
                          <div>
                            <dt>{t("meeting.botName")}</dt>
                            <dd>{channel.bot_name || t("meeting.botNameUnavailable")}</dd>
                          </div>
                          <div>
                            <dt>{t("meeting.boundAgent")}</dt>
                            <dd>{channel.agent_name || t("meeting.agentUnavailable")}</dd>
                          </div>
                        </dl>
                      </label>
                    );
                  })}
                </div>
              </fieldset>
              <label>
                <span>{t("meeting.number")}</span>
                <input
                  disabled={busy !== ""}
                  inputMode="numeric"
                  maxLength={9}
                  onChange={(event) => setMeetingNumber(event.target.value.replace(/\D/g, "").slice(0, 9))}
                  pattern="[0-9]{9}"
                  placeholder="123456789"
                  value={meetingNumber}
                />
                <small>{t("meeting.numberHint")}</small>
              </label>
              {connectedChannels.length === 0 && (
                <div className="session-notice error">{t("meeting.noConnectedChannels")}</div>
              )}
              {(overview.warnings ?? []).length > 0 && (
                <div className="meeting-remote-warning">
                  <AlertTriangle size={15} />
                  <span>{overview.warnings?.join(" · ")}</span>
                </div>
              )}
              {dialogError && <div className="session-notice error">{dialogError}</div>}
              <p className="meeting-dialog-hint">{t("meeting.greetingHint")}</p>
              <div className="meeting-dialog-actions">
                <button className="ghost-action" disabled={busy !== ""} onClick={() => setJoinOpen(false)} type="button">
                  {t("meeting.cancel")}
                </button>
                <button
                  className="action"
                  disabled={busy !== "" || !selectedChannel || !/^\d{9}$/.test(meetingNumber)}
                  type="submit"
                >
                  {busy === "join" ? <LoaderCircle className="spin" size={16} /> : <Video size={16} />}
                  {t("meeting.joinNow")}
                </button>
              </div>
            </form>
          </section>
        </div>
        ) : null}
      </>, document.body)}
    </>
  );
}
