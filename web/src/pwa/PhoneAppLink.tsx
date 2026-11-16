import { useCallback, useEffect, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { Check, Copy, RefreshCw, X } from "lucide-react";
import { useI18n } from "../i18n";
import { api, APIError, type HubClientLink, type HubClientLinkRefusal } from "../api";

type State =
  | { phase: "loading" }
  | { phase: "ready"; link: HubClientLink }
  | { phase: "refused"; reason: HubClientLinkRefusal; message: string }
  | { phase: "error"; message: string };

const REFUSALS: HubClientLinkRefusal[] = ["disabled", "loopback", "insecure"];

// What a refusal needs changed in the hub's config.toml.
const CONFIG_SNIPPETS: Record<HubClientLinkRefusal, string> = {
  disabled: '[hub]\nclient_api = true\n# addr = "0.0.0.0:8766"  # only if [hub] addr is not set yet',
  loopback: '[hub]\npublic_url = "https://<relay or tunnel address>"\npin_certificate = true',
  insecure: '[hub]\ntls = "auto"',
};

export async function loadPhoneAppLink(): Promise<State> {
  try {
    return { phase: "ready", link: await api.hubClientLink() };
  } catch (err) {
    if (err instanceof APIError && err.status === 409) {
      const reason = REFUSALS.find((candidate) => candidate === err.body.reason) ?? "disabled";
      return { phase: "refused", reason, message: err.message };
    }
    return { phase: "error", message: err instanceof Error ? err.message : String(err) };
  }
}

// PhoneAppLink shows the QR code the AgentMux phone app scans to find this
// hub. The code holds the hub's address and certificate fingerprint only;
// the phone still signs in with an account.
//
// inline renders it as a panel inside the Devices page, opened by its
// "Connect a phone" action and closed with onClose.
export function PhoneAppLink({ inline = false, onClose }: { inline?: boolean; onClose?: () => void } = {}) {
  const { t } = useI18n();
  const [state, setState] = useState<State>({ phase: "loading" });
  const [copied, setCopied] = useState(false);

  const refresh = useCallback(async () => {
    setState({ phase: "loading" });
    setState(await loadPhoneAppLink());
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const copy = async (link: string) => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };

  const refreshButton = (
    <button className="ghost-action" type="button" disabled={state.phase === "loading"} onClick={() => void refresh()}>
      <RefreshCw size={14} /> {t("common.refresh")}
    </button>
  );

  const body = (
      <div className={`${inline ? "" : "surface-body "}phone-app-link-body`}>
        {state.phase === "loading" && <p className="subtle-copy">{t("common.loading")}</p>}

        {state.phase === "ready" && (
          <div className="phone-app-link-pair">
            <span className="qr-frame" data-testid="phone-app-qr">
              <QRCodeSVG value={state.link.link} size={148} level="M" title={t("phoneApp.qrLabel")} />
            </span>
            <div className="phone-app-link-details">
              <ol className="phone-app-link-steps">
                <li>{t("phoneApp.step.open")}</li>
                <li>{t("phoneApp.step.scan")}</li>
                <li>{t("phoneApp.step.signIn")}</li>
              </ol>
              <dl className="phone-app-link-facts">
                <dt>{t("phoneApp.address")}</dt>
                <dd><code>{state.link.hub_url}</code></dd>
                {state.link.fingerprint && (
                  <>
                    <dt>{t("phoneApp.fingerprint")}</dt>
                    <dd><code title={state.link.fingerprint}>{state.link.fingerprint.slice(0, 16)}…</code></dd>
                  </>
                )}
              </dl>
              {state.link.reach === "private" && (
                <div className="notification-callout warning" role="note">
                  <strong>{t("phoneApp.private")}</strong>
                  <span>{t("phoneApp.private.hint")}</span>
                </div>
              )}
              <div className="control-row">
                <button className="ghost-action" type="button" onClick={() => void copy(state.link.link)}>
                  {copied ? <Check size={14} /> : <Copy size={14} />} {t(copied ? "phoneApp.copied" : "phoneApp.copy")}
                </button>
                <small className="subtle-copy">{t("phoneApp.noSecret")}</small>
              </div>
            </div>
          </div>
        )}

        {state.phase === "refused" && (
          <div className="notification-callout" role="note">
            <strong>{t(`phoneApp.refused.${state.reason}`)}</strong>
            <span>{t(`phoneApp.refused.${state.reason}.hint`)}</span>
            <pre className="phone-app-link-config">{CONFIG_SNIPPETS[state.reason]}</pre>
            <small>{state.message}</small>
          </div>
        )}

        {state.phase === "error" && <p className="notification-error" role="alert">{state.message}</p>}
      </div>
  );

  if (inline) {
    return (
      <div className="remote-pairing phone-app-link inline" role="group" aria-labelledby="phone-app-link-title">
        <header>
          <div>
            <strong id="phone-app-link-title">{t("phoneApp.title")}</strong>
            <span>{t("phoneApp.subtitle")}</span>
          </div>
          <div className="table-actions">
            {refreshButton}
            {onClose && (
              <button className="ghost-action" type="button" onClick={onClose}>
                <X size={15} /> {t("common.close")}
              </button>
            )}
          </div>
        </header>
        {body}
      </div>
    );
  }

  return (
    <section className="surface phone-app-link" aria-labelledby="phone-app-link-title">
      <div className="surface-header">
        <div>
          <h2 id="phone-app-link-title">{t("phoneApp.title")}</h2>
          <p className="subtle-copy">{t("phoneApp.subtitle")}</p>
        </div>
        {refreshButton}
      </div>
      {body}
    </section>
  );
}
