import {
  CheckCircle2,
	Download,
	HardDrive,
  Loader2,
  RefreshCw,
  Save,
  Smartphone,
	Trash2,
  XCircle,
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { type Dispatch, type SetStateAction, useEffect, useRef, useState } from "react";
import { Channel, OperationProgress, TTSCatalogStatus, TTSModel, api } from "../../api";
import { OperationProgress as OperationProgressView } from "../../components/OperationProgress";
import { useI18n } from "../../i18n";
import {
  CHANNEL_FIELDS,
  FEISHU_DEFAULTS,
  FEISHU_MEETING_RESPONSE_MODES,
  FEISHU_REPLY_MODES,
  FEISHU_REPLY_SCOPES,
  FeishuSetupPhase,
  completeFeishuDraft,
  configValue,
  defaultChannelConfig,
  platformLabel,
  sleep,
} from "./connectShared";

export function ChannelEditor({
  draft,
  setDraft,
  platforms,
  agents,
  busy,
  onSave,
  onAutoSave,
  onCancel,
}: {
  draft: Partial<Channel>;
  setDraft: Dispatch<SetStateAction<Partial<Channel> | null>>;
  platforms: string[];
  agents: { id: string; name: string; runtime_id: string }[];
  busy: boolean;
  onSave: () => void;
  onAutoSave: (draft: Partial<Channel>) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const fields = CHANNEL_FIELDS[draft.type ?? ""] ?? [];
  const isFeishuLike = draft.type === "feishu" || draft.type === "lark";
  const selectedAgent = agents.find((agent) => agent.id === draft.agent_id);
  const isCodexAgent = selectedAgent?.runtime_id === "codex";
  const setupRef = useRef({ deviceCode: "", baseUrl: "", interval: 5, cancelled: false, polling: false });
  const draftRef = useRef<Partial<Channel>>(draft);
  const [setup, setSetup] = useState<{ phase: FeishuSetupPhase; qrUrl: string; error: string }>({
    phase: "idle",
    qrUrl: "",
    error: "",
  });

  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);

  useEffect(() => {
    return () => {
      setupRef.current.cancelled = true;
    };
  }, []);

  const update = (patch: Partial<Channel>) =>
    setDraft((current) => ({ ...((current ?? draft) as Partial<Channel>), ...patch }));
  const updateConfig = (key: string, value: string) =>
    setDraft((current) => {
      const base = (current ?? draft) as Partial<Channel>;
      return { ...base, config: { ...(base.config ?? {}), [key]: value } };
    });

  async function startFeishuSetup() {
    setupRef.current.cancelled = false;
    setupRef.current.polling = false;
    setSetup({ phase: "loading", qrUrl: "", error: "" });
    try {
      const res = await api.beginFeishuSetup();
      setupRef.current = {
        deviceCode: res.device_code,
        baseUrl: "",
        interval: res.interval || 5,
        cancelled: false,
        polling: false,
      };
      setSetup({ phase: "scanning", qrUrl: res.qr_url, error: "" });
      void pollFeishuSetup();
    } catch (err) {
      setSetup({ phase: "error", qrUrl: "", error: err instanceof Error ? err.message : String(err) });
    }
  }

  async function pollFeishuSetup() {
    if (setupRef.current.polling) return;
    setupRef.current.polling = true;
    try {
      while (!setupRef.current.cancelled) {
        const res = await api.pollFeishuSetup(setupRef.current.deviceCode, setupRef.current.baseUrl);
        if (setupRef.current.cancelled) return;
        if (res.base_url) setupRef.current.baseUrl = res.base_url;
        if (res.slow_down) setupRef.current.interval += 5;
        switch (res.status) {
          case "completed":
            setSetup((current) => ({ ...current, phase: "saving", error: "" }));
            {
              const nextDraft = completeFeishuDraft((draftRef.current ?? draft) as Partial<Channel>, res);
              draftRef.current = nextDraft;
              setDraft(nextDraft);
              await onAutoSave(nextDraft);
            }
            if (!setupRef.current.cancelled) {
              setSetup((current) => ({ ...current, phase: "completed", error: "" }));
            }
            return;
          case "denied":
            setSetup((current) => ({ ...current, phase: "denied", error: "" }));
            return;
          case "expired":
            setSetup((current) => ({ ...current, phase: "expired", error: "" }));
            return;
          case "error":
            setSetup((current) => ({ ...current, phase: "error", error: res.error ?? t("connect.scanError") }));
            return;
        }
        await sleep((setupRef.current.interval || 5) * 1000);
      }
    } catch (err) {
      if (!setupRef.current.cancelled) {
        setSetup((current) => ({
          ...current,
          phase: "error",
          error: err instanceof Error ? err.message : String(err),
        }));
      }
    } finally {
      setupRef.current.polling = false;
    }
  }

  function resetSetup() {
    setupRef.current.cancelled = true;
    setupRef.current.polling = false;
    setSetup({ phase: "idle", qrUrl: "", error: "" });
  }

  return (
    <div className="route-card editor-card">
      <div className="field-grid">
        <label className="field">
          <span>{t("common.name")}</span>
          <input value={draft.name ?? ""} onChange={(e) => update({ name: e.target.value })} />
        </label>
        <label className="field">
          <span>{t("connect.channelType")}</span>
          <select
            value={draft.type ?? ""}
            onChange={(e) => {
              resetSetup();
              update({ type: e.target.value, config: draft.id ? draft.config : defaultChannelConfig(e.target.value) });
            }}
          >
            {platforms.map((p) => (
              <option key={p} value={p}>
                {platformLabel(p)}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>{t("connect.boundAgent")}</span>
          <select
            value={draft.agent_id ?? ""}
            onChange={(e) => {
              const agentID = e.target.value;
              const runtimeID = agents.find((agent) => agent.id === agentID)?.runtime_id;
              const config = { ...(draft.config ?? {}) };
              if (runtimeID !== "codex") config.codex_control_enabled = "false";
              delete config.approval_mode;
              update({ agent_id: agentID, config });
            }}
          >
            <option value="">{t("connect.noAgent")}</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        {fields.map((f) => (
          <label className="field" key={f.key}>
            <span>{t(f.labelKey)}</span>
            <input
              type={f.secret ? "password" : "text"}
              value={draft.config?.[f.key] ?? ""}
              placeholder={f.secret ? "" : undefined}
              onChange={(e) => updateConfig(f.key, e.target.value)}
            />
          </label>
        ))}
      </div>
      {isFeishuLike && (
        <>
          <FeishuSetupBox
            phase={setup.phase}
            qrUrl={setup.qrUrl}
            error={setup.error}
            platform={draft.type ?? "feishu"}
            onStart={startFeishuSetup}
            onReset={resetSetup}
          />
          <FeishuChannelOptions draft={draft} updateConfig={updateConfig} codexAgent={isCodexAgent} />
        </>
      )}
      <div className="table-actions">
        <label className="switch-row">
          <span>
            <strong>{t("common.enable")}</strong>
          </span>
          <input type="checkbox" checked={draft.enabled ?? false} onChange={(e) => update({ enabled: e.target.checked })} />
        </label>
        <button className="ghost-action" onClick={onCancel}>
          {t("common.close")}
        </button>
        <button className="action" disabled={busy || !(draft.name ?? "").trim()} onClick={onSave}>
          <Save size={15} />
          {t("common.save")}
        </button>
      </div>
    </div>
  );
}

export function FeishuChannelOptions({
  draft,
  updateConfig,
  codexAgent,
}: {
  draft: Partial<Channel>;
  updateConfig: (key: string, value: string) => void;
  codexAgent: boolean;
}) {
  const { t } = useI18n();
  const config = draft.config ?? {};
  const replyMode = configValue(config, "reply_mode", FEISHU_DEFAULTS.reply_mode);
  const ackEnabled = configValue(config, "ack_reaction_enabled", FEISHU_DEFAULTS.ack_reaction_enabled) !== "false";
  const legacyMeetingVoice = configValue(config, "meeting_voice_enabled", FEISHU_DEFAULTS.meeting_voice_enabled) === "true";
  const legacyMeetingReplyMode = configValue(config, "meeting_reply_mode", FEISHU_DEFAULTS.meeting_reply_mode);
  const meetingResponseMode = configValue(
    config,
    "meeting_response_mode",
    legacyMeetingVoice ? "text_voice" : legacyMeetingReplyMode === "final" ? "final_text" : FEISHU_DEFAULTS.meeting_response_mode,
  );
  const meetingVoice = meetingResponseMode === "text_voice" || meetingResponseMode === "voice";
	const ttsMode = configValue(config, "meeting_voice_tts_mode", FEISHU_DEFAULTS.meeting_voice_tts_mode);
	const localModelID = configValue(config, "meeting_voice_local_model", FEISHU_DEFAULTS.meeting_voice_local_model);
	const localVoiceID = configValue(config, "meeting_voice_local_voice", FEISHU_DEFAULTS.meeting_voice_local_voice);
	const [ttsCatalog, setTTSCatalog] = useState<TTSCatalogStatus | null>(null);
	const [ttsCatalogError, setTTSCatalogError] = useState("");
	const [ttsBusy, setTTSBusy] = useState("");
	const [ttsProgress, setTTSProgress] = useState<Record<string, OperationProgress>>({});
  const codexControl = configValue(config, "codex_control_enabled", FEISHU_DEFAULTS.codex_control_enabled) === "true";

	async function loadTTSModels() {
		try {
			setTTSCatalog(await api.ttsModels());
			setTTSCatalogError("");
		} catch (err) {
			setTTSCatalogError(err instanceof Error ? err.message : String(err));
		}
	}

	useEffect(() => {
		if (meetingVoice && ttsMode === "local") void loadTTSModels();
	}, [meetingVoice, ttsMode]);

	async function downloadTTSModel(model: TTSModel) {
		setTTSBusy(model.id);
		setTTSCatalogError("");
		try {
			await api.downloadTTSModel(model.id, (progress) => {
				setTTSProgress((current) => ({ ...current, [model.id]: progress }));
			});
			updateConfig("meeting_voice_local_model", model.id);
			updateConfig("meeting_voice_local_voice", model.voices[0]?.id ?? "0");
			await loadTTSModels();
		} catch (err) {
			setTTSCatalogError(err instanceof Error ? err.message : String(err));
		} finally {
			setTTSBusy("");
			setTTSProgress((current) => {
				const next = { ...current };
				delete next[model.id];
				return next;
			});
		}
	}

	async function deleteTTSModel(model: TTSModel) {
		setTTSBusy(model.id);
		setTTSCatalogError("");
		try {
			setTTSCatalog(await api.deleteTTSModel(model.id));
		} catch (err) {
			setTTSCatalogError(err instanceof Error ? err.message : String(err));
		} finally {
			setTTSBusy("");
		}
	}

  return (
    <div className="channel-options">
      <p className="subtle-copy">{t("connect.exclusiveConnectionHint")}</p>
      <div className="field-grid">
        <label className="field">
          <span>{t("connect.replyScope")}</span>
          <select
            value={configValue(config, "reply_scope", FEISHU_DEFAULTS.reply_scope)}
            onChange={(e) => updateConfig("reply_scope", e.target.value)}
          >
            {FEISHU_REPLY_SCOPES.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.labelKey)}
              </option>
            ))}
          </select>
        </label>
        {codexAgent && (
          <label className="switch-row channel-option-toggle">
            <span>
              <strong>{t("connect.codexControl")}</strong>
              <small>{t("connect.codexControlHint")}</small>
            </span>
            <input
              type="checkbox"
              checked={codexControl}
              onChange={(e) => updateConfig("codex_control_enabled", e.target.checked ? "true" : "false")}
            />
          </label>
        )}
        {codexAgent && codexControl && (
          <>
            <div className="field">
              <span>{t("connect.codexCapability")}</span>
              <small>
                {draft.codex_control_capability?.state === "ready"
                  ? t("connect.codexCapabilityReady")
                  : draft.codex_control_capability?.state === "unavailable"
                    ? `${t("connect.codexCapabilityUnavailable")} ${draft.codex_control_capability.error ?? ""}`
                    : draft.codex_control_capability?.state === "disconnected"
                      ? t("connect.codexCapabilityDisconnected")
                      : t("connect.codexCapabilityPending")}
              </small>
            </div>
            <label className="field">
              <span>{t("connect.codexAllowedUsers")}</span>
              <input
                value={configValue(config, "allowed_user_ids", FEISHU_DEFAULTS.allowed_user_ids)}
                onChange={(e) => updateConfig("allowed_user_ids", e.target.value)}
                placeholder="ou_xxx, ou_yyy"
              />
            </label>
            <label className="field">
              <span>{t("connect.codexAdminUsers")}</span>
              <input
                value={configValue(config, "admin_user_ids", FEISHU_DEFAULTS.admin_user_ids)}
                onChange={(e) => updateConfig("admin_user_ids", e.target.value)}
                placeholder="ou_xxx"
              />
            </label>
            <label className="field">
              <span>{t("connect.codexMaxQueue")}</span>
              <input
                type="number"
                min={1}
                max={100}
                value={configValue(config, "codex_max_queue", FEISHU_DEFAULTS.codex_max_queue)}
                onChange={(e) => updateConfig("codex_max_queue", e.target.value)}
              />
            </label>
          </>
        )}
        <label className="field">
          <span>{t("connect.turnTimeout")}</span>
          <input
            type="number"
            min={1}
            max={240}
            value={configValue(
              config,
              "turn_timeout_minutes",
              configValue(config, "codex_turn_timeout_minutes", FEISHU_DEFAULTS.turn_timeout_minutes),
            )}
            onChange={(e) => updateConfig("turn_timeout_minutes", e.target.value)}
          />
        </label>
        <label className="field">
          <span>{t("connect.replyMode")}</span>
          <select value={replyMode} onChange={(e) => updateConfig("reply_mode", e.target.value)}>
            {FEISHU_REPLY_MODES.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.labelKey)}
              </option>
            ))}
            <option value="lark_cli" disabled>
              {t("connect.replyModeLarkCli")}
            </option>
          </select>
        </label>
        <label className="switch-row channel-option-toggle">
          <span>
            <strong>{t("connect.ackReaction")}</strong>
            <small>{t("connect.ackReactionHint")}</small>
          </span>
          <input
            type="checkbox"
            checked={ackEnabled}
            onChange={(e) => updateConfig("ack_reaction_enabled", e.target.checked ? "true" : "false")}
          />
        </label>
        <label className="field">
          <span>{t("connect.ackReactionEmojis")}</span>
          <input
            value={configValue(config, "ack_reaction_emojis", FEISHU_DEFAULTS.ack_reaction_emojis)}
            onChange={(e) => updateConfig("ack_reaction_emojis", e.target.value)}
          />
        </label>
        <label className="field" style={{ gridColumn: "1 / -1" }}>
          <span>{t("connect.meetingGreeting")}</span>
          <textarea
            rows={4}
            value={configValue(config, "meeting_greeting", FEISHU_DEFAULTS.meeting_greeting)}
            onChange={(e) => updateConfig("meeting_greeting", e.target.value)}
            placeholder={t("connect.meetingGreetingPlaceholder")}
          />
          <small>{t("connect.meetingGreetingHint")}</small>
        </label>
        <label className="field">
          <span>{t("connect.meetingReplyMode")}</span>
          <select
            value={meetingResponseMode}
            onChange={(e) => {
              const value = e.target.value;
              updateConfig("meeting_response_mode", value);
              updateConfig("meeting_voice_enabled", value === "text_voice" || value === "voice" ? "true" : "false");
              updateConfig("meeting_reply_mode", value === "final_text" || value === "voice" ? "final" : "stream");
            }}
          >
            {FEISHU_MEETING_RESPONSE_MODES.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.labelKey)}
              </option>
            ))}
          </select>
          <small>{t("connect.meetingReplyModeHint")}</small>
        </label>
        <div className="field">
          <span>{t("connect.meetingVoice")}</span>
          <small>{t("connect.meetingVoiceHint")}</small>
        </div>
        {meetingVoice && (
          <>
				<label className="field" style={{ gridColumn: "1 / -1" }}>
					<span>{t("connect.meetingWakeWords")}</span>
					<textarea
						rows={3}
						value={configValue(config, "meeting_voice_wake_words", FEISHU_DEFAULTS.meeting_voice_wake_words)}
						onChange={(event) => updateConfig("meeting_voice_wake_words", event.target.value)}
						placeholder={t("connect.meetingWakeWordsPlaceholder")}
					/>
					<small>{t("connect.meetingWakeWordsHint")}</small>
				</label>
			<label className="field">
				<span>{t("connect.meetingTTSMode")}</span>
				<select value={ttsMode} onChange={(event) => updateConfig("meeting_voice_tts_mode", event.target.value)}>
					<option value="api">{t("connect.meetingTTSModeApi")}</option>
					<option value="local">{t("connect.meetingTTSModeLocal")}</option>
				</select>
				<small>{t(ttsMode === "local" ? "connect.meetingTTSLocalHint" : "connect.meetingTTSApiHint")}</small>
			</label>
			{ttsMode === "api" ? (
				<>
            <label className="field">
              <span>{t("connect.meetingTTSBaseUrl")}</span>
              <input
                value={configValue(config, "meeting_voice_tts_base_url", FEISHU_DEFAULTS.meeting_voice_tts_base_url)}
                onChange={(e) => updateConfig("meeting_voice_tts_base_url", e.target.value)}
                placeholder="https://api.openai.com/v1"
              />
            </label>
            <label className="field">
              <span>{t("connect.meetingTTSApiKey")}</span>
              <input
                type="password"
                value={configValue(config, "meeting_voice_tts_api_key", FEISHU_DEFAULTS.meeting_voice_tts_api_key)}
                onChange={(e) => updateConfig("meeting_voice_tts_api_key", e.target.value)}
                autoComplete="new-password"
              />
            </label>
            <label className="field">
              <span>{t("connect.meetingTTSModel")}</span>
              <input
                value={configValue(config, "meeting_voice_tts_model", FEISHU_DEFAULTS.meeting_voice_tts_model)}
                onChange={(e) => updateConfig("meeting_voice_tts_model", e.target.value)}
              />
            </label>
            <label className="field">
              <span>{t("connect.meetingTTSVoice")}</span>
              <input
                value={configValue(config, "meeting_voice_tts_voice", FEISHU_DEFAULTS.meeting_voice_tts_voice)}
                onChange={(e) => updateConfig("meeting_voice_tts_voice", e.target.value)}
              />
            </label>
				</>
			) : (
				<LocalTTSModelManager
					catalog={ttsCatalog}
					error={ttsCatalogError}
					busy={ttsBusy}
					progress={ttsProgress}
					selectedModelID={localModelID}
					selectedVoiceID={localVoiceID}
					onSelectModel={(model) => {
						updateConfig("meeting_voice_local_model", model.id);
						updateConfig("meeting_voice_local_voice", model.voices[0]?.id ?? "0");
					}}
					onSelectVoice={(voice) => updateConfig("meeting_voice_local_voice", voice)}
					onDownload={downloadTTSModel}
					onDelete={deleteTTSModel}
					onRetry={() => void loadTTSModels()}
				/>
			)}
          </>
        )}
      </div>
    </div>
  );
}

function LocalTTSModelManager({
	catalog,
	error,
	busy,
	progress,
	selectedModelID,
	selectedVoiceID,
	onSelectModel,
	onSelectVoice,
	onDownload,
	onDelete,
	onRetry,
}: {
	catalog: TTSCatalogStatus | null;
	error: string;
	busy: string;
	progress: Record<string, OperationProgress>;
	selectedModelID: string;
	selectedVoiceID: string;
	onSelectModel: (model: TTSModel) => void;
	onSelectVoice: (voice: string) => void;
	onDownload: (model: TTSModel) => void;
	onDelete: (model: TTSModel) => void;
	onRetry: () => void;
}) {
	const { t } = useI18n();
	const models = catalog?.models ?? [];
	const selected = models.find((model) => model.id === selectedModelID) ?? models[0];
	const selectedVoice = selected?.voices.some((voice) => voice.id === selectedVoiceID)
		? selectedVoiceID
		: selected?.voices[0]?.id ?? "0";
	return (
		<div className="tts-model-manager">
			<div className="tts-local-fields">
				<label className="field">
					<span>{t("connect.meetingTTSLocalModel")}</span>
					<select
						value={selected?.id ?? selectedModelID}
						disabled={!models.length}
						onChange={(event) => {
							const model = models.find((item) => item.id === event.target.value);
							if (model) onSelectModel(model);
						}}
					>
						{models.map((model) => (
							<option key={model.id} value={model.id}>
								{model.name}{model.installed ? ` · ${t("frameworks.installed")}` : ""}
							</option>
						))}
					</select>
				</label>
				<label className="field">
					<span>{t("connect.meetingTTSLocalVoice")}</span>
					<select value={selectedVoice} disabled={!selected} onChange={(event) => onSelectVoice(event.target.value)}>
						{selected?.voices.map((voice) => (
							<option key={voice.id} value={voice.id}>{voice.name}{voice.notes ? ` · ${voice.notes}` : ""}</option>
						))}
					</select>
				</label>
			</div>
			<div className="tts-runtime-row">
				<HardDrive size={15} />
				{catalog ? (
					<span>
						{t("connect.meetingTTSRuntime")} {catalog.runtime.version} · {catalog.runtime.platform} ·{" "}
						{catalog.runtime.installed ? t("connect.meetingTTSReady") : formatTTSBytes(catalog.runtime.download_bytes ?? 0)}
					</span>
				) : <span>{t("common.loading")}</span>}
			</div>
			{error && (
				<div className="notice error tts-model-error">
					<span>{error}</span>
					<button className="ghost-action" type="button" onClick={onRetry}><RefreshCw size={13} />{t("common.retry")}</button>
				</div>
			)}
			<div className="tts-model-grid">
				{models.map((model) => {
					const isBusy = busy === model.id || model.downloading;
					return (
						<div key={model.id} className={`tts-model-card${model.id === selectedModelID ? " selected" : ""}${model.installed ? " installed" : ""}`}>
							<div className="tts-model-card-head">
								<div>
									<strong>{model.name}</strong>
									<small>{model.parameters} · {model.engine} · {model.license}</small>
								</div>
								{model.installed && <CheckCircle2 size={17} />}
							</div>
							<p>{model.description}</p>
							<div className="tts-model-card-foot">
								<span>{formatTTSBytes(model.download_bytes)}</span>
								<div className="table-actions">
									{model.installed ? (
										<>
											<button className="ghost-action" type="button" onClick={() => onSelectModel(model)}>{t("connect.meetingTTSUseModel")}</button>
											{model.id !== selectedModelID && (
												<button className="ghost-action danger-action" type="button" disabled={Boolean(busy)} onClick={() => onDelete(model)}>
													<Trash2 size={13} />{t("common.delete")}
												</button>
											)}
										</>
									) : (
										<button className="action" type="button" disabled={Boolean(busy) || catalog?.runtime.supported === false} onClick={() => onDownload(model)}>
											{isBusy ? <Loader2 className="spin" size={14} /> : <Download size={14} />}
											{isBusy ? t("connect.meetingTTSDownloading") : t("connect.meetingTTSDownload")}
										</button>
									)}
								</div>
							</div>
							{progress[model.id] && <OperationProgressView progress={progress[model.id]} />}
						</div>
					);
				})}
			</div>
			{catalog && !catalog.runtime.supported && <small className="error-text">{t("connect.meetingTTSUnsupported")}</small>}
		</div>
	);
}

function formatTTSBytes(bytes: number): string {
	if (!bytes) return "—";
	return `${(bytes / 1024 / 1024).toFixed(bytes >= 100 * 1024 * 1024 ? 0 : 1)} MB`;
}

export function FeishuSetupBox({
  phase,
  qrUrl,
  error,
  platform,
  onStart,
  onReset,
}: {
  phase: FeishuSetupPhase;
  qrUrl: string;
  error: string;
  platform: string;
  onStart: () => void;
  onReset: () => void;
}) {
  const { t } = useI18n();
  const label = platformLabel(platform);

  if (phase === "idle") {
    return (
      <div className="qr-setup">
        <span className="provider-icon">
          <Smartphone size={15} />
        </span>
        <strong>{label}</strong>
        <button className="ghost-action" onClick={onStart}>
          <Smartphone size={14} />
          {t("connect.scanSetup")}
        </button>
      </div>
    );
  }

  const message =
    phase === "loading"
      ? t("connect.scanGenerating")
      : phase === "scanning"
        ? t("connect.scanWaiting")
        : phase === "saving"
          ? t("connect.scanSaving")
          : phase === "completed"
            ? t("connect.scanCompleted")
            : phase === "expired"
              ? t("connect.scanExpired")
              : phase === "denied"
                ? t("connect.scanDenied")
                : error || t("connect.scanError");

  return (
    <div className={`qr-setup ${phase === "error" || phase === "denied" ? "error" : ""}`}>
      {phase === "loading" || phase === "saving" ? <Loader2 className="spin" size={16} /> : null}
      {phase === "completed" ? <CheckCircle2 size={16} /> : null}
      {phase === "expired" || phase === "denied" || phase === "error" ? <XCircle size={16} /> : null}
      {(phase === "scanning" || phase === "saving") && qrUrl && (
        <span className="qr-frame">
          <QRCodeSVG value={qrUrl} size={148} level="M" />
        </span>
      )}
      <span>{message}</span>
      {(phase === "expired" || phase === "denied" || phase === "error" || phase === "completed") && (
        <button className="ghost-action" onClick={phase === "completed" ? onReset : onStart}>
          <RefreshCw size={14} />
          {phase === "completed" ? t("connect.scanAgain") : t("connect.retryScan")}
        </button>
      )}
    </div>
  );
}
