import { useLayoutEffect, useRef, type HTMLAttributes, type KeyboardEvent } from "react";

type SlidingSelectionProps = HTMLAttributes<HTMLDivElement> & { selected: string; previewOnHover?: boolean };

export function SlidingSelection({ selected, previewOnHover = false, className, children, ...props }: SlidingSelectionProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const markerRef = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    const root = rootRef.current;
    const marker = markerRef.current;
    if (!root || !marker) return;
    const active = root.querySelector<HTMLElement>(":scope > .active");
    if (!active) {
      marker.hidden = true;
      return;
    }
    function positionMarker(option = active) {
      if (!root || !marker || !option) return;
      const bounds = root.getBoundingClientRect();
      const rect = option.getBoundingClientRect();
      marker.hidden = rect.width === 0 || rect.height === 0;
      marker.style.transform = `translate(${rect.left - bounds.left + root.scrollLeft - root.clientLeft}px, ${rect.top - bounds.top + root.scrollTop - root.clientTop}px)`;
      marker.style.width = `${rect.width}px`;
      marker.style.height = `${rect.height}px`;
    }
    function previewSelection(event: PointerEvent) {
      if ((event.type === "pointerdown" && event.button !== 0) || !(event.target instanceof Element)) return;
      const option = event.target.closest<HTMLButtonElement>("button");
      if (option?.parentElement === root && !option.disabled) positionMarker(option);
    }
    const restoreSelection = () => positionMarker();
    function previewHover(event: PointerEvent) {
      if (event.pointerType === "mouse") previewSelection(event);
    }
    positionMarker();
    root.addEventListener("pointerdown", previewSelection);
    root.addEventListener("pointerleave", restoreSelection);
    root.addEventListener("pointercancel", restoreSelection);
    if (previewOnHover) root.addEventListener("pointerover", previewHover);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(restoreSelection);
    observer?.observe(root);
    for (const option of root.querySelectorAll(":scope > button")) observer?.observe(option);
    return () => {
      observer?.disconnect();
      root.removeEventListener("pointerdown", previewSelection);
      root.removeEventListener("pointerleave", restoreSelection);
      root.removeEventListener("pointercancel", restoreSelection);
      root.removeEventListener("pointerover", previewHover);
    };
  }, [selected, previewOnHover]);

  function handleArrowKeys(event: KeyboardEvent<HTMLDivElement>) {
    if (props.role !== "tablist" && props.role !== "radiogroup") return;
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const options = [...event.currentTarget.querySelectorAll<HTMLButtonElement>(":scope > button:not(:disabled)")];
    const current = options.indexOf(event.target as HTMLButtonElement);
    if (current < 0) return;
    let index: number;
    switch (event.key) {
      case "ArrowRight": index = (current + 1) % options.length; break;
      case "ArrowLeft": index = (current - 1 + options.length) % options.length; break;
      case "Home": index = 0; break;
      case "End": index = options.length - 1; break;
      default: return;
    }
    event.preventDefault();
    options[index].focus();
    options[index].click();
  }

  return (
    <div
      {...props}
      ref={rootRef}
      className={`sliding-selection ${className ?? ""}`}
      onKeyDown={handleArrowKeys}
    >
      <span ref={markerRef} className="sliding-selection-indicator" aria-hidden="true" hidden />
      {children}
    </div>
  );
}
