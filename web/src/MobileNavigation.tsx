import { useEffect, useRef } from "react";
import type { ReactNode } from "react";
import { Menu, X } from "lucide-react";
import type { LayoutGrid } from "lucide-react";
import { useI18n } from "./i18n";

// Phone navigation: a bottom tab bar with the pages people check from a phone,
// and a "More" drawer that holds the full grouped navigation and preferences.
// Both render on every screen; CSS shows them only at phone widths, so the
// desktop sidebar stays exactly as it was.

type Icon = typeof LayoutGrid;

export type MobileNavItem<T extends string> = { id: T; labelKey: string; icon: Icon };
export type MobileNavGroup<T extends string> = { id: string; labelKey: string; icon: Icon; items: MobileNavItem<T>[] };

// The tab bar shows these destinations, in this order, when they exist.
export const MOBILE_TAB_IDS = ["overview", "agents", "super-agents", "machines"] as const;

// A badge is a small count on a destination, such as unread messages.
export type MobileNavBadges = Partial<Record<string, number>>;

function NavBadge({ count, className }: { count: number | undefined; className: string }) {
  const { t } = useI18n();
  if (!count || count <= 0) return null;
  return <span className={className} aria-label={t("messages.unreadCount", { count })}>{count > 99 ? "99+" : count}</span>;
}

export function mobileTabItems<T extends string>(items: MobileNavItem<T>[]): MobileNavItem<T>[] {
  return MOBILE_TAB_IDS
    .map((id) => items.find((item) => item.id === id))
    .filter((item): item is MobileNavItem<T> => Boolean(item));
}

export function MobileTabBar<T extends string>({
  items,
  active,
  badges,
  drawerOpen,
  isDisabled,
  onSelect,
  onOpenMore,
}: {
  items: MobileNavItem<T>[];
  active: T;
  badges?: MobileNavBadges;
  drawerOpen: boolean;
  isDisabled: (id: T) => boolean;
  onSelect: (id: T) => void;
  onOpenMore: () => void;
}) {
  const { t } = useI18n();
  const moreActive = drawerOpen || !items.some((item) => item.id === active);
  // A count on a page that is not in the tab bar shows as a dot on More.
  const hiddenUnread = Object.entries(badges ?? {}).some(([id, count]) => (count ?? 0) > 0 && !items.some((item) => item.id === id));
  return (
    <nav className="mobile-tab-bar" aria-label={t("nav.primary")}>
      {items.map((item) => {
        const Icon = item.icon;
        const current = item.id === active;
        return (
          <button
            key={item.id}
            type="button"
            className={`mobile-tab${current && !drawerOpen ? " active" : ""}`}
            aria-current={current ? "page" : undefined}
            disabled={isDisabled(item.id)}
            onClick={() => onSelect(item.id)}
          >
            <Icon size={20} aria-hidden="true" />
            <span>{t(`nav.mobile.${item.id}`)}</span>
            <NavBadge count={badges?.[item.id]} className="nav-badge mobile-tab-badge" />
          </button>
        );
      })}
      <button
        type="button"
        className={`mobile-tab${moreActive ? " active" : ""}`}
        aria-haspopup="dialog"
        aria-expanded={drawerOpen}
        aria-controls="mobile-navigation-drawer"
        onClick={onOpenMore}
      >
        <Menu size={20} aria-hidden="true" />
        <span>{t("nav.mobile.more")}</span>
        {hiddenUnread && <i className="nav-badge-dot" aria-hidden="true" />}
      </button>
    </nav>
  );
}

export function MobileDrawer<T extends string>({
  open,
  overview,
  groups,
  active,
  badges,
  isDisabled,
  onSelect,
  onClose,
  children,
}: {
  open: boolean;
  // Omitted where the overview is not available, such as for a member.
  overview?: MobileNavItem<T>;
  groups: MobileNavGroup<T>[];
  active: T;
  badges?: MobileNavBadges;
  isDisabled: (id: T) => boolean;
  onSelect: (id: T) => void;
  onClose: () => void;
  children?: ReactNode;
}) {
  const { t } = useI18n();
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const previousFocus = document.activeElement as HTMLElement | null;
    const panel = panelRef.current;
    const first = panel?.querySelector<HTMLElement>(".mobile-drawer-body [aria-current='page']") ?? panel?.querySelector<HTMLElement>("button");
    first?.focus({ preventScroll: true });
    document.documentElement.classList.add("mobile-drawer-open");
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("keydown", closeOnEscape);
      document.documentElement.classList.remove("mobile-drawer-open");
      previousFocus?.focus?.({ preventScroll: true });
    };
  }, [open, onClose]);

  if (!open) return null;

  const renderItem = (item: MobileNavItem<T>) => {
    const Icon = item.icon;
    const current = item.id === active;
    return (
      <button
        key={item.id}
        type="button"
        className={`mobile-drawer-item${current ? " active" : ""}`}
        aria-current={current ? "page" : undefined}
        disabled={isDisabled(item.id)}
        onClick={() => onSelect(item.id)}
      >
        <Icon size={17} aria-hidden="true" />
        <span>{t(item.labelKey)}</span>
        <NavBadge count={badges?.[item.id]} className="nav-badge" />
      </button>
    );
  };

  return (
    <div className="mobile-drawer-layer">
      <div className="mobile-drawer-backdrop" onClick={onClose} aria-hidden="true" />
      <div
        ref={panelRef}
        id="mobile-navigation-drawer"
        className="mobile-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={t("nav.mobile.menu")}
      >
        <header className="mobile-drawer-header">
          <img className="brand-logo" src="/icons/icon-192.png" alt="" aria-hidden="true" />
          <strong>AgentMux</strong>
          <button type="button" className="mobile-drawer-close" aria-label={t("common.close")} onClick={onClose}>
            <X size={18} aria-hidden="true" />
          </button>
        </header>
        <div className="mobile-drawer-body">
          <nav aria-label={t("nav.primary")}>
            {overview && <div className="mobile-drawer-group">{renderItem(overview)}</div>}
            {groups.map((group) => (
              <section key={group.id} className="mobile-drawer-group" aria-label={t(group.labelKey)}>
                <h2>{t(group.labelKey)}</h2>
                {group.items.map(renderItem)}
              </section>
            ))}
          </nav>
          {children && <section className="mobile-drawer-preferences">{children}</section>}
        </div>
      </div>
    </div>
  );
}
