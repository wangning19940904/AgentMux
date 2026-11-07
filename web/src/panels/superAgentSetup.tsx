import { Check, CheckCircle2, Copy, ExternalLink, Loader2, RefreshCw, ShieldCheck, Smartphone, Trash2, XCircle } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { useState } from "react";
import type { ReactNode } from "react";
import { api, openExternalURL } from "../api";
import type { Channel, FeishuSetupPollResponse, GitHubAccount, SuperAgentSource } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { completeFeishuDraft } from "./connect/connectShared";
import { useFeishuAutomation, useFeishuRegistration } from "./connect/feishuScan";
import { FeishuActorPicker, FeishuChatPicker, FeishuUserAuthorize, useFeishuDirectory } from "./superAgentFeishu";
import { generateSecret, githubPolls, githubWebhookPath, isFeishuSource, parseActors, requiredKeys, sourceField } from "./superAgentModel";
import {
  EMAIL_PROVIDERS, REDACTED, emailProviderByID, emailProviderConfig, emailProviderForAddress, emailProviderForConfig,
  githubForwardCommand, githubHookSettingsURL, githubPublicBase, githubTokenURL, isReachableOrigin, isRepoName, tokenCanComment,
} from "./superAgentSetupModel";

// SourceEditor is one trigger in the Super Agent form. Each kind is laid out as
// the steps a person follows to connect it: where to go, what to do there, and
// the fields that take what they bring back. Steps that AgentMux can do itself,
// such as creating a Feishu bot or a webhook secret, are done from here.

type SourceProps = {
  source: SuperAgentSource;
  onChange: (patch: Partial<SuperAgentSource>) => void;
};

export function SourceEditor({ source, feishuChannels, onChange, onRemove, onFeishuChannelCreated }: SourceProps & {
  feishuChannels: Channel[];
  onRemove: () => void;
  // Called with a channel the guide just created, once it is saved.
  onFeishuChannelCreated: (channel: Channel) => Promise<void>;
}) {
  const { t } = useI18n();
  return (
    <div className="super-agent-card">
      <div className="super-agent-section-head">
        <strong>{t(`superAgents.kind.${source.kind}`)}</strong>
        <span className="control-row">
          <label className="super-agent-check"><input type="checkbox" checked={source.enabled} onChange={(event) => onChange({ enabled: event.target.checked })} /> {t("common.enabled")}</label>
          <button className="ghost-action danger-action" onClick={onRemove}><Trash2 size={13} /> {t("superAgents.remove")}</button>
        </span>
      </div>
      <p className="muted">{t(`superAgents.kindHelp.${source.kind}`)}</p>
      {isFeishuSource(source.kind) ? (
        <FeishuSteps source={source} onChange={onChange} channels={feishuChannels} onChannelCreated={onFeishuChannelCreated} />
      ) : source.kind === "email" ? (
        <EmailSteps source={source} onChange={onChange} />
      ) : (
        <GitHubSteps source={source} onChange={onChange} />
      )}
    </div>
  );
}

function Steps({ children }: { children: ReactNode }) {
  return <ol className="setup-steps">{children}</ol>;
}

function Step({ index, title, done, children }: { index: number; title: string; done?: boolean; children: ReactNode }) {
  const { t } = useI18n();
  return (
    <li className={`setup-step${done ? " done" : ""}`}>
      <span className="setup-step-marker" aria-label={done ? t("superAgents.setup.stepDone", { index }) : undefined}>{done ? <Check size={13} /> : index}</span>
      <div className="setup-step-body">
        <strong>{title}</strong>
        {children}
      </div>
    </li>
  );
}

// Field renders one config value as SOURCE_FIELDS describes it.
function Field({ source, name, onChange, children }: SourceProps & { name: string; children?: ReactNode }) {
  const { t } = useI18n();
  const field = sourceField(source.kind, name);
  const value = source.config[name] ?? "";
  const set = (next: string) => onChange({ config: { ...source.config, [name]: next } });
  return (
    <label className="field">
      <span>{t(field.labelKey)}{requiredKeys(source).includes(name) ? " *" : ""}</span>
      {field.multiline ? (
        <textarea rows={3} placeholder={field.placeholder} value={value} onChange={(event) => set(event.target.value)} />
      ) : (
        <input type={field.secret ? "password" : "text"} autoComplete="off" placeholder={field.placeholder} value={value} onChange={(event) => set(event.target.value)} />
      )}
      {children}
    </label>
  );
}

function SendersField({ source, onChange }: SourceProps) {
  const { t } = useI18n();
  return (
    <label className="field">
      <span>{t("superAgents.allowedActors")} *</span>
      <textarea rows={2} value={source.allowed_actors.join(", ")} onChange={(event) => onChange({ allowed_actors: parseActors(event.target.value) })} />
      <small>{t(`superAgents.actorsHelp.${source.kind}`)}</small>
    </label>
  );
}

function LinkButton({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a className="ghost-action setup-link" href={href} target="_blank" rel="noreferrer"
      onClick={(event) => { event.preventDefault(); void openExternalURL(href); }}>
      <ExternalLink size={13} /> {children}
    </a>
  );
}

function CopyButton({ value, disabled }: { value: string; disabled?: boolean }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  return (
    <button type="button" className="ghost-action" disabled={disabled || !value} onClick={async () => {
      try {
        await navigator.clipboard.writeText(value);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      } catch {
        // The clipboard is unavailable outside a secure context.
      }
    }}>
      <Copy size={13} /> {copied ? t("connect.copied") : t("superAgents.setup.copy")}
    </button>
  );
}

// Value is a "name: value [Copy]" row for something to paste elsewhere.
function Value({ label, value, display, disabled }: { label: string; value: string; display?: string; disabled?: boolean }) {
  return (
    <div className="setup-value">
      <span>{label}</span>
      <code>{display ?? value}</code>
      <CopyButton value={value} disabled={disabled} />
    </div>
  );
}

function Command({ value, display }: { value: string; display?: string }) {
  return (
    <div className="setup-command">
      <code>{display ?? value}</code>
      <CopyButton value={value} />
    </div>
  );
}

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);

// --- Feishu -----------------------------------------------------------------

function FeishuSteps({ source, onChange, channels, onChannelCreated }: SourceProps & {
  channels: Channel[];
  onChannelCreated: (channel: Channel) => Promise<void>;
}) {
  const { t } = useI18n();
  const documents = source.kind === "feishu_doc";
  const channelID = source.config.channel_id ?? "";
  const channel = channels.find((item) => item.id === channelID);
  const memberChats = parseActors(source.config[documents ? "notify_chat_id" : "chat_ids"] ?? "");
  const directory = useFeishuDirectory(channelID, memberChats, source.allowed_actors);
  const bot = channel?.name ?? t("superAgents.setup.feishu.theBot");
  const setConfig = (key: string, value: string) => onChange({ config: { ...source.config, [key]: value } });

  // A bot made from here is selected, and the person who scanned may ask it.
  const created = async (saved: Channel, owner?: string) => {
    await onChannelCreated(saved);
    onChange({
      config: { ...source.config, channel_id: saved.id },
      ...(owner && source.allowed_actors.length === 0 ? { allowed_actors: [owner] } : {}),
    });
  };

  let index = 0;
  return (
    <Steps>
      <Step index={++index} title={t("superAgents.setup.feishu.botTitle")} done={Boolean(channel)}>
        {channels.length > 0 && (
          <label className="field">
            <span>{t("superAgents.field.channel")} *</span>
            <select value={channelID} onChange={(event) => setConfig("channel_id", event.target.value)}>
              <option value="">{t("superAgents.chooseChannel")}</option>
              {channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </label>
        )}
        <FeishuBotScan hasChannels={channels.length > 0} onCreated={created} />
      </Step>
      <Step index={++index} title={t(documents ? "superAgents.setup.feishu.docEventsTitle" : "superAgents.setup.feishu.eventsTitle")}>
        {channel ? <FeishuEventsSetup channel={channel} documents={documents} /> : <p>{t("superAgents.setup.feishu.pickFirst")}</p>}
      </Step>
      <Step index={++index} title={t("superAgents.setup.feishu.userTitle")} done={Boolean(directory.auth.user)}>
        <FeishuUserAuthorize channelID={channelID} bot={bot} directory={directory} />
      </Step>
      {documents ? (
        <>
          <Step index={++index} title={t("superAgents.setup.feishu.shareTitle")} done={Boolean(source.config.documents?.trim())}>
            <p>{t("superAgents.setup.feishu.shareHelp", { bot })}</p>
            <Field source={source} name="documents" onChange={onChange} />
          </Step>
          <Step index={++index} title={t("superAgents.setup.feishu.resultsTitle")}>
            <FeishuChatPicker label={t("superAgents.field.notifyChat")} single value={source.config.notify_chat_id ?? ""}
              channelID={channelID} directory={directory} onChange={(value) => setConfig("notify_chat_id", value)} />
            <Field source={source} name="instructions" onChange={onChange} />
            <Field source={source} name="debounce_seconds" onChange={onChange} />
          </Step>
        </>
      ) : (
        <Step index={++index} title={t("superAgents.setup.feishu.groupTitle")}>
          <p>{t(directory.auth.user ? "superAgents.setup.feishu.groupHelpAuthorized" : "superAgents.setup.feishu.groupHelp", { bot })}</p>
          <FeishuChatPicker label={t("superAgents.field.chatIDs")} value={source.config.chat_ids ?? ""}
            channelID={channelID} directory={directory} onChange={(value) => setConfig("chat_ids", value)} />
        </Step>
      )}
      <Step index={++index} title={t(documents ? "superAgents.setup.whoDocTitle" : "superAgents.setup.whoTitle")} done={source.allowed_actors.length > 0}>
        <FeishuActorPicker label={t("superAgents.allowedActors")} values={source.allowed_actors} allowEveryone={!documents} scoped={memberChats.length > 0}
          help={t(`superAgents.actorsHelp.${source.kind}`)} channelID={channelID} directory={directory}
          onChange={(allowed_actors) => onChange({ allowed_actors })} />
      </Step>
    </Steps>
  );
}

// FeishuBotScan creates a bot by scanning: Feishu makes the app in the
// scanner's tenant, and it is saved as a channel without anyone copying an App
// ID or secret.
function FeishuBotScan({ hasChannels, onCreated }: { hasChannels: boolean; onCreated: (channel: Channel, owner?: string) => Promise<void> }) {
  const { t } = useI18n();
  const scan = useFeishuRegistration(async (result: FeishuSetupPollResponse) => {
    const saved = await api.upsertChannel(completeFeishuDraft({ name: "", agent_id: "", enabled: true, config: {} }, result));
    await onCreated(saved, result.owner_open_id);
  }, t("superAgents.setup.feishu.failed"));

  if (scan.phase === "idle") {
    return (
      <div className="setup-scan">
        {!hasChannels && <p>{t("superAgents.setup.feishu.botHelp")}</p>}
        <button type="button" className={hasChannels ? "ghost-action" : "action"} onClick={scan.start}>
          <Smartphone size={14} /> {t(hasChannels ? "superAgents.setup.feishu.scanAnother" : "superAgents.setup.feishu.scan")}
        </button>
      </div>
    );
  }
  const failed = scan.phase === "error" || scan.phase === "denied" || scan.phase === "expired";
  const message = {
    loading: t("superAgents.setup.feishu.generating"),
    scanning: t("superAgents.setup.feishu.waiting"),
    saving: t("superAgents.setup.feishu.saving"),
    completed: t("superAgents.setup.feishu.created"),
    expired: t("superAgents.setup.feishu.expired"),
    denied: t("superAgents.setup.feishu.denied"),
    error: scan.error || t("superAgents.setup.feishu.failed"),
  }[scan.phase];
  return (
    <div className={`qr-setup setup-scan-box${failed ? " error" : ""}`} role="status">
      {scan.phase === "scanning" && scan.qrUrl && <span className="qr-frame"><QRCodeSVG value={scan.qrUrl} size={148} level="M" /></span>}
      <span className="setup-scan-status">
        {(scan.phase === "loading" || scan.phase === "saving") && <Loader2 className="spin" size={15} />}
        {scan.phase === "completed" && <CheckCircle2 size={15} />}
        {failed && <XCircle size={15} />}
        {message}
      </span>
      {(failed || scan.phase === "scanning") && (
        <button type="button" className="ghost-action" onClick={failed ? scan.start : scan.reset}>
          {failed ? <><RefreshCw size={13} /> {t("superAgents.setup.retry")}</> : t("common.cancel")}
        </button>
      )}
    </div>
  );
}

const FEISHU_MESSAGE_EVENTS = "im.message.receive_v1";
const FEISHU_DOCUMENT_EVENTS = "im.message.receive_v1, drive.file.edit_v1";

// FeishuEventsSetup switches on what the bot needs to hear requests. Feishu
// bots get it with a second scan; Lark has no such automation, so it is
// described step by step instead.
function FeishuEventsSetup({ channel, documents }: { channel: Channel; documents: boolean }) {
  const { t } = useI18n();
  const automation = useFeishuAutomation(t("connect.openPlatformMissingApp"));
  const [visibility, setVisibility] = useState<"owner" | "all">("owner");
  const appID = channel.config?.app_id ?? "";
  const lark = channel.type === "lark";
  const openPlatform = `https://open.${lark ? "larksuite.com" : "feishu.cn"}/app/${encodeURIComponent(appID)}`;
  const events = documents ? FEISHU_DOCUMENT_EVENTS : FEISHU_MESSAGE_EVENTS;
  const manual = (
    <p className="setup-note">
      {t(lark ? "superAgents.setup.feishu.larkManual" : "superAgents.setup.feishu.manual", { events })}{" "}
      {appID && <LinkButton href={openPlatform}>{t("superAgents.setup.feishu.openPlatform")}</LinkButton>}
    </p>
  );
  if (lark || !appID) return manual;

  const busy = automation.phase === "loading" || automation.phase === "scanning" || automation.phase === "configuring";
  const missing = automation.result?.missing_scopes ?? [];
  return (
    <>
      <p>{t(documents ? "superAgents.setup.feishu.docEventsHelp" : "superAgents.setup.feishu.eventsHelp")}</p>
      {automation.phase === "completed" ? (
        <div className="setup-ok" role="status">
          <CheckCircle2 size={15} /> {t("superAgents.setup.feishu.configured")}
          {missing.length > 0 && <small className="setup-warn">{t("superAgents.setup.feishu.missingScopes", { scopes: missing.join(", ") })}</small>}
        </div>
      ) : (
        <div className={`qr-setup setup-scan-box${automation.phase === "error" || automation.phase === "expired" ? " error" : ""}`}>
          {automation.phase === "scanning" && automation.qrPayload && <span className="qr-frame"><QRCodeSVG value={automation.qrPayload} size={148} level="M" /></span>}
          {busy ? (
            <span className="setup-scan-status">
              <Loader2 className="spin" size={15} />
              {t(automation.phase === "configuring" ? "superAgents.setup.feishu.configuring" : automation.phase === "scanning" ? "superAgents.setup.feishu.waiting" : "superAgents.setup.feishu.generating")}
            </span>
          ) : (
            <>
              <label className="field compact-field">
                <span>{t("connect.openPlatformVisibility")}</span>
                <select value={visibility} onChange={(event) => setVisibility(event.target.value as "owner" | "all")}>
                  <option value="owner">{t("connect.openPlatformOwner")}</option>
                  <option value="all">{t("connect.openPlatformAll")}</option>
                </select>
              </label>
              <button type="button" className="ghost-action" onClick={() => automation.start(appID, visibility, documents, true)}>
                <Smartphone size={14} /> {t("superAgents.setup.feishu.configure")}
              </button>
              {automation.phase === "expired" && <span>{t("superAgents.setup.feishu.expired")}</span>}
              {automation.error && <span>{automation.error}</span>}
            </>
          )}
          {automation.phase === "scanning" && <button type="button" className="ghost-action" onClick={automation.reset}>{t("common.cancel")}</button>}
        </div>
      )}
      {manual}
    </>
  );
}

// --- Email ------------------------------------------------------------------

function EmailSteps({ source, onChange }: SourceProps) {
  const { t } = useI18n();
  const configured = emailProviderForConfig(source.config);
  const [chosen, setChosen] = useState(configured?.id ?? (source.config.imap_host ? "other" : ""));
  const provider = emailProviderByID(chosen);
  const [serversOpen, setServersOpen] = useState(chosen === "other");

  const choose = (id: string, config = source.config) => {
    setChosen(id);
    const next = emailProviderByID(id);
    if (next) onChange({ config: { ...config, ...emailProviderConfig(next) } });
    else onChange({ config });
    if (id === "other") setServersOpen(true);
  };
  const setAddress = (address: string) => {
    const config = { ...source.config, username: address };
    const detected = emailProviderForAddress(address);
    // Detection fills servers only while nothing else was chosen for them.
    if (detected && (chosen === "" || (provider && provider.id !== detected.id))) choose(detected.id, config);
    else onChange({ config });
  };
  const providerName = provider ? t(`superAgents.setup.email.provider.${provider.id}`) : "";

  return (
    <Steps>
      <Step index={1} title={t("superAgents.setup.email.addressTitle")} done={Boolean(source.config.username?.trim() && source.config.imap_host?.trim())}>
        <p>{t("superAgents.setup.email.addressHelp")}</p>
        <div className="field-grid">
          <label className="field">
            <span>{t("superAgents.field.username")} *</span>
            <input type="text" autoComplete="off" placeholder="agent@example.com" value={source.config.username ?? ""} onChange={(event) => setAddress(event.target.value)} />
          </label>
          <label className="field">
            <span>{t("superAgents.field.provider")}</span>
            <select value={chosen} onChange={(event) => choose(event.target.value)}>
              <option value="">{t("superAgents.setup.email.choose")}</option>
              {EMAIL_PROVIDERS.map((item) => <option key={item.id} value={item.id}>{t(`superAgents.setup.email.provider.${item.id}`)}</option>)}
              <option value="other">{t("superAgents.setup.email.other")}</option>
            </select>
          </label>
        </div>
        <details className="setup-details" open={serversOpen} onToggle={(event) => setServersOpen(event.currentTarget.open)}>
          <summary>{t("superAgents.setup.email.servers")}{source.config.imap_host ? ` · ${source.config.imap_host}` : ""}</summary>
          <div className="field-grid">
            <Field source={source} name="imap_host" onChange={onChange} />
            <Field source={source} name="imap_port" onChange={onChange} />
            <Field source={source} name="smtp_host" onChange={onChange} />
            <Field source={source} name="smtp_port" onChange={onChange} />
            <Field source={source} name="mailbox" onChange={onChange} />
            <Field source={source} name="from_address" onChange={onChange} />
          </div>
        </details>
      </Step>
      <Step index={2} title={t("superAgents.setup.email.passwordTitle")} done={Boolean(source.config.password)}>
        <p>{t(`superAgents.setup.email.passwordHelp.${provider?.id ?? "other"}`, { provider: providerName })}</p>
        {provider?.passwordURL && <LinkButton href={provider.passwordURL}>{t("superAgents.setup.email.openPasswordPage", { provider: providerName })}</LinkButton>}
        <Field source={source} name="password" onChange={onChange} />
      </Step>
      <Step index={3} title={t("superAgents.setup.whoTitle")} done={source.allowed_actors.length > 0}>
        <SendersField source={source} onChange={onChange} />
      </Step>
    </Steps>
  );
}

// --- GitHub -----------------------------------------------------------------

function GitHubSteps({ source, onChange }: SourceProps) {
  const { t } = useI18n();
  const setup = useAsync(() => api.superAgentGitHubSetup(), []);
  const [account, setAccount] = useState<GitHubAccount | null>(null);
  const [checkError, setCheckError] = useState("");
  const [checking, setChecking] = useState(false);
  const config = source.config;
  const token = config.github_token ?? "";
  const apiBase = config.api_base ?? "";
  const polls = githubPolls(source);
  const set = (patch: Record<string, string>) => onChange({ config: { ...config, ...patch } });

  const check = async () => {
    setChecking(true);
    setCheckError("");
    setAccount(null);
    try {
      const found = await api.checkGitHubAccount(token, apiBase);
      setAccount(found);
      set({ agent_login: found.login });
    } catch (error) {
      setCheckError(errorText(error));
    } finally {
      setChecking(false);
    }
  };

  return (
    <>
    <Steps>
      <Step index={1} title={t("superAgents.setup.github.accountTitle")} done={Boolean(config.agent_login?.trim())}>
        <p>{t("superAgents.setup.github.accountHelp")}</p>
        <LinkButton href={githubTokenURL(apiBase)}>{t("superAgents.setup.github.createToken")}</LinkButton>
        <Field source={source} name="github_token" onChange={(patch) => { setAccount(null); setCheckError(""); onChange(patch); }} />
        <span className="setup-inline-actions">
          <button type="button" className="ghost-action" disabled={checking || !token.trim() || token === REDACTED} onClick={() => void check()}>
            {checking ? <Loader2 className="spin" size={13} /> : <ShieldCheck size={13} />} {t("superAgents.setup.github.check")}
          </button>
          {account && <span className="setup-ok"><CheckCircle2 size={14} /> {t("superAgents.setup.github.signedInAs", { login: account.login })}</span>}
          {account?.classic && !tokenCanComment(account.scopes) && <span className="setup-warn">{t("superAgents.setup.github.noRepoScope")}</span>}
          {account && !account.classic && polls && <span className="setup-warn">{t("superAgents.setup.github.notClassic")}</span>}
          {checkError && <span className="error">{checkError}</span>}
        </span>
        <Field source={source} name="agent_login" onChange={onChange} />
        <p className="setup-note">{t("superAgents.setup.github.ownAccount")}</p>
      </Step>
      <Step index={2} title={t("superAgents.setup.github.deliveryTitle")} done={polls && Boolean(token.trim() && config.agent_login?.trim())}>
        <div className="segmented setup-delivery" role="group" aria-label={t("superAgents.setup.github.deliveryLabel")}>
          {(["poll", "webhook"] as const).map((delivery) => (
            <button key={delivery} type="button" className={(polls ? "poll" : "webhook") === delivery ? "active" : ""} aria-pressed={(polls ? "poll" : "webhook") === delivery}
              onClick={() => set({ delivery })}>
              {t(delivery === "poll" ? "superAgents.setup.github.deliveryPoll" : "superAgents.setup.github.deliveryWebhook")}
            </button>
          ))}
        </div>
        <Field source={source} name="repo" onChange={onChange} />
        {polls ? (
          <>
            <p>{t("superAgents.setup.github.pollHelp")}</p>
            {!token.trim() && <p className="setup-warn">{t("superAgents.setup.github.pollNeedsToken")}</p>}
          </>
        ) : (
          <GitHubWebhookSetup source={source} onChange={onChange} localURL={setup.data?.local_url ?? "http://127.0.0.1:8765"} apiProtected={setup.data?.api_protected} />
        )}
      </Step>
      <Step index={3} title={t("superAgents.setup.whoTitle")} done={source.allowed_actors.length > 0}>
        <SendersField source={source} onChange={onChange} />
      </Step>
    </Steps>
    <details className="setup-details">
      <summary>{t("superAgents.setup.github.enterprise")}</summary>
      <Field source={source} name="api_base" onChange={onChange} />
    </details>
    </>
  );
}

// GitHubWebhookSetup is what to paste into GitHub, or, when GitHub cannot reach
// this page, the command that forwards one repository's deliveries to it.
function GitHubWebhookSetup({ source, onChange, localURL, apiProtected }: SourceProps & { localURL: string; apiProtected?: boolean }) {
  const { t } = useI18n();
  const config = source.config;
  const apiBase = config.api_base ?? "";
  const repo = config.repo?.trim() ?? "";
  const secret = config.webhook_secret ?? "";
  const secretHidden = secret === REDACTED;
  const publicBase = githubPublicBase(source, window.location.origin);
  return (
    <>
      <div className="setup-values">
        <div className="setup-value">
          <span>{t("superAgents.setup.github.secret")}</span>
          <code>{secretHidden ? t("superAgents.setup.github.secretSaved") : secret ? "•".repeat(8) : "—"}</code>
          <span className="setup-inline-actions">
            <CopyButton value={secretHidden ? "" : secret} />
            <button type="button" className="ghost-action" onClick={() => onChange({ config: { ...config, webhook_secret: generateSecret() } })}><RefreshCw size={13} /> {t("superAgents.setup.github.regenerate")}</button>
          </span>
        </div>
      </div>
      {publicBase ? (
        <>
          <p>{t("superAgents.setup.github.publicHelp")}</p>
          {isRepoName(repo) && <LinkButton href={githubHookSettingsURL(repo, apiBase)}>{t("superAgents.setup.github.openHooks", { repo })}</LinkButton>}
          <div className="setup-values">
            <Value label={t("superAgents.setup.github.payloadURL")} value={`${publicBase}${githubWebhookPath(source)}`} />
            <Value label={t("superAgents.setup.github.contentType")} value="application/json" />
          </div>
        </>
      ) : (
        <>
          <p>{t("superAgents.setup.github.localHelp")}</p>
          <small className="muted">{t("superAgents.setup.github.installForwarder")}</small>
          <Command value="gh extension install cli/gh-webhook" />
          <small className="muted">{t("superAgents.setup.github.runForwarder")}</small>
          {isRepoName(repo) ? <Command value={githubForwardCommand(source, localURL)} display={githubForwardCommand(source, localURL, true)} /> : <p className="setup-warn">{t("superAgents.setup.github.needRepo")}</p>}
          <p className="setup-note">{t("superAgents.setup.github.localNote")}</p>
        </>
      )}
      {!isReachableOrigin(window.location.origin) || config.public_url ? (
        <Field source={source} name="public_url" onChange={onChange}>
          {config.public_url?.trim() && apiProtected === false && <small className="setup-warn">{t("superAgents.setup.github.exposed")}</small>}
        </Field>
      ) : null}
    </>
  );
}

export function sourceHookAddress(source: SuperAgentSource): string {
  return `${githubPublicBase(source, window.location.origin)}${githubWebhookPath(source)}`;
}
