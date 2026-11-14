import { Bot, Cable, CheckCircle2, ExternalLink, Eye, FolderOpen, Link2, LogIn, Pencil, Play, RefreshCw, Save, Trash2, Workflow, Zap } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  activeMachineScope,
  api,
  isDesktopApp,
  openExternalURL,
  type AgentInstance,
  type AgentSession,
  type Channel,
  type FrameworkAuthStatus,
  type FrameworkLoginResult,
  type FrameworkRuntimeSettings,
  type FrameworkSmokeResult,
  type Trigger,
} from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";

import {
  type DrawerMode,
  approvalModeLabelKey,
  approvalModesForRuntime,
  composeInjectedPrompt,
  runtimeLabel,
  runtimeOptionValues,
  serviceTierLabel,
  workDirErrorMessage,
} from "./agentUtils";
import { MarkdownPreview, Picker } from "./widgets";
import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";
import { AgentConversationOptions } from "./AgentConversationOptions";
import { ChannelLogo } from "./ChannelLogo";
import { RuntimeSelect } from "./RuntimeSelect";
import { FrameworkSmokeResultView } from "../frameworks/FrameworkSmokeResult";

// TYPED_MODEL is the model select's "enter another model ID" choice.
const TYPED_MODEL = "__typed__";

export type CLIOption = { id: string; name: string; note?: string; installed: boolean };

export function AgentForm({
  busy,
  canSave,
  channelOptions,
  cliOptions,
  draft,
  drawerMode,
  mcpOptions,
  onDelete,
  onInstallCLI,
  onSave,
  onToggleChannel,
  onToggleTrigger,
  onUpdate,
  readOnly,
  runtimeOptions,
  selectedChannelIDs,
  selectedTriggerIDs,
  skillOptions,
  triggerOptions,
  t,
}: {
  busy: string;
  canSave: boolean;
  channelOptions: Channel[];
  cliOptions: CLIOption[];
  draft: AgentInstance;
  drawerMode: DrawerMode | null;
  mcpOptions: string[];
  onDelete: () => void;
  onInstallCLI: (id: string) => void;
  onSave: () => void;
  onToggleChannel: (id: string) => void;
  onToggleTrigger: (id: string) => void;
  onUpdate: <K extends keyof AgentInstance>(key: K, value: AgentInstance[K]) => void;
  readOnly: boolean;
  runtimeOptions: string[];
  selectedChannelIDs: string[];
  selectedTriggerIDs: string[];
  skillOptions: string[];
  triggerOptions: Trigger[];
  t: (key: string) => string;
}) {
  const [directoryBusy, setDirectoryBusy] = useState("");
  const [directoryNotice, setDirectoryNotice] = useState("");
  const [remoteDirectoryPickerOpen, setRemoteDirectoryPickerOpen] = useState(false);
  const [promptView, setPromptView] = useState<"edit" | "preview">(readOnly ? "preview" : "edit");
  const [authStatus, setAuthStatus] = useState<FrameworkAuthStatus | null>(null);
  const [authBusy, setAuthBusy] = useState(false);
  const [authNotice, setAuthNotice] = useState("");
	const [localRuntimeSettings, setLocalRuntimeSettings] = useState<FrameworkRuntimeSettings | null>(null);
	const [localRuntimeSettingsBusy, setLocalRuntimeSettingsBusy] = useState(false);
	const [localRuntimeSettingsError, setLocalRuntimeSettingsError] = useState("");
  const [localRuntimeSettingsSource, setLocalRuntimeSettingsSource] = useState("");
  const [catalogReload, setCatalogReload] = useState(0);
  const [loginBusy, setLoginBusy] = useState("");
  const [loginResult, setLoginResult] = useState<FrameworkLoginResult | null>(null);
  const [loginCode, setLoginCode] = useState("");
  const [desktopThreads, setDesktopThreads] = useState<AgentSession[]>([]);
  const [desktopThreadsBusy, setDesktopThreadsBusy] = useState(false);
  const [desktopThreadsError, setDesktopThreadsError] = useState("");
  const [typingModel, setTypingModel] = useState(false);
  const [modelTest, setModelTest] = useState<{ key: string; busy: boolean; result?: FrameworkSmokeResult }>({ key: "", busy: false });
  const injectedPrompt = useMemo(() => {
    const logPaths = selectedChannelIDs.map((id) => `~/.agentmux/logs/channels/${id}.jsonl`);
    const channelPrompts = selectedChannelIDs
      .map((id) => channelOptions.find((channel) => channel.id === id))
      .filter((channel): channel is Channel => Boolean(channel?.default_message_prompt?.trim()))
      .map((channel) => ({
        name: channel.name || channel.type,
        prompt: channel.default_message_prompt ?? "",
      }));
    const clis = (draft.clis ?? [])
      .map((id) => cliOptions.find((option) => option.id === id))
      .filter((option): option is CLIOption => Boolean(option?.installed))
      .map((option) => ({ name: option.name, note: option.note ?? "" }));
    return composeInjectedPrompt(
      draft.system_prompt ?? "",
      logPaths,
      clis,
      channelPrompts,
      t("agents.channelDefaultPrompt"),
    );
  }, [draft.system_prompt, draft.clis, selectedChannelIDs, channelOptions, cliOptions, t]);
  const desktopRuntime = draft.runtime_id === "codex-app";
  const frameworkRuntimeID = desktopRuntime ? "codex" : draft.runtime_id;
  const authTargetID = draft.target_id || authStatus?.target_id;
  const authenticated = authStatus?.state === "authenticated";
  // Models come from the framework itself, for whatever configures it: its
  // own sign-in, a gateway or a router. AgentMux does not manage providers.
  const runtimeSettingsSource = JSON.stringify([draft.runtime_id, draft.work_dir ?? "", authTargetID, catalogReload]);
  const localSettings = localRuntimeSettingsSource === runtimeSettingsSource ? localRuntimeSettings : null;
  const localCapabilities = localSettings?.capabilities;
  const modelOptions = runtimeOptionValues(localCapabilities?.models);
  const modelLabels = new Map((localCapabilities?.models ?? []).filter((option) => option.label && option.label !== option.value).map((option) => [option.value, option.label!]));
  // A typed model is one the framework does not list, such as a router's
  // routing group. It is allowed; the test below checks that it works.
  const typedModel = Boolean(draft.default_model) && Boolean(localCapabilities) && !modelOptions.includes(draft.default_model!);
  const showTypedModel = typingModel || typedModel || (!localRuntimeSettingsBusy && modelOptions.length === 0);
  const selectedModel = draft.default_model || localSettings?.defaults.model || "";
  const selectedModelCapabilities = localCapabilities?.model_capabilities?.[selectedModel];
  // Nothing is known about a typed model, so it may use anything the framework offers.
  const localModelCapabilities = localCapabilities?.model_capabilities && !typedModel ? selectedModelCapabilities : localCapabilities;
  const reasoningOptions = runtimeOptionValues(localModelCapabilities?.reasoning_efforts);
  const serviceTierOptions = runtimeOptionValues(localModelCapabilities?.service_tiers);
  const variants = typedModel ? undefined : selectedModelCapabilities?.variants;
  const availableServiceTiers = variants?.length
    ? serviceTierOptions.filter((tier) => variants.some((variant) => (!draft.default_reasoning_effort || variant.reasoning_effort === draft.default_reasoning_effort) && variant.service_tier === tier))
    : serviceTierOptions;
  const approvalOptions = localCapabilities
    ? runtimeOptionValues(localCapabilities.approval_modes)
    : approvalModesForRuntime(draft.runtime_id);
  const settingsReady = Boolean(localCapabilities);
  const invalidEffort = settingsReady && Boolean(draft.default_reasoning_effort) && !reasoningOptions.includes(draft.default_reasoning_effort!);
  const invalidTier = settingsReady && Boolean(draft.default_service_tier) && !availableServiceTiers.includes(draft.default_service_tier!);
  const invalidApproval = Boolean(draft.default_approval_mode) && !approvalOptions.includes(draft.default_approval_mode!);
  const invalidSettings = invalidEffort || invalidTier || invalidApproval;
  // Effort and speed can only be checked against a model list. A model on
  // its own is accepted even when the list cannot be read.
  const unverifiedSettings = !settingsReady && Boolean(draft.default_reasoning_effort || draft.default_service_tier);
  const runtimeDefaults = localSettings?.defaults;
  const modelPlaceholder = t("agents.defaultModelRuntimePlaceholder") + (runtimeDefaults?.model ? `（${runtimeDefaults.model}）` : "");
  const defaultModelStatus = localRuntimeSettingsBusy
    ? t("agents.defaultModelLoading")
    : localRuntimeSettingsError
      ? localRuntimeSettingsError
      : showTypedModel && modelOptions.length > 0
        ? t("agents.defaultModelTypedHelp")
        : modelOptions.length > 0
          ? t("agents.defaultModelLocalLogin")
          : authenticated
            ? t("agents.defaultModelRuntimeUnavailable")
            : t("agents.defaultModelLoginRequired");
  const modelTestKey = JSON.stringify([draft.runtime_id, authTargetID, draft.default_model ?? "", draft.default_reasoning_effort ?? "", draft.default_service_tier ?? ""]);
  const currentModelTest = modelTest.key === modelTestKey ? modelTest : null;

  async function testModel() {
    const key = modelTestKey;
    setModelTest({ key, busy: true });
    try {
      const result = await api.frameworkTest({
        kind: draft.runtime_id,
        ...(draft.id ? { agent_id: draft.id } : {}),
        model: draft.default_model ?? "",
        reasoning_effort: draft.default_reasoning_effort ?? "",
        service_tier: draft.default_service_tier ?? "",
      }, authTargetID);
      setModelTest((current) => current.key === key ? { key, busy: false, result } : current);
    } catch (err) {
      const result: FrameworkSmokeResult = {
        kind: draft.runtime_id, model: draft.default_model ?? "", ok: false, latency_ms: 0, error_kind: "unknown",
        error_detail: err instanceof Error ? err.message : String(err), checked_at: new Date().toISOString(),
      };
      setModelTest((current) => current.key === key ? { key, busy: false, result } : current);
    }
  }

  useEffect(() => {
    if (!desktopRuntime) {
      setDesktopThreads([]);
      setDesktopThreadsError("");
      setDesktopThreadsBusy(false);
      return;
    }
    let active = true;
    setDesktopThreadsBusy(true);
    setDesktopThreadsError("");
    void Promise.resolve()
      .then(() => api.codexDesktopThreads(draft.target_id))
      .then((items) => {
        if (active) setDesktopThreads(items ?? []);
      })
      .catch((err) => {
        if (active) setDesktopThreadsError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (active) setDesktopThreadsBusy(false);
      });
    return () => {
      active = false;
    };
  }, [desktopRuntime, draft.target_id]);

  useEffect(() => {
    let active = true;
    setAuthStatus(null);
    setAuthNotice("");
    setLoginResult(null);
    setLoginCode("");
    if (!frameworkRuntimeID) {
      setAuthBusy(false);
      return () => {
        active = false;
      };
    }
    setAuthBusy(true);
    void Promise.resolve()
      .then(() => api.frameworkAuth(frameworkRuntimeID, draft.target_id))
      .then((status) => {
        if (active) setAuthStatus(status);
      })
      .catch((err) => {
        if (active) setAuthNotice(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (active) setAuthBusy(false);
      });
    return () => {
      active = false;
    };
  }, [draft.target_id, frameworkRuntimeID]);

	useEffect(() => {
		let active = true;
		setLocalRuntimeSettings(null);
		setLocalRuntimeSettingsError("");
		if (!draft.runtime_id) {
			setLocalRuntimeSettingsBusy(false);
			return () => {
				active = false;
			};
		}
		setLocalRuntimeSettingsBusy(true);
		void Promise.resolve()
      .then(() => api.frameworkRuntimeSettings(draft.runtime_id, draft.work_dir ?? "", authTargetID))
			.then((settings) => {
				if (active) {
          setLocalRuntimeSettings(settings);
          setLocalRuntimeSettingsSource(runtimeSettingsSource);
        }
			})
			.catch((err) => {
				if (active) setLocalRuntimeSettingsError(err instanceof Error ? err.message : String(err));
			})
			.finally(() => {
				if (active) setLocalRuntimeSettingsBusy(false);
			});
		return () => {
			active = false;
		};
	}, [authenticated, authTargetID, draft.runtime_id, draft.work_dir, runtimeSettingsSource]);

  useEffect(() => {
    if (!loginResult || !frameworkRuntimeID) return;
    let active = true;
    const interval = window.setInterval(() => {
      void Promise.resolve()
        .then(() => api.frameworkAuth(frameworkRuntimeID, authTargetID))
        .then((status) => {
          if (!active) return;
          setAuthStatus(status);
          if (status.state === "authenticated") {
            setAuthNotice(t("agents.loginComplete"));
            window.clearInterval(interval);
          }
        })
        .catch(() => undefined);
    }, 2000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [authTargetID, frameworkRuntimeID, loginResult?.session_id, t]);

  async function refreshFrameworkAuth() {
    if (!frameworkRuntimeID) return;
    setAuthBusy(true);
    setAuthNotice("");
    try {
      const status = await api.frameworkAuth(frameworkRuntimeID, authTargetID);
      setAuthStatus(status);
      if (status.state === "authenticated") setAuthNotice(t("agents.loginComplete"));
    } catch (err) {
      setAuthNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setAuthBusy(false);
    }
  }

  async function refreshDesktopThreads() {
    setDesktopThreadsBusy(true);
    setDesktopThreadsError("");
    try {
      setDesktopThreads((await api.codexDesktopThreads(authTargetID)) ?? []);
    } catch (err) {
      setDesktopThreadsError(err instanceof Error ? err.message : String(err));
    } finally {
      setDesktopThreadsBusy(false);
    }
  }

  function selectDesktopThread(threadID: string) {
    onUpdate("desktop_thread_id", threadID);
    const thread = desktopThreads.find((item) => item.session_id === threadID);
    if (thread?.project_dir) onUpdate("work_dir", thread.project_dir);
  }

  async function startFrameworkLogin() {
    if (!frameworkRuntimeID) return;
    setLoginBusy("start");
    setAuthNotice("");
    setLoginResult(null);
    setLoginCode("");
    try {
      setLoginResult(await api.startFrameworkLogin(frameworkRuntimeID, authTargetID));
    } catch (err) {
      setAuthNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setLoginBusy("");
    }
  }

  async function completeFrameworkLogin() {
    if (!loginResult || !loginCode.trim()) return;
    setLoginBusy("complete");
    setAuthNotice("");
    try {
      await api.completeFrameworkLogin(loginResult.session_id, loginCode.trim(), authTargetID);
      setAuthNotice(t("agents.loginCodeSubmitted"));
    } catch (err) {
      setAuthNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setLoginBusy("");
    }
  }

  async function selectWorkDir() {
    setDirectoryNotice("");
    const targetID = draft.target_id || activeMachineScope();
    if (targetID !== "local" || !isDesktopApp()) {
      setRemoteDirectoryPickerOpen(true);
      return;
    }
    setDirectoryBusy("select");
    try {
      const selected = await api.selectDirectory(draft.work_dir ?? "");
      if (selected.path) {
        onUpdate("work_dir", selected.path);
        setDirectoryNotice(t("agents.workDirSelected"));
      }
    } catch (err) {
      setDirectoryNotice(workDirErrorMessage(err, t));
    } finally {
      setDirectoryBusy("");
    }
  }

  return (
    <>
      {remoteDirectoryPickerOpen && (
        <RemoteDirectoryPicker
          initialPath={draft.work_dir ?? ""}
          targetID={draft.target_id || activeMachineScope()}
          onClose={() => setRemoteDirectoryPickerOpen(false)}
          onSelect={(path) => {
            onUpdate("work_dir", path);
            setDirectoryNotice(t("agents.workDirSelected"));
            setRemoteDirectoryPickerOpen(false);
          }}
          t={t}
        />
      )}
      <section className="agent-section">
        <header>
          <Workflow size={17} />
          <h3>{t("agents.identity")}</h3>
        </header>
        <div className="field-grid">
          <label className="field">
            <span>{t("agents.name")}</span>
            <input disabled={readOnly} value={draft.name} onChange={(event) => onUpdate("name", event.target.value)} />
          </label>
          <RuntimeSelect
            label={t("agents.runtime")}
            emptyLabel={t("agents.noInstalledRuntime")}
            unavailableLabel={t("gateway.frameworkNotInstalled")}
            disabled={readOnly || (drawerMode === "create" && runtimeOptions.length === 0)}
            value={draft.runtime_id}
            options={runtimeOptions}
            onChange={(runtime) => onUpdate("runtime_id", runtime)}
          />
          {desktopRuntime && (
            <label className="field wide">
              <span>{t("agents.desktopThread")}</span>
              <div className="directory-input-row">
                <select
                  disabled={readOnly || desktopThreadsBusy}
                  value={draft.desktop_thread_id ?? ""}
                  onChange={(event) => selectDesktopThread(event.target.value)}
                >
                  <option value="">
                    {desktopThreadsBusy ? t("agents.desktopThreadsLoading") : t("agents.desktopThreadPlaceholder")}
                  </option>
                  {draft.desktop_thread_id && !desktopThreads.some((thread) => thread.session_id === draft.desktop_thread_id) && (
                    <option value={draft.desktop_thread_id} disabled>
                      {draft.desktop_thread_id} · {t("agents.desktopThreadUnavailable")}
                    </option>
                  )}
                  {desktopThreads.map((thread) => (
                    <option key={thread.session_id} value={thread.session_id}>
                      {desktopThreadLabel(thread)}
                    </option>
                  ))}
                </select>
                <button
                  className="ghost-action icon-action"
                  disabled={readOnly || desktopThreadsBusy}
                  onClick={() => void refreshDesktopThreads()}
                  title={t("agents.desktopThreadsRefresh")}
                  type="button"
                  aria-label={t("agents.desktopThreadsRefresh")}
                >
                  <RefreshCw className={desktopThreadsBusy ? "spin" : ""} size={15} />
                </button>
              </div>
              {desktopThreadsError ? (
                <small className="directory-notice error">{desktopThreadsError}</small>
              ) : (
                <small>
                  {desktopThreads.length === 0 && !desktopThreadsBusy
                    ? t("agents.desktopThreadsEmpty")
                    : t("agents.desktopThreadHelp")}
                </small>
              )}
            </label>
          )}
          <label className="field">
            <span>{t("agents.workDir")}</span>
            <div className="directory-input-row">
              <input
                disabled={readOnly || desktopRuntime}
                value={draft.work_dir ?? ""}
                onChange={(event) => onUpdate("work_dir", event.target.value)}
              />
              <button
                className="ghost-action icon-action"
                disabled={readOnly || desktopRuntime || directoryBusy === "select"}
                onClick={selectWorkDir}
                title={t("agents.selectWorkDir")}
                type="button"
                aria-label={t("agents.selectWorkDir")}
              >
                <FolderOpen size={15} />
              </button>
            </div>
            {directoryNotice && <small className="directory-notice" role="status">{directoryNotice}</small>}
          </label>
          <label className="field">
            <span>{t("agents.workspaceMode")}</span>
            <select
              disabled={readOnly || desktopRuntime}
              value={draft.workspace_mode || "shared"}
              onChange={(event) => onUpdate("workspace_mode", event.target.value as AgentInstance["workspace_mode"])}
            >
              <option value="shared">{t("agents.workspaceModeShared")}</option>
              <option value="worktree">{t("agents.workspaceModeWorktree")}</option>
            </select>
            <small>{draft.workspace_mode === "worktree" ? t("agents.workspaceModeWorktreeHelp") : t("agents.workspaceModeSharedHelp")}</small>
          </label>
          {draft.workspace_mode === "worktree" && !desktopRuntime && (
            <label className="field">
              <span>{t("agents.worktreeBaseRef")}</span>
              <input
                disabled={readOnly}
                value={draft.worktree_base_ref ?? ""}
                onChange={(event) => onUpdate("worktree_base_ref", event.target.value)}
                placeholder="HEAD"
              />
              <small>{t("agents.worktreeBaseRefHelp")}</small>
            </label>
          )}
          <label className="field">
            <span>{t("agents.sessionBackend")}</span>
            <select
              disabled={readOnly || desktopRuntime}
              value={draft.session_backend || "structured"}
              onChange={(event) => onUpdate("session_backend", event.target.value as AgentInstance["session_backend"])}
            >
              <option value="structured">{t("agents.sessionBackendStructured")}</option>
              <option value="tmux">{t("agents.sessionBackendTmux")}</option>
            </select>
            <small>{draft.session_backend === "tmux" ? t("agents.sessionBackendTmuxHelp") : t("agents.sessionBackendStructuredHelp")}</small>
          </label>
          <label className="field">
            <span>{t("agents.memoryScope")}</span>
            <input
              disabled={readOnly}
              value={draft.memory_scope ?? ""}
              onChange={(event) => onUpdate("memory_scope", event.target.value)}
            />
          </label>
          <div className="field wide agent-prompt-field">
            <div className="field-label-row">
              <span>{promptView === "preview" ? t("agents.fullInjectedPrompt") : t("agents.systemPrompt")}</span>
              <div className="markdown-mode-toggle" role="group" aria-label={t("agents.markdownViewMode")}>
                <button
                  className={promptView === "edit" ? "active" : ""}
                  type="button"
                  aria-pressed={promptView === "edit"}
                  onClick={() => setPromptView("edit")}
                >
                  <Pencil size={13} />
                  {t("agents.markdownEdit")}
                </button>
                <button
                  className={promptView === "preview" ? "active" : ""}
                  type="button"
                  aria-pressed={promptView === "preview"}
                  onClick={() => setPromptView("preview")}
                >
                  <Eye size={14} />
                  {t("agents.markdownPreview")}
                </button>
              </div>
            </div>
            {promptView === "edit" ? (
              <textarea
                className="markdown-editor"
                disabled={readOnly}
                rows={8}
                value={draft.system_prompt ?? ""}
                onChange={(event) => onUpdate("system_prompt", event.target.value)}
                placeholder={t("agents.markdownPlaceholder")}
              />
            ) : (
              <MarkdownPreview
                className="injected-prompt-preview agent-prompt-full-preview"
                content={injectedPrompt}
                empty={t("agents.injectedPromptEmpty")}
              />
            )}
            <small>{promptView === "preview" ? t("agents.injectedPromptHelp") : t("agents.markdownHelp")}</small>
          </div>
          {promptView === "edit" && (
            <div className="field wide">
              <span>{t("agents.injectedPrompt")}</span>
              <MarkdownPreview
                className="injected-prompt-preview"
                content={injectedPrompt}
                empty={t("agents.injectedPromptEmpty")}
              />
              <small>{t("agents.injectedPromptHelp")}</small>
            </div>
          )}
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Link2 size={17} />
          <h3>{t("agents.routing")}</h3>
        </header>
        <div className="field-grid">
          {frameworkRuntimeID && (
            <div className={`agent-route-notice${authStatus?.state === "authenticated" ? " success" : ""}`}>
              <strong className="agent-route-notice-title">
                {authBusy ? (
                  <><RefreshCw className="spin" size={14} /> {t("agents.loginChecking")} {runtimeLabel(draft.runtime_id)}</>
                ) : authStatus?.state === "authenticated" ? (
                  <><CheckCircle2 size={14} /> {t("agents.localLoginReadyTitle")}</>
                ) : (
                  <><LogIn size={14} /> {t("agents.runtimeNotReadyTitle")}</>
                )}
              </strong>
              <span>
                {authStatus?.state === "authenticated"
                  ? t("agents.localLoginReady")
                  : authStatus?.state === "unauthenticated"
                    ? t("agents.runtimeLoggedOut")
                    : authStatus && !authStatus.login_supported
                      ? t("agents.runtimeProviderRequired")
                      : t("agents.runtimeAuthUnknown")}
              </span>
              {authStatus?.auto_refresh_supported && authStatus.expires_at && (
                <span>
                  {t("agents.loginAutoRefresh")} {t("agents.loginExpiresAt")} {new Date(authStatus.expires_at).toLocaleString()}
                </span>
              )}
              {authStatus?.state !== "authenticated" && (
                <div className="agent-route-actions">
                  {authStatus?.login_supported && (
                    <button className="action" disabled={authBusy || Boolean(loginBusy)} onClick={startFrameworkLogin} type="button">
                      <LogIn size={14} />
                      {loginBusy === "start" ? t("agents.loginStarting") : t("agents.loginFramework")}
                    </button>
                  )}
                  <button className="ghost-action" disabled={authBusy} onClick={refreshFrameworkAuth} type="button">
                    <RefreshCw className={authBusy ? "spin" : ""} size={14} />
                    {t("agents.refreshLoginStatus")}
                  </button>
                </div>
              )}
              {loginResult?.login_url && authStatus?.state !== "authenticated" && (
                <div className="agent-login-result">
                  <span>{t("agents.loginLinkReady")}</span>
                  <a
                    href={loginResult.login_url}
                    onClick={(event) => {
                      if (!isDesktopApp()) return;
                      event.preventDefault();
                      void openExternalURL(loginResult.login_url).catch((err) => {
                        setAuthNotice(err instanceof Error ? err.message : String(err));
                      });
                    }}
                    rel="noreferrer"
                    target="_blank"
                  >
                    <ExternalLink size={14} />
                    {t("agents.openLoginLink")}
                  </a>
                  {loginResult.verification_code && (
                    <span>{t("agents.verificationCode")} <code>{loginResult.verification_code}</code></span>
                  )}
                  {loginResult.input_required && (
                    <div className="agent-login-code-row">
                      <input
                        aria-label={t("agents.loginCodePlaceholder")}
                        onChange={(event) => setLoginCode(event.target.value)}
                        placeholder={t("agents.loginCodePlaceholder")}
                        value={loginCode}
                      />
                      <button className="ghost-action" disabled={!loginCode.trim() || Boolean(loginBusy)} onClick={completeFrameworkLogin} type="button">
                        {loginBusy === "complete" ? t("common.save") : t("agents.submitLoginCode")}
                      </button>
                    </div>
                  )}
                </div>
              )}
              {authNotice && <span className="agent-auth-detail">{authNotice}</span>}
            </div>
          )}
          <label className="field">
            <span>{t("agents.defaultModel")}</span>
            <div className="directory-input-row">
            <select
              aria-label={t("agents.defaultModel")}
              disabled={readOnly || localRuntimeSettingsBusy}
              value={showTypedModel ? TYPED_MODEL : draft.default_model ?? ""}
              onChange={(event) => {
                if (event.target.value === TYPED_MODEL) {
                  setTypingModel(true);
                  return;
                }
                setTypingModel(false);
                onUpdate("default_model", event.target.value);
              }}
            >
              <option value="">{modelPlaceholder}</option>
              {modelOptions.map((model) => (
                <option key={model} value={model}>
                  {modelLabels.has(model) ? `${model} · ${modelLabels.get(model)}` : model}
                </option>
              ))}
              <option value={TYPED_MODEL}>{t("agents.defaultModelTyped")}</option>
            </select>
            <button type="button" className="ghost-action icon-action" disabled={localRuntimeSettingsBusy}
              aria-label={t("agents.refreshRuntimeSettings")} title={t("agents.refreshRuntimeSettings")}
              onClick={() => setCatalogReload((value) => value + 1)}>
              <RefreshCw size={15} className={localRuntimeSettingsBusy ? "spin" : ""} />
            </button>
            </div>
            {showTypedModel && (
              <input
                aria-label={t("agents.defaultModelTypedPlaceholder")}
                disabled={readOnly}
                placeholder={t("agents.defaultModelTypedPlaceholder")}
                value={draft.default_model ?? ""}
                onChange={(event) => onUpdate("default_model", event.target.value.trim())}
              />
            )}
            <small>{defaultModelStatus}</small>
            {draft.runtime_id && (
              <div className="agent-model-test">
                <button type="button" className="ghost-action" disabled={currentModelTest?.busy}
                  title={t("frameworks.health.testHint")} onClick={() => void testModel()}>
                  <Play size={14} />{t("agents.testModel")}
                </button>
                {currentModelTest && <FrameworkSmokeResultView result={currentModelTest.result} busy={currentModelTest.busy} />}
              </div>
            )}
          </label>
          <label className="field">
            <span>{t("agents.defaultReasoningEffort")}</span>
            <select disabled={readOnly || !settingsReady || (reasoningOptions.length === 0 && !draft.default_reasoning_effort)} aria-invalid={invalidEffort} value={draft.default_reasoning_effort ?? ""} onChange={(event) => onUpdate("default_reasoning_effort", event.target.value)}>
              <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
              {draft.default_reasoning_effort && !reasoningOptions.includes(draft.default_reasoning_effort) && (
                <option value={draft.default_reasoning_effort} disabled>{draft.default_reasoning_effort}{invalidEffort ? ` · ${t("agents.runtimeSettingUnsupported")}` : ""}</option>
              )}
              {reasoningOptions.map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
            <small>{t("agents.defaultReasoningEffortHelp")}</small>
          </label>
          <label className="field">
            <span>{t("agents.defaultServiceTier")}</span>
            <select disabled={readOnly || !settingsReady || (availableServiceTiers.length === 0 && !draft.default_service_tier)} aria-invalid={invalidTier} value={draft.default_service_tier ?? ""} onChange={(event) => onUpdate("default_service_tier", event.target.value)}>
              <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
              {draft.default_service_tier && !availableServiceTiers.includes(draft.default_service_tier) && (
                <option value={draft.default_service_tier} disabled>{serviceTierLabel(draft.default_service_tier)}{invalidTier ? ` · ${t("agents.runtimeSettingUnsupported")}` : ""}</option>
              )}
              {availableServiceTiers.map((value) => <option key={value} value={value}>{serviceTierLabel(value)}</option>)}
            </select>
            <small>{t("agents.defaultServiceTierHelp")}</small>
          </label>
          <label className="field">
            <span>{t("agents.defaultApprovalMode")}</span>
            <select disabled={readOnly || (approvalOptions.length === 0 && !draft.default_approval_mode)} aria-invalid={invalidApproval} value={draft.default_approval_mode ?? ""} onChange={(event) => onUpdate("default_approval_mode", event.target.value)}>
              <option value="">{t("agents.defaultApprovalModePlaceholder")}</option>
              {invalidApproval && <option value={draft.default_approval_mode} disabled>{draft.default_approval_mode} · {t("agents.runtimeSettingUnsupported")}</option>}
              {approvalOptions.map((value) => <option key={value} value={value}>{t(approvalModeLabelKey(value))}</option>)}
            </select>
            <small>{t("agents.defaultApprovalModeHelp")}</small>
          </label>
          {invalidSettings && <p className="directory-notice error wide" role="alert">{t("agents.runtimeSettingsInvalid")}</p>}
        </div>
      </section>

      <section className="agent-section">
        <header>
          <Link2 size={17} />
          <h3>{t("nav.connect")}</h3>
        </header>
        <p className="subtle-copy">{t("agents.connectBindingHelp")}</p>
        <div className="agent-picker-grid">
          <div className="mapping-card">
            <strong>
              <Cable size={13} /> {t("agents.boundChannels")} ({selectedChannelIDs.length})
            </strong>
            {channelOptions.length === 0 && <span className="muted">{t("connect.noChannels")}</span>}
            <div className="provider-chip-row agent-channel-options">
              {channelOptions.map((channel) => {
                const selected = selectedChannelIDs.includes(channel.id);
                const displayName = channel.bot_name || channel.name;
                const subtitle = [
                  channel.bot_name && channel.name !== channel.bot_name ? channel.name : "",
                  channel.type,
                  (channel.agent_name || channel.agent_id) && !selected ? channel.agent_name || channel.agent_id : "",
                ]
                  .filter(Boolean)
                  .join(" · ");
                return (
                  <button
                    key={channel.id}
                    className={`agent-channel-option ${selected ? "selected" : ""}`}
                    disabled={readOnly}
                    onClick={() => onToggleChannel(channel.id)}
                    title={subtitle ? `${displayName} · ${subtitle}` : displayName}
                    type="button"
                  >
                    <ChannelLogo channel={channel} />
                    <ChannelAvatar channel={channel} size="small" />
                    <span className="agent-channel-copy">
                      <strong>{displayName}</strong>
                      {subtitle && <small>{subtitle}</small>}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
          <div className="mapping-card">
            <strong>
              <Zap size={13} /> {t("agents.boundTriggers")} ({selectedTriggerIDs.length})
            </strong>
            {triggerOptions.length === 0 && <span className="muted">{t("connect.noTriggers")}</span>}
            <div className="provider-chip-row">
              {triggerOptions.map((trigger) => (
                <button
                  key={trigger.id}
                  className={`status-badge ${selectedTriggerIDs.includes(trigger.id) ? "success" : ""}`}
                  disabled={readOnly}
                  onClick={() => onToggleTrigger(trigger.id)}
                  type="button"
                >
                  {trigger.name} · {trigger.kind}
                  {(trigger.agent_name || trigger.agent_id) && !selectedTriggerIDs.includes(trigger.id)
                    ? ` · ${trigger.agent_name || trigger.agent_id}`
                    : ""}
                </button>
              ))}
            </div>
          </div>
        </div>
      </section>

      <AgentConversationOptions
        privateMode={draft.private_chat_mode}
        groupMode={draft.group_chat_mode}
        readOnly={readOnly}
        onPrivateModeChange={(value) => onUpdate("private_chat_mode", value)}
        onGroupModeChange={(value) => onUpdate("group_chat_mode", value)}
        t={t}
      />

      <section className="agent-section">
        <header>
          <Bot size={17} />
          <h3>{t("agents.tools")}</h3>
        </header>
        <div className="agent-picker-grid">
          <Picker
            title={t("agents.mcpServers")}
            items={mcpOptions}
            selected={draft.mcp_servers ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("mcp_servers", next)}
            empty={t("mcp.empty")}
          />
          <Picker
            title={t("agents.skills")}
            items={skillOptions}
            selected={draft.skills ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("skills", next)}
            empty={t("skills.empty")}
          />
          <Picker
            title={t("agents.clis")}
            items={cliOptions.map((option) => option.id)}
            labels={Object.fromEntries(cliOptions.map((option) => [option.id, option.name]))}
            selected={draft.clis ?? []}
            readOnly={readOnly}
            onChange={(next) => onUpdate("clis", next)}
            unavailableItems={cliOptions.filter((option) => !option.installed).map((option) => option.id)}
            unavailableTitle={t("agents.cliInstallHint")}
            onUnavailableClick={onInstallCLI}
            empty={t("agents.clisEmpty")}
          />
        </div>
      </section>

      <div className="agent-drawer-actions">
        <button className="ghost-action danger-action" disabled={!draft.id || readOnly || busy === "delete"} onClick={onDelete}>
          <Trash2 size={15} />
          {t("common.delete")}
        </button>
        <button className="action" disabled={!canSave || busy === "save" || invalidSettings || unverifiedSettings || localRuntimeSettingsBusy} onClick={onSave}>
          <Save size={15} />
          {drawerMode === "create" ? t("agents.create") : t("common.save")}
        </button>
      </div>
    </>
  );
}

function desktopThreadLabel(thread: AgentSession): string {
  const parts = [thread.title || thread.session_id, thread.project_dir || ""];
  if (thread.last_active_at) {
    const timestamp = new Date(thread.last_active_at);
    if (!Number.isNaN(timestamp.getTime())) parts.push(timestamp.toLocaleString());
  }
  return parts.filter(Boolean).join(" · ");
}
