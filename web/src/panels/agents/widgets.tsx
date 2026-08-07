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
  empty,
}: {
  title: string;
  items: string[];
  labels?: Record<string, string>;
  selected: string[];
  readOnly: boolean;
  onChange: (next: string[]) => void;
  empty: string;
}) {
  return (
    <div className="mapping-card">
      <strong>{title}</strong>
      {items.length === 0 && <span className="muted">{empty}</span>}
      <div className="provider-chip-row">
        {items.map((item) => {
          const active = selected.includes(item);
          return (
            <button
              key={item}
              className={`status-badge ${active ? "success" : ""}`}
              disabled={readOnly}
              onClick={() => onChange(active ? selected.filter((value) => value !== item) : [...selected, item])}
            >
              {labels?.[item] ?? item}
            </button>
          );
        })}
      </div>
    </div>
  );
}

// composeInjectedPrompt mirrors core.ComposeSystemPrompt so the agent form can
// preview the exact prompt injected at runtime.
