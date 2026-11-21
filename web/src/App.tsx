import { Component, Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, ErrorInfo, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode } from "react";
import {
  Activity,
  Blocks,
  Bot,
  Boxes,
  Brain,
  Cable,
  CalendarClock,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ExternalLink,
  Gauge,
  HeartPulse,
  LayoutGrid,
  ListChecks,
  PanelTop,
  RefreshCw,
  Plus,
  Search,
  ServerCog,
  MessageSquareText,
  MonitorDot,
  Settings,
  ShieldCheck,
  Sparkles,
  UsersRound,
  Video,
  Zap,
} from "lucide-react";
import { MeetingInvitationOverlay } from "./MeetingControls";
import { MeetingProvider } from "./MeetingContext";
import { prefetchPanel, RegisteredPanel } from "./panelRegistry";
import { SidebarSearchPopover } from "./components/SidebarSearchPopover";
import { SidebarGroup } from "./components/SidebarGroup";
import { SETTINGS_SECTIONS, settingsSectionFromHash, type SettingsSection } from "./settingsNavigation";
import { MobileDrawer, MobileTabBar, mobileTabItems } from "./MobileNavigation";
import { ConnectionBanner } from "./pwa/ConnectionBanner";

const TriggersPanel = lazy(() => import("./panels/connect/TriggersPanel").then((m) => ({ default: m.TriggersPanel })));
const ConnectPanel = lazy(() => import("./panels/ConnectPanel").then((m) => ({ default: m.ConnectPanel })));
const SettingsPanel = lazy(() => import("./panels/SettingsPanel").then((m) => ({ default: m.SettingsPanel })));
const RemoteHostsPanel = lazy(() => import("./panels/RemoteHostsPanel").then((m) => ({ default: m.RemoteHostsPanel })));
import { I18nProvider, Language, ThemeMode, useI18n } from "./i18n";
import {
  activeMachineScope,
  api,
  FLEET_WARNING_EVENT,
  currentFleetWarnings,
  isFleetStaleNotice,
  isDesktopApp,
  openLocalWebUI,
  setActiveMachineScope,
  type AuthSession,
} from "./api";
import { currentOfflineTargets, resetFleetWarnings } from "./api/fleetWarnings";
import {
  accountLabel,
  AUTH_CHANGED_EVENT,
  consoleRole,
  loadSessionWithRetry,
} from "./signIn";
import { SignInGate } from "./SignInGate";
import { HubApprovalPrompt } from "./components/HubApprovalPrompt";
import { ConfirmHost } from "./components/ConfirmDialog";
import {
  MEMBER_LANDING_TAB,
  navigationForRole,
  navigationGroupForTab,
  primaryGroupDestination,
  searchNavigationItems,
  tabAllowedForRole,
} from "./navigationModel";
import {
  NAVIGATION_GROUP_SEARCH_ALIASES,
  NAVIGATION_SEARCH_ALIASES,
  type NavigationTabID,
} from "./navigationSearchAliases";
import {
  clampSidebarWidth,
  PRIMARY_SIDEBAR_COLLAPSED_KEY,
  PRIMARY_SIDEBAR_COLLAPSED_WIDTH,
  PRIMARY_SIDEBAR_STORAGE_KEY,
  PRIMARY_SIDEBAR_WIDTH,
  readSidebarWidth,
} from "./sidebarSizing";

type Tab = NavigationTabID;

type NavItem = { id: Tab; labelKey: string; icon: typeof LayoutGrid };
type NavGroup = { id: string; labelKey: string; icon: typeof LayoutGrid; items: NavItem[] };

const NAV_GROUPS: NavGroup[] = [
  {
    id: "agents",
    labelKey: "nav.group.agents",
    icon: Bot,
    items: [
      { id: "agents", labelKey: "nav.agents", icon: Bot },
      { id: "work", labelKey: "nav.work", icon: ListChecks },
      { id: "frameworks", labelKey: "nav.frameworks", icon: Blocks },
      { id: "skills", labelKey: "nav.skills", icon: Sparkles },
      { id: "mcp", labelKey: "nav.mcp", icon: Boxes },
      { id: "memory", labelKey: "nav.memory", icon: Brain },
    ],
  },
  {
    id: "connectivity",
    labelKey: "nav.group.connectivity",
    icon: Cable,
    items: [
      { id: "channels", labelKey: "nav.channels", icon: Cable },
      { id: "schedules", labelKey: "nav.schedules", icon: CalendarClock },
      { id: "triggers", labelKey: "nav.triggers", icon: Zap },
      { id: "meetings", labelKey: "nav.meetings", icon: Video },
    ],
  },
  {
    id: "operations",
    labelKey: "nav.group.operations",
    icon: Activity,
    // The two dashboards lead the section: Usage is where it opens.
    items: [
      { id: "usage", labelKey: "nav.usage", icon: Gauge },
      { id: "health", labelKey: "nav.health", icon: HeartPulse },
      { id: "sessions", labelKey: "nav.sessions", icon: MessageSquareText },
      { id: "machine-status", labelKey: "nav.machineStatus", icon: MonitorDot },
      { id: "observability", labelKey: "nav.observability", icon: Activity },
      { id: "feedback", labelKey: "nav.feedback", icon: MessageSquareText },
      { id: "guard", labelKey: "nav.guard", icon: ShieldCheck },
    ],
  },
  {
    id: "system",
    labelKey: "nav.group.system",
    icon: PanelTop,
    items: [
      { id: "machines", labelKey: "nav.machines", icon: ServerCog },
      { id: "accounts", labelKey: "nav.accounts", icon: UsersRound },
      { id: "settings", labelKey: "nav.settings", icon: Settings },
    ],
  },
];

const TABS: NavItem[] = NAV_GROUPS.flatMap((group) => group.items);

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
  // Providers and routing moved to the frameworks; old links land there.
  if (value === "providers" || value === "gateway") return "frameworks";
  if (value === "connect") return "channels";
  // Event subscriptions joined the Triggers page as its outbound part.
  if (value === "subscriptions") return "triggers";
  // Tenants are gone; account management took their place.
  if (value === "tenants") return "accounts";
  // The Overview page became the Usage and Health dashboards.
  if (value === "overview") return "usage";
  return TABS.some((item) => item.id === value) ? value as Tab : null;
}

function initialTab(): Tab {
  return tabFromHash() ?? "agents";
}

export function App() {
  const [tab, setTab] = useState<Tab>(initialTab);
  const [settingsSection, setSettingsSection] = useState(() => settingsSectionFromHash(window.location.hash));
  const activeRouteRef = useRef(tab);
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
      if (hashTab) {
        if (activeRouteRef.current !== hashTab) resetFleetWarnings();
        activeRouteRef.current = hashTab;
        setTab(hashTab);
        if (hashTab === "settings") setSettingsSection(settingsSectionFromHash(window.location.hash));
      }
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  // Every tab owns a hash route (#tab or #tab/subview); panels with internal
  // sub-navigation (observability) extend the same scheme.
  function selectTab(next: Tab, section: SettingsSection = "general") {
    if (activeRouteRef.current !== next) resetFleetWarnings();
    activeRouteRef.current = next;
    setTab(next);
    if (next === "observability") {
      if (!window.location.hash.startsWith("#observability")) window.location.hash = "#observability/overview";
      return;
    }
    if (next === "settings") {
      setSettingsSection(section);
      window.location.hash = section === "general" ? "#settings" : `#settings/${section}`;
      return;
    }
    window.location.hash = `#${next}`;
  }

  return (
    <I18nProvider language={language}>
      <SignInGate>
        <Shell
          tab={tab}
          settingsSection={settingsSection}
          setTab={selectTab}
          language={language}
          setLanguage={setLanguage}
          themeMode={themeMode}
          setThemeMode={setThemeMode}
        />
        <HubApprovalPrompt />
      </SignInGate>
      <ConfirmHost />
    </I18nProvider>
  );
}

function Shell({
  tab,
  settingsSection,
  setTab,
  language,
  setLanguage,
  themeMode,
  setThemeMode,
}: {
  tab: Tab;
  settingsSection: SettingsSection;
  setTab: (tab: Tab, section?: SettingsSection) => void;
  language: Language;
  setLanguage: (language: Language) => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}) {
  const { t } = useI18n();
  const identity = useConsoleIdentity();
  const session = identity.session;
  const nativeDesktopAdmin = window.location.protocol === "wails:";
  const role = consoleRole(session, nativeDesktopAdmin);
  const identityIsAdmin = role === "admin";
  // Admin-only requests wait until the session confirms the role, so a member
  // never fires them while the session is still loading.
  const adminReady = identityIsAdmin && (identity.state === "ready" || nativeDesktopAdmin);
  // Members also have client-only appearance and language settings.
  const navGroups = useMemo(() => navigationForRole(NAV_GROUPS, role).map((group) => role === "member"
    ? { ...group, items: group.items.map((item) => item.id === "accounts" ? { ...item, labelKey: "nav.myAccount" } : item) }
    : group), [role]);
  const navTabs = useMemo(
    () => navGroups.flatMap((group) => group.items),
    [navGroups],
  );
  const mobileTabs = useMemo(() => mobileTabItems(navTabs), [navTabs]);
  const identityName = session?.account ? accountLabel(session.account) : t("app.admin");
  const navigationLocked = identity.state !== "ready" && !nativeDesktopAdmin;
  const visibleTab = tabAllowedForRole(tab, role) ? tab : MEMBER_LANDING_TAB;
  const active = useMemo(
    () => navTabs.find((item) => item.id === visibleTab) ?? TABS.find((item) => item.id === visibleTab) ?? TABS[0],
    [navTabs, visibleTab],
  );
  const sidebarGroups = useMemo(() => navGroups.map((group) => ({
    ...group,
    items: group.items.filter((item) => item.id !== "settings"),
  })).filter((group) => group.items.length > 0), [navGroups]);
  const activeGroup = navigationGroupForTab(sidebarGroups, visibleTab);
  const activeSettingsSection = identityIsAdmin ? settingsSection : "general";
  const activeGroupId = visibleTab === "settings" ? "settings" : activeGroup?.id;
  const [closedGroupId, setClosedGroupId] = useState<string | null>(null);
  const [primarySidebarWidth, setPrimarySidebarWidth] = useState(() =>
    readSidebarWidth(localStorage, PRIMARY_SIDEBAR_STORAGE_KEY, PRIMARY_SIDEBAR_WIDTH.default,
      PRIMARY_SIDEBAR_WIDTH.min, PRIMARY_SIDEBAR_WIDTH.max),
  );
  const [primaryCollapsed, setPrimaryCollapsed] = useState(() =>
    localStorage.getItem(PRIMARY_SIDEBAR_COLLAPSED_KEY) === "true",
  );
  const expandedGroupId = !primaryCollapsed && activeGroupId !== closedGroupId ? activeGroupId : undefined;
  const mainRef = useRef<HTMLElement>(null);
  const [quickActionError, setQuickActionError] = useState("");
  const [fleetWarnings, setFleetWarnings] = useState<string[]>(currentFleetWarnings);
  const [dismissedFleetWarnings, setDismissedFleetWarnings] = useState("");
  const fleetWarningKey = JSON.stringify(fleetWarnings);
  // Set once every machine a warning found offline is connected again: what
  // the page shows of them is then out of date and worth reloading.
  const [fleetRecovered, setFleetRecovered] = useState("");
  const [machineScopeVersion, setMachineScopeVersion] = useState(0);
  const [createAgentRequested, setCreateAgentRequested] = useState(false);
  const handleCreateAgentRequest = useCallback(() => setCreateAgentRequested(false), []);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [activeSearchIndex, setActiveSearchIndex] = useState(0);
  const searchAnchorRef = useRef<HTMLDivElement>(null);
  const closeSearchResults = useCallback(() => setSearchOpen(false), []);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const closeMobileNav = useCallback(() => setMobileNavOpen(false), []);
  const desktop = isDesktopApp();
  const ActiveIcon = active.icon;
  const prefetchItems = activeGroup?.items ?? [];
  const prefetchKey = prefetchItems.map((item) => item.id).join(",");
  useEffect(() => {
    const timer = window.setTimeout(() => prefetchItems.forEach((item) => prefetchPanel(item.id)), 1_500);
    return () => window.clearTimeout(timer);
  }, [prefetchKey]);
  const shellStyle = {
    "--primary-sidebar-width": `${primaryCollapsed ? PRIMARY_SIDEBAR_COLLAPSED_WIDTH : primarySidebarWidth}px`,
  } as CSSProperties;
  const searchResults = useMemo(() => {
    const searchableTabs = navTabs
      .filter(() => !navigationLocked)
      .map((item) => {
        const group = navigationGroupForTab(navGroups, item.id);
        return {
          ...item,
          label: t(item.labelKey),
          groupLabel: group ? t(group.labelKey) : "",
          keywords: [item.labelKey, ...NAVIGATION_SEARCH_ALIASES[item.id]],
          groupKeywords: group ? NAVIGATION_GROUP_SEARCH_ALIASES[group.id] ?? [] : [],
        };
      });
    return searchNavigationItems(searchableTabs, searchQuery);
  }, [language, navGroups, navTabs, navigationLocked, searchQuery, t]);
  const showSearchResults = searchOpen && searchQuery.trim().length > 0;

  useEffect(() => {
    // Members reach only this hub; a remote machine selection left over from
    // an administrator session would route their requests elsewhere.
    if (session && role === "member" && activeMachineScope() !== "local") {
      setActiveMachineScope("local");
      window.location.reload();
      return;
    }
    // Administrators always see every machine. A machine picked before there
    // was no picker would otherwise keep the Console on that one machine.
    if (adminReady && activeMachineScope() !== "all") {
      setActiveMachineScope("all");
    }
  }, [adminReady, role, session]);

  useEffect(() => {
    if (identity.state === "ready" && !tabAllowedForRole(tab, role)) setTab(MEMBER_LANDING_TAB);
  }, [identity.state, role, setTab, tab]);

  useEffect(() => {
    mainRef.current?.scrollTo?.({ top: 0, left: 0, behavior: "auto" });
    window.scrollTo({ top: 0, left: 0, behavior: "auto" });
    setMobileNavOpen(false);
    setClosedGroupId(null);
  }, [activeSettingsSection, visibleTab]);

  useEffect(() => {
    localStorage.setItem(PRIMARY_SIDEBAR_STORAGE_KEY, String(primarySidebarWidth));
  }, [primarySidebarWidth]);

  useEffect(() => {
    localStorage.setItem(PRIMARY_SIDEBAR_COLLAPSED_KEY, String(primaryCollapsed));
    if (primaryCollapsed) setSearchOpen(false);
  }, [primaryCollapsed]);

  useEffect(() => {
    setActiveSearchIndex((current) => Math.min(current, Math.max(0, searchResults.length - 1)));
  }, [searchResults.length]);

  useEffect(() => {
    const receiveWarnings = (event: Event) => {
      const warnings = (event as CustomEvent<string[]>).detail;
      if (Array.isArray(warnings)) {
        setFleetWarnings(warnings);
        if (!warnings.length) setDismissedFleetWarnings("");
      }
    };
    window.addEventListener(FLEET_WARNING_EVENT, receiveWarnings);
    setFleetWarnings(currentFleetWarnings());
    return () => window.removeEventListener(FLEET_WARNING_EVENT, receiveWarnings);
  }, []);

  useEffect(() => {
    const offline = currentOfflineTargets();
    if (!offline.length) return;
    let cancelled = false;
    const check = async () => {
      try {
        const targets = await api.fleetTargets();
        if (cancelled) return;
        const online = new Set(targets.filter((target) => target.online && !target.error).map((target) => target.id));
        if (offline.every((id) => online.has(id))) {
          setFleetRecovered(fleetWarningKey);
          window.clearInterval(timer);
        }
      } catch {
        // The next check tries again.
      }
    };
    const timer = window.setInterval(check, 10_000);
    void check();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [fleetWarningKey]);

  useEffect(() => {
    const refreshScope = () => {
      setFleetWarnings([]);
      setDismissedFleetWarnings("");
      setMachineScopeVersion((version) => version + 1);
    };
    window.addEventListener("agentmux:machine-scope-changed", refreshScope);
    return () => window.removeEventListener("agentmux:machine-scope-changed", refreshScope);
  }, []);

  function requestNewAgent() {
    setCreateAgentRequested(true);
    setTab("agents");
  }

  function toggleGroup(id: string, destination?: Tab | null) {
    if (expandedGroupId === id) {
      setClosedGroupId(id);
      return;
    }
    setPrimaryCollapsed(false);
    setClosedGroupId(null);
    if (destination) setTab(destination);
  }

  function selectSettingsSection(section: SettingsSection) {
    setTab("settings", section);
  }

  function openWebUI() {
    setQuickActionError("");
    openLocalWebUI().catch((error) => {
      setQuickActionError(error instanceof Error ? error.message : String(error));
    });
  }

  function selectSearchResult(item: NavItem) {
    setSearchQuery("");
    setSearchOpen(false);
    setActiveSearchIndex(0);
    setTab(item.id);
  }

  function handleSearchKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.key === "Escape") {
      setSearchQuery("");
      setSearchOpen(false);
      setActiveSearchIndex(0);
      return;
    }
    if (!searchResults.length) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setSearchOpen(true);
      setActiveSearchIndex((current) => (current + 1) % searchResults.length);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setSearchOpen(true);
      setActiveSearchIndex((current) => (current - 1 + searchResults.length) % searchResults.length);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      selectSearchResult(searchResults[activeSearchIndex] ?? searchResults[0]);
    }
  }

  return (
    <MeetingProvider enabled={adminReady}>
    <div
      className={`app-shell${primaryCollapsed ? " primary-collapsed" : ""}${desktop ? " native-desktop" : ""}`}
      style={shellStyle}
    >
      <aside className="sidebar">
        <div className="brand-actions titlebar-actions">
          {desktop && (
            <button
              className="brand-action-button"
              type="button"
              title={t("app.openLocalWebUI")}
              aria-label={t("app.openLocalWebUI")}
              onClick={openWebUI}
            >
              <ExternalLink size={17} />
              <span>{t("nav.openWeb")}</span>
            </button>
          )}
          <button
            className="brand-action-button primary-collapse"
            type="button"
            title={t(primaryCollapsed ? "nav.expandPrimary" : "nav.collapsePrimary")}
            aria-label={t(primaryCollapsed ? "nav.expandPrimary" : "nav.collapsePrimary")}
            aria-expanded={!primaryCollapsed}
            aria-controls="primary-navigation"
            onClick={() => setPrimaryCollapsed((collapsed) => !collapsed)}
          >
            {primaryCollapsed ? <ChevronsRight size={17} /> : <ChevronsLeft size={17} />}
            <span>{t("nav.collapsePrimary")}</span>
          </button>
        </div>
        <div className="brand">
          <img className="brand-logo" src="/agentmux-logo.png" alt="" aria-hidden="true" />
          <div className="brand-copy">
            <strong>AgentMux</strong>
          </div>
        </div>

        <div
          ref={searchAnchorRef}
          className="sidebar-search-container"
        >
          <div className="sidebar-search search-box">
            <Search size={15} aria-hidden="true" />
            <input
              type="search"
              role="combobox"
              aria-label={t("app.search")}
              aria-autocomplete="list"
              aria-controls="sidebar-search-results"
              aria-expanded={showSearchResults}
              aria-activedescendant={showSearchResults && searchResults.length
                ? `sidebar-search-result-${searchResults[activeSearchIndex]?.id ?? searchResults[0].id}`
                : undefined}
              autoComplete="off"
              placeholder={t("app.search")}
              value={searchQuery}
              onChange={(event) => {
                setSearchQuery(event.target.value);
                setSearchOpen(true);
                setActiveSearchIndex(0);
              }}
              onFocus={() => setSearchOpen(true)}
              onKeyDown={handleSearchKeyDown}
            />
            {searchQuery && (
              <button
                className="sidebar-search-clear"
                type="button"
                title={t("app.searchClear")}
                aria-label={t("app.searchClear")}
                onClick={() => {
                  setSearchQuery("");
                  setSearchOpen(false);
                  setActiveSearchIndex(0);
                }}
              >
                <span aria-hidden="true">×</span>
              </button>
            )}
          </div>
          {showSearchResults && !primaryCollapsed && (
            <SidebarSearchPopover
              anchorRef={searchAnchorRef}
              layoutKey={`${primarySidebarWidth}:${primaryCollapsed}`}
              onClose={closeSearchResults}
              id="sidebar-search-results"
              aria-label={t("app.searchResults")}
            >
              {searchResults.length ? searchResults.map((item, index) => {
                const Icon = item.icon;
                return (
                  <button
                    id={`sidebar-search-result-${item.id}`}
                    key={item.id}
                    className={index === activeSearchIndex ? "active" : ""}
                    type="button"
                    role="option"
                    aria-selected={index === activeSearchIndex}
                    onMouseEnter={() => setActiveSearchIndex(index)}
                    onClick={() => selectSearchResult(item)}
                  >
                    <Icon size={16} aria-hidden="true" />
                    <span>
                      <strong>{item.label}</strong>
                      {item.groupLabel && <small>{item.groupLabel}</small>}
                    </span>
                  </button>
                );
              }) : (
                <div className="sidebar-search-empty" role="status">{t("app.searchEmpty")}</div>
              )}
            </SidebarSearchPopover>
          )}
        </div>

        <nav id="primary-navigation" className="nav primary-nav" aria-label={t("nav.primary")}>
          {sidebarGroups.map((group) => (
            <SidebarGroup
              key={group.id}
              id={group.id}
              label={t(group.labelKey)}
              icon={group.icon}
              items={group.items.map((item) => ({ ...item, label: t(item.labelKey) }))}
              current={activeGroupId === group.id ? visibleTab : ""}
              expanded={expandedGroupId === group.id}
              disabled={navigationLocked}
              onToggle={() => toggleGroup(group.id, primaryGroupDestination(group, visibleTab))}
              onSelect={setTab}
              onPrefetch={prefetchPanel}
            />
          ))}

          <SidebarGroup
            id="settings"
            label={t("app.settings")}
            icon={Settings}
            items={(identityIsAdmin ? SETTINGS_SECTIONS : SETTINGS_SECTIONS.slice(0, 1)).map((item) => ({
              ...item, label: t(item.labelKey),
            }))}
            current={visibleTab === "settings" ? activeSettingsSection : ""}
            expanded={expandedGroupId === "settings"}
            disabled={navigationLocked}
            onToggle={() => toggleGroup("settings", visibleTab === "settings" ? null : "settings")}
            onSelect={selectSettingsSection}
          />
        </nav>

        <MobileTabBar
          items={mobileTabs}
          active={visibleTab}
          drawerOpen={mobileNavOpen}
          isDisabled={() => navigationLocked}
          onSelect={setTab}
          onOpenMore={() => setMobileNavOpen((open) => !open)}
        />

        <button
          type="button"
          className="account primary-account"
          data-initial={(identityName.trim()[0] ?? "A").toUpperCase()}
          title={identityName}
          aria-label={t("app.openAccount", { name: identityName })}
          aria-current={visibleTab === "accounts" ? "page" : undefined}
          onClick={() => setTab("accounts")}
        >
          <strong>{identityName}</strong>
        </button>

        {!primaryCollapsed && <SidebarResizeHandle
          label={t("nav.resizePrimary")}
          value={primarySidebarWidth}
          limits={PRIMARY_SIDEBAR_WIDTH}
          onChange={setPrimarySidebarWidth}
        />}
      </aside>

      <MobileDrawer
        open={mobileNavOpen}
        groups={navGroups}
        active={visibleTab}
        isDisabled={() => navigationLocked}
        onSelect={(id) => {
          setMobileNavOpen(false);
          setTab(id);
        }}
        onClose={closeMobileNav}
      />

      <div className="workspace">
        <div className="workspace-canvas">
          <header className="workspace-header">
          <div className="title-row">
            <ActiveIcon size={20} />
            <h1>{t(active.labelKey)}</h1>
            {visibleTab === "settings" && (
              <span className="settings-breadcrumb">
                <ChevronRight size={13} aria-hidden="true" />
                {t(SETTINGS_SECTIONS.find((item) => item.id === activeSettingsSection)!.labelKey)}
              </span>
            )}
            <span className={`system-pill${fleetWarnings.length || identity.state === "error" ? " warning" : ""}`} role="status">
              <span className="status-dot" />
              {t(identity.state === "loading" ? "app.identityLoading"
                : identity.state !== "ready" ? "app.identityUnavailable"
                : !fleetWarnings.length ? "app.status"
                : fleetWarnings.every(isFleetStaleNotice) ? "app.statusStale" : "app.statusWarning")}
            </span>
          </div>
		    <div className="workspace-header-actions">
              {visibleTab === "agents" && (
                <>
                  <button
                    className="action workspace-create-action"
                    type="button"
                    onClick={requestNewAgent}
                  >
                    <Plus size={14} />
                    {t("agents.new")}
                  </button>
                </>
              )}
		    </div>
          </header>

        <ConnectionBanner />
        <MeetingInvitationOverlay />

        <main ref={mainRef} className={`main main-${visibleTab}`}>
          {quickActionError && <small className="brand-action-error" role="status">{quickActionError}</small>}
          {fleetWarnings.length > 0 && dismissedFleetWarnings !== fleetWarningKey && fleetRecovered === fleetWarningKey && (
            <div className="session-notice" role="status">
              <strong>{t("remote.fleetRecovered")}</strong>
              <span>{t("remote.fleetRecoveredHint")}</span>
              <button className="ghost-action" type="button" onClick={() => window.dispatchEvent(new Event("agentmux:machine-scope-changed"))}>{t("common.refresh")}</button>
              <button className="ghost-action" type="button" onClick={() => setDismissedFleetWarnings(fleetWarningKey)}>{t("common.close")}</button>
            </div>
          )}
          {fleetWarnings.length > 0 && dismissedFleetWarnings !== fleetWarningKey && fleetRecovered !== fleetWarningKey && (
            <div className="session-notice warning" role="status">
              <strong>{t(fleetWarnings.every(isFleetStaleNotice) ? "remote.staleFleetNotice" : "remote.partialFleetWarning")}</strong>
              <span>{fleetWarnings.join(" · ")}</span>
              <button className="ghost-action" type="button" onClick={() => setDismissedFleetWarnings(fleetWarningKey)}>{t("common.close")}</button>
            </div>
          )}
          <PanelErrorBoundary
            key={`${visibleTab}:${machineScopeVersion}`}
            resetKey={`${visibleTab}:${machineScopeVersion}`}
            title={t("app.panelError")}
            description={t("app.panelErrorHint")}
            retryLabel={t("common.retry")}
          >
            <Suspense fallback={<div className="empty-state">{t("common.loading")}</div>}>
              {identity.state === "loading" ? (
                <div className="empty-state">{t("app.identityLoading")}</div>
              ) : identity.state === "error" ? (
                <section className="surface" role="alert">
                  <div className="surface-header">
                    <div>
                      <h2>{t("app.identityUnavailable")}</h2>
                      <p>{t("app.identityError")}</p>
                    </div>
                    <button className="ghost-action" onClick={() => void identity.refresh()}>
                      <RefreshCw size={15} /> {t("common.retry")}
                    </button>
                  </div>
                </section>
              ) : (
                <>
				  {visibleTab === "settings" && <SettingsPanel
                    section={activeSettingsSection}
                    onSectionChange={selectSettingsSection}
                    admin={identityIsAdmin}
                    language={language}
                    setLanguage={setLanguage}
                    themeMode={themeMode}
                    setThemeMode={setThemeMode}
                  />}
				  <RegisteredPanel
				    tab={visibleTab}
				    createRequested={createAgentRequested}
				    onCreateRequestHandled={handleCreateAgentRequest}
				  />
				  {visibleTab === "channels" && <ConnectPanel view="channels" />}
				  {visibleTab === "schedules" && <ConnectPanel view="schedules" />}
				  {visibleTab === "triggers" && <TriggersPanel />}
				  {visibleTab === "machines" && <RemoteHostsPanel />}
                </>
              )}
            </Suspense>
          </PanelErrorBoundary>
          </main>
        </div>
      </div>
    </div>
    </MeetingProvider>
  );
}

function SidebarResizeHandle({
  label,
  value,
  limits,
  onChange,
}: {
  label: string;
  value: number;
  limits: { default: number; min: number; max: number };
  onChange: (value: number) => void;
}) {
  const drag = useRef<{ pointerID: number; startX: number; startWidth: number } | null>(null);

  useEffect(() => () => document.body.classList.remove("sidebar-resizing"), []);

  function startResize(event: ReactPointerEvent<HTMLDivElement>) {
    if (event.button !== 0) return;
    drag.current = { pointerID: event.pointerId, startX: event.clientX, startWidth: value };
    event.currentTarget.setPointerCapture(event.pointerId);
    document.body.classList.add("sidebar-resizing");
    event.preventDefault();
  }

  function resize(event: ReactPointerEvent<HTMLDivElement>) {
    if (!drag.current || drag.current.pointerID !== event.pointerId) return;
    onChange(clampSidebarWidth(
      drag.current.startWidth + event.clientX - drag.current.startX,
      limits.min,
      limits.max,
    ));
  }

  function finishResize(event: ReactPointerEvent<HTMLDivElement>) {
    if (!drag.current || drag.current.pointerID !== event.pointerId) return;
    drag.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    document.body.classList.remove("sidebar-resizing");
  }

  function resizeWithKeyboard(event: ReactKeyboardEvent<HTMLDivElement>) {
    const step = event.shiftKey ? 1 : 8;
    let next = value;
    if (event.key === "ArrowLeft") next -= step;
    else if (event.key === "ArrowRight") next += step;
    else if (event.key === "Home") next = limits.min;
    else if (event.key === "End") next = limits.max;
    else return;
    event.preventDefault();
    onChange(clampSidebarWidth(next, limits.min, limits.max));
  }

  return (
    <div
      className="sidebar-resize-handle"
      role="separator"
      aria-label={label}
      aria-orientation="vertical"
      aria-valuemin={limits.min}
      aria-valuemax={limits.max}
      aria-valuenow={value}
      aria-valuetext={`${value}px`}
      tabIndex={0}
      title={label}
      onDoubleClick={() => onChange(limits.default)}
      onKeyDown={resizeWithKeyboard}
      onPointerCancel={finishResize}
      onPointerDown={startResize}
      onPointerMove={resize}
      onPointerUp={finishResize}
    >
      <span aria-hidden="true" />
    </div>
  );
}

type ConsoleIdentityState = "loading" | "ready" | "error";

// useConsoleIdentity reads who this Console is from /api/v1/auth/session,
// which decides between the administrator and member navigation.
function useConsoleIdentity(): {
  state: ConsoleIdentityState;
  session: AuthSession | null;
  refresh: () => Promise<void>;
} {
  const [state, setState] = useState<ConsoleIdentityState>("loading");
  const [session, setSession] = useState<AuthSession | null>(null);
  const loaded = useRef(false);

  const refresh = useCallback(async () => {
    // Only the first check blanks the page; later ones update in place.
    if (!loaded.current) setState("loading");
    try {
      const next = await loadSessionWithRetry(
        () => api.authSession(),
        // First launch may download PostgreSQL before the native API is ready.
        { attempts: window.location.protocol === "wails:" ? 1200 : 30 },
      );
      loaded.current = true;
      setSession(next);
      setState("ready");
    } catch {
      if (loaded.current) return;
      setState("error");
      setSession(null);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const onAuthChanged = () => void refresh();
    window.addEventListener(AUTH_CHANGED_EVENT, onAuthChanged);
    return () => window.removeEventListener(AUTH_CHANGED_EVENT, onAuthChanged);
  }, [refresh]);

  return { state, session, refresh };
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
