import { useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode, RefObject } from "react";
import { createPortal } from "react-dom";

type Props = {
  anchorRef: RefObject<HTMLElement>;
  layoutKey: string;
  onClose: () => void;
  id: string;
  "aria-label": string;
  children: ReactNode;
};

// Render outside the overflow-hidden sidebar while preserving its search anchor.
export function SidebarSearchPopover({ anchorRef, layoutKey, onClose, children, ...attributes }: Props) {
  const panelRef = useRef<HTMLDivElement>(null);
  const [style, setStyle] = useState<CSSProperties>({ visibility: "hidden" });

  useLayoutEffect(() => {
    const position = () => {
      const anchor = anchorRef.current;
      if (!anchor) return;
      const rect = anchor.getBoundingClientRect();
      if (!rect.width || !rect.height) {
        onClose();
        return;
      }
      const padding = 12;
      const gap = 5;
      const width = Math.min(Math.max(260, rect.width), Math.max(0, window.innerWidth - 2 * padding));
      const left = Math.max(padding, Math.min(rect.left, window.innerWidth - width - padding));
      const below = Math.max(0, window.innerHeight - rect.bottom - gap - padding);
      const above = Math.max(0, rect.top - gap - padding);
      const openAbove = below < 180 && above > below;
      setStyle({
        position: "fixed",
        left,
        width,
        top: openAbove ? "auto" : rect.bottom + gap,
        bottom: openAbove ? window.innerHeight - rect.top + gap : "auto",
        maxHeight: Math.min(360, openAbove ? above : below),
      });
    };
    const closeOutside = (event: Event) => {
      const target = event.target as Node | null;
      if (anchorRef.current?.contains(target) || panelRef.current?.contains(target)) return;
      onClose();
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    position();
    window.addEventListener("resize", position);
    window.addEventListener("scroll", position, true);
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("focusin", closeOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      window.removeEventListener("resize", position);
      window.removeEventListener("scroll", position, true);
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("focusin", closeOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [anchorRef, layoutKey, onClose]);

  return createPortal(
    <div {...attributes} ref={panelRef} className="sidebar-search-results" role="listbox" style={style}>
      {children}
    </div>,
    document.body,
  );
}
