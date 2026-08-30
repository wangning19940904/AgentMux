import { lazy } from "react";
import type { ComponentType, LazyExoticComponent } from "react";
import type { NavigationTabID } from "./navigationSearchAliases";

const STATIC_PANELS: Partial<Record<NavigationTabID, LazyExoticComponent<ComponentType>>> = {
  overview: lazy(() => import("./panels/OverviewPanel").then((module) => ({ default: module.OverviewPanel }))),
  agents: lazy(() => import("./panels/AgentsPanel").then((module) => ({ default: module.AgentsPanel }))),
  orchestrations: lazy(() => import("./panels/OrchestrationsPanel").then((module) => ({ default: module.OrchestrationsPanel }))),
  frameworks: lazy(() => import("./panels/FrameworksPanel").then((module) => ({ default: module.FrameworksPanel }))),
  gateway: lazy(() => import("./panels/GatewayPanel").then((module) => ({ default: module.GatewayPanel }))),
  observability: lazy(() => import("./panels/ObservabilityPanel").then((module) => ({ default: module.ObservabilityPanel }))),
  usage: lazy(() => import("./panels/UsagePanel").then((module) => ({ default: module.UsagePanel }))),
  settings: lazy(() => import("./panels/MenuBarPanel").then((module) => ({ default: module.MenuBarPanel }))),
  sessions: lazy(() => import("./panels/SessionsPanel").then((module) => ({ default: module.SessionsPanel }))),
  meetings: lazy(() => import("./panels/MeetingsPanel").then((module) => ({ default: module.MeetingsPanel }))),
  memory: lazy(() => import("./panels/MemoryPanel").then((module) => ({ default: module.MemoryPanel }))),
  skills: lazy(() => import("./panels/SkillsPanel").then((module) => ({ default: module.SkillsPanel }))),
  mcp: lazy(() => import("./panels/MCPPanel").then((module) => ({ default: module.MCPPanel }))),
  feedback: lazy(() => import("./panels/FeedbackPanel").then((module) => ({ default: module.FeedbackPanel }))),
  guard: lazy(() => import("./panels/GuardPanel").then((module) => ({ default: module.GuardPanel }))),
};

export function RegisteredPanel({ tab }: { tab: NavigationTabID }) {
  const Panel = STATIC_PANELS[tab];
  return Panel ? <Panel /> : null;
}
