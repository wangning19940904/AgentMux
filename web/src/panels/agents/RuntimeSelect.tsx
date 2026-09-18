import { Check, ChevronDown } from "lucide-react";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { runtimeLabel } from "./agentUtils";
import { RuntimeBadge } from "./RuntimeBadge";

export function RuntimeSelect({ value, options, disabled, label, emptyLabel, unavailableLabel, onChange }: {
  value: string;
  options: string[];
  disabled: boolean;
  label: string;
  emptyLabel: string;
  unavailableLabel: string;
  onChange: (runtime: string) => void;
}) {
  const id = useId();
  const container = useRef<HTMLDivElement>(null);
  const typeahead = useRef({ text: "", time: 0 });
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const available = [...new Set(options)];
  const unavailable = Boolean(value) && !available.includes(value);
  const blocked = disabled || available.length === 0;
  const expanded = open && !blocked;
  const highlightedIndex = Math.max(0, Math.min(activeIndex, available.length - 1));

  useEffect(() => {
    if (blocked) setOpen(false);
  }, [blocked]);

  useEffect(() => {
    if (!expanded) return;
    const closeOutside = (event: PointerEvent) => {
      if (!container.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", closeOutside);
    return () => document.removeEventListener("pointerdown", closeOutside);
  }, [expanded]);

  useEffect(() => {
    if (expanded) document.getElementById(`${id}-option-${highlightedIndex}`)?.scrollIntoView?.({ block: "nearest" });
  }, [expanded, highlightedIndex, id]);

  function showOptions() {
    setActiveIndex(Math.max(0, available.indexOf(value)));
    setOpen(true);
  }

  function choose(runtime: string) {
    if (blocked || !available.includes(runtime)) return;
    onChange(runtime);
    setOpen(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    if (blocked) return;
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        if (!expanded) showOptions();
        else setActiveIndex((highlightedIndex + (event.key === "ArrowDown" ? 1 : -1) + available.length) % available.length);
        break;
      case "Home":
      case "End":
        event.preventDefault();
        setOpen(true);
        setActiveIndex(event.key === "Home" ? 0 : available.length - 1);
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (expanded) choose(available[highlightedIndex]);
        else showOptions();
        break;
      case "Escape":
        if (expanded) {
          event.preventDefault();
          event.stopPropagation();
          setOpen(false);
        }
        break;
      case "Tab":
        setOpen(false);
        break;
      default:
        if (event.key.length !== 1 || event.ctrlKey || event.altKey || event.metaKey) return;
        event.preventDefault();
        const now = Date.now();
        const query = (now - typeahead.current.time < 700 ? typeahead.current.text : "") + event.key.toLocaleLowerCase();
        typeahead.current = { text: query, time: now };
        const index = available.findIndex((runtime) => runtimeLabel(runtime).toLocaleLowerCase().startsWith(query));
        if (index >= 0) {
          setActiveIndex(index);
          setOpen(true);
        }
    }
  }

  return (
    <div className="field runtime-field">
      <label id={`${id}-label`} htmlFor={`${id}-control`}>{label}</label>
      <div ref={container} className="runtime-select" onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
      }}>
        <button id={`${id}-control`} type="button" className="runtime-select-trigger" role="combobox"
          aria-labelledby={`${id}-label`} aria-haspopup="listbox" aria-expanded={expanded}
          aria-controls={expanded ? `${id}-list` : undefined}
          aria-activedescendant={expanded ? `${id}-option-${highlightedIndex}` : undefined}
          disabled={blocked} onClick={() => expanded ? setOpen(false) : showOptions()} onKeyDown={onKeyDown}>
          <RuntimeBadge runtime={value} emptyLabel={emptyLabel} />
          {unavailable && <span className="runtime-unavailable">({unavailableLabel})</span>}
          <ChevronDown size={14} aria-hidden="true" />
        </button>
        {expanded && (
          <div id={`${id}-list`} className="runtime-select-options" role="listbox" aria-labelledby={`${id}-label`}>
            {unavailable && <div className="runtime-select-option unavailable" role="option" aria-disabled="true" aria-selected="true">
              <RuntimeBadge runtime={value} /><span className="runtime-unavailable">({unavailableLabel})</span>
            </div>}
            {available.map((runtime, index) => (
              <div id={`${id}-option-${index}`} key={runtime} role="option" aria-selected={runtime === value}
                className={`runtime-select-option${index === highlightedIndex ? " highlighted" : ""}`}
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setActiveIndex(index)} onClick={() => choose(runtime)}>
                <RuntimeBadge runtime={runtime} />
                {runtime === value && <Check size={14} aria-hidden="true" />}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
