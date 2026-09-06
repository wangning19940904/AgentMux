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
  ChevronDown,
  ChevronLeft,
  Coffee,
  ExternalLink,
  Gauge,
  Languages,
  LayoutGrid,
  Moon,
  PanelLeft,
  PanelTop,
  RefreshCw,
  Search,
  ServerCog,
  MessageSquareText,
  Settings,
  ShieldCheck,
  Sparkles,
  Sun,
  Workflow,
  Video,
  Zap,
} from "lucide-react";
import { RemoteTargetSelector } from "./RemoteTargetSelector";
import { MeetingInvitationOverlay } from "./MeetingControls";
import { MeetingProvider } from "./MeetingContext";
import { RegisteredPanel } from "./panelRegistry";

const ConnectPanel = lazy(() => import("./panels/ConnectPanel").then((m) => ({ default: m.ConnectPanel })));
const RemoteHostsPanel = lazy(() => import("./panels/RemoteHostsPanel").then((m) => ({ default: m.RemoteHostsPanel })));
const TenantsPanel = lazy(() => import("./panels/TenantsPanel").then((m) => ({ default: m.TenantsPanel })));
import { I18nProvider, Language, ThemeMode, useI18n } from "./i18n";
import {
	activeTenantScopeKey,
  activeMachineScope,
  api,
  FLEET_WARNING_EVENT,
  currentFleetWarnings,
  getLaunchAtLogin,
  isDesktopApp,
  KeepAwakeStatus,
  LaunchAtLoginStatus,
  openLocalWebUI,
  setActiveTenantScopeID,
  setActiveMachineScope,
  setLaunchAtLogin,
	tenantScopeKey,
  type Tenant,
  type TenancySelf,
} from "./api";
import { resolveTenancyGateWithRetry, type TenancyGateState } from "./tenancyGate";
import { resetFleetWarnings } from "./api/fleetWarnings";
import {
  navigationGroupForTab,
  primaryGroupDestination,
  searchNavigationItems,
  secondaryNavigationForTab,
} from "./navigationModel";
import {
  NAVIGATION_GROUP_SEARCH_ALIASES,
  NAVIGATION_SEARCH_ALIASES,
  type NavigationTabID,
} from "./navigationSearchAliases";
import {
  clampSidebarWidth,
  PRIMARY_SIDEBAR_STORAGE_KEY,
  PRIMARY_SIDEBAR_WIDTH,
  readSidebarWidth,
  SECONDARY_SIDEBAR_STORAGE_KEY,
  SECONDARY_SIDEBAR_WIDTH,
} from "./sidebarSizing";

type Tab = NavigationTabID;

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
      { id: "orchestrations", labelKey: "nav.orchestrations", icon: Workflow },
      { id: "frameworks", labelKey: "nav.frameworks", icon: Blocks },
      { id: "gateway", labelKey: "nav.gateway", icon: Workflow },
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
    items: [
      { id: "sessions", labelKey: "nav.sessions", icon: MessageSquareText },
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
      { id: "tenants", labelKey: "nav.tenants", icon: ShieldCheck },
      { id: "settings", labelKey: "nav.settings", icon: Settings },
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

const TENANT_OPTIONS_CACHE_KEY = "agentmux:tenant-options";

function cachedTenantOptions(): Tenant[] {
  try {
    const value = JSON.parse(localStorage.getItem(TENANT_OPTIONS_CACHE_KEY) || "[]") as unknown;
    if (!Array.isArray(value)) return [];
    return value.filter((item): item is Tenant => Boolean(
      item && typeof item === "object" &&
      typeof (item as Tenant).id === "string" &&
      typeof (item as Tenant).name === "string" &&
      ["active", "disabled"].includes((item as Tenant).status),
    ));
  } catch {
    return [];
  }
}

function cacheTenantOptions(tenants: Tenant[]) {
  localStorage.setItem(TENANT_OPTIONS_CACHE_KEY, JSON.stringify(tenants));
}

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
  if (value === "providers") return "gateway";
  if (value === "connect") return "channels";
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
  const tenantGate = useTenancyGate();
  const tenantIdentity = tenantGate.identity;
  const nativeDesktopAdmin = window.location.protocol === "wails:";
  const identityIsAdmin = tenantIdentity?.admin === true || (!tenantIdentity && nativeDesktopAdmin);
  const [tenantOptions, setTenantOptions] = useState<Tenant[]>(cachedTenantOptions);
  const [tenantOptionsLoaded, setTenantOptionsLoaded] = useState(false);
	const [tenantOptionsRevision, setTenantOptionsRevision] = useState(0);
	const [selectedTenantScopeID, setSelectedTenantScopeID] = useState(activeTenantScopeKey);
  const activeTenantOptions = useMemo(
    () => tenantOptions.filter((tenant) => tenant.status === "active"),
    [tenantOptions],
  );
	const selectedTenant = identityIsAdmin
		? activeTenantOptions.find((tenant) => {
			const canonical = tenantScopeKey(tenant.id, tenant.target_id || "local");
			return canonical === selectedTenantScopeID || tenant.id === selectedTenantScopeID;
		})
    : undefined;
	const selectedTenantTargetID = selectedTenant?.target_id || (selectedTenant ? "local" : undefined);
  const identityName = selectedTenant?.name ?? (identityIsAdmin
    ? t("app.admin")
    : tenantIdentity?.tenant || t("app.tenant"));
  const navigationLocked = tenantGate.state !== "ready";
  const visibleTab = tenantGate.state === "required" ? "tenants" : tab;
  const active = useMemo(
    () => TABS.find((item) => item.id === visibleTab) ?? TABS[0],
    [visibleTab],
  );
  const activeGroup = useMemo(
    () => navigationGroupForTab(NAV_GROUPS, visibleTab),
    [visibleTab],
  );
  const initialSecondaryNavigation = secondaryNavigationForTab(NAV_GROUPS, tab, "overview");
  const [secondaryGroupID, setSecondaryGroupID] = useState<string | null>(initialSecondaryNavigation.groupID);
  const [secondaryOpen, setSecondaryOpen] = useState(initialSecondaryNavigation.open);
  const [primarySidebarWidth, setPrimarySidebarWidth] = useState(() =>
    readSidebarWidth(
      localStorage,
      PRIMARY_SIDEBAR_STORAGE_KEY,
      PRIMARY_SIDEBAR_WIDTH.default,
      PRIMARY_SIDEBAR_WIDTH.min,
      PRIMARY_SIDEBAR_WIDTH.max,
    ),
  );
  const [secondarySidebarWidth, setSecondarySidebarWidth] = useState(() =>
    readSidebarWidth(
      localStorage,
      SECONDARY_SIDEBAR_STORAGE_KEY,
      SECONDARY_SIDEBAR_WIDTH.default,
      SECONDARY_SIDEBAR_WIDTH.min,
      SECONDARY_SIDEBAR_WIDTH.max,
    ),
  );
  const [quickActionError, setQuickActionError] = useState("");
  const [fleetWarnings, setFleetWarnings] = useState<string[]>(currentFleetWarnings);
  const [dismissedFleetWarnings, setDismissedFleetWarnings] = useState("");
  const fleetWarningKey = JSON.stringify(fleetWarnings);
  const [machineScopeVersion, setMachineScopeVersion] = useState(0);
  const [remoteAddRequest, setRemoteAddRequest] = useState(0);
  const [searchQuery, setSearchQuery] = useState("");
  const [searchOpen, setSearchOpen] = useState(false);
  const [activeSearchIndex, setActiveSearchIndex] = useState(0);
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const [preferencesPanelStyle, setPreferencesPanelStyle] = useState<CSSProperties>({ visibility: "hidden" });
  const preferencesButtonRef = useRef<HTMLButtonElement>(null);
  const preferencesPanelRef = useRef<HTMLDivElement>(null);
  const desktop = isDesktopApp();
  const secondaryGroup = NAV_GROUPS.find((group) => group.id === secondaryGroupID) ?? activeGroup;
  const ActiveIcon = active.icon;
  const SecondaryIcon = secondaryGroup?.icon ?? LayoutGrid;
  const tenantPanelOptions = useMemo(() => {
    const machineScope = activeMachineScope();
    return machineScope === "all"
      ? tenantOptions
      : tenantOptions.filter((tenant) => (tenant.target_id || "local") === machineScope);
  }, [machineScopeVersion, tenantOptions]);
  const shellStyle = {
    "--primary-sidebar-width": `${primarySidebarWidth}px`,
    "--secondary-sidebar-width": `${secondarySidebarWidth}px`,
  } as CSSProperties;
  const searchResults = useMemo(() => {
    const searchableTabs = TABS
      .filter((item) => !navigationLocked || item.id === "tenants")
      .map((item) => {
        const group = navigationGroupForTab(NAV_GROUPS, item.id);
        return {
          ...item,
          label: t(item.labelKey),
          groupLabel: group ? t(group.labelKey) : "",
          keywords: [item.labelKey, ...NAVIGATION_SEARCH_ALIASES[item.id]],
          groupKeywords: group ? NAVIGATION_GROUP_SEARCH_ALIASES[group.id] ?? [] : [],
        };
      });
    return searchNavigationItems(searchableTabs, searchQuery);
  }, [language, navigationLocked, searchQuery, t]);
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
    const top = rect.bottom + panelGap;
    setPreferencesPanelStyle({
      top,
      left,
      width: panelWidth,
      maxHeight: Math.max(120, window.innerHeight - top - viewportPadding),
    });
  }, []);

  useEffect(() => {
    if (tenantIdentity && !tenantIdentity.admin && activeMachineScope() !== "local") {
      setActiveMachineScope("local");
      window.location.reload();
    }
  }, [tenantIdentity]);

  useEffect(() => {
    if (visibleTab === "overview") {
      setSecondaryOpen(false);
      return;
    }
    if (activeGroup) {
      setSecondaryGroupID(activeGroup.id);
      setSecondaryOpen(true);
    }
  }, [activeGroup, visibleTab]);

  useEffect(() => {
    if (tenantGate.state === "required" && tab !== "tenants") {
      setTab("tenants");
    }
  }, [setTab, tab, tenantGate.state]);

  useEffect(() => {
    window.scrollTo({ top: 0, left: 0, behavior: "auto" });
  }, [visibleTab]);

  useEffect(() => {
    // Do not treat the identity-loading state as a confirmed tenant session.
    // Clearing here loses a persisted admin preview before /tenancy/self has
    // had a chance to prove that the Console is still running as admin.
    if (!tenantIdentity) return;
    if (!tenantIdentity?.admin) {
      setTenantOptions([]);
      setTenantOptionsLoaded(true);
      if (selectedTenantScopeID) {
        setActiveTenantScopeID("");
        setSelectedTenantScopeID("");
      }
      return;
    }
    let active = true;
    setTenantOptionsLoaded(false);
		api.allTenants()
      .then((tenants) => {
        if (!active) return;
        const nextTenants = tenants ?? [];
        setTenantOptions(nextTenants);
        cacheTenantOptions(nextTenants);
        setTenantOptionsLoaded(true);
      })
      .catch(() => {
        if (!active) return;
        // Keep the last successful tenant index visible while a remote machine
        // is temporarily slow or offline.
        setTenantOptionsLoaded(true);
      });
    return () => { active = false; };
  }, [tenantIdentity?.admin, tenantOptionsRevision]);

  useEffect(() => {
    if (!tenantOptionsLoaded || !selectedTenantScopeID || !identityIsAdmin) return;
		const selected = activeTenantOptions.find((tenant) => {
			const canonical = tenantScopeKey(tenant.id, tenant.target_id || "local");
			return canonical === selectedTenantScopeID || tenant.id === selectedTenantScopeID;
		});
		if (selected) {
			const canonical = tenantScopeKey(selected.id, selected.target_id || "local");
			if (canonical !== selectedTenantScopeID) {
				setActiveTenantScopeID(selected.id, selected.target_id || "local");
				setSelectedTenantScopeID(canonical);
			}
			return;
		}
    setActiveTenantScopeID("");
    setSelectedTenantScopeID("");
  }, [activeTenantOptions, identityIsAdmin, selectedTenantScopeID, tenantOptionsLoaded]);

  useEffect(() => {
    localStorage.setItem(PRIMARY_SIDEBAR_STORAGE_KEY, String(primarySidebarWidth));
  }, [primarySidebarWidth]);

  useEffect(() => {
    localStorage.setItem(SECONDARY_SIDEBAR_STORAGE_KEY, String(secondarySidebarWidth));
  }, [secondarySidebarWidth]);

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
  }, [preferencesOpen, positionPreferencesPanel, primarySidebarWidth]);

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
    const refreshScope = () => {
      setFleetWarnings([]);
      setDismissedFleetWarnings("");
      setMachineScopeVersion((version) => version + 1);
    };
    window.addEventListener("agentmux:machine-scope-changed", refreshScope);
    return () => window.removeEventListener("agentmux:machine-scope-changed", refreshScope);
  }, []);

  function selectGroup(group: NavGroup) {
    const destination = primaryGroupDestination(group, visibleTab);
    setSecondaryGroupID(group.id);
    setSecondaryOpen(true);
    if (destination) setTab(destination);
  }

  function selectOverview() {
    setSecondaryOpen(false);
    setTab("overview");
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
    <div
      className={`app-shell${secondaryOpen && secondaryGroup ? " secondary-open" : ""}${desktop ? " native-desktop" : ""}`}
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
            </button>
          )}
        </div>
        <div className="brand">
          <img className="brand-logo" src="/agentmux-logo.png" alt="" aria-hidden="true" />
          <div className="brand-copy">
            <strong>AgentMux</strong>
          </div>
        </div>

        <div
          className="sidebar-search-container"
          onBlur={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget)) setSearchOpen(false);
          }}
        >
          <div className="sidebar-search search-box">
            <Search size={17} aria-hidden="true" />
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
          {showSearchResults && (
            <div
              id="sidebar-search-results"
              className="sidebar-search-results"
              role="listbox"
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
                    <Icon size={17} aria-hidden="true" />
                    <span>
                      <strong>{item.label}</strong>
                      {item.groupLabel && <small>{item.groupLabel}</small>}
                    </span>
                  </button>
                );
              }) : (
                <div className="sidebar-search-empty" role="status">{t("app.searchEmpty")}</div>
              )}
            </div>
          )}
        </div>
        {quickActionError && <small className="brand-action-error" role="status">{quickActionError}</small>}

        <nav className="nav primary-nav" aria-label={t("nav.primary")}>
          <button
            className={`nav-item nav-primary-item${visibleTab === OVERVIEW_ITEM.id ? " active" : ""}`}
            onClick={selectOverview}
            title={t(OVERVIEW_ITEM.labelKey)}
            disabled={navigationLocked}
          >
            <LayoutGrid size={18} />
            <span>{t(OVERVIEW_ITEM.labelKey)}</span>
          </button>

          {NAV_GROUPS.map((group) => {
            const GroupIcon = group.icon;
            const hasActive = group.items.some((item) => item.id === visibleTab);
            const canOpenWhileLocked = group.items.some((item) => item.id === "tenants");
            return (
              <button
                key={group.id}
                className={`nav-item nav-primary-item${hasActive ? " active" : ""}`}
                onClick={() => selectGroup(group)}
                aria-expanded={secondaryOpen && secondaryGroup?.id === group.id}
                title={t(group.labelKey)}
                disabled={navigationLocked && !canOpenWhileLocked}
              >
                <GroupIcon size={18} />
                <span>{t(group.labelKey)}</span>
              </button>
            );
          })}
        </nav>

        <nav className="mobile-flat-nav" aria-label={t("nav.primary")}>
          {TABS.map((item) => {
            const Icon = item.icon;
            return (
              <button
                key={item.id}
                className={`nav-item${visibleTab === item.id ? " active" : ""}`}
                onClick={() => setTab(item.id)}
                title={t(item.labelKey)}
                disabled={navigationLocked && item.id !== "tenants"}
              >
                <Icon size={18} />
                <span>{t(item.labelKey)}</span>
              </button>
            );
          })}
        </nav>

        <div className={`account${identityIsAdmin && activeTenantOptions.length > 0 ? " switchable" : ""}`}>
          {identityIsAdmin && activeTenantOptions.length > 0 ? (
            <>
              <select
                className="account-identity-select"
                aria-label={t("app.switchIdentity")}
                value={selectedTenantScopeID}
                onChange={(event) => {
					const scopeKey = event.target.value;
					const tenant = activeTenantOptions.find((item) =>
						tenantScopeKey(item.id, item.target_id || "local") === scopeKey,
					);
					setActiveTenantScopeID(tenant?.id || "", tenant?.target_id || "local");
					setSelectedTenantScopeID(scopeKey);
                }}
              >
                <option value="">{t("app.admin")}</option>
                {activeTenantOptions.map((tenant) => (
				  <option
					key={tenantScopeKey(tenant.id, tenant.target_id || "local")}
					value={tenantScopeKey(tenant.id, tenant.target_id || "local")}
				  >
					{tenant.name} · {(tenant.target_id || "local") === "local" ? t("remote.localMachine") : tenant.target_name || tenant.target_id}
				  </option>
                ))}
              </select>
              <ChevronDown size={15} aria-hidden="true" />
            </>
          ) : (
            <strong>{identityName}</strong>
          )}
        </div>
        <SidebarResizeHandle
          label={t("nav.resizePrimary")}
          value={primarySidebarWidth}
          limits={PRIMARY_SIDEBAR_WIDTH}
          onChange={setPrimarySidebarWidth}
        />
      </aside>

      {secondaryOpen && secondaryGroup && (
        <aside className="secondary-sidebar">
          <header className="secondary-sidebar-header">
            <span className="secondary-sidebar-title">
              <SecondaryIcon size={18} />
              <strong>{t(secondaryGroup.labelKey)}</strong>
            </span>
            <button
              type="button"
              className="secondary-collapse"
              onClick={() => setSecondaryOpen(false)}
              title={t("nav.collapse")}
              aria-label={t("nav.collapse")}
            >
              <ChevronLeft size={18} />
            </button>
          </header>
          <nav className="secondary-nav" aria-label={t(secondaryGroup.labelKey)}>
            {secondaryGroup.items.map((item) => {
              const Icon = item.icon;
              return (
                <button
                  key={item.id}
                  className={`secondary-nav-item${visibleTab === item.id ? " active" : ""}`}
                  onClick={() => setTab(item.id)}
                  disabled={navigationLocked && item.id !== "tenants"}
                >
                  <Icon size={18} />
                  <span>{t(item.labelKey)}</span>
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
      )}

      <div className="workspace">
        <header className="workspace-header">
          <div className="title-row">
            <ActiveIcon size={20} />
            <h1>{t(active.labelKey)}</h1>
            <span className={`system-pill${fleetWarnings.length || tenantGate.state === "error" ? " warning" : ""}`} role="status">
              <span className="status-dot" />
              {t(tenantGate.state === "loading" ? "tenants.checking"
                : tenantGate.state !== "ready" ? "tenants.requiredTitle"
                : fleetWarnings.length ? "app.statusWarning" : "app.status")}
            </span>
          </div>
		  {identityIsAdmin && (
			<RemoteTargetSelector
				allowedTargetID={selectedTenantTargetID}
				onAddMachine={selectedTenant ? undefined : () => {
                  setRemoteAddRequest((request) => request + 1);
                  setTab("machines");
                }}
			/>
		  )}
        </header>

        <MeetingInvitationOverlay />

        <main className="main">
          {fleetWarnings.length > 0 && dismissedFleetWarnings !== fleetWarningKey && (
            <div className="session-notice warning" role="status">
              <strong>{t("remote.partialFleetWarning")}</strong>
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
              {tenantGate.state === "loading" ? (
                <div className="empty-state">{t("tenants.checking")}</div>
              ) : tenantGate.state === "error" ? (
                <section className="surface" role="alert">
                  <div className="surface-header">
                    <div>
                      <h2>{t("tenants.requiredTitle")}</h2>
                      <p>{t("tenants.identityError")}</p>
                    </div>
                    <button className="ghost-action" onClick={() => void tenantGate.refresh()}>
                      <RefreshCw size={15} /> {t("common.retry")}
                    </button>
                  </div>
                </section>
              ) : (
                <>
				  <RegisteredPanel tab={visibleTab} />
				  {visibleTab === "channels" && <ConnectPanel view="channels" />}
				  {visibleTab === "schedules" && <ConnectPanel view="schedules" />}
				  {visibleTab === "triggers" && <ConnectPanel view="triggers" />}
				  {visibleTab === "machines" && <RemoteHostsPanel addRequest={remoteAddRequest} />}
                  {visibleTab === "tenants" && tenantIdentity && (
                    <TenantsPanel
                      identity={tenantIdentity}
                      initialTenants={tenantPanelOptions}
                      onContinue={() => setTab("agents")}
                      onTenantChanged={(change) => {
                        if (change?.type === "delete") {
                          const deletedKey = tenantScopeKey(change.tenant.id, change.tenant.target_id || "local");
                          setTenantOptions((current) => {
                            const next = current.filter((tenant) =>
                              tenantScopeKey(tenant.id, tenant.target_id || "local") !== deletedKey,
                            );
                            cacheTenantOptions(next);
                            return next;
                          });
                          if (selectedTenantScopeID === deletedKey || selectedTenantScopeID === change.tenant.id) {
                            setActiveTenantScopeID("");
                            setSelectedTenantScopeID("");
                          }
                        }
                        setTenantOptionsRevision((revision) => revision + 1);
                        void tenantGate.refresh();
                      }}
                    />
                  )}
                </>
              )}
            </Suspense>
          </PanelErrorBoundary>
        </main>
      </div>
    </div>
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

function useTenancyGate(): {
  state: TenancyGateState;
  identity: TenancySelf | null;
  refresh: () => Promise<void>;
} {
  const [state, setState] = useState<TenancyGateState>("loading");
  const [identity, setIdentity] = useState<TenancySelf | null>(null);

  const refresh = useCallback(async () => {
    setState("loading");
    try {
      const resolved = await resolveTenancyGateWithRetry(
        () => api.tenancySelf(),
      );
      setIdentity(resolved.identity);
      setState(resolved.state);
    } catch {
      // A failed identity request is not a missing tenant. Keep the retry in
      // the shell so a successful check also unlocks navigation.
      setState("error");
      setIdentity(null);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return { state, identity, refresh };
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
      {preferenceError && <small className="preference-error">{preferenceError}</small>}
    </>
  );
}
