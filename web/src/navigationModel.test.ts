import { describe, expect, it } from "vitest";
import {
  navigationGroupForTab,
  primaryGroupDestination,
  searchNavigationItems,
  secondaryNavigationForTab,
} from "./navigationModel";
import { NAVIGATION_SEARCH_ALIASES } from "./navigationSearchAliases";

const groups = [
  { id: "agents", items: [{ id: "agents" }, { id: "gateway" }] },
  { id: "connectivity", items: [{ id: "channels" }, { id: "schedules" }, { id: "triggers" }, { id: "meetings" }] },
  { id: "operations", items: [{ id: "sessions" }, { id: "usage" }] },
  { id: "system", items: [{ id: "machines" }, { id: "tenants" }] },
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

  it("maps tenancy gating to the system rail", () => {
    expect(navigationGroupForTab([...groups], "tenants")?.id).toBe("system");
  });

  it("navigates a newly selected primary group to its first child", () => {
    expect(primaryGroupDestination(groups[3], "meetings")).toBe("machines");
    expect(primaryGroupDestination(groups[3], "tenants")).toBeNull();
  });

  it("places model configuration with agents and records with runtime analytics", () => {
    expect(navigationGroupForTab([...groups], "gateway")?.id).toBe("agents");
    expect(navigationGroupForTab([...groups], "sessions")?.id).toBe("operations");
  });
});

describe("navigation search", () => {
  const items = [
    { id: "overview", label: "Overview", groupLabel: "" },
    { id: "agents", label: "智能体", groupLabel: "智能体", keywords: NAVIGATION_SEARCH_ALIASES.agents },
    { id: "frameworks", label: "Agent 框架", groupLabel: "智能体", keywords: NAVIGATION_SEARCH_ALIASES.frameworks },
    { id: "gateway", label: "LLM Provider 与路由", groupLabel: "智能体", keywords: NAVIGATION_SEARCH_ALIASES.gateway },
  ] as const;

  it("matches labels and route IDs case-insensitively", () => {
    expect(searchNavigationItems(items, "  AGENT  ").map((item) => item.id)).toEqual([
      "agents",
      "frameworks",
    ]);
    expect(searchNavigationItems(items, "providers").map((item) => item.id)).toEqual([
      "gateway",
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
    expect(searchNavigationItems(items, "fu wu shang").map((item) => item.id)).toEqual(["gateway"]);
    expect(searchNavigationItems(items, "fws").map((item) => item.id)).toEqual(["gateway"]);
  });

  it("matches English names, synonyms, and common provider brands in a Chinese UI", () => {
    expect(searchNavigationItems(items, "assistant").map((item) => item.id)).toEqual(["agents"]);
    expect(searchNavigationItems(items, "vendor").map((item) => item.id)).toEqual(["gateway"]);
    expect(searchNavigationItems(items, "openai").map((item) => item.id)).toEqual(["gateway"]);
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
      bp: "orchestrations",
      workflow: "orchestrations",
      zcb: "mcp",
      duihua: "sessions",
      feishu: "channels",
      cron: "schedules",
      webhook: "triggers",
      router: "gateway",
      fws: "gateway",
      openai: "gateway",
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
