import {
  CalendarClock,
  Webhook,
  Zap,
} from "lucide-react";
import { Channel, FeishuSetupPollResponse, Trigger } from "../../api";


// Config fields rendered per channel type (secret-ish values round-trip as
// "<redacted>"; the server restores originals when unchanged).
export const FEISHU_FIELDS = [
  { key: "app_id", labelKey: "connect.cfgAppId" },
  { key: "app_secret", labelKey: "connect.cfgAppSecret", secret: true },
];

export const FEISHU_DEFAULTS = {
  private_chat_mode: "chat",
  group_chat_mode: "chat-topic",
  max_queue: "20",
  reply_scope: "dm_and_mentions",
  reply_mode: "stream_card",
  ack_reaction_enabled: "true",
  ack_reaction_emojis: "OK,THUMBSUP,MUSCLE,THANKS",
  meeting_voice_enabled: "false",
	meeting_voice_wake_words: "",
  meeting_greeting: "",
  meeting_reply_mode: "stream",
  meeting_response_mode: "stream_text",
  meeting_voice_tts_base_url: "https://api.openai.com/v1",
  meeting_voice_tts_api_key: "",
  meeting_voice_tts_model: "gpt-4o-mini-tts",
  meeting_voice_tts_voice: "alloy",
	meeting_voice_tts_mode: "api",
	meeting_voice_local_model: "kokoro-82m-zh-int8",
	meeting_voice_local_voice: "3",
  codex_control_enabled: "false",
  codex_max_queue: "20",
  codex_turn_timeout_minutes: "20",
  turn_timeout_minutes: "20",
};

export const FEISHU_REPLY_SCOPES = [
  { value: "dm_and_mentions", labelKey: "connect.replyScopeDmMentions" },
  { value: "all", labelKey: "connect.replyScopeAll" },
  { value: "mentions_only", labelKey: "connect.replyScopeMentionsOnly" },
];

export const FEISHU_REPLY_MODES = [
  { value: "stream_message", labelKey: "connect.replyModeStreamMessage" },
  { value: "stream_card", labelKey: "connect.replyModeStreamCard" },
];

export const FEISHU_MEETING_REPLY_MODES = [
  { value: "stream", labelKey: "connect.meetingReplyModeStream" },
  { value: "final", labelKey: "connect.meetingReplyModeFinal" },
];

export const FEISHU_MEETING_RESPONSE_MODES = [
  { value: "stream_text", labelKey: "meetings.responseModeStreamText" },
  { value: "final_text", labelKey: "meetings.responseModeFinalText" },
  { value: "text_voice", labelKey: "meetings.responseModeTextVoice" },
  { value: "voice", labelKey: "meetings.responseModeVoice" },
];

export const CHANNEL_FIELDS: Record<string, { key: string; labelKey: string; secret?: boolean }[]> = {
  feishu: FEISHU_FIELDS,
  lark: FEISHU_FIELDS,
  telegram: [{ key: "token", labelKey: "connect.cfgBotToken", secret: true }],
  dingtalk: [
    { key: "client_id", labelKey: "connect.cfgClientId" },
    { key: "client_secret", labelKey: "connect.cfgClientSecret", secret: true },
    { key: "robot_code", labelKey: "connect.cfgRobotCode" },
  ],
  slack: [
    { key: "bot_token", labelKey: "connect.cfgSlackBotToken", secret: true },
    { key: "app_token", labelKey: "connect.cfgSlackAppToken", secret: true },
  ],
  discord: [{ key: "token", labelKey: "connect.cfgBotToken", secret: true }],
  webhook: [
    { key: "listen", labelKey: "connect.cfgListen" },
    { key: "outbound_url", labelKey: "connect.cfgOutboundUrl" },
  ],
};

export type FeishuSetupPhase = "idle" | "loading" | "scanning" | "saving" | "completed" | "expired" | "denied" | "error";

export const EVENT_OPTIONS = [
  "message.received",
  "message.sent",
  "session.started",
  "session.ended",
  "cron.triggered",
  "webhook.triggered",
  "permission.requested",
  "task.queued",
  "task.started",
  "task.steered",
  "task.controller_changed",
  "task.interrupted",
  "task.completed",
  "interaction.resolved",
  "thread.bound",
  "error",
];

export const EMPTY_CHANNEL: Partial<Channel> = { name: "", type: "feishu", agent_id: "", config: {}, enabled: true };
export const EMPTY_TRIGGER: Partial<Trigger> = {
  name: "",
  kind: "cron",
  cron_expr: "0 9 * * *",
  prompt: "",
  session_mode: "reuse",
  enabled: true,
};


export function stateBadge(state: string | undefined, enabled: boolean, t: (key: string) => string) {
  if (!enabled) return { className: "", label: t("common.disabled") };
  switch (state) {
    case "running":
      return { className: "success", label: t("connect.stateRunning") };
    case "starting":
      return { className: "warning", label: t("connect.stateStarting") };
    case "reconnecting":
      return { className: "warning", label: t("connect.stateReconnecting") };
    case "degraded":
      return { className: "danger", label: t("connect.stateDegraded") };
    case "error":
      return { className: "danger", label: t("connect.stateError") };
    case "pending":
      return { className: "warning", label: t("connect.statePending") };
    default:
      return { className: "warning", label: t("connect.stateStopped") };
  }
}


export function formatChannelTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}


export function preferredDefaultPlatform(platforms: string[]) {
  if (platforms.includes("feishu")) return "feishu";
  return platforms[0] ?? "feishu";
}

export function defaultChannelConfig(type: string) {
	return type === "feishu" || type === "lark" ? { ...FEISHU_DEFAULTS } : {};
}

export function configValue(config: Record<string, string>, key: string, fallback: string) {
  const value = config[key];
  return value === undefined || value === "" ? fallback : value;
}

export function completeFeishuDraft(draft: Partial<Channel>, res: FeishuSetupPollResponse): Partial<Channel> {
  const platform = res.platform ?? draft.type ?? "feishu";
  const config: Record<string, string> = { ...defaultChannelConfig(platform), ...(draft.config ?? {}) };
  delete config.allowed_user_ids;
  delete config.admin_user_ids;
  return {
    ...draft,
    name: (draft.name ?? "").trim() || `${platformLabel(platform)} Bot`,
    type: platform,
    enabled: draft.enabled ?? true,
    config: {
      ...config,
      app_id: res.app_id ?? "",
      app_secret: res.app_secret ?? "",
    },
  };
}

export function platformLabel(platform: string) {
  switch (platform) {
    case "feishu":
      return "Feishu";
    case "lark":
      return "Lark";
    default:
      return platform;
  }
}

export function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

export function kindBadge(kind: string, t: (key: string) => string) {
  switch (kind) {
    case "cron":
      return { icon: <CalendarClock size={14} />, label: t("connect.kindCron") };
    case "webhook":
      return { icon: <Webhook size={14} />, label: t("connect.kindWebhook") };
    default:
      return { icon: <Zap size={14} />, label: t("connect.kindEvent") };
  }
}
