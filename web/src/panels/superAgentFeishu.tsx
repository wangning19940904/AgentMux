import { ExternalLink, RefreshCw, X } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { APIError, api, isDesktopApp, openExternalURL } from "../api";
import type { FeishuChat, FeishuScopeRequest, FeishuUser } from "../api";
import { useI18n } from "../i18n";
import { parseActors } from "./superAgentModel";

const MAX_SHOWN = 50;
const MAX_NAME_LOOKUP = 50; // Feishu's batch limit.

// scope is set when the error is a missing Feishu permission.
type Listing<T> = { items: T[]; loading: boolean; error: string; scope?: FeishuScopeRequest; truncated: boolean; warnings: string[] };
const idle = { items: [], loading: false, error: "", truncated: false, warnings: [] };

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);

// scopeRequestOf reads the permission the server says Feishu wants, if any.
function scopeRequestOf(error: unknown): FeishuScopeRequest | undefined {
  if (!(error instanceof APIError)) return undefined;
  const { scopes, apply_url: applyURL } = error.body;
  const request = {
    scopes: Array.isArray(scopes) ? scopes.filter((scope): scope is string => typeof scope === "string") : [],
    apply_url: typeof applyURL === "string" && /^https:\/\//.test(applyURL) ? applyURL : "",
  };
  return request.apply_url || request.scopes.length ? request : undefined;
}

export type FeishuDirectory = {
  chats: Listing<FeishuChat>;
  members: Listing<FeishuUser>;
  // Everyone the Console knows by open_id: group members and people looked up.
  people: Record<string, FeishuUser>;
  loadMembers: () => void;
  lookupEmails: (emails: string[]) => Promise<FeishuUser[]>;
  reload: () => void;
};

// useFeishuDirectory reads what a channel's bot can see. Members load on first
// use, since listing every group the bot is in can take a while. open_ids in
// `actors` that no listing named are resolved once, so saved senders show names.
export function useFeishuDirectory(channelID: string, memberChatIDs: string[], actors: string[]): FeishuDirectory {
  const [chats, setChats] = useState<Listing<FeishuChat>>(idle);
  const [members, setMembers] = useState<Listing<FeishuUser>>(idle);
  const [looked, setLooked] = useState<Record<string, FeishuUser>>({});
  const [membersWanted, setMembersWanted] = useState(false);
  const [generation, setGeneration] = useState(0);
  const attempted = useRef(new Set<string>());
  const memberKey = memberChatIDs.join(",");

  useEffect(() => {
    setLooked({});
    attempted.current = new Set();
  }, [channelID]);

  useEffect(() => {
    if (!channelID) {
      setChats(idle);
      return;
    }
    let current = true;
    setChats({ ...idle, loading: true });
    api.feishuChats(channelID).then(
      (list) => { if (current) setChats({ ...idle, items: list.items ?? [], truncated: Boolean(list.truncated) }); },
      (error) => { if (current) setChats({ ...idle, error: errorText(error), scope: scopeRequestOf(error) }); },
    );
    return () => { current = false; };
  }, [channelID, generation]);

  useEffect(() => {
    if (!channelID || !membersWanted) {
      setMembers(idle);
      return;
    }
    let current = true;
    setMembers({ ...idle, loading: true });
    api.feishuMembers(channelID, memberKey ? memberKey.split(",") : []).then(
      (list) => { if (current) setMembers({ ...idle, items: list.items ?? [], truncated: Boolean(list.truncated), warnings: list.warnings ?? [] }); },
      (error) => { if (current) setMembers({ ...idle, error: errorText(error), scope: scopeRequestOf(error) }); },
    );
    return () => { current = false; };
  }, [channelID, memberKey, membersWanted, generation]);

  const people = useMemo(() => {
    const known: Record<string, FeishuUser> = { ...looked };
    for (const member of members.items) known[member.open_id] = { ...known[member.open_id], ...member };
    return known;
  }, [looked, members.items]);

  const remember = useCallback((found: FeishuUser[]) => {
    setLooked((current) => ({ ...current, ...Object.fromEntries(found.map((user) => [user.open_id, { ...current[user.open_id], ...user }])) }));
  }, []);

  const unnamed = actors.filter((actor) => actor.startsWith("ou_") && !people[actor]?.name && !attempted.current.has(actor)).slice(0, MAX_NAME_LOOKUP);
  const unnamedKey = unnamed.join(",");
  useEffect(() => {
    if (!channelID || !unnamedKey) return;
    const ids = unnamedKey.split(",");
    for (const id of ids) attempted.current.add(id);
    // A missing contact scope only costs the names; the ids still work.
    api.feishuUsers(channelID, { open_ids: ids }).then(remember, () => undefined);
  }, [channelID, unnamedKey, remember]);

  const lookupEmails = useCallback(async (emails: string[]) => {
    const found = await api.feishuUsers(channelID, { emails });
    remember(found);
    return found;
  }, [channelID, remember]);

  return {
    chats, members, people, lookupEmails,
    loadMembers: useCallback(() => setMembersWanted(true), []),
    reload: useCallback(() => setGeneration((value) => value + 1), []),
  };
}

export type PickerOption = { value: string; label: string; detail?: string };

// TokenPicker edits a list of ids as chips. Typing searches the options; Enter
// adds the highlighted option, or hands the typed text to onText.
export function TokenPicker({ label, required, values, options, single, placeholder, status, help, enterHint, commitOnBlur, onChange, onFocus, onText }: {
  label: string;
  required?: boolean;
  values: string[];
  options: PickerOption[];
  single?: boolean;
  placeholder?: string;
  status?: ReactNode;
  help?: ReactNode;
  enterHint?: string;
  // Typed text that matches is added when the field loses focus, so a pasted
  // id is not lost; anything else is a search.
  commitOnBlur?: RegExp;
  onChange: (values: string[]) => void;
  onFocus?: () => void;
  onText?: (text: string) => Promise<{ values: string[]; error?: string }>;
}) {
  const { t } = useI18n();
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [upward, setUpward] = useState(false);
  const [highlighted, setHighlighted] = useState(0);
  const [busy, setBusy] = useState(false);
  const [textError, setTextError] = useState("");
  const [textScope, setTextScope] = useState<FeishuScopeRequest>();
  const unmatched = useRef("");
  const byValue = useMemo(() => new Map(options.map((option) => [option.value, option])), [options]);
  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return options.filter((option) => !values.includes(option.value) &&
      (!needle || [option.label, option.detail ?? "", option.value].some((text) => text.toLowerCase().includes(needle))));
  }, [options, values, query]);
  const shown = matches.slice(0, MAX_SHOWN);
  const active = Math.min(highlighted, shown.length - 1);
  const expanded = open && (shown.length > 0 || Boolean(query.trim() && enterHint));

  function add(next: string[]) {
    const merged = single ? next.slice(-1) : [...values, ...next.filter((value) => !values.includes(value))];
    onChange(merged);
    setQuery("");
    setHighlighted(0);
  }

  async function commitText() {
    const text = query.trim();
    if (!text || busy) return;
    setBusy(true);
    setTextError("");
    setTextScope(undefined);
    try {
      const result = onText ? await onText(text) : { values: parseActors(text) };
      if (result.values.length) add(result.values);
      else setQuery(text);
      if (result.error) setTextError(result.error);
      unmatched.current = result.values.length ? "" : text;
    } catch (error) {
      setTextError(errorText(error));
      setTextScope(scopeRequestOf(error));
      unmatched.current = text;
    } finally {
      setBusy(false);
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      setOpen(true);
      setHighlighted(Math.max(0, Math.min(shown.length - 1, active + (event.key === "ArrowDown" ? 1 : -1))));
    } else if (event.key === "Enter") {
      event.preventDefault();
      if (expanded && shown[active]) add([shown[active].value]);
      else void commitText();
    } else if (event.key === "Escape") {
      setOpen(false);
    } else if (event.key === "Backspace" && !query && values.length) {
      onChange(values.slice(0, -1));
    }
  }

  return (
    <div className="field super-agent-picker-field">
      <span id={`${id}-label`}>{label}{required ? " *" : ""}</span>
      <div className="super-agent-picker" onClick={() => input.current?.focus()}>
        {values.map((value) => {
          const name = byValue.get(value)?.label ?? value;
          return (
            <span key={value} className="super-agent-token" title={value}>
              {name}
              <button type="button" aria-label={t("superAgents.picker.remove", { name })} onClick={(event) => {
                event.stopPropagation();
                onChange(values.filter((item) => item !== value));
              }}><X size={12} /></button>
            </span>
          );
        })}
        <input ref={input} aria-label={label} role="combobox" aria-autocomplete="list" aria-expanded={expanded}
          aria-controls={expanded ? `${id}-list` : undefined}
          aria-activedescendant={expanded && shown[active] ? `${id}-option-${active}` : undefined}
          autoComplete="off" placeholder={values.length && single ? "" : placeholder} value={query} readOnly={busy} aria-busy={busy}
          onChange={(event) => { setQuery(event.target.value); setHighlighted(0); setOpen(true); setTextError(""); setTextScope(undefined); }}
          onFocus={() => {
            // These fields sit near the bottom of a scrolling drawer, so the
            // list opens upward when the window has more room above.
            const box = input.current?.parentElement?.getBoundingClientRect();
            if (box) setUpward(window.innerHeight - box.bottom < 300 && box.top > window.innerHeight - box.bottom);
            setOpen(true);
            onFocus?.();
          }}
          onBlur={() => {
            setOpen(false);
            const tokens = parseActors(query);
            if (commitOnBlur && query.trim() !== unmatched.current && tokens.length && tokens.every((token) => commitOnBlur.test(token))) void commitText();
          }}
          onKeyDown={onKeyDown} />
        {expanded && (
          <div id={`${id}-list`} className={`runtime-select-options super-agent-picker-options${upward ? " upward" : ""}`} role="listbox" aria-labelledby={`${id}-label`}>
            {shown.map((option, index) => (
              <div id={`${id}-option-${index}`} key={option.value} role="option" aria-selected={index === active}
                className={`runtime-select-option${index === active ? " highlighted" : ""}`}
                onMouseDown={(event) => { event.preventDefault(); add([option.value]); }}
                onMouseEnter={() => setHighlighted(index)}>
                <span>{option.label}</span>
                {option.detail && <small>{option.detail}</small>}
              </div>
            ))}
            {matches.length > shown.length && <div className="super-agent-picker-note">{t("superAgents.picker.more", { count: matches.length - shown.length })}</div>}
            {shown.length === 0 && enterHint && <div className="super-agent-picker-note">{enterHint}</div>}
          </div>
        )}
      </div>
      {textError && <small className="super-agent-problem" role="alert" title={textScope ? textError : undefined}>{textScope ? t("superAgents.picker.lookupScopeMissing") : textError}</small>}
      {textScope && <small><ScopeRequest request={textScope} /></small>}
      {status}
      {help && <small>{help}</small>}
    </div>
  );
}

// ScopeRequest names the scopes Feishu would accept and links to the app's
// developer console page, where the operator requests one.
function ScopeRequest({ request }: { request: FeishuScopeRequest }) {
  const { t } = useI18n();
  return (
    <span className="super-agent-scope-request">
      {request.scopes.length > 0 && (
        <span className="super-agent-scope-list">
          {t("superAgents.picker.scopeOptions")}
          {request.scopes.map((scope) => <code key={scope}>{scope}</code>)}
        </span>
      )}
      {request.apply_url && (
        <a className="ghost-action super-agent-scope-apply" href={request.apply_url} target="_blank" rel="noreferrer"
          onClick={(event) => {
            if (!isDesktopApp()) return;
            event.preventDefault();
            void openExternalURL(request.apply_url);
          }}>
          <ExternalLink size={12} />{t("superAgents.picker.requestScope")}
        </a>
      )}
      <span>{t("superAgents.picker.afterScope")}</span>
    </span>
  );
}

function ListingStatus({ listing, loadingKey, loadedText, failedKey, onReload }: {
  listing: Listing<unknown>;
  loadingKey: string;
  loadedText: string;
  failedKey: string;
  onReload: () => void;
}) {
  const { t } = useI18n();
  // A missing permission is explained below rather than in Feishu's words,
  // which repeat the scopes in two languages.
  const error = listing.scope ? t("superAgents.picker.scopeMissing") : listing.error;
  let text = listing.loading ? t(loadingKey) : listing.error ? t(failedKey, { error }) : loadedText;
  if (!listing.loading && !listing.error && listing.truncated) text += ` ${t("superAgents.picker.truncated")}`;
  return (
    <small className={`super-agent-picker-status${listing.error ? " super-agent-problem" : ""}`}>
      <span title={listing.scope ? listing.error : undefined}>{text}</span>
      {!listing.loading && <button type="button" className="ghost-action icon-action" title={t("common.refresh")} aria-label={t("common.refresh")} onClick={onReload}><RefreshCw size={12} /></button>}
      {!listing.loading && listing.scope && <ScopeRequest request={listing.scope} />}
      {listing.warnings.map((warning) => <span key={warning} className="super-agent-problem">{warning}</span>)}
    </small>
  );
}

export function FeishuChatPicker({ label, required, value, single, channelID, directory, onChange }: {
  label: string;
  required?: boolean;
  value: string;
  single?: boolean;
  channelID: string;
  directory: FeishuDirectory;
  onChange: (value: string) => void;
}) {
  const { t } = useI18n();
  const { chats } = directory;
  const options = useMemo(() => chats.items.map((chat) => ({
    value: chat.chat_id,
    label: chat.name || chat.chat_id,
    detail: chat.external ? `${chat.chat_id} · ${t("superAgents.picker.external")}` : chat.chat_id,
  })), [chats.items, t]);
  return (
    <TokenPicker
      label={label} required={required} single={single} options={options}
      values={parseActors(value)}
      placeholder={t(!channelID ? "superAgents.picker.channelFirst" : single ? "superAgents.picker.searchChat" : "superAgents.picker.searchChats")}
      enterHint={t("superAgents.picker.enterChat")}
      commitOnBlur={/^oc_\S+$/}
      onChange={(values) => onChange(values.join(", "))}
      status={channelID && (
        <ListingStatus listing={chats} onReload={directory.reload}
          loadingKey="superAgents.picker.chatsLoading" failedKey="superAgents.picker.chatsFailed"
          loadedText={t(chats.items.length ? "superAgents.picker.chatsLoaded" : "superAgents.picker.noChats", { count: chats.items.length })} />
      )}
    />
  );
}

export function FeishuActorPicker({ label, values, allowEveryone, scoped, help, channelID, directory, onChange }: {
  label: string;
  values: string[];
  allowEveryone: boolean;
  // Members come from the chats this Source names rather than all the bot's.
  scoped: boolean;
  help: string;
  channelID: string;
  directory: FeishuDirectory;
  onChange: (values: string[]) => void;
}) {
  const { t } = useI18n();
  const { chats, members, people } = directory;
  const options = useMemo(() => {
    const chatNames = new Map(chats.items.map((chat) => [chat.chat_id, chat.name]));
    const everyone = allowEveryone || values.includes("*") ? [{ value: "*", label: t("superAgents.picker.everyone"), detail: t("superAgents.picker.everyoneDetail") }] : [];
    return [...everyone, ...Object.values(people).map((person) => ({
      value: person.open_id,
      label: person.name || person.email || person.open_id,
      detail: [person.email, (person.chat_ids ?? []).map((id) => chatNames.get(id) || id).join(", ")].filter(Boolean).join(" · ") || person.open_id,
    }))];
  }, [allowEveryone, values, people, chats.items, t]);

  async function onText(text: string) {
    const tokens = parseActors(text);
    const emails = tokens.filter((token) => token.includes("@"));
    const ids = tokens.filter((token) => !token.includes("@"));
    if (emails.length === 0) return { values: ids };
    if (!channelID) return { values: ids, error: t("superAgents.picker.channelFirst") };
    const found = await directory.lookupEmails(emails);
    const missing = emails.filter((email) => !found.some((user) => user.email?.toLowerCase() === email.toLowerCase()));
    return {
      values: [...ids, ...found.map((user) => user.open_id)],
      error: missing.length ? t("superAgents.picker.emailNotFound", { emails: missing.join(", ") }) : undefined,
    };
  }

  return (
    <TokenPicker
      label={label} required values={values} options={options} onChange={onChange} onText={onText}
      placeholder={t(channelID ? "superAgents.picker.searchPeople" : "superAgents.picker.channelFirst")}
      enterHint={t("superAgents.picker.enterPerson")}
      commitOnBlur={/^(ou_\S+|\*|[^\s@]+@[^\s@]+)$/}
      onFocus={directory.loadMembers}
      help={help}
      status={channelID && (members.loading || members.error || members.items.length > 0) && (
        <ListingStatus listing={members} onReload={directory.reload}
          loadingKey="superAgents.picker.membersLoading" failedKey="superAgents.picker.membersFailed"
          loadedText={t(scoped ? "superAgents.picker.membersLoadedChosen" : "superAgents.picker.membersLoaded", { count: members.items.length })} />
      )}
    />
  );
}
