import { ChevronDown } from "lucide-react";
import { useCallback, useId, useState, type ReactNode } from "react";
import { useI18n } from "../i18n";

export function DisclosurePanel({ id, expanded, children }: {
  id: string;
  expanded: boolean;
  children: ReactNode;
}) {
  const setPanel = useCallback((panel: HTMLDivElement | null) => {
    if (panel) panel.inert = !expanded;
  }, [expanded]);
  return (
    <div className="disclosure-body" id={id} ref={setPanel} aria-hidden={!expanded}>
      <div className="disclosure-clip">{children}</div>
    </div>
  );
}

export function RowDisclosure({ className, name, summary, preview, children, onDoubleClick }: {
  className: string;
  name: string;
  summary: ReactNode;
  preview?: ReactNode;
  children: ReactNode;
  onDoubleClick?: () => void;
}) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const panelId = useId();
  return (
    <article className={`disclosure-row ${className}`} onDoubleClick={onDoubleClick}>
      <div className="disclosure-summary">
        {summary}
        <button
          className="ghost-action disclosure-toggle"
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          aria-label={t(expanded ? "common.collapseDetailsFor" : "common.detailsFor", { name })}
          onClick={() => setExpanded((open) => !open)}
          onDoubleClick={(event) => event.stopPropagation()}
        >
          {t(expanded ? "common.collapseDetails" : "common.details")}
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      </div>
      {preview && <div className="disclosure-preview">{preview}</div>}
      <DisclosurePanel id={panelId} expanded={expanded}>
        <div className="disclosure-content">{children}</div>
      </DisclosurePanel>
    </article>
  );
}
