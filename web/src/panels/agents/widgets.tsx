import { Download } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export function Summary({ label, value }: { label: string; value: number }) {
  return (
    <div className="summary-stat">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

export function MarkdownPreview({
  className = "",
  content,
  empty,
}: {
  className?: string;
  content: string;
  empty: string;
}) {
  if (!content.trim()) {
    return <div className={`markdown-preview markdown-preview-empty ${className}`.trim()}>{empty}</div>;
  }

  return (
    <div className={`markdown-preview ${className}`.trim()}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml>
        {content}
      </ReactMarkdown>
    </div>
  );
}

export function Picker({
  title,
  items,
  labels,
  selected,
  readOnly,
  onChange,
  unavailableItems = [],
  unavailableTitle,
  onUnavailableClick,
  empty,
}: {
  title: string;
  items: string[];
  labels?: Record<string, string>;
  selected: string[];
  readOnly: boolean;
  onChange: (next: string[]) => void;
  unavailableItems?: string[];
  unavailableTitle?: string;
  onUnavailableClick?: (item: string) => void;
  empty: string;
}) {
  return (
    <div className="mapping-card">
      <strong>{title}</strong>
      {items.length === 0 && <span className="muted">{empty}</span>}
      <div className="provider-chip-row">
        {items.map((item) => {
          const active = selected.includes(item);
          const unavailable = unavailableItems.includes(item);
          return (
            <button
              key={item}
              className={`status-badge ${active && !unavailable ? "success" : ""}${unavailable ? " unavailable" : ""}`}
              disabled={readOnly && !unavailable}
              onClick={() => {
                if (unavailable) {
                  onUnavailableClick?.(item);
                  return;
                }
                onChange(active ? selected.filter((value) => value !== item) : [...selected, item]);
              }}
              title={unavailable ? unavailableTitle : undefined}
              type="button"
            >
              {labels?.[item] ?? item}
              {unavailable && <Download size={12} />}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// composeInjectedPrompt mirrors core.ComposeSystemPrompt so the agent form can
// preview the exact prompt injected at runtime.
