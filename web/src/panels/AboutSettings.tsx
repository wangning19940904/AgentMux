import { useState } from "react";
import { CheckCircle2, Download, ExternalLink, RefreshCw } from "lucide-react";
import {
  api,
  checkDesktopUpdate,
  installDesktopUpdate,
  isDesktopApp,
  openExternalURL,
  type DesktopUpdateStatus,
} from "../api";
import { useI18n } from "../i18n";
import { useAsync } from "../useAsync";

export function AboutSettings() {
  const { t } = useI18n();
  const appStatus = useAsync(() => api.localStatus(), []);
  const [updateInfo, setUpdateInfo] = useState<DesktopUpdateStatus | null>(null);
  const [updatePhase, setUpdatePhase] = useState<"idle" | "checking" | "installing" | "error">("idle");
  const [updateError, setUpdateError] = useState("");

  const currentVersion = (appStatus.data?.version || updateInfo?.current_version || "").replace(/^v/, "");

  const checkForUpdates = async () => {
    setUpdatePhase("checking");
    setUpdateError("");
    try {
      setUpdateInfo(await checkDesktopUpdate(currentVersion));
      setUpdatePhase("idle");
    } catch (updateCheckError) {
      setUpdatePhase("error");
      setUpdateError(updateCheckError instanceof Error ? updateCheckError.message : String(updateCheckError));
    }
  };

  const applyUpdate = async () => {
    if (!updateInfo) return;
    if (!updateInfo.supported) {
      if (updateInfo.release_url) await openExternalURL(updateInfo.release_url);
      return;
    }
    setUpdatePhase("installing");
    setUpdateError("");
    try {
      const installed = await installDesktopUpdate();
      setUpdateInfo(installed);
    } catch (installError) {
      setUpdatePhase("error");
      setUpdateError(installError instanceof Error ? installError.message : String(installError));
    }
  };

  return (
    <section className="settings-group settings-appearance">
      <div className="settings-heading">
        <div>
          <h2>{t("settings.softwareUpdate")}</h2>
          <p className="subtle-copy">{t("settings.softwareUpdateHint")}</p>
        </div>
        <button
          className="ghost-action"
          type="button"
          disabled={!currentVersion || updatePhase === "checking" || updatePhase === "installing"}
          onClick={() => void checkForUpdates()}
        >
          <RefreshCw size={14} className={updatePhase === "checking" ? "spin" : ""} />
          {updatePhase === "checking" ? t("settings.checking") : t("settings.checkForUpdates")}
        </button>
      </div>
      <div className="software-update-body">
        <img src="/agentmux-logo.png" alt="" aria-hidden="true" />
        <div className="software-update-copy">
          <strong>AgentMux {currentVersion ? `v${currentVersion}` : "—"}</strong>
          {!updateInfo && <span>{t("settings.currentVersion")}</span>}
          {updateInfo && !updateInfo.update_available && (
            <span className="software-update-current">
              <CheckCircle2 size={15} />
              {t("settings.upToDate")}
            </span>
          )}
          {updateInfo?.update_available && (
            <span>{t("settings.updateAvailable", { version: `v${updateInfo.latest_version ?? ""}` })}</span>
          )}
          {!isDesktopApp() && <small>{t("settings.webUpdateHint")}</small>}
        </div>
        {updateInfo?.update_available && (
          <button
            className="action software-update-action"
            type="button"
            disabled={updatePhase === "installing"}
            onClick={() => void applyUpdate()}
          >
            {updateInfo.supported ? <Download size={15} /> : <ExternalLink size={15} />}
            {updatePhase === "installing"
              ? t("settings.installing")
              : updateInfo.supported
                ? t("settings.updateNow")
                : t("settings.openDownload")}
          </button>
        )}
      </div>
      {updateError && <div className="software-update-error">{t("settings.updateFailed")}: {updateError}</div>}
    </section>
  );
}
