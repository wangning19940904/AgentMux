import { useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import type { Framework, FrameworkAuthStatus } from "../../api";
import { api } from "../../api";
import { isDesktopApp, openExternalURL } from "../../api/desktop";
import { useI18n } from "../../i18n";
import type { FrameworkBusyAction } from "./FrameworkTableRows";
import { resolveFrameworkLoginFlow } from "./frameworkAuthModel";
import type { FrameworkLoginFlow } from "./frameworkAuthModel";

const LOGIN_POLL_MS = 1_500;

export function useFrameworkAuth({
  items,
  targetID,
  currentMachine,
  markBusy,
  clearBusy,
  setNotice,
}: {
  items: Framework[];
  targetID: string;
  currentMachine: string;
  markBusy: (kind: string, action: FrameworkBusyAction) => void;
  clearBusy: (kind: string) => void;
  setNotice: Dispatch<SetStateAction<string>>;
}) {
  const { t } = useI18n();
  const [auth, setAuth] = useState<Record<string, FrameworkAuthStatus>>({});
  const [loginFlows, setLoginFlows] = useState<Record<string, FrameworkLoginFlow>>({});
  const [loginCodes, setLoginCodes] = useState<Record<string, string>>({});
  const [copiedCode, setCopiedCode] = useState("");
  const notifiedSessions = useRef(new Set<string>());
  const authTargetKey = items
    .filter((item) => item.installed && item.spec.kind_type === "cli")
    .map((item) => item.spec.kind)
    .sort()
    .join(",");
  const activeLoginKey = JSON.stringify(
    Object.entries(loginFlows)
      .filter(([, flow]) => flow.state === "waiting")
      .map(([kind, flow]) => [kind, flow.session_id, Boolean(flow.reAuthenticate)] as [string, string, boolean])
      .sort((left, right) => left[0].localeCompare(right[0])),
  );
  const succeededLoginKey = Object.entries(loginFlows)
    .filter(([, flow]) => flow.state === "succeeded")
    .map(([kind]) => kind)
    .sort()
    .join(",");

  useEffect(() => {
    setAuth({});
    setLoginFlows({});
    notifiedSessions.current.clear();
    if (!authTargetKey) return;
    let active = true;
    void Promise.all(authTargetKey.split(",").map(async (kind) => {
      try {
        return await api.frameworkAuth(kind);
      } catch {
        return null;
      }
    })).then((statuses) => {
      if (!active) return;
      setAuth(Object.fromEntries(statuses.filter((status): status is FrameworkAuthStatus => Boolean(status)).map((status) => [status.kind, status])));
    });
    return () => { active = false; };
  }, [authTargetKey, targetID]);

  useEffect(() => {
    const sessions = JSON.parse(activeLoginKey) as [string, string, boolean][];
    if (sessions.length === 0) return;
    let active = true;
    let timerID = 0;
    const poll = async () => {
      await Promise.all(sessions.map(async ([kind, sessionID, reAuthenticate]) => {
        try {
          const status = await api.frameworkAuth(kind);
          let lifecycleActive = true;
          let lifecycleState = "unknown";
          let lifecycleError = "";
          try {
            const lifecycle = await api.frameworkLoginSession(sessionID);
            lifecycleActive = lifecycle.active;
            lifecycleState = lifecycle.state;
            lifecycleError = lifecycle.error || "";
          } catch {
            // Older remote AgentMux versions do not expose lifecycle status.
          }
          if (!active) return;
          setAuth((current) => ({ ...current, [kind]: status }));
          setLoginFlows((current) => {
            const flow = current[kind];
            const nextFlow = resolveFrameworkLoginFlow(flow, sessionID, {
              auth: status, lifecycleActive, lifecycleState, lifecycleError,
              endedMessage: t("frameworks.authEnded"),
            });
            if (!flow || nextFlow === flow) return current;
            return { ...current, [kind]: nextFlow as FrameworkLoginFlow };
          });
          const succeeded = lifecycleState === "succeeded" || status.state === "authenticated" && !reAuthenticate;
          if (succeeded && !notifiedSessions.current.has(sessionID)) {
            notifiedSessions.current.add(sessionID);
            const display = items.find((item) => item.spec.kind === kind)?.spec.display || kind;
            const successNotice = t("frameworks.authSuccess", { framework: display, machine: currentMachine });
            setNotice(successNotice);
            window.setTimeout(() => setNotice((current) => current === successNotice ? "" : current), 2_600);
          }
        } catch {
          // A transient network interruption must not cancel the CLI process.
        }
      }));
      if (active) timerID = window.setTimeout(() => void poll(), LOGIN_POLL_MS);
    };
    void poll();
    return () => {
      active = false;
      window.clearTimeout(timerID);
    };
  }, [activeLoginKey, currentMachine, items, setNotice, t]);

  useEffect(() => {
    if (!succeededLoginKey) return;
    const kinds = succeededLoginKey.split(",");
    const timeoutID = window.setTimeout(() => {
      setLoginFlows((current) => {
        const next = { ...current };
        kinds.forEach((kind) => {
          if (next[kind]?.state === "succeeded") delete next[kind];
        });
        return next;
      });
    }, 1_800);
    return () => window.clearTimeout(timeoutID);
  }, [succeededLoginKey]);

  async function refreshAuth(kind: string) {
    try {
      const status = await api.frameworkAuth(kind);
      setAuth((current) => ({ ...current, [kind]: status }));
      return status;
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
      return null;
    }
  }

  async function startAuth(kind: string) {
    markBusy(kind, "auth");
    setNotice("");
    setLoginCodes((current) => ({ ...current, [kind]: "" }));
    try {
      const session = await api.startFrameworkLogin(kind);
      setLoginFlows((current) => ({
        ...current,
        [kind]: { ...session, state: "waiting", error: "", codeSubmitted: false, reAuthenticate: auth[kind]?.state === "authenticated" },
      }));
      if (session.login_url && isDesktopApp()) await openExternalURL(session.login_url);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      clearBusy(kind);
    }
  }

  async function completeAuth(kind: string, sessionID: string) {
    const code = loginCodes[kind]?.trim();
    if (!code) return;
    markBusy(kind, "complete");
    setNotice("");
    try {
      await api.completeFrameworkLogin(sessionID, code);
      setLoginFlows((current) => current[kind]
        ? { ...current, [kind]: { ...current[kind], codeSubmitted: true } }
        : current);
      setNotice(t("frameworks.authCodeSubmitted"));
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      clearBusy(kind);
    }
  }

  async function cancelAuth(kind: string, sessionID: string) {
    markBusy(kind, "cancel");
    setNotice("");
    try {
      await api.cancelFrameworkLogin(sessionID);
      setLoginFlows((current) => current[kind]
        ? { ...current, [kind]: { ...current[kind], state: "cancelled", error: "" } }
        : current);
      setNotice(t("tools.authCancelled"));
      void refreshAuth(kind);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
    } finally {
      clearBusy(kind);
    }
  }

  async function copyCode(kind: string, code: string) {
    try {
      await navigator.clipboard.writeText(code);
      setCopiedCode(kind);
      window.setTimeout(() => setCopiedCode((current) => current === kind ? "" : current), 1_600);
    } catch {
      setNotice(t("frameworks.authCopyFailed"));
    }
  }

  const relevantAuth = Object.values(auth).filter((status) => status.installed && (status.login_supported || status.state !== "unknown"));
  return {
    auth,
    loginFlows,
    loginCodes,
    copiedCode,
    relevantAuth,
    readyAuthCount: relevantAuth.filter((status) => status.state === "authenticated").length,
    setLoginCode: (kind: string, code: string) => setLoginCodes((current) => ({ ...current, [kind]: code })),
    startAuth,
    completeAuth,
    cancelAuth,
    dismissAuth: (kind: string) => setLoginFlows((current) => {
      const next = { ...current };
      delete next[kind];
      return next;
    }),
    copyCode,
  };
}
