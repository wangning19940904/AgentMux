import { Bot, Cable, CheckCircle2, ExternalLink, Eye, FolderOpen, Link2, LogIn, Pencil, RefreshCw, Save, Trash2, Workflow, Zap } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  activeRemoteID,
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
  type Provider,
  type ProviderRoute,
  type Trigger,
} from "../../api";
import { ChannelAvatar } from "../../ChannelAvatar";

import {
  type DrawerMode,
  activeRouteForTool,
  approvalModeLabelKey,
  approvalModesForRuntime,
  composeInjectedPrompt,
  providerModelOptions,
  routeToolForRuntime,
  routeToolOptionsForRuntime,
  runtimeLabel,
  runtimeProviderOptions,
  runtimeOptionValues,
  serviceTierLabel,
  workDirErrorMessage,
} from "./agentUtils";
import { MarkdownPreview, Picker } from "./widgets";
import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";

export type CLIOption = { id: string; name: string; note?: string; installed: boolean };

export function AgentForm({
  busy,
  canSave,
  activeRoutes,
  channelOptions,
  cliOptions,
  compatibleProviders,
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
  activeRoutes: ProviderRoute[];
  channelOptions: Channel[];
  cliOptions: CLIOption[];
  compatibleProviders: Provider[];
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
  const [loginBusy, setLoginBusy] = useState("");
  const [loginResult, setLoginResult] = useState<FrameworkLoginResult | null>(null);
  const [loginCode, setLoginCode] = useState("");
  const [desktopThreads, setDesktopThreads] = useState<AgentSession[]>([]);
  const [desktopThreadsBusy, setDesktopThreadsBusy] = useState(false);
  const [desktopThreadsError, setDesktopThreadsError] = useState("");
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
  const selectedRouteTool = draft.provider_tool || routeToolForRuntime(draft.runtime_id);
  const desktopRuntime = draft.runtime_id === "codex-app";
  const frameworkRuntimeID = desktopRuntime ? "codex" : draft.runtime_id;
  const routeToolOptions = routeToolOptionsForRuntime(draft.runtime_id);
  const activeRoute = activeRouteForTool(activeRoutes, selectedRouteTool);
  const activeRouteProvider = activeRoute?.configured ? activeRoute.provider_name || activeRoute.provider_id || "" : "";
	const usingLocalLogin = !draft.provider_id && !activeRouteProvider;
  const overrideProvider = compatibleProviders.find((provider) => provider.id === draft.provider_id);
  const routeProvider = activeRoute?.configured
    ? compatibleProviders.find((provider) => provider.id === activeRoute.provider_id)
    : undefined;
  const modelProvider = overrideProvider ?? routeProvider;
	const localCapabilities = localRuntimeSettings?.capabilities;
	const modelOptions = modelProvider
		? providerModelOptions(modelProvider)
		: usingLocalLogin
			? runtimeOptionValues(localCapabilities?.models)
			: [];
	const reasoningOptions = modelProvider
		? runtimeProviderOptions(modelProvider, "supported_reasoning_efforts")
		: usingLocalLogin
			? runtimeOptionValues(localCapabilities?.reasoning_efforts)
			: [];
	const serviceTierOptions = modelProvider
		? runtimeProviderOptions(modelProvider, "supported_service_tiers")
		: usingLocalLogin
			? runtimeOptionValues(localCapabilities?.service_tiers)
			: [];
  const providerStatus = draft.provider_id
    ? `${t("agents.providerOverrideActive")} ${overrideProvider?.name || draft.provider_id}`
    : activeRouteProvider
      ? `${t("agents.activeRouteProvider")} ${runtimeLabel(selectedRouteTool)} -> ${activeRouteProvider}`
      : `${t("agents.noActiveRouteProvider")} ${runtimeLabel(selectedRouteTool)}. ${t("agents.localLoginActive")}`;
  const defaultModelStatus =
	localRuntimeSettingsBusy && usingLocalLogin
		? t("agents.defaultModelLoading")
		: localRuntimeSettingsError && usingLocalLogin
			? localRuntimeSettingsError
			: modelOptions.length > 0
				? usingLocalLogin ? t("agents.defaultModelLocalLogin") : t("agents.defaultModelHelp")
      : modelProvider
        ? t("agents.defaultModelUnavailable")
        : usingLocalLogin
					? authStatus?.state === "authenticated"
						? t("agents.defaultModelRuntimeUnavailable")
						: t("agents.defaultModelLoginRequired")
          : t("agents.defaultModelNoProvider");

  useEffect(() => {
    if (readOnly || !draft.runtime_id) return;
    const nextRouteTool = routeToolForRuntime(draft.runtime_id);
    if (draft.provider_tool !== nextRouteTool) onUpdate("provider_tool", nextRouteTool);
  }, [draft.provider_tool, draft.runtime_id, onUpdate, readOnly]);

  useEffect(() => {
    if (readOnly || !draft.default_model) return;
		if (usingLocalLogin && (localRuntimeSettingsBusy || localRuntimeSettings === null)) return;
    if (modelOptions.length === 0 || !modelOptions.includes(draft.default_model)) {
      onUpdate("default_model", "");
    }
  }, [draft.default_model, localRuntimeSettings, localRuntimeSettingsBusy, modelOptions.join("\u0000"), onUpdate, readOnly, usingLocalLogin]);

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
    void api.codexDesktopThreads(draft.target_id)
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
    if (!usingLocalLogin || !frameworkRuntimeID) {
      setAuthBusy(false);
      return () => {
        active = false;
      };
    }
    setAuthBusy(true);
    void api.frameworkAuth(frameworkRuntimeID, draft.target_id)
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
  }, [draft.target_id, frameworkRuntimeID, usingLocalLogin]);

	useEffect(() => {
		let active = true;
		setLocalRuntimeSettings(null);
		setLocalRuntimeSettingsError("");
		if (!usingLocalLogin || !draft.runtime_id || authStatus?.state !== "authenticated") {
			setLocalRuntimeSettingsBusy(false);
			return () => {
				active = false;
			};
		}
		setLocalRuntimeSettingsBusy(true);
		void api.frameworkRuntimeSettings(draft.runtime_id, draft.work_dir ?? "", draft.target_id)
			.then((settings) => {
				if (active) setLocalRuntimeSettings(settings);
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
	}, [authStatus?.state, draft.runtime_id, draft.target_id, usingLocalLogin]);

  useEffect(() => {
    if (!loginResult || !usingLocalLogin || !frameworkRuntimeID) return;
    let active = true;
    const interval = window.setInterval(() => {
      void api.frameworkAuth(frameworkRuntimeID, draft.target_id).then((status) => {
        if (!active) return;
        setAuthStatus(status);
        if (status.state === "authenticated") {
          setAuthNotice(t("agents.loginComplete"));
          window.clearInterval(interval);
        }
      }).catch(() => undefined);
    }, 2000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [draft.target_id, frameworkRuntimeID, loginResult?.session_id, usingLocalLogin, t]);

  async function refreshFrameworkAuth() {
    if (!frameworkRuntimeID) return;
    setAuthBusy(true);
    setAuthNotice("");
    try {
      const status = await api.frameworkAuth(frameworkRuntimeID, draft.target_id);
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
      setDesktopThreads((await api.codexDesktopThreads(draft.target_id)) ?? []);
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
      setLoginResult(await api.startFrameworkLogin(frameworkRuntimeID, draft.target_id));
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
      await api.completeFrameworkLogin(loginResult.session_id, loginCode.trim(), draft.target_id);
      setAuthNotice(t("agents.loginCodeSubmitted"));
    } catch (err) {
      setAuthNotice(err instanceof Error ? err.message : String(err));
    } finally {
      setLoginBusy("");
    }
  }

  async function selectWorkDir() {
    setDirectoryNotice("");
    if ((draft.target_id && draft.target_id !== "local") || activeRemoteID()) {
      setRemoteDirectoryPickerOpen(true);
      return;
    }
    if (!draft.target_id && activeMachineScope() === "all") {
      setDirectoryNotice(t("agents.bulkWorkDirHint"));
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
          targetID={draft.target_id}
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
          <label className="field">
            <span>{t("agents.runtime")}</span>
            <select
              disabled={readOnly || (drawerMode === "create" && runtimeOptions.length === 0)}
              value={draft.runtime_id}
              onChange={(event) => onUpdate("runtime_id", event.target.value)}
            >
              {runtimeOptions.length === 0 && <option value="">{t("agents.noInstalledRuntime")}</option>}
              {drawerMode === "edit" && draft.runtime_id && !runtimeOptions.includes(draft.runtime_id) && (
                <option value={draft.runtime_id} disabled>
                  {runtimeLabel(draft.runtime_id)} ({t("gateway.frameworkNotInstalled")})
                </option>
              )}
              {runtimeOptions.map((runtime) => (
                <option key={runtime} value={runtime}>
                  {runtimeLabel(runtime)}
                </option>
              ))}
            </select>
          </label>
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
            {directoryNotice && <small className="directory-notice">{directoryNotice}</small>}
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
          <label className="field">
            <span>{t("agents.routeTool")}</span>
            <select
              disabled={readOnly || routeToolOptions.length <= 1}
              value={selectedRouteTool}
              onChange={(event) => onUpdate("provider_tool", event.target.value)}
            >
              {routeToolOptions.map((tool) => (
                <option key={tool} value={tool}>
                  {runtimeLabel(tool)}
                </option>
              ))}
            </select>
            <small>{t("agents.routeToolFollowsRuntime")}</small>
          </label>
          <label className="field">
            <span>{t("agents.providerOverride")}</span>
            <select disabled={readOnly} value={draft.provider_id ?? ""} onChange={(event) => onUpdate("provider_id", event.target.value)}>
              <option value="">{activeRouteProvider ? t("agents.followActiveRoute") : t("agents.useLocalLogin")}</option>
              {compatibleProviders.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name}
                </option>
              ))}
            </select>
            <small>{providerStatus}</small>
          </label>
          {usingLocalLogin && (
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
              {authStatus?.state !== "authenticated" && (
                <div className="agent-route-actions">
                  {authStatus?.login_supported && (
                    <button className="action" disabled={authBusy || Boolean(loginBusy)} onClick={startFrameworkLogin} type="button">
                      <LogIn size={14} />
                      {loginBusy === "start" ? t("agents.loginStarting") : t("agents.loginFramework")}
                    </button>
                  )}
                  <button className="ghost-action" onClick={() => { window.location.hash = "#gateway"; }} type="button">
                    {t("agents.configureProvider")}
                  </button>
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
            <select
				disabled={readOnly || localRuntimeSettingsBusy || modelOptions.length === 0}
              value={draft.default_model ?? ""}
              onChange={(event) => onUpdate("default_model", event.target.value)}
            >
				<option value="">{t(usingLocalLogin ? "agents.defaultModelRuntimePlaceholder" : "agents.defaultModelPlaceholder")}</option>
              {modelOptions.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </select>
            <small>{defaultModelStatus}</small>
          </label>
          <label className="field">
            <span>{t("agents.defaultReasoningEffort")}</span>
            <select disabled={readOnly || reasoningOptions.length === 0} value={draft.default_reasoning_effort ?? ""} onChange={(event) => onUpdate("default_reasoning_effort", event.target.value)}>
              <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
              {reasoningOptions.map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
            <small>{t("agents.defaultReasoningEffortHelp")}</small>
          </label>
          <label className="field">
            <span>{t("agents.defaultServiceTier")}</span>
            <select disabled={readOnly || serviceTierOptions.length === 0} value={draft.default_service_tier ?? ""} onChange={(event) => onUpdate("default_service_tier", event.target.value)}>
              <option value="">{t("agents.defaultRuntimeSettingPlaceholder")}</option>
              {serviceTierOptions.map((value) => <option key={value} value={value}>{serviceTierLabel(value)}</option>)}
            </select>
            <small>{t("agents.defaultServiceTierHelp")}</small>
          </label>
          <label className="field">
            <span>{t("agents.defaultApprovalMode")}</span>
            <select disabled={readOnly || approvalModesForRuntime(draft.runtime_id).length === 0} value={draft.default_approval_mode ?? ""} onChange={(event) => onUpdate("default_approval_mode", event.target.value)}>
              <option value="">{t("agents.defaultApprovalModePlaceholder")}</option>
              {approvalModesForRuntime(draft.runtime_id).map((value) => <option key={value} value={value}>{t(approvalModeLabelKey(value))}</option>)}
            </select>
            <small>{t("agents.defaultApprovalModeHelp")}</small>
          </label>
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
        <button className="action" disabled={!canSave || busy === "save"} onClick={onSave}>
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
