import { ChevronDown, type LucideIcon } from "lucide-react";
import { DisclosurePanel } from "./RowDisclosure";
import { SlidingSelection } from "./SlidingSelection";

export function SidebarGroup<T extends string>({
  id, label, icon: Icon, items, current, expanded, disabled, onToggle, onSelect,
}: {
  id: string;
  label: string;
  icon: LucideIcon;
  items: readonly { id: T; label: string; icon: LucideIcon }[];
  current: T | "";
  expanded: boolean;
  disabled: boolean;
  onToggle: () => void;
  onSelect: (id: T) => void;
}) {
  const panelId = `sidebar-group-${id}`;
  return (
    <div className="sidebar-nav-group">
      <button
        type="button"
        className={`nav-item nav-primary-item sidebar-group-button${current && !expanded ? " active" : ""}`}
        aria-expanded={expanded}
        aria-controls={panelId}
        aria-label={label}
        title={label}
        disabled={disabled}
        onClick={onToggle}
      >
        <Icon size={17} aria-hidden="true" />
        <span>{label}</span>
        <ChevronDown className="sidebar-chevron" size={14} aria-hidden="true" />
      </button>
      <DisclosurePanel id={panelId} expanded={expanded}>
        <SlidingSelection className="sidebar-subnav-list" selected={current} role="group" aria-label={label}>
          {items.map(({ id: itemId, label: itemLabel, icon: ItemIcon }) => (
            <button
              key={itemId}
              type="button"
              className={`sidebar-child${current === itemId ? " active" : ""}`}
              aria-current={current === itemId ? "page" : undefined}
              title={itemLabel}
              disabled={disabled}
              onClick={() => onSelect(itemId)}
            >
              <ItemIcon size={16} aria-hidden="true" />
              <span>{itemLabel}</span>
            </button>
          ))}
        </SlidingSelection>
      </DisclosurePanel>
    </div>
  );
}
