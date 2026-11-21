import { Download } from "lucide-react";
import { useState } from "react";
import { fetchDiagnostics, formatFileSize, saveBlob, type DiagnosticsFile } from "../api";
import { useI18n } from "../i18n";

// Settings › About › Diagnostics: download the redacted support zip that
// `amux diagnose` writes, and list what is in it.
export function DiagnosticsSettings() {
  const { t } = useI18n();
  const [includeContent, setIncludeContent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState<{ name: string; files: DiagnosticsFile[] } | null>(null);

  async function download() {
    setBusy(true);
    setError("");
    setSaved(null);
    try {
      const result = await fetchDiagnostics(includeContent);
      saveBlob(result.blob, result.filename);
      setSaved({ name: result.filename, files: result.files });
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="settings-group" aria-labelledby="settings-diagnostics">
      <div className="settings-heading">
        <div>
          <h2 id="settings-diagnostics">{t("settings.diagnostics")}</h2>
          <p className="subtle-copy">{t("settings.diagnosticsHint")}</p>
        </div>
        <button className="ghost-action" type="button" disabled={busy} onClick={() => void download()}>
          <Download size={14} />
          {busy ? t("settings.diagnosticsBuilding") : t("settings.diagnosticsDownload")}
        </button>
      </div>
      <label className="settings-row switch-row">
        <span className="settings-row-copy">
          <strong>{t("settings.diagnosticsIncludeContent")}</strong>
          <small>{t("settings.diagnosticsIncludeContentHint")}</small>
        </span>
        <input type="checkbox" role="switch" checked={includeContent} disabled={busy}
          onChange={(event) => setIncludeContent(event.target.checked)} />
      </label>
      {error && <p className="preference-error" role="alert">{t("settings.diagnosticsFailed")}: {error}</p>}
      {saved && (
        <div className="settings-row diagnostics-files" role="status">
          <span className="settings-row-copy">
            <small>{t("settings.diagnosticsSaved", { name: saved.name })}</small>
            <ul>
              {saved.files.map((file) => (
                <li key={file.name}><code>{file.name}</code> <small>{formatFileSize(file.size)}</small></li>
              ))}
            </ul>
          </span>
        </div>
      )}
    </section>
  );
}
