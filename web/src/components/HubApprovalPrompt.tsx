import { Check, Laptop, ShieldCheck, X } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { api, type DeviceApproval } from "../api";
import { useI18n } from "../i18n";

// HubApprovalPrompt shows the machines asking to sign in with the account
// this Console is signed in as, so a hub can let them in without their
// password. It asks every few seconds while the window is visible; requests
// expire on their own after five minutes.
const POLL_MS = 4000;

export function HubApprovalPrompt() {
  const { t } = useI18n();
  const [approvals, setApprovals] = useState<DeviceApproval[]>([]);
  const [busy, setBusy] = useState("");
  const [failed, setFailed] = useState("");

  const load = useCallback(async () => {
    if (document.visibilityState !== "visible") return;
    try {
      const result = await api.pendingApprovals();
      setApprovals(result.approvals ?? []);
    } catch {
      // An older daemon, or no account signed in: nothing to approve.
      setApprovals([]);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), POLL_MS);
    document.addEventListener("visibilitychange", load);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, [load]);

  async function decide(approval: DeviceApproval, approve: boolean) {
    setBusy(approval.id);
    setFailed("");
    try {
      await api.decideApproval(approval.id, approve);
      setApprovals((current) => current.filter((entry) => entry.id !== approval.id));
    } catch (error) {
      setFailed(error instanceof Error ? error.message : String(error));
      void load();
    } finally {
      setBusy("");
    }
  }

  if (!approvals.length) return null;
  return (
    <div className="hub-approvals" role="region" aria-label={t("auth.approvalsRegion")}>
      {approvals.map((approval) => (
        <section key={approval.id} className="hub-approval" role="alertdialog" aria-labelledby={`approval-${approval.id}`}>
          <span className="hub-approval-icon" aria-hidden="true"><Laptop size={16} /></span>
          <div className="hub-approval-text">
            <strong id={`approval-${approval.id}`}>{t("auth.approvalPromptTitle", { machine: approval.machine || "?" })}</strong>
            <small>{t("auth.approvalPromptBody", { login: approval.login })}</small>
            {failed && busy === "" ? <small className="hub-approval-error">{failed}</small> : null}
          </div>
          <div className="hub-approval-actions">
            <button type="button" className="ghost-action" disabled={busy === approval.id} onClick={() => void decide(approval, false)}>
              <X size={13} /> {t("auth.approvalDeny")}
            </button>
            <button type="button" className="action" disabled={busy === approval.id} onClick={() => void decide(approval, true)}>
              {busy === approval.id ? <ShieldCheck size={13} /> : <Check size={13} />} {t("auth.approvalAllow")}
            </button>
          </div>
        </section>
      ))}
    </div>
  );
}
