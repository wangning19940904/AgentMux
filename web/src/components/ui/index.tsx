import { RefreshCw } from "lucide-react";
import { createPortal } from "react-dom";
import { useEffect, type ReactNode } from "react";
import { useI18n } from "../../i18n";

/** Surface is the standard card container: header row + body. */
export function Surface({
  title,
  actions,
  children,
  className,
}: {
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={className ? `surface ${className}` : "surface"}>
      <div className="surface-header">
        {typeof title === "string" ? <h2>{title}</h2> : title}
        {actions}
      </div>
      {children}
    </section>
  );
}

/** StatusBadge renders the shared dot + label badge. */
export function StatusBadge({
  tone,
  label,
  withDot = true,
}: {
  tone: "success" | "warning" | "danger" | "neutral";
  label: ReactNode;
  withDot?: boolean;
}) {
  return (
    <span className={`status-badge ${tone}`}>
      {withDot && <span className="status-dot" />}
      {label}
    </span>
  );
}

/** RefreshButton is the shared ghost refresh action used by panel headers. */
export function RefreshButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  const { t } = useI18n();
  return (
    <button className="ghost-action" onClick={onClick} disabled={disabled}>
      <RefreshCw size={15} />
      {t("common.refresh")}
    </button>
  );
}

/** SegmentedTabs renders the shared segmented sub-tab switcher. */
export function SegmentedTabs<T extends string>({
  value,
  onChange,
  options,
}: {
  value: T;
  onChange: (next: T) => void;
  options: { id: T; label: ReactNode }[];
}) {
  return (
    <div className="segmented">
      {options.map((option) => (
        <button
          key={option.id}
          className={value === option.id ? "active" : ""}
          onClick={() => onChange(option.id)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

/** SummaryStat is one label/value block of a panel summary strip. */
export function SummaryStat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="summary-stat">
      <span className="summary-label">{label}</span>
      <span className="summary-value">{value}</span>
    </div>
  );
}

/**
 * Drawer renders a right-side overlay panel in a portal, closing on Escape
 * and locking body scroll while open.
 */
export function Drawer({
  open,
  onClose,
  children,
  className,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  className?: string;
}) {
  useEffect(() => {
    if (!open) return;
    const previousOverflow = document.body.style.overflow;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <div className="provider-drawer-layer">
      <div className="provider-drawer-backdrop" onClick={onClose} />
      <aside className={className ? `provider-drawer ${className}` : "provider-drawer"}>{children}</aside>
    </div>,
    document.body,
  );
}
