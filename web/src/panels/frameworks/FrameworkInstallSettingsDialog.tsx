import { useEffect, useState } from "react";
import { Settings2, TriangleAlert, X } from "lucide-react";
import { api } from "../../api";
import type { FrameworkInstallDefaults, FrameworkInstallOverride } from "../../api";
import { useI18n } from "../../i18n";

// FrameworkInstallSettingsDialog edits this machine's npm registries and
// custom install/update commands. The server validates and stores them, and
// refuses changes relayed from another machine.
export function FrameworkInstallSettingsDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const { t } = useI18n();
  const [defaults, setDefaults] = useState<FrameworkInstallDefaults[] | null>(null);
  const [defaultRegistry, setDefaultRegistry] = useState("");
  const [overrides, setOverrides] = useState<Record<string, FrameworkInstallOverride>>({});
  // Sections start expanded for frameworks that were already customised;
  // editing a field never collapses its section.
  const [initiallyOpen, setInitiallyOpen] = useState<Set<string>>(new Set());
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let active = true;
    api.frameworkInstallSettings().then((response) => {
      if (!active) return;
      setDefaults(response.frameworks);
      setDefaultRegistry(response.settings.default_registry ?? "");
      setOverrides(response.settings.frameworks ?? {});
      setInitiallyOpen(new Set(Object.keys(response.settings.frameworks ?? {})));
    }).catch((cause) => {
      if (active) setError(cause instanceof Error ? cause.message : String(cause));
    });
    return () => { active = false; };
  }, []);

  function setOverride(kind: string, field: keyof FrameworkInstallOverride, value: string) {
    setOverrides((current) => ({ ...current, [kind]: { ...current[kind], [field]: value } }));
  }

  async function save() {
    setSaving(true);
    setError("");
    try {
      await api.saveFrameworkInstallSettings({ default_registry: defaultRegistry, frameworks: overrides });
      onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.close")} disabled={saving} onClick={onClose} type="button" />
      <section className="surface meeting-dialog framework-install-dialog framework-settings-dialog" role="dialog" aria-modal="true" aria-labelledby="framework-settings-title">
        <button className="meeting-dialog-close" aria-label={t("common.close")} disabled={saving} onClick={onClose} type="button"><X size={17} /></button>
        <div className="framework-install-dialog-copy">
          <span className="meeting-dialog-icon"><Settings2 size={22} /></span>
          <div>
            <h2 id="framework-settings-title">{t("frameworks.installSettings")}</h2>
            <p>{t("frameworks.installSettingsHint")}</p>
          </div>
        </div>
        {error && <div className="session-notice error" role="alert">{error}</div>}
        {!defaults && !error && <div className="empty-state">{t("common.loading")}</div>}
        {defaults && (
          <>
            <label className="field">
              <span>{t("frameworks.defaultRegistry")}</span>
              <input
                className="mono"
                onChange={(event) => setDefaultRegistry(event.target.value)}
                placeholder={t("frameworks.registryPlaceholder")}
                spellCheck={false}
                value={defaultRegistry}
              />
              <small>{t("frameworks.defaultRegistryHint")}</small>
            </label>
            <div className="framework-settings-list">
              {defaults.map((entry) => {
                const override = overrides[entry.kind] ?? {};
                const customized = Boolean(override.registry?.trim() || override.install_command?.trim() || override.update_command?.trim());
                return (
                  <details className="framework-settings-item" key={entry.kind} open={initiallyOpen.has(entry.kind) || undefined}>
                    <summary>
                      <strong>{entry.display}</strong>
                      {customized && <span className="pill on">{t("frameworks.customized")}</span>}
                    </summary>
                    <div className="framework-settings-fields">
                      {entry.registry_applies && (
                        <label className="field">
                          <span>{t("frameworks.frameworkRegistry")}</span>
                          <input
                            className="mono"
                            onChange={(event) => setOverride(entry.kind, "registry", event.target.value)}
                            placeholder={defaultRegistry.trim() || t("frameworks.registryPlaceholder")}
                            spellCheck={false}
                            value={override.registry ?? ""}
                          />
                          <small>{t("frameworks.frameworkRegistryHint")}</small>
                        </label>
                      )}
                      {entry.install_command && (
                        <label className="field">
                          <span>{t("frameworks.customInstallCommand")}</span>
                          <input
                            className="mono"
                            onChange={(event) => setOverride(entry.kind, "install_command", event.target.value)}
                            placeholder={entry.install_command}
                            spellCheck={false}
                            value={override.install_command ?? ""}
                          />
                        </label>
                      )}
                      {entry.update_command && (
                        <label className="field">
                          <span>{t("frameworks.customUpdateCommand")}</span>
                          <input
                            className="mono"
                            onChange={(event) => setOverride(entry.kind, "update_command", event.target.value)}
                            placeholder={entry.update_command}
                            spellCheck={false}
                            value={override.update_command ?? ""}
                          />
                        </label>
                      )}
                      <small className="framework-settings-note">{t("frameworks.customCommandHint")}</small>
                    </div>
                  </details>
                );
              })}
            </div>
          </>
        )}
        <div className="meeting-dialog-actions">
          <button className="ghost-action" disabled={saving} onClick={onClose} type="button">{t("common.cancel")}</button>
          <button className="action" disabled={saving || !defaults} onClick={() => void save()} type="button">{t("common.save")}</button>
        </div>
      </section>
    </div>
  );
}

export interface CustomCommandEntry {
  key: string;
  name: string;
  action: "install" | "update";
  command: string;
}

// CustomCommandConfirmDialog shows the exact custom commands about to run.
// The server only runs a custom command when the request echoes this text.
export function CustomCommandConfirmDialog({ entries, onCancel, onConfirm }: {
  entries: CustomCommandEntry[];
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="meeting-dialog-layer" role="presentation">
      <button className="meeting-dialog-backdrop internal-dialog-backdrop" aria-label={t("common.cancel")} onClick={onCancel} type="button" />
      <section className="surface meeting-dialog internal-only-dialog" role="alertdialog" aria-modal="true" aria-labelledby="custom-command-title">
        <div className="meeting-dialog-icon"><TriangleAlert size={22} /></div>
        <div className="meeting-dialog-heading">
          <h2 id="custom-command-title">{t("frameworks.customConfirmTitle")}</h2>
          <p>{t("frameworks.customConfirmBody")}</p>
        </div>
        <ul className="internal-component-list framework-custom-command-list">
          {entries.map((entry) => (
            <li key={`${entry.key}:${entry.action}`}>
              <span>{entry.name} · {entry.action === "install" ? t("frameworks.install") : t("tools.update")}</span>
              <code>{entry.command}</code>
            </li>
          ))}
        </ul>
        <div className="meeting-dialog-actions">
          <button className="ghost-action" onClick={onCancel} type="button">{t("common.cancel")}</button>
          <button className="action" onClick={onConfirm} type="button">{t("frameworks.customConfirmAction")}</button>
        </div>
      </section>
    </div>
  );
}
