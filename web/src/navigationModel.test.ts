import { describe, expect, it } from "vitest";
import {
  MEMBER_LANDING_TAB,
  navigationForRole,
  navigationGroupForTab,
  primaryGroupDestination,
  searchNavigationItems,
  secondaryNavigationForTab,
  tabAllowedForRole,
} from "./navigationModel";
import { NAVIGATION_SEARCH_ALIASES } from "./navigationSearchAliases";

const groups = [
  { id: "agents", items: [{ id: "agents" }, { id: "frameworks" }] },
  { id: "connectivity", items: [{ id: "channels" }, { id: "schedules" }, { id: "triggers" }, { id: "meetings" }] },
  { id: "operations", items: [{ id: "sessions" }, { id: "usage" }] },
  { id: "system", items: [{ id: "machines" }, { id: "accounts" }] },
] as const;

describe("horizontal navigation model", () => {
  it("opens the owning secondary rail for a deep-linked child", () => {
    expect(secondaryNavigationForTab([...groups], "meetings", "overview")).toEqual({
      groupID: "connectivity",
      open: true,
    });
  });

  it("closes the secondary rail on overview", () => {
    expect(secondaryNavigationForTab([...groups], "overview", "overview")).toEqual({
      groupID: null,
      open: false,
    });
  });

  it("places account management in the system rail", () => {
    expect(navigationGroupForTab([...groups], "accounts")?.id).toBe("system");
  });

  it("navigates a newly selected primary group to its first child", () => {
    expect(primaryGroupDestination(groups[3], "meetings")).toBe("machines");
    expect(primaryGroupDestination(groups[3], "accounts")).toBeNull();
  });

  it("places model configuration with agents and records with runtime analytics", () => {
    expect(navigationGroupForTab([...groups], "frameworks")?.id).toBe("agents");
    expect(navigationGroupForTab([...groups], "sessions")?.id).toBe("operations");
  });
});

describe("navigation by role", () => {
  // Mirrors the Console's full navigation.
  const consoleGroups = [
    { id: "agents", items: ["agents", "frameworks", "skills", "mcp", "memory"].map((id) => ({ id })) },
    { id: "connectivity", items: ["channels", "schedules", "triggers", "subscriptions", "meetings"].map((id) => ({ id })) },
    { id: "operations", items: ["sessions", "machine-status", "observability", "usage", "feedback", "guard"].map((id) => ({ id })) },
    { id: "system", items: ["machines", "accounts", "settings"].map((id) => ({ id })) },
  ];

  it("shows administrators everything", () => {
    expect(navigationForRole(consoleGroups, "admin")).toEqual(consoleGroups);
    expect(tabAllowedForRole("overview", "admin")).toBe(true);
  });

  it("shows a member only their own account", () => {
    const member = navigationForRole(consoleGroups, "member");
    expect(member.map((group) => group.id)).toEqual(["system"]);
    expect(member.flatMap((group) => group.items.map((item) => item.id))).toEqual(["accounts"]);
    expect(MEMBER_LANDING_TAB).toBe("accounts");
    for (const tab of ["overview", "agents", "machines", "settings", "channels", "usage"]) {
      expect(tabAllowedForRole(tab, "member"), tab).toBe(false);
    }
  });
});

describe("navigation search", () => {
  const items = [
    { id: "overview", label: "Overview", groupLabel: "" },
    { id: "agents", label: "智能体", groupLabel: "智能体", keywords: NAVIGATION_SEARCH_ALIASES.agents },
    { id: "frameworks", label: "Agent 框架", groupLabel: "智能体", keywords: NAVIGATION_SEARCH_ALIASES.frameworks },
  ] as const;

  it("matches labels and route IDs case-insensitively", () => {
    expect(searchNavigationItems(items, "  AGENT  ").map((item) => item.id)).toEqual([
      "agents",
      "frameworks",
    ]);
    expect(searchNavigationItems(items, "frameworks").map((item) => item.id)).toEqual([
      "frameworks",
    ]);
  });

  it("matches multiple words across the searchable metadata", () => {
    expect(searchNavigationItems(items, "agent frame").map((item) => item.id)).toEqual([
      "frameworks",
    ]);
  });

  it("matches full pinyin with or without spaces and pinyin initials", () => {
    expect(searchNavigationItems(items, "zhi neng ti").map((item) => item.id)).toEqual(["agents"]);
    expect(searchNavigationItems(items, "znt").map((item) => item.id)).toEqual(["agents"]);
    expect(searchNavigationItems(items, "ke yong xing").map((item) => item.id)).toEqual(["frameworks"]);
    expect(searchNavigationItems(items, "kyx").map((item) => item.id)).toEqual(["frameworks"]);
  });

  it("matches English names and synonyms in a Chinese UI", () => {
    expect(searchNavigationItems(items, "assistant").map((item) => item.id)).toEqual(["agents"]);
    expect(searchNavigationItems(items, "availability").map((item) => item.id)).toEqual(["frameworks"]);
    expect(searchNavigationItems(items, "magpie").map((item) => item.id)).toEqual(["frameworks"]);
  });

  it("ranks representative intents correctly across the full navigation vocabulary", () => {
    const allItems = Object.entries(NAVIGATION_SEARCH_ALIASES).map(([id, keywords]) => ({
      id,
      label: id,
      keywords,
    }));
    const expectations: Record<string, string> = {
      znt: "agents",
      "zhi neng ti": "agents",
      assistant: "agents",
      zcb: "mcp",
      duihua: "sessions",
      feishu: "channels",
      cron: "schedules",
      webhook: "triggers",
      router: "frameworks",
      magpie: "frameworks",
      moxing: "frameworks",
      otel: "observability",
      token: "usage",
      pj: "feedback",
      ssh: "machines",
      sz: "settings",
    };

    for (const [query, expectedID] of Object.entries(expectations)) {
      expect(searchNavigationItems(allItems, query)[0]?.id, query).toBe(expectedID);
    }
  });

  it("returns no suggestions for a blank or unmatched query", () => {
    expect(searchNavigationItems(items, "   ")).toEqual([]);
    expect(searchNavigationItems(items, "missing")).toEqual([]);
  });
});
