import { List, Play, RefreshCw, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../../api";
import type { Framework, FrameworkRuntimeSettings, FrameworkSmokeResult } from "../../api";
import { useI18n } from "../../i18n";
import { FrameworkSmokeResultView } from "./FrameworkSmokeResult";

// FrameworkModelsDialog lists the models a framework offers with its current
// setup and tests any of them, including model IDs the framework does not
// list (such as a router's routing group).
export function FrameworkModelsDialog({ item, results, testing, onTest, onClose }: {
  item: Framework;
  // Latest results for this framework, keyed by model ("" is the default).
  results: Record<string, FrameworkSmokeResult>;
  testing: Record<string, boolean>;
  onTest: (model: string) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [settings, setSettings] = useState<FrameworkRuntimeSettings | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);
  const [typed, setTyped] = useState("");

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    api.frameworkRuntimeSettings(item.spec.kind, "", item.target_id)
      .then((value) => { if (active) setSettings(value); })
      .catch((err) => { if (active) setError(err instanceof Error ? err.message : String(err)); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [item.spec.kind, item.target_id, reload]);

  useEffect(() => {
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    document.addEventListener("keydown", escape);
    return () => document.removeEventListener("keydown", escape);
  }, [onClose]);

  const models = settings?.capabilities.models ?? [];
  const defaultModel = settings?.defaults.model ?? "";
  const typedModel = typed.trim();
  const listed = new Set(models.map((model) => model.value));
  // Typed models that were tested stay visible after the input is cleared.
  const extraModels = Object.keys(results).filter((model) => model && !listed.has(model));

  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.close")} onClick={onClose} type="button" />
      <section className="surface meeting-dialog framework-models-dialog" role="dialog" aria-modal="true" aria-labelledby="framework-models-title">
        <button className="meeting-dialog-close" aria-label={t("common.close")} onClick={onClose} type="button"><X size={17} /></button>
        <div className="framework-install-dialog-copy">
          <span className="meeting-dialog-icon"><List size={22} /></span>
          <div>
            <h2 id="framework-models-title">{t("frameworks.health.modelsTitle", { framework: item.spec.display })}</h2>
            <p>{t("frameworks.health.modelsHint")}</p>
          </div>
        </div>
        <div className="framework-models-list">
          <ModelRow label={t("frameworks.health.defaultModel")} detail={defaultModel} result={results[""]} testing={testing[""]} onTest={() => onTest("")} t={t} />
          {loading && <small className="muted"><RefreshCw className="spin" size={13} /> {t("frameworks.health.modelsLoading")}</small>}
          {!loading && error && <div className="session-notice error">{error}</div>}
          {!loading && !error && models.length === 0 && <small className="muted">{t("frameworks.health.modelsEmpty")}</small>}
          {models.map((model) => (
            <ModelRow key={model.value} label={model.value} detail={model.label && model.label !== model.value ? model.label : ""}
              result={results[model.value]} testing={testing[model.value]} onTest={() => onTest(model.value)} t={t} />
          ))}
          {extraModels.map((model) => (
            <ModelRow key={model} label={model} detail={t("frameworks.health.typedModel")}
              result={results[model]} testing={testing[model]} onTest={() => onTest(model)} t={t} />
          ))}
        </div>
        <form className="framework-models-typed" onSubmit={(event) => {
          event.preventDefault();
          if (typedModel) onTest(typedModel);
        }}>
          <input aria-label={t("frameworks.health.typedModel")} placeholder={t("frameworks.health.typedModelPlaceholder")} value={typed}
            onChange={(event) => setTyped(event.target.value)} />
          <button className="ghost-action" disabled={!typedModel || testing[typedModel]} type="submit">
            <Play size={14} />{t("frameworks.health.test")}
          </button>
          <button className="ghost-action icon-action" disabled={loading} onClick={() => setReload((value) => value + 1)}
            aria-label={t("agents.refreshRuntimeSettings")} title={t("agents.refreshRuntimeSettings")} type="button">
            <RefreshCw size={15} className={loading ? "spin" : ""} />
          </button>
        </form>
      </section>
    </div>
  );
}

function ModelRow({ label, detail, result, testing, onTest, t }: {
  label: string;
  detail: string;
  result?: FrameworkSmokeResult;
  testing?: boolean;
  onTest: () => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}) {
  return (
    <div className="framework-models-row">
      <span className="framework-models-name">
        <strong className="mono" title={label}>{label}</strong>
        {detail && <small>{detail}</small>}
      </span>
      <button className="ghost-action" disabled={testing} onClick={onTest} title={t("frameworks.health.testHint")} type="button">
        <Play size={14} />{t("frameworks.health.test")}
      </button>
      {(testing || result) && <FrameworkSmokeResultView result={result} busy={testing} />}
    </div>
  );
}
