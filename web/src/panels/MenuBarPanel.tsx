import { useEffect, useState } from "react";
import { PanelTop } from "lucide-react";
import { api, MenubarSettings } from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

const ICON_THEMES = ["flame", "drop", "custom"] as const;
const ICON_METRICS = ["cost", "tokens", "messages"] as const;
const BREAKDOWNS = ["model", "runtime", "date"] as const;

const STAGE_PRESETS: Record<string, string[]> = {
  flame: ["💤", "✨", "🔥", "🔥🔥", "🔥🔥🔥"],
  drop: ["💧", "💦", "⛲", "🌊", "☔"],
};

function stagesFor(settings: MenubarSettings): string[] {
  if (settings.icon_theme === "custom" && settings.icon_stages?.length === 5) {
    return settings.icon_stages;
  }
  return STAGE_PRESETS[settings.icon_theme] ?? STAGE_PRESETS.flame;
}

export function MenuBarPanel() {
  const { t } = useI18n();
  const { data, error, loading } = useAsync(() => api.menubarSettings(), []);
  const [settings, setSettings] = useState<MenubarSettings | null>(null);
  const [status, setStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");

  useEffect(() => {
    if (data) setSettings(data);
  }, [data]);

  if (loading && !settings) {
    return <div className="surface surface-body muted">{t("common.loading")}</div>;
  }
  if (error && !settings) {
    return <div className="surface surface-body error">{error}</div>;
  }
  if (!settings) return null;

  const update = (patch: Partial<MenubarSettings>) => {
    setSettings((prev) => (prev ? { ...prev, ...patch } : prev));
    setStatus("idle");
  };

  const toggleBreakdown = (dim: string) => {
    const has = settings.breakdowns.includes(dim);
    const next = has
      ? settings.breakdowns.filter((d) => d !== dim)
      : [...settings.breakdowns, dim];
    update({ breakdowns: next });
  };

  const save = async () => {
    if (!settings) return;
    setStatus("saving");
    try {
      const saved = await api.saveMenubarSettings(settings);
      setSettings(saved);
      setStatus("saved");
    } catch {
      setStatus("error");
    }
  };

  const stages = stagesFor(settings);
  const previewMetric =
    settings.icon_metric === "tokens"
      ? "1.2B"
      : settings.icon_metric === "messages"
      ? "128"
      : "$18,019.74";
  const previewParts: string[] = [];
  if (settings.show_status_icon) previewParts.push(stages[stages.length - 1]);
  if (settings.show_cost) previewParts.push("$18.0K");
  if (settings.show_tokens) previewParts.push("1.20B/90.71M");
  if (settings.show_messages) previewParts.push("128");

  return (
    <div className="page-stack">
      <section className="surface">
        <div className="surface-header">
          <div>
            <h2>{t("menubar.title")}</h2>
            <p className="subtle-copy">{t("menubar.subtitle")}</p>
          </div>
          <button className="action" onClick={save} disabled={status === "saving"}>
            <PanelTop size={14} />
            {status === "saving" ? "…" : t("menubar.save")}
          </button>
        </div>
        <div className="surface-body">
          <div className="menubar-preview">
            <span className="menubar-preview-chip">
              <img className="menubar-preview-logo" src="/agentmux-logo.png" alt={t("menubar.logo")} />
              {previewParts.length > 0 && <span>{previewParts.join("  ")}</span>}
            </span>
            {status === "saved" && <span className="muted">{t("menubar.saved")}</span>}
            {status === "error" && <span className="error">{t("menubar.saveError")}</span>}
          </div>
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("menubar.icon")}</h2>
        </div>
        <div className="surface-body">
          <div className="field-grid">
            <label className="field">
              <span>{t("menubar.iconTheme")}</span>
              <select
                value={settings.icon_theme}
                onChange={(e) => update({ icon_theme: e.target.value })}
              >
                {ICON_THEMES.map((theme) => (
                  <option key={theme} value={theme}>
                    {t(`menubar.theme.${theme}`)}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>{t("menubar.iconMetric")}</span>
              <select
                value={settings.icon_metric}
                onChange={(e) => update({ icon_metric: e.target.value })}
              >
                {ICON_METRICS.map((metric) => (
                  <option key={metric} value={metric}>
                    {t(`menubar.metric.${metric}`)}
                  </option>
                ))}
              </select>
            </label>
            {settings.icon_theme === "custom" && (
              <label className="field">
                <span>{t("menubar.customStages")}</span>
                <input
                  type="text"
                  value={(settings.icon_stages ?? []).join(" ")}
                  placeholder="💤 ✨ 🔥 🔥🔥 🔥🔥🔥"
                  onChange={(e) =>
                    update({
                      icon_stages: e.target.value.trim().split(/\s+/).filter(Boolean),
                    })
                  }
                />
              </label>
            )}
          </div>
          <div className="menubar-stages" aria-label={`${t("menubar.preview")} · ${previewMetric}`}>
            {stages.map((glyph, i) => (
              <span key={i} className="menubar-stage" title={`${i + 1}`}>
                {glyph}
              </span>
            ))}
          </div>
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("menubar.display")}</h2>
        </div>
        <div className="surface-body">
          <label className="switch-row">
            <span>{t("menubar.showStatusIcon")}</span>
            <input
              type="checkbox"
              checked={settings.show_status_icon}
              onChange={(e) => update({ show_status_icon: e.target.checked })}
            />
          </label>
          <label className="switch-row">
            <span>{t("menubar.showMessages")}</span>
            <input
              type="checkbox"
              checked={settings.show_messages}
              onChange={(e) => update({ show_messages: e.target.checked })}
            />
          </label>
          <label className="switch-row">
            <span>{t("menubar.showTokens")}</span>
            <input
              type="checkbox"
              checked={settings.show_tokens}
              onChange={(e) => update({ show_tokens: e.target.checked })}
            />
          </label>
          <label className="switch-row">
            <span>{t("menubar.showCost")}</span>
            <input
              type="checkbox"
              checked={settings.show_cost}
              onChange={(e) => update({ show_cost: e.target.checked })}
            />
          </label>
          <label className="switch-row">
            <span>{t("menubar.showCNY")}</span>
            <input
              type="checkbox"
              checked={settings.show_cny}
              onChange={(e) => update({ show_cny: e.target.checked })}
            />
          </label>
          <div className="field-grid">
            <label className="field">
              <span>{t("menubar.cnyRate")}</span>
              <input
                type="number"
                step="0.01"
                min="0"
                value={settings.cny_rate}
                onChange={(e) => update({ cny_rate: Number(e.target.value) })}
              />
            </label>
          </div>
        </div>
      </section>

      <section className="surface">
        <div className="surface-header">
          <h2>{t("menubar.breakdowns")}</h2>
        </div>
        <div className="surface-body">
          {BREAKDOWNS.map((dim) => (
            <label key={dim} className="switch-row">
              <span>{t(`menubar.breakdown.${dim}`)}</span>
              <input
                type="checkbox"
                checked={settings.breakdowns.includes(dim)}
                onChange={() => toggleBreakdown(dim)}
              />
            </label>
          ))}
          <div className="field-grid">
            <label className="field">
              <span>{t("menubar.topN")}</span>
              <input
                type="number"
                step="1"
                min="1"
                max="10"
                value={settings.top_n}
                onChange={(e) => update({ top_n: Number(e.target.value) })}
              />
            </label>
          </div>
        </div>
      </section>
    </div>
  );
}
