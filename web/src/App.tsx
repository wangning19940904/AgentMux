import { Component, useEffect, useMemo, useState } from "react";
import type { ErrorInfo, ReactNode } from "react";
import {
  Activity,
  BookOpen,
  Bot,
  Boxes,
  Brain,
  Cable,
  ChevronDown,
  Command,
  DatabaseZap,
  Gauge,
  KeyRound,
  Languages,
  LayoutGrid,
  Moon,
  Network,
  PanelLeft,
  Search,
  MessageSquareText,
  Settings,
  ShieldCheck,
  Sun,
  TerminalSquare,
  Workflow,
} from "lucide-react";
import { ProvidersPanel } from "./panels/ProvidersPanel";
import { UsagePanel } from "./panels/UsagePanel";
import { AgentsPanel } from "./panels/AgentsPanel";
import { ConnectPanel } from "./panels/ConnectPanel";
import { GatewayPanel } from "./panels/GatewayPanel";
import { OverviewPanel } from "./panels/OverviewPanel";
import { MemoryPanel } from "./panels/MemoryPanel";
import { SkillsPanel } from "./panels/SkillsPanel";
import { MCPPanel } from "./panels/MCPPanel";
import { GuardPanel } from "./panels/GuardPanel";
import { SessionsPanel } from "./panels/SessionsPanel";
import { I18nProvider, Language, ThemeMode, useI18n } from "./i18n";

type Tab =
  | "overview"
  | "agents"
  | "connect"
  | "usage"
  | "providers"
  | "gateway"
  | "memory"
  | "sessions"
  | "skills"
  | "mcp"
  | "guard";

const TABS: { id: Tab; labelKey: string; icon: typeof LayoutGrid }[] = [
  { id: "overview", labelKey: "nav.overview", icon: LayoutGrid },
  { id: "agents", labelKey: "nav.agents", icon: Bot },
  { id: "connect", labelKey: "nav.connect", icon: Cable },
  { id: "gateway", labelKey: "nav.gateway", icon: Workflow },
  { id: "usage", labelKey: "nav.usage", icon: Gauge },
  { id: "sessions", labelKey: "nav.sessions", icon: MessageSquareText },
  { id: "memory", labelKey: "nav.memory", icon: Brain },
  { id: "skills", labelKey: "nav.skills", icon: Bot },
  { id: "mcp", labelKey: "nav.mcp", icon: Boxes },
  { id: "guard", labelKey: "nav.guard", icon: ShieldCheck },
  { id: "providers", labelKey: "nav.providers", icon: DatabaseZap },
];

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
  const stored = localStorage.getItem("agentnexus:language");
  if (stored === "zh" || stored === "en") return stored;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

function initialThemeMode(): ThemeMode {
  const stored = localStorage.getItem("agentnexus:theme");
  if (stored === "system" || stored === "light" || stored === "dark") return stored;
  return "system";
}

export function App() {
  const [tab, setTab] = useState<Tab>("agents");
  const [language, setLanguage] = useState<Language>(initialLanguage);
  const [themeMode, setThemeMode] = useState<ThemeMode>(initialThemeMode);

  useEffect(() => {
    localStorage.setItem("agentnexus:language", language);
    document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  }, [language]);

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const applyTheme = () => {
      const resolved = themeMode === "system" ? (media.matches ? "dark" : "light") : themeMode;
      document.documentElement.dataset.theme = resolved;
      document.documentElement.dataset.themeMode = themeMode;
    };

    localStorage.setItem("agentnexus:theme", themeMode);
    applyTheme();
    media.addEventListener("change", applyTheme);
    return () => media.removeEventListener("change", applyTheme);
  }, [themeMode]);

  return (
    <I18nProvider language={language}>
      <Shell
        tab={tab}
        setTab={setTab}
        language={language}
        setLanguage={setLanguage}
        themeMode={themeMode}
        setThemeMode={setThemeMode}
      />
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

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark" aria-hidden="true">
            <Network size={22} strokeWidth={2.4} />
          </div>
          <div>
            <strong>AgentNexus</strong>
            <span>Command Surface</span>
          </div>
        </div>

        <nav className="nav" aria-label="Primary">
          {TABS.map((item) => {
            const Icon = item.icon;
            return (
              <button
                key={item.id}
                className={tab === item.id ? "active" : ""}
                onClick={() => setTab(item.id)}
                title={t(item.labelKey)}
              >
                <Icon size={18} />
                <span>{t(item.labelKey)}</span>
              </button>
            );
          })}
        </nav>

        <div className="sidebar-tools">
          <button>
            <KeyRound size={17} />
            <span>{t("app.apiKeys")}</span>
          </button>
          <button>
            <TerminalSquare size={17} />
            <span>{t("app.auditLogs")}</span>
          </button>
          <button>
            <BookOpen size={17} />
            <span>{t("app.docs")}</span>
          </button>
        </div>

        <div className="account">
          <div className="avatar">AN</div>
          <div>
            <strong>{t("app.admin")}</strong>
            <span>admin@agentnexus.ai</span>
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
            {tab === "overview" && <OverviewPanel />}
            {tab === "agents" && <AgentsPanel />}
            {tab === "connect" && <ConnectPanel />}
            {tab === "usage" && <UsagePanel />}
            {tab === "sessions" && <SessionsPanel />}
            {tab === "providers" && <ProvidersPanel />}
            {tab === "gateway" && <GatewayPanel />}
            {tab === "memory" && <MemoryPanel />}
            {tab === "skills" && <SkillsPanel />}
            {tab === "mcp" && <MCPPanel />}
            {tab === "guard" && <GuardPanel />}
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
    console.error("AgentNexus panel render failed", error, info);
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

      <button className="status-action" title={t("app.status")}>
        <Activity size={17} />
        <span>{t("app.status")}</span>
      </button>
    </>
  );
}
