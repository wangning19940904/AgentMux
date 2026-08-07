import { useEffect } from "react";

/**
 * usePolling invokes reload on a fixed interval while enabled, pausing when
 * the document is hidden and firing immediately when the page becomes
 * visible or regains focus. It unifies the per-panel setInterval variants.
 */
export function usePolling(reload: () => void, intervalMs: number, options?: { enabled?: boolean }) {
  const enabled = options?.enabled ?? true;
  useEffect(() => {
    if (!enabled || intervalMs <= 0) return;
    const tick = () => {
      if (!document.hidden) reload();
    };
    const timer = window.setInterval(tick, intervalMs);
    const onVisible = () => {
      if (!document.hidden) reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("focus", onVisible);
    window.addEventListener("pageshow", onVisible);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("focus", onVisible);
      window.removeEventListener("pageshow", onVisible);
    };
  }, [reload, intervalMs, enabled]);
}
