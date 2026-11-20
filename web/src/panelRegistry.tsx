import { lazy } from "react";
import type { ComponentType, LazyExoticComponent } from "react";
import type { NavigationTabID } from "./navigationSearchAliases";
import type { AgentsPanelProps } from "./panels/agents/AgentsPanel";
import { prefetchFrameworks } from "./panels/frameworks/frameworkPageCache";

const AgentsPanel = lazy(() => import("./panels/AgentsPanel").then((module) => ({ default: module.AgentsPanel })));

const STATIC_PANELS: Partial<Record<NavigationTabID, LazyExoticComponent<ComponentType>>> = {
  work: lazy(() => import("./panels/WorkPanel").then((module) => ({ default: module.WorkPanel }))),
  frameworks: lazy(() => loadFrameworksPanel().then((module) => ({ default: module.FrameworksPanel }))),
  observability: lazy(() => import("./panels/ObservabilityPanel").then((module) => ({ default: module.ObservabilityPanel }))),
  usage: lazy(() => loadUsagePanel().then((module) => ({ default: module.UsagePanel }))),
  health: lazy(() => loadHealthPanel().then((module) => ({ default: module.HealthPanel }))),
  settings: lazy(() => import("./panels/MenuBarPanel").then((module) => ({ default: module.MenuBarPanel }))),
  "machine-status": lazy(() => import("./panels/MachineStatusPanel").then((module) => ({ default: module.MachineStatusPanel }))),
  sessions: lazy(() => import("./panels/SessionsPanel").then((module) => ({ default: module.SessionsPanel }))),
  meetings: lazy(() => import("./panels/MeetingsPanel").then((module) => ({ default: module.MeetingsPanel }))),
  memory: lazy(() => import("./panels/MemoryPanel").then((module) => ({ default: module.MemoryPanel }))),
  skills: lazy(() => import("./panels/SkillsPanel").then((module) => ({ default: module.SkillsPanel }))),
  mcp: lazy(() => import("./panels/MCPPanel").then((module) => ({ default: module.MCPPanel }))),
  feedback: lazy(() => import("./panels/FeedbackPanel").then((module) => ({ default: module.FeedbackPanel }))),
  guard: lazy(() => import("./panels/GuardPanel").then((module) => ({ default: module.GuardPanel }))),
  accounts: lazy(() => import("./panels/AccountsPanel").then((module) => ({ default: module.AccountsPanel }))),
};

function loadFrameworksPanel() {
  return import("./panels/FrameworksPanel");
}

function loadUsagePanel() {
  return import("./panels/UsagePanel");
}

function loadHealthPanel() {
  return import("./panels/HealthPanel");
}

// Pages whose data takes a while to gather (every machine is asked) start
// loading their code and data as soon as their navigation entry is pointed at.
const PREFETCHERS: Partial<Record<NavigationTabID, () => void>> = {
  frameworks: () => {
    void loadFrameworksPanel();
    prefetchFrameworks();
  },
  usage: () => {
    void loadUsagePanel().then((module) => module.prefetchDefaultUsage());
  },
  health: () => {
    void loadHealthPanel().then((module) => module.prefetchHealth());
  },
};

export function prefetchPanel(tab: NavigationTabID) {
  try {
    PREFETCHERS[tab]?.();
  } catch {
    // Prefetching is best effort; the page loads its data when it opens.
  }
}

export function RegisteredPanel({ tab, ...agentProps }: {
  tab: NavigationTabID;
} & AgentsPanelProps) {
  // There is one kind of agent: the former Super Agents page is the Agents
  // page, where each agent's workers and triggers open from its row.
  if (tab === "agents" || tab === "super-agents") return <AgentsPanel {...agentProps} />;
  const Panel = STATIC_PANELS[tab];
  return Panel ? <Panel /> : null;
}
