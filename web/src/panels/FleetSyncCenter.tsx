import { ArrowRightLeft, CheckCircle2, RefreshCw, ShieldAlert } from "lucide-react";
import { useMemo, useState } from "react";
import { api, type FleetSyncApplyResult, type FleetSyncPathMapping, type FleetSyncPreview, type RemoteHost } from "../api";
import { useI18n } from "../i18n";

const CATEGORIES = ["agents", "providers", "channels", "triggers", "mcp", "skills", "guard", "memory"] as const;

export function FleetSyncCenter({ hosts }: { hosts: RemoteHost[] }) {
  const { t } = useI18n();
  const targets = useMemo(() => [
    { id: "local", name: t("remote.localMachine") },
    ...hosts.filter((host) => host.trusted).map((host) => ({ id: host.id, name: host.name })),
  ], [hosts, t]);
  const [source, setSource] = useState("local");
  const [destinations, setDestinations] = useState<string[]>([]);
  const [categories, setCategories] = useState<string[]>([...CATEGORIES]);
  const [includeCredentials, setIncludeCredentials] = useState(false);
  const [preserveActivation, setPreserveActivation] = useState(false);
  const [mappings, setMappings] = useState<Record<string, FleetSyncPathMapping[]>>({});
  const [preview, setPreview] = useState<FleetSyncPreview | null>(null);
  const [result, setResult] = useState<FleetSyncApplyResult | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | "">("");
  const [error, setError] = useState("");
  const availableDestinations = targets.filter((target) => target.id !== source);
  const selectedDestinations = destinations.filter((id) => id !== source && availableDestinations.some((target) => target.id === id));

  function toggle(list: string[], value: string) {
    return list.includes(value) ? list.filter((item) => item !== value) : [...list, value];
  }

  async function loadPreview() {
    if (!selectedDestinations.length || !categories.length) return;
    setBusy("preview"); setError(""); setResult(null);
    try {
      setPreview(await api.previewFleetSync({
        source_target_id: source, destination_target_ids: selectedDestinations, categories,
        include_credentials: includeCredentials, preserve_activation: preserveActivation, path_mappings: mappings,
      }));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally { setBusy(""); }
  }

  async function apply() {
    if (!preview || !window.confirm(t("fleetSync.applyConfirm"))) return;
    setBusy("apply"); setError("");
    try { setResult(await api.applyFleetSync(preview.plan_id)); }
    catch (reason) { setError(reason instanceof Error ? reason.message : String(reason)); }
    finally { setBusy(""); }
  }

  function updateMapping(targetID: string, key: "from" | "to", value: string) {
    const current = mappings[targetID]?.[0] ?? { from: "", to: "" };
    setMappings((items) => ({ ...items, [targetID]: [{ ...current, [key]: value }] }));
    setPreview(null);
  }

  return (
    <section className="surface fleet-sync-center">
      <div className="surface-header">
        <div><h2><ArrowRightLeft size={18} /> {t("fleetSync.title")}</h2><p className="subtle-copy">{t("fleetSync.subtitle")}</p></div>
        {preview && <span className="status-badge success"><CheckCircle2 size={13} />{t("fleetSync.previewReady")}</span>}
      </div>
      <div className="surface-body fleet-sync-body">
        <div className="fleet-sync-route">
          <label className="field"><span>{t("fleetSync.source")}</span><select value={source} onChange={(event) => { setSource(event.target.value); setDestinations((items) => items.filter((id) => id !== event.target.value)); setPreview(null); }}>{targets.map((target) => <option value={target.id} key={target.id}>{target.name}</option>)}</select></label>
          <div className="fleet-sync-destinations"><strong>{t("fleetSync.destinations")}</strong>{availableDestinations.map((target) => <label className="check-row" key={target.id}><input type="checkbox" checked={selectedDestinations.includes(target.id)} onChange={() => { setDestinations((items) => toggle(items, target.id)); setPreview(null); }} /><span>{target.name}</span></label>)}</div>
        </div>
        <div className="fleet-sync-categories"><strong>{t("fleetSync.resources")}</strong><div>{CATEGORIES.map((category) => <label className="check-row" key={category}><input type="checkbox" checked={categories.includes(category)} onChange={() => { setCategories((items) => toggle(items, category)); setPreview(null); }} /><span>{t(`fleetSync.category.${category}`)}</span></label>)}</div></div>
        <div className="fleet-sync-options">
          <label className="switch-row"><span><strong>{t("fleetSync.credentials")}</strong><small>{t("fleetSync.credentialsHint")}</small></span><input type="checkbox" checked={includeCredentials} onChange={(event) => { setIncludeCredentials(event.target.checked); setPreview(null); }} /></label>
          <label className="switch-row"><span><strong>{t("fleetSync.activation")}</strong><small>{t("fleetSync.activationHint")}</small></span><input type="checkbox" checked={preserveActivation} onChange={(event) => { setPreserveActivation(event.target.checked); setPreview(null); }} /></label>
        </div>
        {selectedDestinations.map((targetID) => {
          const target = targets.find((item) => item.id === targetID); const mapping = mappings[targetID]?.[0] ?? { from: "", to: "" };
          return <div className="fleet-sync-mapping" key={targetID}><strong>{target?.name} · {t("fleetSync.pathMapping")}</strong><input value={mapping.from} onChange={(event) => updateMapping(targetID, "from", event.target.value)} placeholder={t("fleetSync.sourcePrefix")} /><span>→</span><input value={mapping.to} onChange={(event) => updateMapping(targetID, "to", event.target.value)} placeholder={t("fleetSync.destinationPrefix")} /></div>;
        })}
        {error && <div className="session-notice error">{error}</div>}
        <div className="table-actions"><button className="ghost-action" disabled={busy !== "" || !selectedDestinations.length || !categories.length} onClick={() => void loadPreview()}><RefreshCw size={14} className={busy === "preview" ? "spin" : ""} />{t("fleetSync.preview")}</button><button className="action" disabled={!preview || busy !== ""} onClick={() => void apply()}><ArrowRightLeft size={14} />{busy === "apply" ? t("fleetSync.applying") : t("fleetSync.apply")}</button></div>
        {preview && <SyncResult destinations={preview.destinations} />}
        {result && <SyncResult destinations={result.targets} applied />}
      </div>
    </section>
  );
}

type DestinationResult = { target: { id: string; name: string }; inspection: { resources: Array<{ action: string; type: string; name: string; reason?: string; credentials_missing?: boolean }>; warnings?: string[] }; path_mappings?: FleetSyncPathMapping[]; error?: string };

function SyncResult({ destinations, applied = false }: { destinations: DestinationResult[]; applied?: boolean }) {
  const { t } = useI18n();
  return <div className="fleet-sync-results">{destinations.map((destination) => {
    const counts = destination.inspection.resources.reduce<Record<string, number>>((items, resource) => { items[resource.action] = (items[resource.action] ?? 0) + 1; return items; }, {});
    return <article key={destination.target.id} className="fleet-sync-result"><header><strong>{destination.target.name}</strong><span>{applied ? t("fleetSync.applyResult") : t("fleetSync.previewResult")}</span></header>{destination.error && <div className="session-notice error"><ShieldAlert size={14} />{destination.error}</div>}{(destination.path_mappings ?? []).map((mapping) => <code key={`${mapping.from}:${mapping.to}`}>{mapping.from} → {mapping.to}</code>)}<div className="fleet-sync-counts">{Object.entries(counts).map(([action, count]) => <span className={`status-badge ${action === "add" || action === "exists" ? "success" : "warning"}`} key={action}>{t(`fleetSync.action.${action}`)}: {count}</span>)}</div><details><summary>{t("fleetSync.details")}</summary><div className="fleet-sync-resource-list">{destination.inspection.resources.map((resource) => <div key={`${resource.type}:${resource.name}`}><span>{resource.type}</span><strong>{resource.name}</strong><small>{t(`fleetSync.action.${resource.action}`)}{resource.credentials_missing ? ` · ${t("fleetSync.missingCredentials")}` : ""}{resource.reason ? ` · ${resource.reason}` : ""}</small></div>)}</div></details>{(destination.inspection.warnings ?? []).map((warning) => <div className="session-notice" key={warning}>{warning}</div>)}</article>;
  })}</div>;
}
