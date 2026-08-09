import { Component, Suspense, lazy, useEffect, useMemo, useState } from "react";
import type { ErrorInfo, ReactNode } from "react";
import {
  Activity,
  Blocks,
  Bot,
  Boxes,
  Brain,
  Cable,
  ChevronDown,
  Command,
  Coffee,
  DatabaseZap,
  ExternalLink,
  Gauge,
  Languages,
  LayoutGrid,
  Moon,
  PanelLeft,
  PanelTop,
  Search,
  ServerCog,
  MessageSquareText,
  Settings,
  ShieldCheck,
  Sparkles,
  Sun,
  Workflow,
  Video,
} from "lucide-react";
import { RemoteTargetSelector } from "./RemoteTargetSelector";
import { MeetingControls } from "./MeetingControls";
import { MeetingProvider } from "./MeetingContext";

// Panels load lazily so the initial bundle only carries the shell; each panel
// chunk downloads when its tab is first selected.
const ProvidersPanel = lazy(() => import("./panels/ProvidersPanel").then((m) => ({ default: m.ProvidersPanel })));
const UsagePanel = lazy(() => import("./panels/UsagePanel").then((m) => ({ default: m.UsagePanel })));
const ObservabilityPanel = lazy(() => import("./panels/ObservabilityPanel").then((m) => ({ default: m.ObservabilityPanel })));
const AgentsPanel = lazy(() => import("./panels/AgentsPanel").then((m) => ({ default: m.AgentsPanel })));
const ConnectPanel = lazy(() => import("./panels/ConnectPanel").then((m) => ({ default: m.ConnectPanel })));
const FrameworksPanel = lazy(() => import("./panels/FrameworksPanel").then((m) => ({ default: m.FrameworksPanel })));
const GatewayPanel = lazy(() => import("./panels/GatewayPanel").then((m) => ({ default: m.GatewayPanel })));
const OverviewPanel = lazy(() => import("./panels/OverviewPanel").then((m) => ({ default: m.OverviewPanel })));
const MemoryPanel = lazy(() => import("./panels/MemoryPanel").then((m) => ({ default: m.MemoryPanel })));
const SkillsPanel = lazy(() => import("./panels/SkillsPanel").then((m) => ({ default: m.SkillsPanel })));
const MCPPanel = lazy(() => import("./panels/MCPPanel").then((m) => ({ default: m.MCPPanel })));
const GuardPanel = lazy(() => import("./panels/GuardPanel").then((m) => ({ default: m.GuardPanel })));
const SessionsPanel = lazy(() => import("./panels/SessionsPanel").then((m) => ({ default: m.SessionsPanel })));
const MenuBarPanel = lazy(() => import("./panels/MenuBarPanel").then((m) => ({ default: m.MenuBarPanel })));
const RemoteHostsPanel = lazy(() => import("./panels/RemoteHostsPanel").then((m) => ({ default: m.RemoteHostsPanel })));
const MeetingsPanel = lazy(() => import("./panels/MeetingsPanel").then((m) => ({ default: m.MeetingsPanel })));
import { I18nProvider, Language, ThemeMode, useI18n } from "./i18n";
import {
  api,
  getLaunchAtLogin,
  isDesktopApp,
  KeepAwakeStatus,
  LaunchAtLoginStatus,
  openLocalWebUI,
  setLaunchAtLogin,
} from "./api";

type Tab =
  | "overview"
  | "agents"
  | "connect"
  | "frameworks"
  | "observability"
  | "usage"
  | "menubar"
  | "machines"
  | "providers"
  | "gateway"
  | "memory"
  | "sessions"
  | "meetings"
  | "skills"
  | "mcp"
  | "guard";

type NavItem = { id: Tab; labelKey: string; icon: typeof LayoutGrid };
type NavGroup = { id: string; labelKey: string; icon: typeof LayoutGrid; items: NavItem[] };

const OVERVIEW_ITEM: NavItem = { id: "overview", labelKey: "nav.overview", icon: LayoutGrid };

const NAV_GROUPS: NavGroup[] = [
  {
    id: "agents",
    labelKey: "nav.group.agents",
    icon: Bot,
    items: [
      { id: "agents", labelKey: "nav.agents", icon: Bot },
      { id: "frameworks", labelKey: "nav.frameworks", icon: Blocks },
      { id: "skills", labelKey: "nav.skills", icon: Sparkles },
      { id: "mcp", labelKey: "nav.mcp", icon: Boxes },
      { id: "memory", labelKey: "nav.memory", icon: Brain },
      { id: "sessions", labelKey: "nav.sessions", icon: MessageSquareText },
      { id: "meetings", labelKey: "nav.meetings", icon: Video },
    ],
  },
  {
    id: "connectivity",
    labelKey: "nav.group.connectivity",
    icon: Cable,
    items: [
      { id: "connect", labelKey: "nav.connect", icon: Cable },
      { id: "gateway", labelKey: "nav.gateway", icon: Workflow },
      { id: "providers", labelKey: "nav.providers", icon: DatabaseZap },
    ],
  },
  {
    id: "operations",
    labelKey: "nav.group.operations",
    icon: Activity,
    items: [
      { id: "observability", labelKey: "nav.observability", icon: Activity },
      { id: "usage", labelKey: "nav.usage", icon: Gauge },
      { id: "guard", labelKey: "nav.guard", icon: ShieldCheck },
    ],
  },
  {
    id: "system",
    labelKey: "nav.group.system",
    icon: PanelTop,
    items: [
      { id: "machines", labelKey: "nav.machines", icon: ServerCog },
      { id: "menubar", labelKey: "nav.menubar", icon: PanelTop },
    ],
  },
];

const TABS: NavItem[] = [OVERVIEW_ITEM, ...NAV_GROUPS.flatMap((group) => group.items)];

const THEME_OPTIONS: { id: ThemeMode; labelKey: string; icon: typeof Sun }[] = [
  { id: "system", labelKey: "theme.system", icon: PanelLeft },
  { id: "light", labelKey: "theme.light", icon: Sun },
  { id: "dark", labelKey: "theme.dark", icon: Moon },
];

const LANG_OPTIONS: { id: Language; labelKey: string }[] = [
  { id: "en", labelKey: "lang.en" },
  { id: "zh", labelKey: "lang.zh" },
];

function initialLanguage(): Language {
  const stored = localStorage.getItem("agentmux:language");
  if (stored === "zh" || stored === "en") return stored;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

function initialThemeMode(): ThemeMode {
  const stored = localStorage.getItem("agentmux:theme");
  if (stored === "system" || stored === "light" || stored === "dark") return stored;
  return "system";
}

function tabFromHash(): Tab | null {
  const value = window.location.hash.replace(/^#\/?/, "").split(/[/?]/, 1)[0];
  if (value === "tools") return "skills";
  return TABS.some((item) => item.id === value) ? value as Tab : null;
}

function initialTab(): Tab {
  return tabFromHash() ?? "agents";
}

export function App() {
  const [tab, setTab] = useState<Tab>(initialTab);
  const [language, setLanguage] = useState<Language>(initialLanguage);
  const [themeMode, setThemeMode] = useState<ThemeMode>(initialThemeMode);

  useEffect(() => {
    localStorage.setItem("agentmux:language", language);
    document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  }, [language]);

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const applyTheme = () => {
      const resolved = themeMode === "system" ? (media.matches ? "dark" : "light") : themeMode;
      document.documentElement.dataset.theme = resolved;
      document.documentElement.dataset.themeMode = themeMode;
    };

    localStorage.setItem("agentmux:theme", themeMode);
    applyTheme();
    media.addEventListener("change", applyTheme);
    return () => media.removeEventListener("change", applyTheme);
  }, [themeMode]);

  useEffect(() => {
    const onHashChange = () => {
      const hashTab = tabFromHash();
      if (hashTab) setTab(hashTab);
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  // Every tab owns a hash route (#tab or #tab/subview); panels with internal
  // sub-navigation (observability) extend the same scheme.
  function selectTab(next: Tab) {
    setTab(next);
    if (next === "observability") {
      if (!window.location.hash.startsWith("#observability")) window.location.hash = "#observability/overview";
      return;
    }
    window.location.hash = `#${next}`;
  }

  return (
    <I18nProvider language={language}>
      <MeetingProvider>
        <Shell
          tab={tab}
          setTab={selectTab}
          language={language}
          setLanguage={setLanguage}
          themeMode={themeMode}
          setThemeMode={setThemeMode}
        />
      </MeetingProvider>
    </I18nProvider>
  );
}

function Shell({
  tab,
  setTab,
  language,
  setLanguage,
  themeMode,
  setThemeMode,
}: {
  tab: Tab;
  setTab: (tab: Tab) => void;
  language: Language;
  setLanguage: (language: Language) => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}) {
  const { t } = useI18n();
  const active = useMemo(() => TABS.find((item) => item.id === tab) ?? TABS[0], [tab]);
  const groupOfActive = useMemo(
    () => NAV_GROUPS.find((group) => group.items.some((item) => item.id === tab))?.id ?? null,
    [tab],
  );
  const [openGroups, setOpenGroups] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(NAV_GROUPS.map((group) => [group.id, true])),
  );

  useEffect(() => {
    if (groupOfActive) {
      setOpenGroups((current) => (current[groupOfActive] ? current : { ...current, [groupOfActive]: true }));
    }
  }, [groupOfActive]);

  function toggleGroup(id: string) {
    setOpenGroups((current) => ({ ...current, [id]: !current[id] }));
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <img className="brand-logo" src="/agentmux-logo.png" alt="" aria-hidden="true" />
          <div>
            <strong>AgentMux</strong>
            <span>Command Surface</span>
          </div>
        </div>

        <nav className="nav" aria-label="Primary">
          <button
            className={`nav-item${tab === OVERVIEW_ITEM.id ? " active" : ""}`}
            onClick={() => setTab(OVERVIEW_ITEM.id)}
            title={t(OVERVIEW_ITEM.labelKey)}
          >
            <LayoutGrid size={18} />
            <span>{t(OVERVIEW_ITEM.labelKey)}</span>
          </button>

          {NAV_GROUPS.map((group) => {
            const GroupIcon = group.icon;
            const isOpen = openGroups[group.id];
            const hasActive = group.items.some((item) => item.id === tab);
            return (
              <div key={group.id} className="nav-group">
                <button
                  className={`nav-group-header${hasActive && !isOpen ? " has-active" : ""}`}
                  onClick={() => toggleGroup(group.id)}
                  aria-expanded={isOpen}
                >
                  <GroupIcon size={16} />
                  <span>{t(group.labelKey)}</span>
                  <ChevronDown size={15} className={`nav-chevron${isOpen ? " open" : ""}`} />
                </button>
                {isOpen && (
                  <div className="nav-group-items">
                    {group.items.map((item) => {
                      const Icon = item.icon;
                      return (
                        <button
                          key={item.id}
                          className={`nav-item nav-subitem${tab === item.id ? " active" : ""}`}
                          onClick={() => setTab(item.id)}
                          title={t(item.labelKey)}
                        >
                          <Icon size={17} />
                          <span>{t(item.labelKey)}</span>
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })}
        </nav>

        <div className="account">
          <div className="avatar">AN</div>
          <div>
            <strong>{t("app.admin")}</strong>
            <span>admin@agentmux.ai</span>
          </div>
          <ChevronDown size={16} />
        </div>
      </aside>

      <div className="workspace">
        <header className="topbar">
          <div className="title-block">
            <div className="title-row">
              <Command size={22} />
              <h1>{t(active.labelKey)}</h1>
              <span className="system-pill">
                <span className="status-dot" />
                {t("app.status")}
              </span>
            </div>
            <label className="search-box">
              <Search size={17} />
              <input placeholder={t("app.search")} />
            </label>
          </div>
          <MeetingControls />
          <RemoteTargetSelector onManage={() => setTab("machines")} />
          <details className="topbar-preferences">
            <summary title={t("app.settings")} aria-label={t("app.settings")}>
              <Settings size={18} />
            </summary>
            <div className="preference-panel">
              <PreferenceControls
                language={language}
                setLanguage={setLanguage}
                themeMode={themeMode}
                setThemeMode={setThemeMode}
              />
            </div>
          </details>
        </header>

        <main className="main">
          <PanelErrorBoundary
            resetKey={tab}
            title={t("app.panelError")}
            description={t("app.panelErrorHint")}
            retryLabel={t("common.retry")}
          >
            <Suspense fallback={<div className="empty-state">{t("common.loading")}</div>}>
              {tab === "overview" && <OverviewPanel />}
              {tab === "agents" && <AgentsPanel />}
              {tab === "connect" && <ConnectPanel />}
              {tab === "frameworks" && <FrameworksPanel />}
              {tab === "observability" && <ObservabilityPanel />}
              {tab === "usage" && <UsagePanel />}
              {tab === "menubar" && <MenuBarPanel />}
              {tab === "machines" && <RemoteHostsPanel />}
              {tab === "sessions" && <SessionsPanel />}
              {tab === "meetings" && <MeetingsPanel />}
              {tab === "providers" && <ProvidersPanel />}
              {tab === "gateway" && <GatewayPanel />}
              {tab === "memory" && <MemoryPanel />}
              {tab === "skills" && <SkillsPanel />}
              {tab === "mcp" && <MCPPanel />}
              {tab === "guard" && <GuardPanel />}
            </Suspense>
          </PanelErrorBoundary>
        </main>
      </div>
    </div>
  );
}

type PanelErrorBoundaryProps = {
  resetKey: string;
  title: string;
  description: string;
  retryLabel: string;
  children: ReactNode;
};

type PanelErrorBoundaryState = {
  error: Error | null;
};

class PanelErrorBoundary extends Component<PanelErrorBoundaryProps, PanelErrorBoundaryState> {
  state: PanelErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): PanelErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("AgentMux panel render failed", error, info);
  }

  componentDidUpdate(prevProps: PanelErrorBoundaryProps) {
    if (prevProps.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    if (this.state.error) {
      return (
        <section className="surface panel-error">
          <div className="surface-body">
            <h2>{this.props.title}</h2>
            <p className="subtle-copy">{this.props.description}</p>
            <pre>{this.state.error.message}</pre>
            <button className="action" onClick={() => this.setState({ error: null })}>
              {this.props.retryLabel}
            </button>
          </div>
        </section>
      );
    }

    return this.props.children;
  }
}

function PreferenceControls({
  language,
  setLanguage,
  themeMode,
  setThemeMode,
}: {
  language: Language;
  setLanguage: (language: Language) => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}) {
  const { t } = useI18n();
  const [launchAtLogin, setLaunchAtLoginStatus] = useState<LaunchAtLoginStatus | null>(null);
  const [launchAtLoginBusy, setLaunchAtLoginBusy] = useState(false);
  const [keepAwake, setKeepAwakeStatus] = useState<KeepAwakeStatus | null>(null);
  const [keepAwakeMinutes, setKeepAwakeMinutes] = useState("60");
  const [keepAwakeBusy, setKeepAwakeBusy] = useState(false);
  const [preferenceError, setPreferenceError] = useState("");
  const desktop = isDesktopApp();

  useEffect(() => {
    if (!desktop) return;
    let active = true;
    getLaunchAtLogin()
      .then((status) => {
        if (active) setLaunchAtLoginStatus(status);
      })
      .catch((error) => {
        if (active) setPreferenceError(error instanceof Error ? error.message : String(error));
      });
    return () => {
      active = false;
    };
  }, [desktop]);

  useEffect(() => {
    let active = true;
    api.keepAwakeStatus()
      .then((status) => {
        if (!active) return;
        setKeepAwakeStatus(status);
        if (status.duration_minutes > 0) setKeepAwakeMinutes(String(status.duration_minutes));
      })
      .catch((error) => {
        if (active) setPreferenceError(error instanceof Error ? error.message : String(error));
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!keepAwake?.enabled) return;
    const timer = window.setInterval(() => {
      api.keepAwakeStatus()
        .then((status) => setKeepAwakeStatus(status))
        .catch(() => undefined);
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [keepAwake?.enabled]);

  async function updateLaunchAtLogin(enabled: boolean) {
    setLaunchAtLoginBusy(true);
    setPreferenceError("");
    try {
      setLaunchAtLoginStatus(await setLaunchAtLogin(enabled));
    } catch (error) {
      setPreferenceError(error instanceof Error ? error.message : String(error));
    } finally {
      setLaunchAtLoginBusy(false);
    }
  }

  async function updateKeepAwake(durationMinutes: number) {
    setKeepAwakeBusy(true);
    setPreferenceError("");
    try {
      setKeepAwakeStatus(await api.setKeepAwake(durationMinutes));
    } catch (error) {
      setPreferenceError(error instanceof Error ? error.message : String(error));
    } finally {
      setKeepAwakeBusy(false);
    }
  }

  const parsedKeepAwakeMinutes = Number(keepAwakeMinutes);
  const keepAwakeDurationValid = Number.isInteger(parsedKeepAwakeMinutes)
    && parsedKeepAwakeMinutes >= 1
    && parsedKeepAwakeMinutes <= 1440;

  return (
    <>
      <div className="segmented lang-toggle" aria-label="Language">
        <Languages size={16} />
        {LANG_OPTIONS.map((item) => (
          <button
            key={item.id}
            className={language === item.id ? "active" : ""}
            onClick={() => setLanguage(item.id)}
          >
            {t(item.labelKey)}
          </button>
        ))}
      </div>

      <div className="segmented theme-toggle" aria-label="Theme">
        {THEME_OPTIONS.map((item) => {
          const Icon = item.icon;
          return (
            <button
              key={item.id}
              className={themeMode === item.id ? "active" : ""}
              onClick={() => setThemeMode(item.id)}
              title={t(item.labelKey)}
            >
              <Icon size={15} />
              <span>{t(item.labelKey)}</span>
            </button>
          );
        })}
      </div>

      {desktop && launchAtLogin?.supported && (
        <label className="switch-row preference-switch-row">
          <span>
            <strong>{t("app.launchAtLogin")}</strong>
            <small>{t("app.launchAtLoginHint")}</small>
          </span>
          <input
            type="checkbox"
            checked={launchAtLogin.enabled}
            disabled={launchAtLoginBusy}
            onChange={(event) => updateLaunchAtLogin(event.target.checked)}
          />
        </label>
      )}
      {keepAwake?.supported && (
        <div className="preference-keep-awake">
          <div className="preference-keep-awake-title">
            <Coffee size={16} />
            <span>
              <strong>{t("app.keepAwake")}</strong>
              <small>
                {keepAwake.enabled
                  ? t("app.keepAwakeRemaining", { minutes: Math.max(1, Math.ceil(keepAwake.remaining_seconds / 60)) })
                  : t("app.keepAwakeHint")}
              </small>
            </span>
          </div>
          <div className="preference-keep-awake-controls">
            <label>
              <input
                type="number"
                min={1}
                max={1440}
                step={1}
                value={keepAwakeMinutes}
                disabled={keepAwakeBusy}
                aria-label={t("app.keepAwakeDuration")}
                onChange={(event) => setKeepAwakeMinutes(event.target.value)}
              />
              <span>{t("app.minutesShort")}</span>
            </label>
            <button
              type="button"
              disabled={keepAwakeBusy || !keepAwakeDurationValid}
              onClick={() => updateKeepAwake(parsedKeepAwakeMinutes)}
            >
              {keepAwake.enabled ? t("app.keepAwakeUpdate") : t("app.keepAwakeStart")}
            </button>
            {keepAwake.enabled && (
              <button
                className="keep-awake-stop"
                type="button"
                disabled={keepAwakeBusy}
                onClick={() => updateKeepAwake(0)}
              >
                {t("app.keepAwakeStop")}
              </button>
            )}
          </div>
        </div>
      )}
      {desktop && (
        <button
          className="preference-action"
          type="button"
          onClick={() => {
            setPreferenceError("");
            openLocalWebUI().catch((error) => {
              setPreferenceError(error instanceof Error ? error.message : String(error));
            });
          }}
        >
          <ExternalLink size={17} />
          <span>{t("app.openLocalWebUI")}</span>
        </button>
      )}
      {preferenceError && <small className="preference-error">{preferenceError}</small>}
    </>
  );
}
