import { Component, Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, ErrorInfo, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  Activity,
  Blocks,
  Bot,
  Boxes,
  Brain,
  Cable,
  CalendarClock,
  ChevronsLeft,
  ChevronsRight,
  ExternalLink,
  Gauge,
  Languages,
  LayoutGrid,
  Moon,
  PanelLeft,
  PanelLeftClose,
  PanelLeftOpen,
  PanelTop,
  RefreshCw,
  Plus,
  Search,
  ServerCog,
  MessageSquareText,
  MessagesSquare,
  MonitorDot,
  Settings,
  ShieldCheck,
  Sparkles,
  Sun,
  UsersRound,
  Workflow,
  Video,
  Zap,
} from "lucide-react";
import { RemoteTargetSelector } from "./RemoteTargetSelector";
import { KeepAwakeControl } from "./KeepAwakeControl";
import { MeetingInvitationOverlay } from "./MeetingControls";
import { MeetingProvider } from "./MeetingContext";
import { RegisteredPanel } from "./panelRegistry";
import { SidebarSearchPopover } from "./components/SidebarSearchPopover";
import { MobileDrawer, MobileTabBar, mobileTabItems } from "./MobileNavigation";
import { ConnectionBanner } from "./pwa/ConnectionBanner";
import { NotificationSettings } from "./pwa/NotificationSettings";

const EventSubscriptionsPanel = lazy(() => import("./panels/connect/EventSubscriptionsPanel").then((m) => ({ default: m.EventSubscriptionsPanel })));
const ConnectPanel = lazy(() => import("./panels/ConnectPanel").then((m) => ({ default: m.ConnectPanel })));
const RemoteHostsPanel = lazy(() => import("./panels/RemoteHostsPanel").then((m) => ({ default: m.RemoteHostsPanel })));
import { I18nProvider, Language, ThemeMode, useI18n } from "./i18n";
import {
  activeMachineScope,
  api,
  APIError,
  FLEET_WARNING_EVENT,
  MAILBOX_UNREAD_EVENT,
  mailboxAPI,
  currentFleetWarnings,
  isFleetStaleNotice,
  getLaunchAtLogin,
  isDesktopApp,
  LaunchAtLoginStatus,
  openLocalWebUI,
  setActiveMachineScope,
  setLaunchAtLogin,
  type AuthSession,
} from "./api";
import { currentOfflineTargets, resetFleetWarnings } from "./api/fleetWarnings";
import { usePolling } from "./hooks/usePolling";
import {
  accountLabel,
  AUTH_CHANGED_EVENT,
  consoleRole,
  loadSessionWithRetry,
} from "./signIn";
import { SignInGate } from "./SignInGate";
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
  SECONDARY_SIDEBAR_COLLAPSED_KEY,
  SECONDARY_SIDEBAR_STORAGE_KEY,
  SECONDARY_SIDEBAR_WIDTH,
} from "./sidebarSizing";

type Tab = NavigationTabID;

const SIDEBAR_LAYOUT_VERSION_KEY = "agentmux:sidebar-layout-version";
const SIDEBAR_LAYOUT_VERSION = "two-level-sidebar-v1";

type NavItem = { id: Tab; labelKey: string; icon: typeof LayoutGrid };
type NavGroup = { id: string; labelKey: string; shortLabelKey?: string; icon: typeof LayoutGrid; items: NavItem[] };

const OVERVIEW_ITEM: NavItem = { id: "overview", labelKey: "nav.overview", icon: LayoutGrid };

const NAV_GROUPS: NavGroup[] = [
  {
    id: "agents",
    labelKey: "nav.group.agents",
    icon: Bot,
    items: [
      { id: "agents", labelKey: "nav.agents", icon: Bot },
      { id: "messages", labelKey: "nav.messages", icon: MessagesSquare },
      { id: "orchestrations", labelKey: "nav.orchestrations", icon: Workflow },
      { id: "frameworks", labelKey: "nav.frameworks", icon: Blocks },
      { id: "skills", labelKey: "nav.skills", icon: Sparkles },
      { id: "mcp", labelKey: "nav.mcp", icon: Boxes },
      { id: "memory", labelKey: "nav.memory", icon: Brain },
    ],
  },
  {
    id: "connectivity",
    labelKey: "nav.group.connectivity",
    shortLabelKey: "nav.short.connectivity",
    icon: Cable,
    items: [
      { id: "channels", labelKey: "nav.channels", icon: Cable },
      { id: "schedules", labelKey: "nav.schedules", icon: CalendarClock },
      { id: "triggers", labelKey: "nav.triggers", icon: Zap },
      { id: "subscriptions", labelKey: "nav.subscriptions", icon: Cable },
      { id: "meetings", labelKey: "nav.meetings", icon: Video },
    ],
  },
  {
    id: "operations",
    labelKey: "nav.group.operations",
    shortLabelKey: "nav.short.operations",
    icon: Activity,
    items: [
      { id: "sessions", labelKey: "nav.sessions", icon: MessageSquareText },
      { id: "machine-status", labelKey: "nav.machineStatus", icon: MonitorDot },
      { id: "observability", labelKey: "nav.observability", icon: Activity },
      { id: "usage", labelKey: "nav.usage", icon: Gauge },
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

const TABS: NavItem[] = [OVERVIEW_ITEM, ...NAV_GROUPS.flatMap((group) => group.items)];
const UNREAD_POLL_INTERVAL_MS = 30_000;

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
  if (value === "menubar") return "settings";
  // Providers and routing moved to the frameworks; old links land there.
  if (value === "providers" || value === "gateway") return "frameworks";
  if (value === "connect") return "channels";
  // Tenants are gone; account management took their place.
  if (value === "tenants") return "accounts";
  return TABS.some((item) => item.id === value) ? value as Tab : null;
}

function initialTab(): Tab {
  return tabFromHash() ?? "agents";
}

export function App() {
  const [tab, setTab] = useState<Tab>(initialTab);
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
      }
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  // Every tab owns a hash route (#tab or #tab/subview); panels with internal
  // sub-navigation (observability) extend the same scheme.
  function selectTab(next: Tab) {
    if (activeRouteRef.current !== next) resetFleetWarnings();
    activeRouteRef.current = next;
    setTab(next);
    if (next === "observability") {
      if (!window.location.hash.startsWith("#observability")) window.location.hash = "#observability/overview";
      return;
    }
    window.location.hash = `#${next}`;
  }

  return (
    <I18nProvider language={language}>
      <SignInGate>
        <Shell
          tab={tab}
          setTab={selectTab}
          language={language}
          setLanguage={setLanguage}
          themeMode={themeMode}
          setThemeMode={setThemeMode}
        />
      </SignInGate>
      <ConfirmHost />
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
  const identity = useConsoleIdentity();
  const session = identity.session;
  const nativeDesktopAdmin = window.location.protocol === "wails:";
  const role = consoleRole(session, nativeDesktopAdmin);
  const identityIsAdmin = role === "admin";
  // Admin-only requests wait until the session confirms the role, so a member
  // never fires them while the session is still loading.
  const adminReady = identityIsAdmin && (identity.state === "ready" || nativeDesktopAdmin);
  // A member reaches only what the server lets them use: their messages and
  // their own account (password and devices).
  const navGroups = useMemo(() => navigationForRole(NAV_GROUPS, role).map((group) => role === "member"
    ? { ...group, items: group.items.map((item) => item.id === "accounts" ? { ...item, labelKey: "nav.myAccount" } : item) }
    : group), [role]);
  const navTabs = useMemo(
    () => [...(identityIsAdmin ? [OVERVIEW_ITEM] : []), ...navGroups.flatMap((group) => group.items)],
    [identityIsAdmin, navGroups],
  );
  const mobileTabs = useMemo(() => mobileTabItems(navTabs), [navTabs]);
  const identityName = session?.account ? accountLabel(session.account) : t("app.admin");
  const navigationLocked = identity.state !== "ready" && !nativeDesktopAdmin;
  const visibleTab = tabAllowedForRole(tab, role) ? tab : MEMBER_LANDING_TAB;
  const active = useMemo(
    () => navTabs.find((item) => item.id === visibleTab) ?? TABS.find((item) => item.id === visibleTab) ?? TABS[0],
    [navTabs, visibleTab],
  );
  const activeGroup = useMemo(
    () => navigationGroupForTab(navGroups, visibleTab),
    [navGroups, visibleTab],
  );
  const [primarySidebarWidth, setPrimarySidebarWidth] = useState(() => {
    if (localStorage.getItem(SIDEBAR_LAYOUT_VERSION_KEY) !== SIDEBAR_LAYOUT_VERSION) {
      localStorage.setItem(SIDEBAR_LAYOUT_VERSION_KEY, SIDEBAR_LAYOUT_VERSION);
      localStorage.setItem(PRIMARY_SIDEBAR_STORAGE_KEY, String(PRIMARY_SIDEBAR_WIDTH.default));
      localStorage.setItem(SECONDARY_SIDEBAR_STORAGE_KEY, String(SECONDARY_SIDEBAR_WIDTH.default));
      return PRIMARY_SIDEBAR_WIDTH.default;
    }
    return readSidebarWidth(
      localStorage,
      PRIMARY_SIDEBAR_STORAGE_KEY,
      PRIMARY_SIDEBAR_WIDTH.default,
      PRIMARY_SIDEBAR_WIDTH.min,
      PRIMARY_SIDEBAR_WIDTH.max,
    );
  });
  const [secondarySidebarWidth, setSecondarySidebarWidth] = useState(() =>
    readSidebarWidth(
      localStorage,
      SECONDARY_SIDEBAR_STORAGE_KEY,
      SECONDARY_SIDEBAR_WIDTH.default,
      SECONDARY_SIDEBAR_WIDTH.min,
      SECONDARY_SIDEBAR_WIDTH.max,
    ),
  );
  const [primaryCollapsed, setPrimaryCollapsed] = useState(() =>
    localStorage.getItem(PRIMARY_SIDEBAR_COLLAPSED_KEY) === "true",
  );
  const [secondaryCollapsed, setSecondaryCollapsed] = useState(() =>
    localStorage.getItem(SECONDARY_SIDEBAR_COLLAPSED_KEY) === "true",
  );
  const [quickActionError, setQuickActionError] = useState("");
  const [fleetWarnings, setFleetWarnings] = useState<string[]>(currentFleetWarnings);
  const [dismissedFleetWarnings, setDismissedFleetWarnings] = useState("");
  const fleetWarningKey = JSON.stringify(fleetWarnings);
  // Set once every machine a warning found offline is connected again: what
  // the page shows of them is then out of date and worth reloading.
  const [fleetRecovered, setFleetRecovered] = useState("");
  const [machineScopeVersion, setMachineScopeVersion] = useState(0);
  const [remoteAddRequest, setRemoteAddRequest] = useState(0);
  const [createAgentRequested, setCreateAgentRequested] = useState(false);
  const handleCreateAgentRequest = useCallback(() => setCreateAgentRequested(false), []);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [activeSearchIndex, setActiveSearchIndex] = useState(0);
  const searchAnchorRef = useRef<HTMLDivElement>(null);
  const closeSearchResults = useCallback(() => setSearchOpen(false), []);
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const closeMobileNav = useCallback(() => setMobileNavOpen(false), []);
  const unreadMessages = useUnreadMessages(identity.state === "ready", visibleTab === "messages");
  const navBadges = useMemo(() => ({ messages: unreadMessages }), [unreadMessages]);
  const [preferencesPanelStyle, setPreferencesPanelStyle] = useState<CSSProperties>({ visibility: "hidden" });
  const preferencesButtonRef = useRef<HTMLButtonElement>(null);
  const preferencesPanelRef = useRef<HTMLDivElement>(null);
  const desktop = isDesktopApp();
  const ActiveIcon = active.icon;
  const SecondaryIcon = activeGroup?.icon ?? LayoutGrid;
  const secondaryItems = activeGroup?.items ?? [OVERVIEW_ITEM];
  const shellStyle = {
    "--primary-sidebar-width": `${primaryCollapsed ? PRIMARY_SIDEBAR_COLLAPSED_WIDTH : primarySidebarWidth}px`,
    "--secondary-sidebar-width": `${secondarySidebarWidth}px`,
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

  const positionPreferencesPanel = useCallback(() => {
    const anchor = preferencesButtonRef.current;
    if (!anchor) return;
    const rect = anchor.getBoundingClientRect();
    const viewportPadding = 12;
    const panelGap = 8;
    const panelWidth = Math.min(270, Math.max(0, window.innerWidth - viewportPadding * 2));
    const left = Math.min(
      Math.max(viewportPadding, rect.left),
      Math.max(viewportPadding, window.innerWidth - panelWidth - viewportPadding),
    );
    const spaceBelow = window.innerHeight - rect.bottom - panelGap - viewportPadding;
    const openAbove = spaceBelow < 360 && rect.top > window.innerHeight / 2;
    setPreferencesPanelStyle({
      ...(openAbove
        ? { bottom: window.innerHeight - rect.top + panelGap }
        : { top: rect.bottom + panelGap }),
      left,
      width: panelWidth,
      maxHeight: Math.max(120, openAbove ? rect.top - panelGap - viewportPadding : spaceBelow),
    });
  }, []);

  useEffect(() => {
    // Members reach only this hub; a remote machine selection left over from
    // an administrator session would route their requests elsewhere.
    if (session && role === "member" && activeMachineScope() !== "local") {
      setActiveMachineScope("local");
      window.location.reload();
    }
  }, [role, session]);

  useEffect(() => {
    if (identity.state === "ready" && !tabAllowedForRole(tab, role)) setTab(MEMBER_LANDING_TAB);
  }, [identity.state, role, setTab, tab]);

  useEffect(() => {
    window.scrollTo({ top: 0, left: 0, behavior: "auto" });
    setMobileNavOpen(false);
  }, [visibleTab]);

  useEffect(() => {
    localStorage.setItem(PRIMARY_SIDEBAR_STORAGE_KEY, String(primarySidebarWidth));
  }, [primarySidebarWidth]);

  useEffect(() => {
    localStorage.setItem(SECONDARY_SIDEBAR_STORAGE_KEY, String(secondarySidebarWidth));
  }, [secondarySidebarWidth]);

  useEffect(() => {
    localStorage.setItem(PRIMARY_SIDEBAR_COLLAPSED_KEY, String(primaryCollapsed));
  }, [primaryCollapsed]);

  useEffect(() => {
    localStorage.setItem(SECONDARY_SIDEBAR_COLLAPSED_KEY, String(secondaryCollapsed));
    if (secondaryCollapsed) setSearchOpen(false);
  }, [secondaryCollapsed]);

  useEffect(() => {
    setActiveSearchIndex((current) => Math.min(current, Math.max(0, searchResults.length - 1)));
  }, [searchResults.length]);

  useEffect(() => {
    if (!preferencesOpen) return;
    positionPreferencesPanel();
    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (preferencesButtonRef.current?.contains(target) || preferencesPanelRef.current?.contains(target)) return;
      setPreferencesOpen(false);
    };
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") setPreferencesOpen(false);
    };
    window.addEventListener("resize", positionPreferencesPanel);
    window.addEventListener("scroll", positionPreferencesPanel, true);
    document.addEventListener("pointerdown", closeOnOutsidePointer);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("resize", positionPreferencesPanel);
      window.removeEventListener("scroll", positionPreferencesPanel, true);
      document.removeEventListener("pointerdown", closeOnOutsidePointer);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [preferencesOpen, positionPreferencesPanel, primarySidebarWidth, primaryCollapsed]);

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

  function selectOverview() {
    setTab("overview");
  }

  function requestNewAgent() {
    setCreateAgentRequested(true);
    setTab("agents");
  }

  function selectGroup(group: NavGroup) {
    setSecondaryCollapsed(false);
    const destination = primaryGroupDestination(group, visibleTab);
    if (destination) setTab(destination);
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
      className={`app-shell${secondaryCollapsed ? "" : " secondary-open"}${primaryCollapsed ? " primary-collapsed" : ""}${desktop ? " native-desktop" : ""}`}
      style={shellStyle}
    >
      <aside className="sidebar">
        <div className="brand-actions titlebar-actions">
          <button
            ref={preferencesButtonRef}
            className="brand-action-button preference-trigger"
            type="button"
            title={t("app.settings")}
            aria-label={t("app.settings")}
            aria-haspopup="dialog"
            aria-expanded={preferencesOpen}
            aria-controls="sidebar-preference-panel"
            onClick={() => setPreferencesOpen((open) => !open)}
          >
            <Settings size={17} />
            <span>{t("app.settings")}</span>
          </button>
          {preferencesOpen && createPortal(
            <div
              ref={preferencesPanelRef}
              id="sidebar-preference-panel"
              className="preference-panel sidebar-preference-popover"
              style={preferencesPanelStyle}
              role="dialog"
              aria-label={t("app.settings")}
            >
              <PreferenceControls
                admin={identityIsAdmin}
                language={language}
                setLanguage={setLanguage}
                themeMode={themeMode}
                setThemeMode={setThemeMode}
              />
            </div>,
            document.body,
          )}
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

        <nav id="primary-navigation" className="nav primary-nav" aria-label={t("nav.primary")}>
          {identityIsAdmin && <button
            className={`nav-item nav-primary-item${visibleTab === OVERVIEW_ITEM.id ? " active" : ""}`}
            onClick={selectOverview}
            title={t(OVERVIEW_ITEM.labelKey)}
            aria-label={t(OVERVIEW_ITEM.labelKey)}
            aria-current={visibleTab === OVERVIEW_ITEM.id ? "page" : undefined}
            disabled={navigationLocked}
          >
            <LayoutGrid size={18} />
            <span>{t(OVERVIEW_ITEM.labelKey)}</span>
          </button>}

          {navGroups.map((group) => {
            const GroupIcon = group.icon;
            const hasActive = group.items.some((item) => item.id === visibleTab);
            const hasUnread = unreadMessages > 0 && group.items.some((item) => item.id === "messages");
            return (
              <button
                key={group.id}
                className={`nav-item nav-primary-item${hasActive ? " active" : ""}`}
                onClick={() => selectGroup(group)}
                aria-expanded={hasActive && !secondaryCollapsed}
                aria-controls="secondary-navigation"
                aria-label={t(group.labelKey)}
                aria-current={hasActive ? "true" : undefined}
                title={t(group.labelKey)}
                disabled={navigationLocked}
              >
                <GroupIcon size={18} />
                <span>{t(group.shortLabelKey ?? group.labelKey)}</span>
                {hasUnread && <i className="nav-badge-dot" aria-hidden="true" />}
              </button>
            );
          })}
        </nav>

        <MobileTabBar
          items={mobileTabs}
          active={visibleTab}
          badges={navBadges}
          drawerOpen={mobileNavOpen}
          isDisabled={() => navigationLocked}
          onSelect={setTab}
          onOpenMore={() => setMobileNavOpen((open) => !open)}
        />

        <div className="account primary-account" title={identityName}>
          <strong>{identityName}</strong>
        </div>

        {!primaryCollapsed && <SidebarResizeHandle
          label={t("nav.resizePrimary")}
          value={primarySidebarWidth}
          limits={PRIMARY_SIDEBAR_WIDTH}
          onChange={setPrimarySidebarWidth}
        />}
      </aside>

      <aside id="secondary-navigation" className="secondary-sidebar" hidden={secondaryCollapsed}>
        <header className="secondary-sidebar-header">
          <span className="secondary-sidebar-title">
            <SecondaryIcon size={16} />
            <strong title={activeGroup ? t(activeGroup.labelKey) : t(OVERVIEW_ITEM.labelKey)}>{activeGroup ? t(activeGroup.labelKey) : t(OVERVIEW_ITEM.labelKey)}</strong>
          </span>
          <button
            className="secondary-collapse"
            type="button"
            title={t("nav.collapse")}
            aria-label={t("nav.collapse")}
            aria-expanded="true"
            aria-controls="secondary-navigation"
            onClick={() => setSecondaryCollapsed(true)}
          >
            <PanelLeftClose size={16} />
          </button>
        </header>

        <div
          ref={searchAnchorRef}
          className="sidebar-search-container secondary-search-container"
        >
          <div className="sidebar-search search-box">
            <Search size={15} aria-hidden="true" />
            <input
              type="search"
              role="combobox"
              aria-label={t("app.search")}
              aria-autocomplete="list"
              aria-controls="secondary-sidebar-search-results"
              aria-expanded={showSearchResults}
              aria-activedescendant={showSearchResults && searchResults.length
                ? `secondary-sidebar-search-result-${searchResults[activeSearchIndex]?.id ?? searchResults[0].id}`
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
          {showSearchResults && !secondaryCollapsed && (
            <SidebarSearchPopover
              anchorRef={searchAnchorRef}
              layoutKey={`${primarySidebarWidth}:${secondarySidebarWidth}:${primaryCollapsed}`}
              onClose={closeSearchResults}
              id="secondary-sidebar-search-results"
              aria-label={t("app.searchResults")}
            >
              {searchResults.length ? searchResults.map((item, index) => {
                const Icon = item.icon;
                return (
                  <button
                    id={`secondary-sidebar-search-result-${item.id}`}
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

        <nav className="secondary-nav" aria-label={activeGroup ? t(activeGroup.labelKey) : t(OVERVIEW_ITEM.labelKey)}>
          {secondaryItems.map((item) => {
            const Icon = item.icon;
            return (
              <button
                key={item.id}
                className={`secondary-nav-item${visibleTab === item.id ? " active" : ""}`}
                onClick={() => setTab(item.id)}
                disabled={navigationLocked}
              >
                <Icon size={16} />
                <span>{t(item.labelKey)}</span>
                {item.id === "messages" && unreadMessages > 0 && (
                  <span className="nav-badge" aria-label={t("messages.unreadCount", { count: unreadMessages })}>
                    {unreadMessages > 99 ? "99+" : unreadMessages}
                  </span>
                )}
              </button>
            );
          })}
        </nav>

        <SidebarResizeHandle
          label={t("nav.resizeSecondary")}
          value={secondarySidebarWidth}
          limits={SECONDARY_SIDEBAR_WIDTH}
          onChange={setSecondarySidebarWidth}
        />
      </aside>

      <MobileDrawer
        open={mobileNavOpen}
        overview={identityIsAdmin ? OVERVIEW_ITEM : undefined}
        groups={navGroups}
        active={visibleTab}
        badges={navBadges}
        isDisabled={() => navigationLocked}
        onSelect={(id) => {
          setMobileNavOpen(false);
          setTab(id);
        }}
        onClose={closeMobileNav}
      >
        <PreferenceControls
          admin={identityIsAdmin}
          language={language}
          setLanguage={setLanguage}
          themeMode={themeMode}
          setThemeMode={setThemeMode}
        />
      </MobileDrawer>

      <div className="workspace">
        <div className="workspace-canvas">
          <header className="workspace-header">
          <div className="title-row">
            {secondaryCollapsed && (
              <button
                className="secondary-collapse secondary-expand"
                type="button"
                title={t("nav.expand")}
                aria-label={t("nav.expand")}
                aria-expanded="false"
                aria-controls="secondary-navigation"
                onClick={() => setSecondaryCollapsed(false)}
              >
                <PanelLeftOpen size={16} />
              </button>
            )}
            <ActiveIcon size={20} />
            <h1>{t(active.labelKey)}</h1>
            {visibleTab === "agents" && (
              <span className="workspace-header-description">{t("agents.subtitle")}</span>
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
		      {adminReady && (
			    <RemoteTargetSelector
				  onAddMachine={() => {
                  setRemoteAddRequest((request) => request + 1);
                  setTab("machines");
                }}
			    />
		      )}
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

        <main className={`main main-${visibleTab}`}>
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
				  {visibleTab === "settings" && <NotificationSettings />}
				  <RegisteredPanel
				    tab={visibleTab}
				    createRequested={createAgentRequested}
				    onCreateRequestHandled={handleCreateAgentRequest}
				  />
				  {visibleTab === "channels" && <ConnectPanel view="channels" />}
				  {visibleTab === "schedules" && <ConnectPanel view="schedules" />}
				  {visibleTab === "triggers" && <ConnectPanel view="triggers" />}
                  {visibleTab === "subscriptions" && <EventSubscriptionsPanel />}
				  {visibleTab === "machines" && <RemoteHostsPanel addRequest={remoteAddRequest} />}
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

// useUnreadMessages counts the direct messages the signed-in person has not
// read, for the navigation badge. The Messages page reports the count itself
// while it is open, so this only asks when that page is closed. It stops
// asking where there is no mailbox for the caller.
function useUnreadMessages(enabled: boolean, pageOpen: boolean): number {
  const [unread, setUnread] = useState(0);
  const [available, setAvailable] = useState(true);
  const polling = enabled && available && !pageOpen;

  const refresh = useCallback(() => {
    mailboxAPI.overview()
      .then((overview) => setUnread(overview.unread))
      .catch((error) => {
        if (error instanceof APIError && [403, 404, 503].includes(error.status)) setAvailable(false);
      });
  }, []);

  useEffect(() => {
    if (polling) refresh();
  }, [polling, refresh]);
  usePolling(refresh, UNREAD_POLL_INTERVAL_MS, { enabled: polling });

  useEffect(() => {
    const receive = (event: Event) => {
      const count = (event as CustomEvent<number>).detail;
      if (typeof count === "number") setUnread(count);
    };
    window.addEventListener(MAILBOX_UNREAD_EVENT, receive);
    return () => window.removeEventListener(MAILBOX_UNREAD_EVENT, receive);
  }, []);

  return unread;
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

function PreferenceControls({
  admin,
  language,
  setLanguage,
  themeMode,
  setThemeMode,
}: {
  // Machine controls such as keep-awake are for administrators only.
  admin: boolean;
  language: Language;
  setLanguage: (language: Language) => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
}) {
  const { t } = useI18n();
  const [launchAtLogin, setLaunchAtLoginStatus] = useState<LaunchAtLoginStatus | null>(null);
  const [launchAtLoginBusy, setLaunchAtLoginBusy] = useState(false);
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
      {admin && <KeepAwakeControl onError={setPreferenceError} />}
      {preferenceError && <small className="preference-error">{preferenceError}</small>}
    </>
  );
}
