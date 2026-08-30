import { Copy, KeyRound, Plus, RefreshCw, ShieldCheck, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  api,
  type GrantLevel,
  type GrantableResourceType,
  type ResourceGrant,
  type Tenant,
  type TenantKind,
  type TenancySelf,
} from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";
import { TargetBadge, targetKey } from "../components/TargetBadge";
import {
  resourcesForType,
  toggleResourceSelection,
  type GrantResource,
} from "./tenantGrantModel";

// Tenants are the host applications that share this AgentMux instance. This
// panel is administrator-only: a tenant-scoped Console session gets 403 from
// every endpoint it uses, so the panel renders a notice instead.

const KINDS: TenantKind[] = ["app", "web", "service"];
const LEVELS: GrantLevel[] = ["read", "use", "manage"];
const RESOURCE_TYPES: GrantableResourceType[] = ["agent", "channel", "trigger", "provider"];

export function TenantsPanel({
  onContinue,
  onTenantChanged,
  identity,
  initialTenants,
}: {
  onContinue?: () => void;
  onTenantChanged?: (change?: { type: "delete"; tenant: Tenant }) => void;
  identity?: TenancySelf;
  initialTenants?: Tenant[];
}) {
  const { t } = useI18n();
  const self = useAsync(() => identity ? Promise.resolve(identity) : api.tenancySelf(), [identity]);
  const admin = identity?.admin === true || self.data?.admin === true;
  const tenants = useAsync(() => admin ? api.tenants() : Promise.resolve([]), [admin]);

  const [selectedID, setSelectedID] = useState("");
  const [notice, setNotice] = useState("");
  const [issued, setIssued] = useState<{ label: string; value: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [registerOpen, setRegisterOpen] = useState(false);
  const [deleteCandidate, setDeleteCandidate] = useState<Tenant | null>(null);
  const [deletedTenantIDs, setDeletedTenantIDs] = useState<string[]>([]);

  const items = useMemo(
    () => (tenants.data ?? initialTenants ?? [])
      .filter((tenant) => !deletedTenantIDs.includes(targetKey(tenant.target_id, tenant.id))),
    [deletedTenantIDs, initialTenants, tenants.data],
  );
  const selected = selectedID
    ? items.find((item) => targetKey(item.target_id, item.id) === selectedID)
    : undefined;
  const details = useAsync(
    () => admin && selected
      ? api.tenantAdminDetails(selected.id, selected.target_id)
      : Promise.resolve({ grants: [], agents: [], channels: [], triggers: [], providers: [] }),
    [admin, selectedID],
  );
  const scoped = self.data && !self.data.admin;
  const hasActiveTenant = scoped
    ? Boolean(self.data?.tenant_id && self.data.status !== "disabled")
    : items.some((item) => item.status === "active");

  async function run(
    action: () => Promise<unknown>,
    reload: { tenants?: boolean; details?: boolean } = {},
  ): Promise<boolean> {
    if (busy) return false;
    setBusy(true);
    setNotice("");
    try {
      await action();
      await Promise.all([
        reload.tenants ? tenants.reload() : Promise.resolve(null),
        reload.details ? details.reload() : Promise.resolve(null),
      ]);
      return true;
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
      return false;
    } finally {
      setBusy(false);
    }
  }

  if (self.loading) {
    return <div className="empty-state">{t("tenants.checking")}</div>;
  }

  if (self.error || !self.data) {
    return (
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tenants.requiredTitle")}</h2>
            <p>{t("tenants.identityError")}</p>
          </div>
          <button className="ghost-action" onClick={() => void self.reload()}>
            <RefreshCw size={15} /> {t("common.retry")}
          </button>
        </div>
      </section>
    );
  }

  if (scoped) {
    return (
      <div className="page-stack">
        <section className="surface">
          <div className="surface-header">
            <div>
              <h2>{t("tenants.registeredTitle")}</h2>
              <p>{t("tenants.registeredHint", { tenant: self.data?.tenant ?? "" })}</p>
            </div>
            <button className="primary-action" onClick={onContinue}>
              <ShieldCheck size={15} /> {t("tenants.enterConfig")}
            </button>
          </div>
          <div className="surface-body">
            <article className="agent-registry-row">
              <div className="agent-list-main">
                <span className="provider-icon">
                  <ShieldCheck size={15} />
                </span>
                <span>
                  <strong>{self.data.tenant}</strong>
                  <small>{self.data.tenant_id}</small>
                </span>
              </div>
              <div className="agent-list-meta">
                <span className="status-badge success">
                  <span className="status-dot" /> {t("tenants.active")}
                </span>
                <span className="source-badge manual">{self.data.kind ?? "app"}</span>
              </div>
            </article>
          </div>
        </section>
      </div>
    );
  }

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("tenants.title")}</h2>
            <p>{t("tenants.subtitle")}</p>
          </div>
          <div className="table-actions">
            <button className="primary-action" onClick={onContinue}>
              <ShieldCheck size={15} /> {t("tenants.enterConfig")}
            </button>
            <button className="ghost-action" onClick={() => void tenants.reload()}>
              <RefreshCw size={15} /> {t("common.refresh")}
            </button>
            <button className="primary-action" onClick={() => setRegisterOpen(true)}>
              <Plus size={15} /> {t("tenants.register")}
            </button>
          </div>
        </div>
        {notice && <div className="surface-body error">{notice}</div>}
        {!tenants.loading && !hasActiveTenant && (
          <div className="surface-body">
            <p className="muted">{t("tenants.requiredHint")}</p>
          </div>
        )}
        {issued && (
          <div className="surface-body">
            <p className="muted">{t("tenants.shownOnce")}</p>
            <div className="control-row">
              <code className="tenant-secret">{issued.value}</code>
              <button
                className="ghost-action"
                onClick={() => void navigator.clipboard?.writeText(issued.value)}
              >
                <Copy size={14} /> {t("tenants.copy")}
              </button>
              <button className="ghost-action" onClick={() => setIssued(null)}>
                {t("common.close")}
              </button>
            </div>
            <p className="muted">{issued.label}</p>
          </div>
        )}
      </section>

      <section className="surface">
        <div className="surface-header">
          <div>
            <h3>{t("tenants.listTitle")}</h3>
            <p>{t("tenants.listSubtitle")}</p>
          </div>
        </div>
        {tenants.loading && items.length === 0 ? (
          <div className="empty-state">{t("common.loading")}</div>
        ) : items.length === 0 ? (
          <div className="empty-state">{t("tenants.empty")}</div>
        ) : (
          <div className="surface-body">
            {items.map((tenant) => (
              <article
                aria-expanded={targetKey(tenant.target_id, tenant.id) === selectedID}
                className={`agent-registry-row tenant-list-row${targetKey(tenant.target_id, tenant.id) === selectedID ? " is-selected" : ""}`}
                key={targetKey(tenant.target_id, tenant.id)}
                onClick={() => setSelectedID(targetKey(tenant.target_id, tenant.id))}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget) return;
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    setSelectedID(targetKey(tenant.target_id, tenant.id));
                  }
                }}
                role="button"
                tabIndex={0}
              >
                <div className="agent-list-main">
                  <span className="provider-icon">
                    <ShieldCheck size={15} />
                  </span>
                  <span>
                    <strong>{tenant.name}</strong>
                    <small>{tenant.id}</small>
                  </span>
                </div>
                <div className="agent-list-meta">
                  <span className={`status-badge ${tenant.status === "active" ? "success" : "warning"}`}>
                    <span className="status-dot" />
                    {t(tenant.status === "active" ? "tenants.active" : "tenants.disabled")}
                  </span>
                  <span className="source-badge manual">{tenant.kind ?? "app"}</span>
                  <TargetBadge target_id={tenant.target_id} target_name={tenant.target_name} />
                  <span className="pill">
                    {tenant.resource_count ?? 0}{" "}
                    {t("tenants.resourceCount")}
                  </span>
                </div>
                <div className="table-actions">
                  <button
                    className="ghost-action"
                    disabled={busy}
                    onClick={(event) => {
                      event.stopPropagation();
                      void run(async () => {
                        const token = await api.createTenantToken({
                          tenant_id: tenant.id,
                          name: "console",
                          target_id: tenant.target_id,
                        });
                        if (token.secret) {
                          setIssued({
                            value: token.secret,
                            label: t("tenants.tokenIssued", { tenant: tenant.name }),
                          });
                        }
                      });
                    }}
                  >
                    <KeyRound size={14} /> {t("tenants.mintToken")}
                  </button>
                  <button
                    className="ghost-action"
                    disabled={busy}
                    onClick={async (event) => {
                      event.stopPropagation();
                      const changed = await run(
                        () => api.upsertTenant({
                          ...tenant,
                          status: tenant.status === "active" ? "disabled" : "active",
                        }),
                        { tenants: true },
                      );
                      if (changed) onTenantChanged?.();
                    }}
                  >
                    {t(tenant.status === "active" ? "tenants.disable" : "tenants.enable")}
                  </button>
                  <button
                    className="ghost-action danger-action"
                    disabled={busy}
                    onClick={(event) => {
                      event.stopPropagation();
                      setDeleteCandidate(tenant);
                    }}
                  >
                    <Trash2 size={14} /> {t("common.delete")}
                  </button>
                </div>
              </article>
            ))}
          </div>
        )}
      </section>


      {selected && (
        details.loading ? (
          <section className="surface">
            <div className="empty-state">{t("common.loading")}</div>
          </section>
        ) : details.error || !details.data ? (
          <section className="surface">
            <div className="surface-header">
              <div>
                <h3>{t("tenants.grantsTitle", { tenant: selected.name })}</h3>
                <p className="error">{details.error}</p>
              </div>
              <button className="ghost-action" onClick={() => void details.reload()}>
                <RefreshCw size={15} /> {t("common.retry")}
              </button>
            </div>
          </section>
        ) : (
          <GrantEditor
            tenant={selected}
            grants={details.data.grants}
            agents={details.data.agents}
            channels={details.data.channels}
            triggers={details.data.triggers}
            providers={details.data.providers}
            busy={busy}
            onGrantMany={(resources, level) =>
              run(
                () => Promise.all(resources.map((resource) => api.upsertResourceGrant({
                  tenant_id: selected.id,
                  resource_type: resource.type,
                  resource_id: resource.id,
                  level,
                  target_id: selected.target_id,
                }))),
                { details: true },
              ).then(() => undefined)
            }
            onRevoke={(grant) => void run(() => api.deleteResourceGrant(grant), { details: true })}
            onAssign={(resourceType, resourceID) => void run(
              () => api.assignResourceOwner({
                resource_type: resourceType,
                resource_id: resourceID,
                tenant_id: selected.id,
                target_id: selected.target_id,
              }),
              { details: true },
            ).then((changed) => {
              if (changed) void tenants.reload();
            })}
          />
        )
      )}

      {deleteCandidate && (
        <DeleteTenantDialog
          tenant={deleteCandidate}
          busy={busy}
          onClose={() => {
            if (!busy) setDeleteCandidate(null);
          }}
          onConfirm={async () => {
            const key = targetKey(deleteCandidate.target_id, deleteCandidate.id);
            const removed = await run(
              () => api.deleteTenant(deleteCandidate.id, deleteCandidate.target_id),
            );
            if (!removed) return;
            setDeletedTenantIDs((current) => [...current, key]);
            if (selectedID === key) setSelectedID("");
            setDeleteCandidate(null);
            onTenantChanged?.({ type: "delete", tenant: deleteCandidate });
            void tenants.reload();
          }}
        />
      )}

      {registerOpen && (
        <RegisterTenantDialog
          onClose={() => setRegisterOpen(false)}
          onCreated={() => {
            void tenants.reload().then(() => onTenantChanged?.());
          }}
        />
      )}
    </div>
  );
}

function DeleteTenantDialog({
  tenant,
  busy,
  onClose,
  onConfirm,
}: {
  tenant: Tenant;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const { t } = useI18n();
  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button
        aria-label={t("common.cancel")}
        className="meeting-dialog-backdrop internal-dialog-backdrop"
        disabled={busy}
        onClick={onClose}
        type="button"
      />
      <section
        aria-labelledby="tenant-delete-title"
        aria-modal="true"
        className="surface meeting-dialog tenant-delete-dialog"
        role="dialog"
      >
        <div className="meeting-dialog-icon tenant-delete-dialog-icon">
          <Trash2 size={22} />
        </div>
        <div className="meeting-dialog-heading">
          <h2 id="tenant-delete-title">{t("tenants.deleteTitle", { tenant: tenant.name })}</h2>
          <p>{t("tenants.confirmRemove", { tenant: tenant.name })}</p>
        </div>
        <div className="meeting-dialog-actions">
          <button className="ghost-action" disabled={busy} onClick={onClose} type="button">
            {t("common.cancel")}
          </button>
          <button
            className="ghost-action danger-action"
            disabled={busy}
            onClick={() => void onConfirm()}
            type="button"
          >
            <Trash2 size={14} /> {busy ? t("tenants.deleting") : t("common.delete")}
          </button>
        </div>
      </section>
    </div>
  );
}

// RegisterTenantDialog is the prominent one-click creation flow: confirm and
// the tenant plus its token exist immediately, with no approval step. The
// token is shown exactly once, inside the dialog.
function RegisterTenantDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated?: (tenantID: string) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<TenantKind>("app");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ tenant: string; token: string } | null>(null);

  async function submit() {
    const trimmed = name.trim();
    if (busy || !trimmed) return;
    setBusy(true);
    setError("");
    try {
      const created = await api.registerTenant({ name: trimmed, kind });
      setResult({ tenant: created.tenant.name, token: created.token });
      onCreated?.(created.tenant.id);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <button
        aria-label={t("common.close")}
        className="remote-host-dialog-backdrop"
        onClick={onClose}
        type="button"
      />
      <section
        aria-labelledby="tenant-register-title"
        aria-modal="true"
        className="surface remote-host-dialog tenant-register-dialog"
        role="dialog"
      >
        <div className="surface-header">
          <div>
            <h2 id="tenant-register-title">{t("tenants.registerTitle")}</h2>
            <p className="subtle-copy">{t("tenants.registerHint")}</p>
          </div>
          <button className="ghost-action" onClick={onClose} aria-label={t("common.close")}>
            <X size={15} />
          </button>
        </div>
        <div className="surface-body">
          {result ? (
            <>
              <p>{t("tenants.registerDone", { tenant: result.tenant })}</p>
              <p className="muted">{t("tenants.shownOnce")}</p>
              <div className="control-row">
                <code className="tenant-secret">{result.token}</code>
                <button
                  className="ghost-action"
                  onClick={() => void navigator.clipboard?.writeText(result.token)}
                >
                  <Copy size={14} /> {t("tenants.copy")}
                </button>
              </div>
              <p className="muted">{t("tenants.tokenIssued", { tenant: result.tenant })}</p>
              <div className="control-row">
                <button className="primary-action" onClick={onClose}>
                  {t("common.close")}
                </button>
              </div>
            </>
          ) : (
            <>
              {error && <p className="error">{error}</p>}
              <div className="control-row">
                <input
                  autoFocus
                  value={name}
                  placeholder={t("tenants.namePlaceholder")}
                  onChange={(event) => setName(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") void submit();
                  }}
                  aria-label={t("tenants.name")}
                />
                <select
                  value={kind}
                  onChange={(event) => setKind(event.target.value as TenantKind)}
                  aria-label={t("tenants.kind")}
                >
                  {KINDS.map((item) => (
                    <option key={item} value={item}>
                      {item}
                    </option>
                  ))}
                </select>
              </div>
              <div className="control-row">
                <button
                  className="primary-action"
                  disabled={busy || !name.trim()}
                  onClick={() => void submit()}
                >
                  <ShieldCheck size={15} /> {t("tenants.registerConfirm")}
                </button>
                <button className="ghost-action" onClick={onClose}>
                  {t("common.close")}
                </button>
              </div>
            </>
          )}
        </div>
      </section>
    </>
  );
}

function GrantEditor({
  tenant,
  grants,
  agents,
  channels,
  triggers,
  providers,
  busy,
  onGrantMany,
  onRevoke,
  onAssign,
}: {
  tenant: Tenant;
  grants: ResourceGrant[];
  agents: Omit<GrantResource, "type">[];
  channels: Omit<GrantResource, "type">[];
  triggers: Omit<GrantResource, "type">[];
  providers: Omit<GrantResource, "type">[];
  busy: boolean;
  onGrantMany: (resources: GrantResource[], level: GrantLevel) => Promise<void>;
  onRevoke: (grant: ResourceGrant) => void;
  onAssign: (resourceType: Exclude<GrantableResourceType, "provider">, resourceID: string) => void;
}) {
  const { t } = useI18n();
  const [resourceType, setResourceType] = useState<GrantableResourceType>("agent");
  const [level, setLevel] = useState<GrantLevel>("use");
  const [selectedResourceIDs, setSelectedResourceIDs] = useState<string[]>([]);
  const resources = useMemo(
    () => resourcesForType(resourceType, { agents, channels, triggers, providers }),
    [agents, channels, providers, resourceType, triggers],
  );
  const selectedResources = resources.filter((resource) => selectedResourceIDs.includes(resource.id));
  const allSelected = resources.length > 0 && selectedResources.length === resources.length;

  useEffect(() => {
    setSelectedResourceIDs([]);
  }, [resourceType, tenant.id]);

  function toggleResource(id: string) {
    setSelectedResourceIDs((current) => toggleResourceSelection(current, id));
  }

  async function grantSelected(resourcesToGrant: GrantResource[]) {
    if (resourcesToGrant.length === 0) return;
    await onGrantMany(resourcesToGrant, level);
    setSelectedResourceIDs([]);
  }

  return (
    <section className="surface">
      <div className="surface-header">
        <div>
          <h3>{t("tenants.grantsTitle", { tenant: tenant.name })}</h3>
          <p>{t("tenants.grantsSubtitle")}</p>
        </div>
      </div>
      <div className="surface-body">
        <div className="control-row">
          <select
            value={resourceType}
            onChange={(event) => {
              setResourceType(event.target.value as GrantableResourceType);
            }}
            aria-label={t("tenants.resourceType")}
          >
            {RESOURCE_TYPES.map((item) => (
              <option key={item} value={item}>
                {t(`tenants.resource.${item}`)}
              </option>
            ))}
          </select>
          <select
            value={level}
            onChange={(event) => setLevel(event.target.value as GrantLevel)}
            aria-label={t("tenants.level")}
          >
            {LEVELS.map((item) => (
              <option key={item} value={item}>
                {t(`tenants.level.${item}`)}
              </option>
            ))}
          </select>
          <label className="tenant-resource-select-all">
            <input
              type="checkbox"
              checked={allSelected}
              disabled={resources.length === 0}
              onChange={(event) =>
                setSelectedResourceIDs(event.target.checked ? resources.map((item) => item.id) : [])
              }
            />
            {t("tenants.selectAll")}
          </label>
          <button
            className="primary-action"
            disabled={busy || selectedResources.length === 0}
            onClick={() => void grantSelected(selectedResources)}
          >
            {t("tenants.batchGrant", { count: selectedResources.length })}
          </button>
        </div>

        <p className="muted">{t("tenants.availableHint")}</p>
        {resources.length === 0 ? (
          <div className="empty-state">{t("tenants.noResources")}</div>
        ) : (
          <div className="surface-body">
            {resources.map((resource) => {
              const grant = grants.find((item) =>
                item.resource_type === resource.type && item.resource_id === resource.id
              );
              const assignable = resource.type !== "provider" && !resource.owner_tenant_id;
              const ownedBySelected = resource.owner_tenant_id === tenant.id;
              return (
                <article className="agent-registry-row" key={`${resource.type}:${resource.id}`}>
                  <div className="agent-list-main">
                    <input
                      className="tenant-resource-checkbox"
                      type="checkbox"
                      checked={selectedResourceIDs.includes(resource.id)}
                      aria-label={t("tenants.selectResourceNamed", { resource: resource.name })}
                      onChange={() => toggleResource(resource.id)}
                    />
                    <span>
                      <strong>{resource.name}</strong>
                      <small>
                        {t(`tenants.resource.${resource.type}`)} · {resource.id}
                      </small>
                    </span>
                  </div>
                  <div className="agent-list-meta">
                    {grant && (
                      <span className="source-badge console">
                        {t("tenants.grantedLevel", { level: t(`tenants.level.${grant.level}`) })}
                      </span>
                    )}
                    {ownedBySelected && (
                      <span className="source-badge manual">{t("tenants.ownedByTenant")}</span>
                    )}
                    {resource.owner_tenant_name && !ownedBySelected && (
                      <span className="pill">{resource.owner_tenant_name}</span>
                    )}
                  </div>
                  <div className="table-actions">
                    {grant ? (
                      <button className="ghost-action danger" onClick={() => onRevoke(grant)}>
                        {t("tenants.revoke")}
                      </button>
                    ) : (
                      <button
                        className="ghost-action"
                        disabled={busy}
                        onClick={() => void grantSelected([resource])}
                      >
                        {t("tenants.grant")}
                      </button>
                    )}
                    {assignable && (
                      <button
                        className="ghost-action"
                        disabled={busy}
                        onClick={() => {
                          if (resource.type !== "provider") onAssign(resource.type, resource.id);
                        }}
                      >
                        {t("tenants.assignTo", { tenant: tenant.name })}
                      </button>
                    )}
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}
