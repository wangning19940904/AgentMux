import { Copy, KeyRound, Laptop, LogOut, Plus, RefreshCw, Trash2, UserRound, UserPlus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, type Account, type AccountRole, type AuthSession, type DevicePairingCode, type PairedDevice } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { accountFormError, accountLabel, MIN_PASSWORD_LENGTH, notifyAuthChanged } from "../signIn";
import { confirmAction } from "../components/ConfirmDialog";

// Accounts are the people who sign in to this AgentMux. Everyone signed in
// sees who they are, can change their password and sign out; administrators
// manage accounts and decide which account owns each paired device; members
// see and pair their own devices.

const fieldStyle = { display: "grid", gap: 4 } as const;

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

// onAccountsChanged runs after an account is created, enabled or disabled.
export function AccountsPanel({ onAccountsChanged }: { onAccountsChanged?: () => void } = {}) {
  const session = useAsync(() => api.authSession(), []);
  const state = session.data;
  if (!state) return null;
  return (
    <>
      {state.account && <SignedInCard session={state} />}
      {state.admin && (state.accounts_enabled || state.setup_available) && (
        <AccountsAdmin session={state} onChanged={() => {
          void session.reload();
          onAccountsChanged?.();
        }} />
      )}
      {(state.admin || state.account) && <Devices session={state} />}
    </>
  );
}

function SignedInCard({ session }: { session: AuthSession }) {
  const { t } = useI18n();
  const account = session.account!;
  const [changing, setChanging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [form, setForm] = useState({ current: "", next: "", confirm: "" });

  async function signOut() {
    setBusy(true);
    try {
      await api.signOut();
      notifyAuthChanged();
    } catch (error) {
      setNotice(errorText(error));
      setBusy(false);
    }
  }

  async function changePassword(event: FormEvent) {
    event.preventDefault();
    const problem = accountFormError({ login: account.login, password: form.next, confirm: form.confirm }, { passwordRequired: true });
    if (problem) {
      setNotice(t(problem, { min: String(MIN_PASSWORD_LENGTH) }));
      return;
    }
    setBusy(true);
    setNotice("");
    try {
      await api.changeOwnPassword(form.current, form.next);
      setForm({ current: "", next: "", confirm: "" });
      setChanging(false);
      setNotice(t("auth.passwordChanged"));
    } catch (error) {
      setNotice(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="surface" aria-label={t("auth.signedInAs", { name: accountLabel(account) })}>
      <div className="surface-header">
        <div className="agent-list-main">
          <span className="provider-icon"><UserRound size={15} /></span>
          <span>
            <strong>{t("auth.signedInAs", { name: accountLabel(account) })}</strong>
            <small>
              {account.login} · {t(account.role === "admin" ? "auth.roleAdmin" : "auth.roleMember")}
            </small>
          </span>
        </div>
        <div className="table-actions">
          <button className="ghost-action" disabled={busy} onClick={() => setChanging((value) => !value)}>
            <KeyRound size={14} /> {t("auth.changePassword")}
          </button>
          <button className="ghost-action" disabled={busy} onClick={() => void signOut()}>
            <LogOut size={14} /> {t("auth.signOut")}
          </button>
        </div>
      </div>
      {notice && <div className="surface-body"><p className="muted" role="status">{notice}</p></div>}
      {changing && (
        <form className="surface-body" style={{ display: "grid", gap: 12, maxWidth: 360 }} onSubmit={(event) => void changePassword(event)}>
          <label style={fieldStyle}>
            <span>{t("auth.currentPassword")}</span>
            <input type="password" autoComplete="current-password" value={form.current}
              onChange={(event) => setForm({ ...form, current: event.target.value })} />
          </label>
          <label style={fieldStyle}>
            <span>{t("auth.newPassword")}</span>
            <input type="password" autoComplete="new-password" value={form.next}
              onChange={(event) => setForm({ ...form, next: event.target.value })} />
          </label>
          <label style={fieldStyle}>
            <span>{t("auth.confirmPassword")}</span>
            <input type="password" autoComplete="new-password" value={form.confirm}
              onChange={(event) => setForm({ ...form, confirm: event.target.value })} />
          </label>
          <div className="control-row">
            <button className="primary-action" type="submit" disabled={busy || !form.current || !form.next}>
              {t("common.save")}
            </button>
            <button className="ghost-action" type="button" onClick={() => setChanging(false)}>
              {t("common.cancel")}
            </button>
          </div>
        </form>
      )}
    </section>
  );
}

function OneTimeSecret({ label, value, onClose }: { label: string; value: string; onClose: () => void }) {
  const { t } = useI18n();
  return (
    <div className="surface-body">
      <p className="muted">{label}</p>
      <div className="control-row">
        <code className="account-secret">{value}</code>
        <button className="ghost-action" onClick={() => void navigator.clipboard?.writeText(value)}>
          <Copy size={14} /> {t("accounts.copy")}
        </button>
        <button className="ghost-action" onClick={onClose}>{t("common.close")}</button>
      </div>
      <p className="muted">{t("accounts.shownOnce")}</p>
    </div>
  );
}

function AccountsAdmin({ session, onChanged }: { session: AuthSession; onChanged: () => void }) {
  const { t } = useI18n();
  const setup = !session.accounts_enabled;
  const accounts = useAsync(() => setup ? Promise.resolve([] as Account[]) : api.accounts(), [setup]);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [issued, setIssued] = useState<{ label: string; value: string } | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ login: "", display_name: "", role: "member" as AccountRole, password: "", confirm: "" });

  async function run(action: () => Promise<unknown>): Promise<boolean> {
    if (busy) return false;
    setBusy(true);
    setNotice("");
    try {
      await action();
      await accounts.reload();
      onChanged();
      return true;
    } catch (error) {
      setNotice(errorText(error));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const problem = accountFormError(
      { login: form.login, password: form.password, confirm: setup ? form.confirm : undefined },
      { passwordRequired: setup },
    );
    if (problem) {
      setNotice(t(problem, { min: String(MIN_PASSWORD_LENGTH) }));
      return;
    }
    const login = form.login.trim().toLowerCase();
    const saved = await run(async () => {
      if (setup) {
        await api.setupFirstAdmin({ login, display_name: form.display_name.trim(), password: form.password });
        // The browser is now signed in as that administrator.
        notifyAuthChanged();
        return;
      }
      const result = await api.saveAccount({
        login, display_name: form.display_name.trim(), role: form.role, password: form.password || undefined,
      });
      if (result.password) setIssued({ label: t("accounts.generatedPassword", { login }), value: result.password });
      setCreating(false);
    });
    if (saved) setForm({ login: "", display_name: "", role: "member", password: "", confirm: "" });
  }

  const form_ = (
    <form className="surface-body" style={{ display: "grid", gap: 12, maxWidth: 420 }} onSubmit={(event) => void submit(event)}>
      <label style={fieldStyle}>
        <span>{t("accounts.login")}</span>
        <input autoCapitalize="none" spellCheck={false} autoComplete="off" value={form.login}
          onChange={(event) => setForm({ ...form, login: event.target.value })} />
      </label>
      <label style={fieldStyle}>
        <span>{t("accounts.displayName")}</span>
        <input value={form.display_name} onChange={(event) => setForm({ ...form, display_name: event.target.value })} />
      </label>
      {!setup && (
        <label style={fieldStyle}>
          <span>{t("accounts.role")}</span>
          <select value={form.role} onChange={(event) => setForm({ ...form, role: event.target.value as AccountRole })}>
            <option value="member">{t("auth.roleMember")}</option>
            <option value="admin">{t("auth.roleAdmin")}</option>
          </select>
        </label>
      )}
      <label style={fieldStyle}>
        <span>{t(setup ? "auth.password" : "accounts.passwordOptional")}</span>
        <input type="password" autoComplete="new-password" value={form.password}
          onChange={(event) => setForm({ ...form, password: event.target.value })} />
      </label>
      {setup && (
        <label style={fieldStyle}>
          <span>{t("auth.confirmPassword")}</span>
          <input type="password" autoComplete="new-password" value={form.confirm}
            onChange={(event) => setForm({ ...form, confirm: event.target.value })} />
        </label>
      )}
      <div className="control-row">
        <button className="primary-action" type="submit" disabled={busy || !form.login.trim()}>
          <UserPlus size={15} /> {t(setup ? "accounts.setupConfirm" : "accounts.create")}
        </button>
        {!setup && (
          <button className="ghost-action" type="button" onClick={() => setCreating(false)}>{t("common.cancel")}</button>
        )}
      </div>
    </form>
  );

  if (setup) {
    return (
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("accounts.setupTitle")}</h2>
            <p>{t("accounts.setupHint")}</p>
          </div>
        </div>
        {notice && <div className="surface-body error">{notice}</div>}
        {form_}
      </section>
    );
  }

  const items = accounts.data ?? [];
  const self = session.account?.id;
  return (
    <section className="surface">
      <div className="surface-header">
        <div>
          <h2>{t("accounts.title")}</h2>
          <p>{t("accounts.subtitle")}</p>
        </div>
        <div className="table-actions">
          <button className="ghost-action" onClick={() => void accounts.reload()}>
            <RefreshCw size={15} /> {t("common.refresh")}
          </button>
          <button className="primary-action" onClick={() => setCreating(true)}>
            <Plus size={15} /> {t("accounts.create")}
          </button>
        </div>
      </div>
      {notice && <div className="surface-body error">{notice}</div>}
      {issued && <OneTimeSecret label={issued.label} value={issued.value} onClose={() => setIssued(null)} />}
      {creating && form_}
      {accounts.loading && items.length === 0 ? (
        <div className="empty-state">{t("common.loading")}</div>
      ) : (
        <div className="surface-body">
          {items.map((account) => (
            <article className="agent-registry-row" key={account.id}>
              <div className="agent-list-main">
                <span className="provider-icon"><UserRound size={15} /></span>
                <span>
                  <strong>
                    {accountLabel(account)}
                    {account.id === self ? ` · ${t("accounts.you")}` : ""}
                  </strong>
                  <small>
                    {account.login} · {t("accounts.lastSignIn")}{" "}
                    {account.last_sign_in_at ? new Date(account.last_sign_in_at).toLocaleString() : t("accounts.never")}
                  </small>
                </span>
              </div>
              <div className="agent-list-meta">
                <span className={`status-badge ${account.status === "active" ? "success" : "warning"}`}>
                  <span className="status-dot" />
                  {t(account.status === "active" ? "accounts.active" : "accounts.disabled")}
                </span>
                <span className="source-badge manual">
                  {t(account.role === "admin" ? "auth.roleAdmin" : "auth.roleMember")}
                </span>
              </div>
              <div className="table-actions">
                <button className="ghost-action" disabled={busy} onClick={() => void run(() => api.saveAccount({
                  id: account.id, role: account.role === "admin" ? "member" : "admin",
                }))}>
                  {t(account.role === "admin" ? "accounts.makeMember" : "accounts.makeAdmin")}
                </button>
                <button className="ghost-action" disabled={busy} onClick={() => void run(async () => {
                  const result = await api.resetAccountPassword(account.id);
                  if (result.password) {
                    setIssued({ label: t("accounts.generatedPassword", { login: account.login }), value: result.password });
                  }
                })}>
                  <KeyRound size={14} /> {t("accounts.resetPassword")}
                </button>
                <button className="ghost-action" disabled={busy || account.id === self} onClick={() => void run(() => api.saveAccount({
                  id: account.id, status: account.status === "active" ? "disabled" : "active",
                }))}>
                  {t(account.status === "active" ? "accounts.disable" : "accounts.enable")}
                </button>
              </div>
            </article>
          ))}
        </div>
      )}
    </section>
  );
}

// Devices lists the machines paired with this hub and lets the caller pair
// another. A member sees the devices they own, and a pairing code they create
// makes the new device theirs. An administrator sees every device with the
// account that owns it and can hand a device to another account; a device
// without an owner is reachable by administrators only.
function Devices({ session }: { session: AuthSession }) {
  const { t } = useI18n();
  const admin = session.admin;
  const devices = useAsync(() => api.pairedDevices(), []);
  const accounts = useAsync(
    () => admin && session.accounts_enabled ? api.accounts() : Promise.resolve([] as Account[]),
    [admin, session.accounts_enabled],
  );
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [pairing, setPairing] = useState<DevicePairingCode | null>(null);

  async function run(action: () => Promise<unknown>) {
    setBusy(true);
    setNotice("");
    try {
      await action();
      await devices.reload();
    } catch (error) {
      setNotice(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  const items = devices.data ?? [];
  const accountOptions = accounts.data ?? [];
  const ownerName = (device: PairedDevice) => {
    if (!device.owner_account_id) return t("accounts.ownerAdmins");
    const owner = accountOptions.find((account) => account.id === device.owner_account_id);
    return owner ? accountLabel(owner) : device.owner_account_name || device.owner_account_id;
  };
  return (
    <section className="surface">
      <div className="surface-header">
        <div>
          <h3>{t(admin ? "accounts.allDevicesTitle" : "accounts.devicesTitle")}</h3>
          <p>{t(admin ? "accounts.allDevicesSubtitle" : "accounts.devicesSubtitle")}</p>
        </div>
        <div className="table-actions">
          <button className="ghost-action" onClick={() => void devices.reload()}>
            <RefreshCw size={15} /> {t("common.refresh")}
          </button>
          <button className="primary-action" disabled={busy} onClick={() => void run(async () => {
            setPairing(await api.createDevicePairingCode(""));
          })}>
            <Plus size={15} /> {t("accounts.pairDevice")}
          </button>
        </div>
      </div>
      {notice && <div className="surface-body error">{notice}</div>}
      {pairing && (
        <OneTimeSecret
          label={t(admin ? "accounts.pairHintAdmin" : "accounts.pairHint")}
          value={pairing.command}
          onClose={() => setPairing(null)}
        />
      )}
      {devices.loading && items.length === 0 ? (
        <div className="empty-state">{t("common.loading")}</div>
      ) : items.length === 0 ? (
        <div className="empty-state">{t("accounts.devicesEmpty")}</div>
      ) : (
        <div className="surface-body">
          {items.map((device) => (
            <article className="agent-registry-row" key={device.id}>
              <div className="agent-list-main">
                <span className="provider-icon"><Laptop size={15} /></span>
                <span>
                  <strong>{device.name}</strong>
                  <small>{[device.hostname, device.os, device.version].filter(Boolean).join(" · ")}</small>
                </span>
              </div>
              <div className="agent-list-meta">
                <span className={`status-badge ${device.online ? "success" : "warning"}`}>
                  <span className="status-dot" /> {t(device.online ? "accounts.online" : "accounts.offline")}
                </span>
                {admin && (
                  <span className="source-badge manual" title={t("accounts.deviceOwner")}>
                    {ownerName(device)}
                  </span>
                )}
              </div>
              <div className="table-actions">
                {admin && accountOptions.length > 0 && (
                  <select
                    aria-label={t("accounts.assignDevice", { name: device.name })}
                    title={t("accounts.deviceOwner")}
                    value={device.owner_account_id ?? ""}
                    disabled={busy}
                    onChange={(event) => {
                      const accountID = event.target.value;
                      void run(() => api.assignDeviceOwner(device.id, accountID));
                    }}
                  >
                    <option value="">{t("accounts.ownerAdmins")}</option>
                    {device.owner_account_id && !accountOptions.some((account) => account.id === device.owner_account_id) && (
                      <option value={device.owner_account_id}>{ownerName(device)}</option>
                    )}
                    {accountOptions.map((account) => (
                      <option key={account.id} value={account.id}>
                        {accountLabel(account)}{account.status === "disabled" ? ` (${t("accounts.disabled")})` : ""}
                      </option>
                    ))}
                  </select>
                )}
                <button className="ghost-action danger-action" disabled={busy}
                  onClick={async () => {
                    if (await confirmAction(t("accounts.removeDeviceConfirm", { name: device.name }), { danger: true })) {
                      void run(() => api.removePairedDevice(device.id));
                    }
                  }}>
                  <Trash2 size={14} /> {t("accounts.removeDevice")}
                </button>
              </div>
            </article>
          ))}
        </div>
      )}
    </section>
  );
}
