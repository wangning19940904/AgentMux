// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { Bot, LayoutGrid, Network, ServerCog, Settings } from "lucide-react";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "./i18n";
import { MobileDrawer, MobileTabBar, mobileTabItems, type MobileNavItem } from "./MobileNavigation";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type ID = "overview" | "agents" | "super-agents" | "machines" | "settings";

const ITEMS: MobileNavItem<ID>[] = [
  { id: "overview", labelKey: "nav.overview", icon: LayoutGrid },
  { id: "settings", labelKey: "nav.settings", icon: Settings },
  { id: "machines", labelKey: "nav.machines", icon: ServerCog },
  { id: "agents", labelKey: "nav.agents", icon: Bot },
  { id: "super-agents", labelKey: "nav.superAgents", icon: Network },
];

let container: HTMLDivElement | null = null;
afterEach(() => {
  container?.remove();
  container = null;
});

function render(node: React.ReactNode) {
  container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  act(() => root.render(<I18nProvider language="en">{node}</I18nProvider>));
  return { root, container };
}

it("puts the phone destinations in a fixed order", () => {
  expect(mobileTabItems(ITEMS).map((item) => item.id)).toEqual(["overview", "agents", "super-agents", "machines"]);
});

it("marks More as current for pages outside the tab bar", () => {
  const onSelect = vi.fn();
  const onOpenMore = vi.fn();
  const { container } = render(
    <MobileTabBar items={mobileTabItems(ITEMS)} active="settings" drawerOpen={false} isDisabled={() => false} onSelect={onSelect} onOpenMore={onOpenMore} />,
  );
  const buttons = [...container.querySelectorAll("button")];
  expect(buttons.map((button) => button.textContent)).toEqual(["Overview", "Agents", "Super", "Machines", "More"]);
  expect(buttons[4].className).toContain("active");
  act(() => buttons[1].click());
  expect(onSelect).toHaveBeenCalledWith("agents");
  act(() => buttons[4].click());
  expect(onOpenMore).toHaveBeenCalled();
});

it("lists every group in the drawer and closes on Escape", () => {
  const onSelect = vi.fn();
  const onClose = vi.fn();
  const { container } = render(
    <MobileDrawer
      open
      overview={ITEMS[0]}
      groups={[{ id: "system", labelKey: "nav.group.system", icon: Settings, items: [ITEMS[2], ITEMS[1]] }]}
      active="machines"
      isDisabled={(id) => id === "settings"}
      onSelect={onSelect}
      onClose={onClose}
    >
      <span>preferences</span>
    </MobileDrawer>,
  );
  expect(container.querySelector('[role="dialog"]')?.getAttribute("aria-modal")).toBe("true");
  expect(container.querySelector("h2")?.textContent).toBe("System");
  const current = container.querySelector('[aria-current="page"]') as HTMLButtonElement;
  expect(current.textContent).toBe("Machines");
  expect(document.activeElement).toBe(current);
  const settings = [...container.querySelectorAll("button")].find((button) => button.textContent === "Settings")!;
  expect(settings.disabled).toBe(true);
  expect(container.textContent).toContain("preferences");
  act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
  expect(onClose).toHaveBeenCalled();
});
