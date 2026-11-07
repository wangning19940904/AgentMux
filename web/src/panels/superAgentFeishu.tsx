import { CheckCircle2, ExternalLink, Loader2, RefreshCw, Smartphone, UserPlus, X, XCircle } from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { APIError, api, openExternalURL } from "../api";
import type { FeishuAddBotResult, FeishuChat, FeishuScopeRequest, FeishuUser, FeishuUserAuth } from "../api";
import { useI18n } from "../i18n";
import { sleep } from "./connect/connectShared";
import { parseActors } from "./superAgentModel";

const MAX_SHOWN = 50;
const MAX_NAME_LOOKUP = 50; // Feishu's batch limit.
const MAX_SEARCH = 50; // Feishu's people search takes at most 50 characters.
const SEARCH_DELAY_MS = 300;

// scope is set when the error is a missing Feishu permission.
type Listing<T> = { items: T[]; loading: boolean; error: string; scope?: FeishuScopeRequest; truncated: boolean; warnings: string[] };
const idle = { items: [], loading: false, error: "", truncated: false, warnings: [] };

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);

// scopeRequestOf reads the permission the server says Feishu wants, if any.
function scopeRequestOf(error: unknown): FeishuScopeRequest | undefined {
  if (!(error instanceof APIError)) return undefined;
  const { scopes, apply_url: applyURL, token_type: tokenType } = error.body;
  const request = {
    scopes: Array.isArray(scopes) ? scopes.filter((scope): scope is string => typeof scope === "string") : [],
    apply_url: typeof applyURL === "string" && /^https:\/\//.test(applyURL) ? applyURL : "",
    token_type: typeof tokenType === "string" ? tokenType : undefined,
  };
  return request.apply_url || request.scopes.length ? request : undefined;
}

// authRequired says the server wants the person to authorize (again).
const authRequired = (error: unknown) => error instanceof APIError && error.body.user_auth_required === true;

// FeishuUserState is who authorized the channel's app as themselves. lapsed
// marks an authorization Feishu stopped accepting since the page loaded.
export type FeishuUserState = { checked: boolean; user: FeishuUserAuth | null; lapsed?: boolean };

export type FeishuDirectory = {
  // The bot's groups; once someone authorizes, theirs, marked with bot_in.
  chats: Listing<FeishuChat>;
  // Members of the bot's groups; once someone authorizes, the people they
  // have chatted with.
  members: Listing<FeishuUser>;
  // Everyone the Console knows by open_id: members, contacts and people looked up.
  people: Record<string, FeishuUser>;
  auth: FeishuUserState;
  setAuth: (user: FeishuUserAuth | null) => void;
  loadMembers: () => void;
  lookupEmails: (emails: string[]) => Promise<FeishuUser[]>;
  // searchPeople finds people by name among everyone the authorizing person
  // can see. Results join `people`; without an authorization it does nothing.
  searchPeople: (query: string) => void;
  addBot: (chatID: string) => Promise<FeishuAddBotResult>;
  reload: () => void;
};

// useFeishuDirectory reads what a channel's bot can see, or what the person
// who authorized its app can see. Members load on first use, since listing
// every group can take a while. open_ids in `actors` that no listing named
// are resolved once, so saved senders show names.
export function useFeishuDirectory(channelID: string, memberChatIDs: string[], actors: string[]): FeishuDirectory {
  const [auth, setAuthState] = useState<FeishuUserState>({ checked: false, user: null });
  const [chats, setChats] = useState<Listing<FeishuChat>>(idle);
  const [members, setMembers] = useState<Listing<FeishuUser>>(idle);
  const [looked, setLooked] = useState<Record<string, FeishuUser>>({});
  const [membersWanted, setMembersWanted] = useState(false);
  const [generation, setGeneration] = useState(0);
  const attempted = useRef(new Set<string>());
  const searchTimer = useRef<number | undefined>(undefined);
  const memberKey = memberChatIDs.join(",");
  const authorized = Boolean(auth.user);

  useEffect(() => {
    setAuthState({ checked: false, user: null });
    if (!channelID) return;
    let current = true;
    // An older daemon has no such route; it then lists what the bot sees.
    api.feishuUserAuth(channelID).then(
      (result) => { if (current) setAuthState({ checked: true, user: result.user ?? null }); },
      () => { if (current) setAuthState({ checked: true, user: null }); },
    );
    return () => { current = false; };
  }, [channelID]);

  const lapsed = useCallback(() => setAuthState({ checked: true, user: null, lapsed: true }), []);
  const setAuth = useCallback((user: FeishuUserAuth | null) => setAuthState({ checked: true, user }), []);

  useEffect(() => {
    setLooked({});
    attempted.current = new Set();
  }, [channelID, authorized]);

  useEffect(() => () => window.clearTimeout(searchTimer.current), []);

  useEffect(() => {
    if (!channelID) {
      setChats(idle);
      return;
    }
    setChats({ ...idle, loading: true });
    if (!auth.checked) return;
    let current = true;
    (authorized ? api.feishuMyChats(channelID) : api.feishuChats(channelID)).then(
      (list) => { if (current) setChats({ ...idle, items: list.items ?? [], truncated: Boolean(list.truncated) }); },
      (error) => {
        if (!current) return;
        if (authorized && authRequired(error)) lapsed();
        else setChats({ ...idle, error: errorText(error), scope: scopeRequestOf(error) });
      },
    );
    return () => { current = false; };
  }, [channelID, generation, auth.checked, authorized, lapsed]);

  useEffect(() => {
    if (!channelID || !membersWanted || !auth.checked) {
      setMembers(idle);
      return;
    }
    let current = true;
    setMembers({ ...idle, loading: true });
    (authorized ? api.feishuContacts(channelID, {}) : api.feishuMembers(channelID, memberKey ? memberKey.split(",") : [])).then(
      (list) => { if (current) setMembers({ ...idle, items: list.items ?? [], truncated: Boolean(list.truncated), warnings: list.warnings ?? [] }); },
      (error) => {
        if (!current) return;
        if (authorized && authRequired(error)) lapsed();
        else setMembers({ ...idle, error: errorText(error), scope: scopeRequestOf(error) });
      },
    );
    return () => { current = false; };
  }, [channelID, memberKey, membersWanted, generation, auth.checked, authorized, lapsed]);

  const people = useMemo(() => {
    const known: Record<string, FeishuUser> = { ...looked };
    for (const member of members.items) known[member.open_id] = { ...known[member.open_id], ...member };
    return known;
  }, [looked, members.items]);

  const remember = useCallback((found: FeishuUser[]) => {
    setLooked((current) => ({ ...current, ...Object.fromEntries(found.map((user) => [user.open_id, { ...current[user.open_id], ...user }])) }));
  }, []);

  const unnamed = actors.filter((actor) => actor.startsWith("ou_") && !people[actor]?.name && !attempted.current.has(actor)).slice(0, MAX_NAME_LOOKUP);
  const unnamedKey = auth.checked ? unnamed.join(",") : "";
  useEffect(() => {
    if (!channelID || !unnamedKey) return;
    const ids = unnamedKey.split(",");
    for (const id of ids) attempted.current.add(id);
    // A missing scope only costs the names; the ids still work.
    if (authorized) api.feishuContacts(channelID, { open_ids: ids }).then((list) => remember(list.items ?? []), () => undefined);
    else api.feishuUsers(channelID, { open_ids: ids }).then(remember, () => undefined);
  }, [channelID, unnamedKey, authorized, remember]);

  const lookupEmails = useCallback(async (emails: string[]) => {
    let found: FeishuUser[];
    if (authorized) {
      // The people search matches whole addresses; keep only exact ones.
      const results = await Promise.all(emails.map((email) => api.feishuContacts(channelID, { query: email }).then((list) =>
        (list.items ?? []).filter((user) => user.email?.toLowerCase() === email.toLowerCase()).slice(0, 1))));
      found = results.flat();
    } else {
      found = await api.feishuUsers(channelID, { emails });
    }
    remember(found);
    return found;
  }, [channelID, authorized, remember]);

  const searchPeople = useCallback((query: string) => {
    window.clearTimeout(searchTimer.current);
    const text = query.trim();
    if (!authorized || !channelID || !text || text.includes("@")) return;
    searchTimer.current = window.setTimeout(() => {
      api.feishuContacts(channelID, { query: Array.from(text).slice(0, MAX_SEARCH).join("") }).then(
        (list) => remember(list.items ?? []),
        (error) => { if (authRequired(error)) lapsed(); },
      );
    }, SEARCH_DELAY_MS);
  }, [authorized, channelID, remember, lapsed]);

  const addBot = useCallback(async (chatID: string) => {
    try {
      const result = await api.addFeishuBotToChat(channelID, chatID);
      if (result.added) setChats((current) => ({ ...current, items: current.items.map((chat) => chat.chat_id === chatID ? { ...chat, bot_in: true } : chat) }));
      return result;
    } catch (error) {
      if (authRequired(error)) lapsed();
      throw error;
    }
  }, [channelID, lapsed]);

  return {
    chats, members, people, auth, setAuth, lookupEmails, searchPeople, addBot,
    loadMembers: useCallback(() => setMembersWanted(true), []),
    reload: useCallback(() => setGeneration((value) => value + 1), []),
  };
}

function ExternalButton({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a className="ghost-action super-agent-scope-apply" href={href} target="_blank" rel="noreferrer"
      onClick={(event) => { event.preventDefault(); void openExternalURL(href); }}>
      <ExternalLink size={12} />{children}
    </a>
  );
}

type AuthFlow = { phase: "idle" | "starting" | "waiting" | "denied" | "expired" | "error"; url?: string; code?: string; error?: string };

// FeishuUserAuthorize lets the operator authorize the channel's app with their
// own Feishu account, once. Feishu's device flow needs no redirect address:
// they scan the code with Feishu, or open the link, and approve.
export function FeishuUserAuthorize({ channelID, bot, directory }: { channelID: string; bot: string; directory: FeishuDirectory }) {
  const { t } = useI18n();
  const [flow, setFlow] = useState<AuthFlow>({ phase: "idle" });
  const [forgetting, setForgetting] = useState(false);
  const run = useRef(0);
  const { auth, setAuth } = directory;

  useEffect(() => () => { run.current += 1; }, []);
  useEffect(() => {
    run.current += 1;
    setFlow({ phase: "idle" });
  }, [channelID]);

  async function start() {
    const id = ++run.current;
    const live = () => run.current === id;
    setFlow({ phase: "starting" });
    try {
      const started = await api.startFeishuUserAuth(channelID);
      if (!live()) return;
      setFlow({ phase: "waiting", url: started.verification_url, code: started.user_code });
      let interval = started.interval || 5;
      const deadline = Date.now() + (started.expires_in || 240) * 1000;
      while (Date.now() < deadline) {
        await sleep(interval * 1000);
        if (!live()) return;
        const polled = await api.pollFeishuUserAuth(channelID, started.session_id);
        if (!live()) return;
        if (polled.status === "slow_down") interval += 5;
        if (polled.status === "completed") {
          setAuth(polled.user ?? null);
          setFlow({ phase: "idle" });
          return;
        }
        if (polled.status === "denied" || polled.status === "expired") {
          setFlow({ phase: polled.status });
          return;
        }
      }
      if (live()) setFlow({ phase: "expired" });
    } catch (error) {
      if (live()) setFlow({ phase: "error", error: errorText(error) });
    }
  }

  function cancel() {
    run.current += 1;
    setFlow({ phase: "idle" });
  }

  async function forget() {
    setForgetting(true);
    try {
      await api.forgetFeishuUserAuth(channelID);
      setAuth(null);
    } catch (error) {
      setFlow({ phase: "error", error: errorText(error) });
    } finally {
      setForgetting(false);
    }
  }

  if (!channelID) return <p>{t("superAgents.setup.feishu.pickFirst")}</p>;
  if (!auth.checked) return <p className="setup-scan-status"><Loader2 className="spin" size={15} /> {t("superAgents.setup.feishu.userChecking")}</p>;

  const user = auth.user;
  if (user && flow.phase === "idle") {
    const missing = user.missing ?? [];
    return (
      <>
        <div className="setup-ok" role="status">
          <CheckCircle2 size={15} /> {t("superAgents.setup.feishu.userDone", { name: user.name || user.open_id })}
          <button type="button" className="ghost-action" onClick={() => void start()}><RefreshCw size={13} /> {t("superAgents.setup.feishu.userAgain")}</button>
          <button type="button" className="ghost-action" disabled={forgetting} onClick={() => void forget()}>{t("superAgents.setup.feishu.userForget")}</button>
        </div>
        {missing.length > 0 && (
          <p className="setup-warn super-agent-scope-request">
            <span>{t("superAgents.setup.feishu.userMissing", { scopes: missing.join(", ") })}</span>
            {user.apply_url && <ExternalButton href={user.apply_url}>{t("superAgents.picker.requestScope")}</ExternalButton>}
          </p>
        )}
      </>
    );
  }

  const failed = flow.phase === "denied" || flow.phase === "expired" || flow.phase === "error";
  return (
    <>
      <p>{t("superAgents.setup.feishu.userHelp", { bot })}</p>
      {auth.lapsed && flow.phase === "idle" && <p className="setup-warn">{t("superAgents.setup.feishu.userLapsed")}</p>}
      {flow.phase === "idle" ? (
        <div className="setup-scan">
          <button type="button" className="ghost-action" onClick={() => void start()}><Smartphone size={14} /> {t("superAgents.setup.feishu.userAuthorize")}</button>
        </div>
      ) : (
        <div className={`qr-setup setup-scan-box${failed ? " error" : ""}`} role="status">
          {flow.phase === "waiting" && flow.url && <span className="qr-frame"><QRCodeSVG value={flow.url} size={148} level="M" /></span>}
          <span className="setup-scan-status">
            {flow.phase === "starting" && <Loader2 className="spin" size={15} />}
            {failed && <XCircle size={15} />}
            {{
              starting: t("superAgents.setup.feishu.generating"),
              waiting: t("superAgents.setup.feishu.userWaiting"),
              denied: t("superAgents.setup.feishu.userDenied"),
              expired: t("superAgents.setup.feishu.userExpired"),
              error: flow.error || t("superAgents.setup.feishu.failed"),
            }[flow.phase]}
          </span>
          {flow.phase === "waiting" && flow.url && <ExternalButton href={flow.url}>{t("superAgents.setup.feishu.userOpen")}</ExternalButton>}
          {flow.phase === "waiting" && flow.code && <small>{t("superAgents.setup.feishu.userCode", { code: flow.code })}</small>}
          {(failed || flow.phase === "waiting") && (
            <button type="button" className="ghost-action" onClick={failed ? () => void start() : cancel}>
              {failed ? <><RefreshCw size={13} /> {t("superAgents.setup.retry")}</> : t("common.cancel")}
            </button>
          )}
        </div>
      )}
    </>
  );
}

export type PickerOption = { value: string; label: string; detail?: string };

// TokenPicker edits a list of ids as chips. Typing searches the options; Enter
// adds the highlighted option, or hands the typed text to onText.
export function TokenPicker({ label, required, values, options, single, placeholder, status, help, enterHint, commitOnBlur, onChange, onFocus, onQuery, onText }: {
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
  // onQuery hears each edit of the typed text, to search for more options.
  onQuery?: (text: string) => void;
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
          onChange={(event) => {
            setQuery(event.target.value);
            setHighlighted(0);
            setOpen(true);
            setTextError("");
            setTextScope(undefined);
            onQuery?.(event.target.value);
          }}
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
      {request.apply_url && <ExternalButton href={request.apply_url}>{t("superAgents.picker.requestScope")}</ExternalButton>}
      <span>{t(request.token_type === "user" ? "superAgents.picker.afterUserScope" : "superAgents.picker.afterScope")}</span>
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

type Joining = { busy?: boolean; added?: boolean; pending?: boolean; error?: string; scope?: FeishuScopeRequest };

// AddBot offers to add the bot, as the authorizing person, to a chosen group
// it is not in yet.
function AddBot({ chat, state, onAdd }: { chat: FeishuChat; state: Joining; onAdd: () => void }) {
  const { t } = useI18n();
  const name = chat.name || chat.chat_id;
  if (state.added) return <small className="setup-ok super-agent-add-bot" role="status"><CheckCircle2 size={13} /> {t("superAgents.picker.botAdded", { chat: name })}</small>;
  if (state.pending) return <small className="setup-warn super-agent-add-bot" role="status">{t("superAgents.picker.botPending", { chat: name })}</small>;
  return (
    <small className="super-agent-add-bot">
      <span className="setup-warn">{t("superAgents.picker.botAbsentIn", { chat: name })}</span>
      <button type="button" className="ghost-action" disabled={state.busy} onClick={onAdd}>
        {state.busy ? <Loader2 className="spin" size={12} /> : <UserPlus size={12} />} {t("superAgents.picker.addBot")}
      </button>
      {state.error && <span className="super-agent-problem" title={state.scope ? state.error : undefined}>{state.scope ? t("superAgents.picker.scopeMissing") : state.error}</span>}
      {state.scope && <ScopeRequest request={state.scope} />}
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
  const authorized = Boolean(directory.auth.user);
  const [joining, setJoining] = useState<Record<string, Joining>>({});
  useEffect(() => setJoining({}), [channelID]);
  const values = parseActors(value);
  const options = useMemo(() => chats.items.map((chat) => ({
    value: chat.chat_id,
    label: chat.name || chat.chat_id,
    detail: [chat.chat_id, chat.external && t("superAgents.picker.external"), chat.bot_in === false && t("superAgents.picker.botAbsent")].filter(Boolean).join(" · "),
  })), [chats.items, t]);
  const absent = chats.items.filter((chat) => values.includes(chat.chat_id) && (chat.bot_in === false || joining[chat.chat_id]?.added));

  async function add(chat: FeishuChat) {
    setJoining((current) => ({ ...current, [chat.chat_id]: { busy: true } }));
    try {
      const result = await directory.addBot(chat.chat_id);
      setJoining((current) => ({ ...current, [chat.chat_id]: { added: result.added, pending: result.pending } }));
    } catch (error) {
      setJoining((current) => ({ ...current, [chat.chat_id]: { error: errorText(error), scope: scopeRequestOf(error) } }));
    }
  }

  const loadedKey = authorized
    ? chats.items.length ? "superAgents.picker.myChatsLoaded" : "superAgents.picker.noMyChats"
    : chats.items.length ? "superAgents.picker.chatsLoaded" : "superAgents.picker.noChats";
  return (
    <TokenPicker
      label={label} required={required} single={single} options={options}
      values={values}
      placeholder={t(!channelID ? "superAgents.picker.channelFirst"
        : authorized ? single ? "superAgents.picker.searchMyChat" : "superAgents.picker.searchMyChats"
        : single ? "superAgents.picker.searchChat" : "superAgents.picker.searchChats")}
      enterHint={t("superAgents.picker.enterChat")}
      commitOnBlur={/^oc_\S+$/}
      onChange={(next) => onChange(next.join(", "))}
      status={channelID && (
        <>
          <ListingStatus listing={chats} onReload={directory.reload}
            loadingKey="superAgents.picker.chatsLoading" failedKey="superAgents.picker.chatsFailed"
            loadedText={t(loadedKey, { count: chats.items.length })} />
          {absent.map((chat) => <AddBot key={chat.chat_id} chat={chat} state={joining[chat.chat_id] ?? {}} onAdd={() => void add(chat)} />)}
        </>
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
  const authorized = Boolean(directory.auth.user);
  const options = useMemo(() => {
    const chatNames = new Map(chats.items.map((chat) => [chat.chat_id, chat.name]));
    const everyone = allowEveryone || values.includes("*") ? [{ value: "*", label: t("superAgents.picker.everyone"), detail: t("superAgents.picker.everyoneDetail") }] : [];
    return [...everyone, ...Object.values(people).map((person) => ({
      value: person.open_id,
      label: person.name || person.email || person.open_id,
      detail: [person.email, person.department, (person.chat_ids ?? []).map((id) => chatNames.get(id) || id).join(", ")].filter(Boolean).join(" · ") || person.open_id,
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

  const loadedKey = authorized ? "superAgents.picker.contactsLoaded" : scoped ? "superAgents.picker.membersLoadedChosen" : "superAgents.picker.membersLoaded";
  return (
    <TokenPicker
      label={label} required values={values} options={options} onChange={onChange} onText={onText}
      placeholder={t(!channelID ? "superAgents.picker.channelFirst" : authorized ? "superAgents.picker.searchContacts" : "superAgents.picker.searchPeople")}
      enterHint={t("superAgents.picker.enterPerson")}
      commitOnBlur={/^(ou_\S+|\*|[^\s@]+@[^\s@]+)$/}
      onFocus={directory.loadMembers}
      onQuery={directory.searchPeople}
      help={help}
      status={channelID && (members.loading || members.error || members.items.length > 0) && (
        <ListingStatus listing={members} onReload={directory.reload}
          loadingKey="superAgents.picker.membersLoading" failedKey="superAgents.picker.membersFailed"
          loadedText={t(loadedKey, { count: members.items.length })} />
      )}
    />
  );
}
