import { Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { getLaunchAtLogin, isDesktopApp, setLaunchAtLogin, type LaunchAtLoginStatus } from "../api";
import { SlidingSelection } from "../components/SlidingSelection";
import { KeepAwakeControl } from "../KeepAwakeControl";
import { useI18n, type Language, type ThemeMode } from "../i18n";
import { NotificationSettings } from "../pwa/NotificationSettings";
import { AboutSettings, MenuBarPanel } from "./MenuBarPanel";
import { SETTINGS_SECTIONS, type SettingsSection } from "../settingsNavigation";

const LANGUAGE_OPTIONS = [
  { id: "zh", labelKey: "lang.zh" },
  { id: "en", labelKey: "lang.en" },
] as const;

const THEME_OPTIONS = [
  { id: "system", labelKey: "theme.system", icon: Monitor },
  { id: "light", labelKey: "theme.light", icon: Sun },
  { id: "dark", labelKey: "theme.dark", icon: Moon },
] as const;

type PreferencesProps = {
  admin: boolean;
  language: Language;
  setLanguage: (language: Language) => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
};

export function SettingsPanel({
  section,
  onSectionChange,
  ...preferences
}: PreferencesProps & {
  section: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
}) {
  const { t } = useI18n();
  const [visited, setVisited] = useState(() => new Set([section]));
  useEffect(() => {
    setVisited((current) => current.has(section) ? current : new Set([...current, section]));
  }, [section]);
  const sections = preferences.admin ? SETTINGS_SECTIONS : SETTINGS_SECTIONS.slice(0, 1);

  return (
    <div className="settings-page">
      {preferences.admin && <SlidingSelection
        className="segmented settings-mobile-tabs"
        selected={section}
        role="group"
        aria-label={t("app.settings")}
      >
        {sections.map((item) => (
          <button key={item.id} type="button" className={item.id === section ? "active" : ""}
            aria-pressed={item.id === section} onClick={() => onSectionChange(item.id)}>
            {t(item.labelKey)}
          </button>
        ))}
      </SlidingSelection>}
      {sections.map((item) => {
        const active = section === item.id;
        if (!active && !visited.has(item.id)) return null;
        return (
          <div key={item.id} className="settings-section" hidden={!active} aria-label={t(item.labelKey)}>
            {item.id === "general" && <GeneralSettings {...preferences} />}
            {item.id === "menubar" && <MenuBarPanel />}
            {item.id === "notifications" && <NotificationSettings />}
            {item.id === "about" && <AboutSettings />}
          </div>
        );
      })}
    </div>
  );
}

function GeneralSettings({ admin, language, setLanguage, themeMode, setThemeMode }: PreferencesProps) {
  const { t } = useI18n();
  const desktop = isDesktopApp();
  const [launchAtLogin, setLaunchStatus] = useState<LaunchAtLoginStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!desktop || !admin) return;
    let current = true;
    getLaunchAtLogin().then(
      (status) => { if (current) setLaunchStatus(status); },
      (failure) => { if (current) setError(String(failure)); },
    );
    return () => { current = false; };
  }, [admin, desktop]);

  async function updateLaunchAtLogin(enabled: boolean) {
    setBusy(true);
    setError("");
    try {
      setLaunchStatus(await setLaunchAtLogin(enabled));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="settings-stack">
      <section className="settings-group settings-appearance" aria-labelledby="settings-appearance">
        <header className="settings-heading"><h2 id="settings-appearance">{t("settings.appearanceAndLanguage")}</h2></header>
        <div className="settings-row">
          <strong id="settings-theme-label">{t("settings.appearance")}</strong>
          <SlidingSelection className="segmented" selected={themeMode} role="group" aria-labelledby="settings-theme-label">
            {THEME_OPTIONS.map(({ id, icon: Icon, labelKey }) => (
              <button key={id} type="button" className={themeMode === id ? "active" : ""} aria-pressed={themeMode === id}
                onClick={() => setThemeMode(id)}>
                <Icon size={15} aria-hidden="true" /> {t(labelKey)}
              </button>
            ))}
          </SlidingSelection>
        </div>
        <div className="settings-row">
          <strong id="settings-language-label">{t("settings.language")}</strong>
          <SlidingSelection className="segmented" selected={language} role="group" aria-labelledby="settings-language-label">
            {LANGUAGE_OPTIONS.map(({ id, labelKey }) => (
              <button key={id} type="button" className={language === id ? "active" : ""} aria-pressed={language === id}
                onClick={() => setLanguage(id)}>
                {t(labelKey)}
              </button>
            ))}
          </SlidingSelection>
        </div>
      </section>
      {desktop && admin && <section className="settings-group" aria-labelledby="settings-device">
        <header className="settings-heading"><h2 id="settings-device">{t("settings.device")}</h2></header>
        {launchAtLogin?.supported && (
          <label className="settings-row switch-row">
            <span className="settings-row-copy">
              <strong>{t("app.launchAtLogin")}</strong>
              <small>{t("app.launchAtLoginHint")}</small>
            </span>
            <input type="checkbox" role="switch" checked={launchAtLogin.enabled} disabled={busy}
              onChange={(event) => void updateLaunchAtLogin(event.target.checked)} />
          </label>
        )}
        <KeepAwakeControl onError={setError} />
        {error && <p className="preference-error" role="alert">{error}</p>}
      </section>}
    </div>
  );
}
