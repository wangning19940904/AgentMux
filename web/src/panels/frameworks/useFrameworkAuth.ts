import { useEffect, useRef, useState } from "react";
import type { SetStateAction } from "react";
import type { Framework, FrameworkAuthStatus } from "../../api";
import { api } from "../../api";
import { isDesktopApp, openExternalURL } from "../../api/desktop";
import { targetKey } from "../../components/TargetBadge";
import { useI18n } from "../../i18n";
import type { FrameworkBusyAction } from "./FrameworkTableRows";
import { resolveFrameworkLoginFlow } from "./frameworkAuthModel";
import type { FrameworkLoginFlow } from "./frameworkAuthModel";

const LOGIN_POLL_MS = 1_500;

function rowKey(item: Framework) {
  return targetKey(item.target_id, item.spec.kind);
}

export function useFrameworkAuth({
  items,
  currentMachine,
  markBusy,
  clearBusy,
  setNotice,
}: {
  items: Framework[];
  currentMachine: string;
  markBusy: (key: string, action: FrameworkBusyAction) => void;
  clearBusy: (key: string) => void;
  setNotice: (value: SetStateAction<string>, error?: boolean) => void;
}) {
  const { t } = useI18n();
  const [auth, setAuth] = useState<Record<string, FrameworkAuthStatus>>({});
  const [loginFlows, setLoginFlows] = useState<Record<string, FrameworkLoginFlow>>({});
  const [loginCodes, setLoginCodes] = useState<Record<string, string>>({});
  const [copiedCode, setCopiedCode] = useState("");
  const notifiedSessions = useRef(new Set<string>());
  const authItems = items.filter((item) => item.installed && item.spec.kind_type === "cli");
  const authTargetKey = JSON.stringify(authItems.map((item) => [rowKey(item), item.target_id || ""]));
  const activeLoginKey = JSON.stringify(
    Object.entries(loginFlows)
      .filter(([, flow]) => flow.state === "waiting")
      .map(([key, flow]) => {
        const item = items.find((candidate) => rowKey(candidate) === key);
        return [key, flow.kind, item?.target_id || "", flow.session_id, Boolean(flow.reAuthenticate)] as [string, string, string, string, boolean];
      })
      .sort((left, right) => left[0].localeCompare(right[0])),
  );
  const succeededLoginKey = JSON.stringify(Object.entries(loginFlows)
    .filter(([, flow]) => flow.state === "succeeded")
    .map(([key]) => key)
    .sort());

  useEffect(() => {
    setAuth({});
    setLoginFlows({});
    notifiedSessions.current.clear();
    if (authItems.length === 0) return;
    let active = true;
    void Promise.all(authItems.map(async (item) => {
      try {
        const status = await api.frameworkAuth(item.spec.kind, item.target_id);
        return [rowKey(item), status] as const;
      } catch {
        return [rowKey(item), {
          kind: item.spec.kind,
          state: "unknown",
          installed: true,
          login_supported: false,
          logout_supported: false,
        } satisfies FrameworkAuthStatus] as const;
      }
    })).then((statuses) => {
      if (!active) return;
      setAuth(Object.fromEntries(statuses));
    });
    return () => { active = false; };
  }, [authTargetKey]);

  useEffect(() => {
    const sessions = JSON.parse(activeLoginKey) as [string, string, string, string, boolean][];
    if (sessions.length === 0) return;
    let active = true;
    let timerID = 0;
    const poll = async () => {
      await Promise.all(sessions.map(async ([key, kind, targetID, sessionID, reAuthenticate]) => {
        try {
          const status = await api.frameworkAuth(kind, targetID || undefined);
          let lifecycleActive = true;
          let lifecycleState = "unknown";
          let lifecycleError = "";
          try {
            const lifecycle = await api.frameworkLoginSession(sessionID, targetID || undefined);
            lifecycleActive = lifecycle.active;
            lifecycleState = lifecycle.state;
            lifecycleError = lifecycle.error || "";
          } catch {
            // Older remote AgentMux versions do not expose lifecycle status.
          }
          if (!active) return;
          setAuth((current) => ({ ...current, [key]: status }));
          setLoginFlows((current) => {
            const flow = current[key];
            const nextFlow = resolveFrameworkLoginFlow(flow, sessionID, {
              auth: status, lifecycleActive, lifecycleState, lifecycleError,
              endedMessage: t("frameworks.authEnded"),
            });
            if (!flow || nextFlow === flow) return current;
            return { ...current, [key]: nextFlow as FrameworkLoginFlow };
          });
          const succeeded = lifecycleState === "succeeded" || status.state === "authenticated" && !reAuthenticate;
          if (succeeded && !notifiedSessions.current.has(sessionID)) {
            notifiedSessions.current.add(sessionID);
            const item = items.find((candidate) => rowKey(candidate) === key);
            const display = item?.spec.display || kind;
            const machine = item?.target_name || item?.target_id || currentMachine;
            const successNotice = t("frameworks.authSuccess", { framework: display, machine });
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
    const keys = JSON.parse(succeededLoginKey) as string[];
    if (keys.length === 0) return;
    const timeoutID = window.setTimeout(() => {
      setLoginFlows((current) => {
        const next = { ...current };
        keys.forEach((key) => {
          if (next[key]?.state === "succeeded") delete next[key];
        });
        return next;
      });
    }, 1_800);
    return () => window.clearTimeout(timeoutID);
  }, [succeededLoginKey]);

  async function refreshAuth(item: Framework) {
    const key = rowKey(item);
    try {
      const status = await api.frameworkAuth(item.spec.kind, item.target_id);
      setAuth((current) => ({ ...current, [key]: status }));
      return status;
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error), true);
      return null;
    }
  }

  async function startAuth(item: Framework) {
    const key = rowKey(item);
    markBusy(key, "auth");
    setNotice("");
    setLoginCodes((current) => ({ ...current, [key]: "" }));
    try {
      const session = await api.startFrameworkLogin(item.spec.kind, item.target_id);
      setLoginFlows((current) => ({
        ...current,
        [key]: { ...session, state: "waiting", error: "", codeSubmitted: false, reAuthenticate: auth[key]?.state === "authenticated" },
      }));
      if (session.login_url && isDesktopApp()) await openExternalURL(session.login_url);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error), true);
    } finally {
      clearBusy(key);
    }
  }

  async function logout(item: Framework) {
    const key = rowKey(item);
    markBusy(key, "logout");
    setNotice("");
    try {
      const status = await api.logoutFramework(item.spec.kind, item.target_id);
      setAuth((current) => ({ ...current, [key]: status }));
      setNotice(t("tools.authLoggedOut"));
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error), true);
    } finally {
      clearBusy(key);
    }
  }

  async function completeAuth(item: Framework, sessionID: string) {
    const key = rowKey(item);
    const code = loginCodes[key]?.trim();
    if (!code) return;
    markBusy(key, "complete");
    setNotice("");
    try {
      await api.completeFrameworkLogin(sessionID, code, item.target_id);
      setLoginFlows((current) => current[key]
        ? { ...current, [key]: { ...current[key], codeSubmitted: true } }
        : current);
      setNotice(t("frameworks.authCodeSubmitted"));
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error), true);
    } finally {
      clearBusy(key);
    }
  }

  async function cancelAuth(item: Framework, sessionID: string) {
    const key = rowKey(item);
    markBusy(key, "cancel");
    setNotice("");
    try {
      await api.cancelFrameworkLogin(sessionID, item.target_id);
      setLoginFlows((current) => current[key]
        ? { ...current, [key]: { ...current[key], state: "cancelled", error: "" } }
        : current);
      setNotice(t("tools.authCancelled"));
      void refreshAuth(item);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error), true);
    } finally {
      clearBusy(key);
    }
  }

  async function copyCode(item: Framework, code: string) {
    const key = rowKey(item);
    try {
      await navigator.clipboard.writeText(code);
      setCopiedCode(key);
      window.setTimeout(() => setCopiedCode((current) => current === key ? "" : current), 1_600);
    } catch {
      setNotice(t("frameworks.authCopyFailed"), true);
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
    setLoginCode: (item: Framework, code: string) => {
      const key = rowKey(item);
      setLoginCodes((current) => ({ ...current, [key]: code }));
    },
    startAuth,
    logout,
    completeAuth,
    cancelAuth,
    dismissAuth: (item: Framework) => {
      const key = rowKey(item);
      setLoginFlows((current) => {
        const next = { ...current };
        delete next[key];
        return next;
      });
    },
    copyCode,
  };
}
