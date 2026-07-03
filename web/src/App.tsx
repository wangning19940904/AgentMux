import { useState } from "react";
import { ProvidersPanel } from "./panels/ProvidersPanel";
import { UsagePanel } from "./panels/UsagePanel";
import { GatewayPanel } from "./panels/GatewayPanel";
import { OverviewPanel } from "./panels/OverviewPanel";
import { MemoryPanel } from "./panels/MemoryPanel";
import { SkillsPanel } from "./panels/SkillsPanel";
import { MCPPanel } from "./panels/MCPPanel";
import { GuardPanel } from "./panels/GuardPanel";

type Tab =
  | "overview"
  | "usage"
  | "providers"
  | "gateway"
  | "memory"
  | "skills"
  | "mcp"
  | "guard";

const TABS: { id: Tab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "gateway", label: "Connect / Router" },
  { id: "usage", label: "Ledger" },
  { id: "memory", label: "Memory" },
  { id: "skills", label: "Skills" },
  { id: "mcp", label: "MCP Registry" },
  { id: "guard", label: "Guard" },
  { id: "providers", label: "Providers" },
];

export function App() {
  const [tab, setTab] = useState<Tab>("overview");
  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <span className="dot" />
          AgentNexus
        </div>
        <nav className="nav">
          {TABS.map((t) => (
            <button
              key={t.id}
              className={tab === t.id ? "active" : ""}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </nav>
      </aside>
      <main className="main">
        {tab === "overview" && <OverviewPanel />}
        {tab === "usage" && <UsagePanel />}
        {tab === "providers" && <ProvidersPanel />}
        {tab === "gateway" && <GatewayPanel />}
        {tab === "memory" && <MemoryPanel />}
        {tab === "skills" && <SkillsPanel />}
        {tab === "mcp" && <MCPPanel />}
        {tab === "guard" && <GuardPanel />}
      </main>
    </div>
  );
}
