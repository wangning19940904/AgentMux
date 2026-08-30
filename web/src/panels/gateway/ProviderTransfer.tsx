import { ArrowRightLeft, CheckCircle2, Download, KeyRound, RefreshCw, ShieldAlert, X } from "lucide-react";
import { useMemo, useState } from "react";
import {
  api,
  type FleetSyncApplyResult,
  type FleetSyncInspection,
  type FleetSyncPreview,
  type MachineTarget,
  type Provider,
} from "../../api";
import { useI18n } from "../../i18n";
import { ProviderMark } from "./ProviderMark";

type ProviderTransferMode = "sync" | "import";

type ProviderTransferFormProps = {
  mode: ProviderTransferMode;
  providers: Provider[];
  targets: MachineTarget[];
  fixedProvider?: Provider;
  fixedSourceTargetID?: string;
  defaultDestinationID: string;
  loading?: boolean;
  loadError?: string;
  onApplied: () => void;
};

type TransferDestination = {
  target: MachineTarget;
  inspection: FleetSyncInspection;
  error?: string;
};

function onlineTarget(target: MachineTarget) {
  return target.trusted && target.online;
}

export function ProviderTransferForm({
  mode,
  providers,
  targets,
  fixedProvider,
  fixedSourceTargetID,
  defaultDestinationID,
  loading = false,
  loadError = "",
  onApplied,
}: ProviderTransferFormProps) {
  const { t } = useI18n();
  const targetName = (target?: MachineTarget) => target?.id === "local" ? t("remote.localMachine") : target?.name || "";
  const [selectedSourceID, setSelectedSourceID] = useState("");
  const [selectedProviderID, setSelectedProviderID] = useState("");
  const [selectedDestinationIDs, setSelectedDestinationIDs] = useState<string[]>([]);
  const [includeCredentials, setIncludeCredentials] = useState(false);
  const [preview, setPreview] = useState<FleetSyncPreview | null>(null);
  const [result, setResult] = useState<FleetSyncApplyResult | null>(null);
  const [busy, setBusy] = useState<"preview" | "apply" | "">("");
  const [error, setError] = useState("");

  const defaultDestination = targets.some((target) => target.id === defaultDestinationID && onlineTarget(target))
    ? defaultDestinationID
    : targets.find(onlineTarget)?.id || "";
  const importDestinationID = selectedDestinationIDs[0] || defaultDestination;
  const sourceCandidates = useMemo(
    () => targets.filter((target) => onlineTarget(target) && target.id !== importDestinationID && providers.some((provider) => provider.target_id === target.id)),
    [importDestinationID, providers, targets],
  );
  const sourceTargetID = mode === "sync"
    ? fixedSourceTargetID || fixedProvider?.target_id || "local"
    : sourceCandidates.some((target) => target.id === selectedSourceID)
      ? selectedSourceID
      : sourceCandidates[0]?.id || "";
  const sourceProviders = mode === "import"
    ? providers.filter((provider) => provider.target_id === sourceTargetID)
    : fixedProvider ? [fixedProvider] : [];
  const provider = mode === "sync"
    ? fixedProvider
    : sourceProviders.find((item) => item.id === selectedProviderID) || sourceProviders[0];
  const destinationCandidates = targets.filter((target) => target.id !== sourceTargetID);
  const destinationIDs = mode === "import"
    ? importDestinationID && importDestinationID !== sourceTargetID ? [importDestinationID] : []
    : selectedDestinationIDs.filter((id) => destinationCandidates.some((target) => target.id === id && onlineTarget(target)));

  function resetPlan() {
    setPreview(null);
    setResult(null);
    setError("");
  }

  function toggleDestination(id: string) {
    setSelectedDestinationIDs((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id]);
    resetPlan();
  }

  async function createPreview() {
    if (!provider || !sourceTargetID || destinationIDs.length === 0) return;
    setBusy("preview");
    setError("");
    setResult(null);
    try {
      setPreview(await api.previewFleetSync({
        source_target_id: sourceTargetID,
        destination_target_ids: destinationIDs,
        categories: ["providers"],
        provider_ids: [provider.id],
        include_credentials: includeCredentials,
        preserve_activation: false,
      }));
    } catch (reason) {
      setPreview(null);
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy("");
    }
  }

  async function applyTransfer() {
    if (!preview) return;
    setBusy("apply");
    setError("");
    try {
      const applied = await api.applyFleetSync(preview.plan_id);
      setResult(applied);
      setPreview(null);
      onApplied();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : String(reason));
    } finally {
      setBusy("");
    }
  }

  const displayedDestinations: TransferDestination[] = result?.targets ?? preview?.destinations ?? [];

  return (
    <div className="provider-transfer-form">
      <div className="provider-transfer-intro">
        <span className="provider-transfer-icon">
          {mode === "import" ? <Download size={18} /> : <ArrowRightLeft size={18} />}
        </span>
        <span>
          <strong>{mode === "import" ? t("gateway.importProvider") : t("gateway.syncProvider")}</strong>
          <small>{mode === "import" ? t("gateway.importProviderHint") : t("gateway.syncProviderHint")}</small>
        </span>
      </div>

      {loadError && <div className="session-notice error"><ShieldAlert size={14} />{loadError}</div>}

      {mode === "import" && (
        <div className="provider-transfer-grid">
          <label className="field">
            <span>{t("gateway.importDestination")}</span>
            <select
              value={importDestinationID}
              disabled={loading || targets.length === 0}
              onChange={(event) => {
                setSelectedDestinationIDs(event.target.value ? [event.target.value] : []);
                setSelectedSourceID("");
                setSelectedProviderID("");
                resetPlan();
              }}
            >
              {targets.map((target) => (
                <option key={target.id} value={target.id} disabled={!onlineTarget(target)}>
                  {targetName(target)}{!target.online ? ` · ${t("remote.offlineShort")}` : ""}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>{t("gateway.importSource")}</span>
            <select
              value={sourceTargetID}
              disabled={loading || sourceCandidates.length === 0}
              onChange={(event) => {
                setSelectedSourceID(event.target.value);
                setSelectedProviderID("");
                resetPlan();
              }}
            >
              {sourceCandidates.map((target) => <option key={target.id} value={target.id}>{targetName(target)}</option>)}
              {!loading && sourceCandidates.length === 0 && <option value="">{t("gateway.noImportSources")}</option>}
            </select>
          </label>
          <label className="field wide">
            <span>{t("gateway.importProviderSelect")}</span>
            <select
              value={provider?.id || ""}
              disabled={loading || sourceProviders.length === 0}
              onChange={(event) => {
                setSelectedProviderID(event.target.value);
                resetPlan();
              }}
            >
              {sourceProviders.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.id}</option>)}
              {!loading && sourceProviders.length === 0 && <option value="">{t("gateway.noImportProviders")}</option>}
            </select>
          </label>
        </div>
      )}

      {mode === "sync" && provider && (
        <div className="provider-transfer-summary">
          <ProviderMark id={provider.id} name={provider.name} />
          <span>
            <strong>{provider.name}</strong>
            <small>{provider.id} · {targetName(targets.find((target) => target.id === sourceTargetID)) || sourceTargetID}</small>
          </span>
        </div>
      )}

      {mode === "sync" && (
        <fieldset className="provider-transfer-targets">
          <legend>{t("gateway.syncDestinations")}</legend>
          {destinationCandidates.map((target) => (
            <label className="check-row" key={target.id}>
              <input
                type="checkbox"
                checked={destinationIDs.includes(target.id)}
                disabled={!onlineTarget(target)}
                onChange={() => toggleDestination(target.id)}
              />
              <span>{targetName(target)}{!target.online ? ` · ${t("remote.offlineShort")}` : ""}</span>
            </label>
          ))}
          {!loading && destinationCandidates.length === 0 && <small>{t("gateway.noSyncDestinations")}</small>}
        </fieldset>
      )}

      <label className="switch-row provider-transfer-credential">
        <span>
          <strong><KeyRound size={14} />{t("gateway.includeProviderKey")}</strong>
          <small>{t("gateway.includeProviderKeyHint")}</small>
        </span>
        <input
          type="checkbox"
          checked={includeCredentials}
          onChange={(event) => {
            setIncludeCredentials(event.target.checked);
            resetPlan();
          }}
        />
      </label>

      {error && <div className="session-notice error"><ShieldAlert size={14} />{error}</div>}

      {displayedDestinations.length > 0 && (
        <div className="provider-transfer-results">
          {displayedDestinations.map((destination) => {
            const item = destination.inspection.resources.find((resource) => resource.type === "provider" && resource.key === provider?.id);
            return (
              <div className="provider-transfer-result" key={destination.target.id}>
                <span>
                  {result ? <CheckCircle2 size={15} /> : <RefreshCw size={15} />}
                  <strong>{targetName(destination.target)}</strong>
                </span>
                {destination.error ? (
                  <small className="error">{destination.error}</small>
                ) : item ? (
                  <small>
                    {t(`fleetSync.action.${item.action}`)}
                    {item.credentials_missing ? ` · ${t("gateway.providerKeyOmitted")}` : ""}
                    {item.reason ? ` · ${item.reason}` : ""}
                  </small>
                ) : null}
              </div>
            );
          })}
        </div>
      )}

      <div className="form-actions">
        <button
          className="ghost-action"
          type="button"
          disabled={loading || busy !== "" || !provider || destinationIDs.length === 0}
          onClick={() => void createPreview()}
        >
          <RefreshCw size={15} className={busy === "preview" ? "spin" : ""} />
          {t("gateway.previewProviderTransfer")}
        </button>
        <button className="action" type="button" disabled={!preview || busy !== ""} onClick={() => void applyTransfer()}>
          {mode === "import" ? <Download size={15} /> : <ArrowRightLeft size={15} />}
          {busy === "apply"
            ? t("gateway.providerTransferApplying")
            : mode === "import" ? t("gateway.confirmProviderImport") : t("gateway.confirmProviderSync")}
        </button>
      </div>
    </div>
  );
}

export function ProviderSyncDialog({
  provider,
  sourceTargetID,
  providers,
  targets,
  loading,
  loadError,
  onApplied,
  onClose,
}: {
  provider: Provider;
  sourceTargetID: string;
  providers: Provider[];
  targets: MachineTarget[];
  loading?: boolean;
  loadError?: string;
  onApplied: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="provider-drawer-layer">
      <button className="provider-drawer-backdrop" type="button" aria-label={t("common.close")} onClick={onClose} />
      <aside className="provider-drawer provider-transfer-drawer" role="dialog" aria-modal="true" aria-labelledby="provider-sync-title">
        <div className="provider-builder-head">
          <div className="provider-form-title">
            <ProviderMark id={provider.id} name={provider.name} size="large" />
            <div>
              <h2 id="provider-sync-title">{t("gateway.syncProviderTitle", { name: provider.name })}</h2>
              <span className="muted">{t("gateway.syncProviderSubtitle")}</span>
            </div>
          </div>
          <button className="ghost-action icon-action" type="button" onClick={onClose} title={t("common.close")}>
            <X size={15} />
          </button>
        </div>
        <div className="surface-body provider-builder-body">
          <ProviderTransferForm
            mode="sync"
            providers={providers}
            targets={targets}
            fixedProvider={provider}
            fixedSourceTargetID={sourceTargetID}
            defaultDestinationID=""
            loading={loading}
            loadError={loadError}
            onApplied={onApplied}
          />
        </div>
      </aside>
    </div>
  );
}
