import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../api";
import type { FeishuAutomationResult, FeishuSetupPollResponse } from "../../api";
import type { FeishuSetupPhase } from "./connectShared";
import { sleep } from "./connectShared";

const errorText = (error: unknown) => error instanceof Error ? error.message : String(error);

export type FeishuRegistration = {
  phase: FeishuSetupPhase;
  qrUrl: string;
  error: string;
  start: () => void;
  reset: () => void;
};

// useFeishuRegistration runs Feishu's scan-to-create-a-bot flow: the scan
// creates an app in the scanner's tenant and hands back its credentials, which
// onCompleted saves. Polling stops when the component unmounts.
export function useFeishuRegistration(onCompleted: (result: FeishuSetupPollResponse) => Promise<void>, fallbackError: string): FeishuRegistration {
  const [state, setState] = useState<{ phase: FeishuSetupPhase; qrUrl: string; error: string }>({ phase: "idle", qrUrl: "", error: "" });
  const run = useRef({ id: 0, deviceCode: "", baseUrl: "", interval: 5 });
  const completed = useRef(onCompleted);
  completed.current = onCompleted;

  useEffect(() => () => { run.current.id += 1; }, []);

  const poll = useCallback(async (id: number) => {
    const live = () => run.current.id === id;
    try {
      while (live()) {
        const res = await api.pollFeishuSetup(run.current.deviceCode, run.current.baseUrl);
        if (!live()) return;
        if (res.base_url) run.current.baseUrl = res.base_url;
        if (res.slow_down) run.current.interval += 5;
        switch (res.status) {
          case "completed":
            setState((current) => ({ ...current, phase: "saving", error: "" }));
            await completed.current(res);
            if (live()) setState((current) => ({ ...current, phase: "completed", error: "" }));
            return;
          case "denied":
          case "expired":
            setState((current) => ({ ...current, phase: res.status as FeishuSetupPhase, error: "" }));
            return;
          case "error":
            setState((current) => ({ ...current, phase: "error", error: res.error ?? fallbackError }));
            return;
        }
        await sleep((run.current.interval || 5) * 1000);
      }
    } catch (error) {
      if (live()) setState((current) => ({ ...current, phase: "error", error: errorText(error) }));
    }
  }, [fallbackError]);

  const start = useCallback(() => {
    const id = run.current.id + 1;
    run.current = { id, deviceCode: "", baseUrl: "", interval: 5 };
    setState({ phase: "loading", qrUrl: "", error: "" });
    api.beginFeishuSetup().then(
      (res) => {
        if (run.current.id !== id) return;
        run.current = { id, deviceCode: res.device_code, baseUrl: "", interval: res.interval || 5 };
        setState({ phase: "scanning", qrUrl: res.qr_url, error: "" });
        void poll(id);
      },
      (error) => { if (run.current.id === id) setState({ phase: "error", qrUrl: "", error: errorText(error) }); },
    );
  }, [poll]);

  const reset = useCallback(() => {
    run.current.id += 1;
    setState({ phase: "idle", qrUrl: "", error: "" });
  }, []);

  return { ...state, start, reset };
}

export type FeishuAutomationPhase = "idle" | "loading" | "scanning" | "configuring" | "completed" | "expired" | "error";

export type FeishuAutomation = {
  phase: FeishuAutomationPhase;
  qrPayload: string;
  error: string;
  result?: FeishuAutomationResult;
  start: (appID: string, visibility: "owner" | "all", documents?: boolean, userScopes?: boolean) => void;
  reset: () => void;
};

// useFeishuAutomation runs the second scan: it signs in to the Feishu Open
// Platform and switches on the scopes, long-connection events and card
// callbacks AgentMux needs, then publishes the bot. `documents` also asks for
// what document triggers need, and `userScopes` for what a person grants when
// they authorize the app as themselves. It works only from the local Console.
export function useFeishuAutomation(missingAppError: string): FeishuAutomation {
  const [state, setState] = useState<{ phase: FeishuAutomationPhase; qrPayload: string; error: string; result?: FeishuAutomationResult }>({ phase: "idle", qrPayload: "", error: "" });
  const run = useRef(0);

  useEffect(() => () => { run.current += 1; }, []);

  const start = useCallback((appID: string, visibility: "owner" | "all", documents = false, userScopes = false) => {
    const id = run.current + 1;
    run.current = id;
    const live = () => run.current === id;
    if (!appID.trim()) {
      setState({ phase: "error", qrPayload: "", error: missingAppError });
      return;
    }
    setState({ phase: "loading", qrPayload: "", error: "" });
    void (async () => {
      try {
        const started = await api.beginFeishuAutomation();
        if (!live()) return;
        setState({ phase: "scanning", qrPayload: started.qr_payload, error: "" });
        while (live()) {
          const polled = await api.pollFeishuAutomation(started.session_id);
          if (!live()) return;
          if (polled.status === "expired") {
            setState((current) => ({ ...current, phase: "expired" }));
            return;
          }
          if (polled.status === "completed") {
            setState((current) => ({ ...current, phase: "configuring", error: "" }));
            const result = await api.configureFeishuAutomation(started.session_id, appID.trim(), visibility, documents, userScopes);
            if (live()) setState({ phase: "completed", qrPayload: "", error: "", result });
            return;
          }
          await sleep(1500);
        }
      } catch (error) {
        if (live()) setState((current) => ({ ...current, phase: "error", error: errorText(error) }));
      }
    })();
  }, [missingAppError]);

  const reset = useCallback(() => {
    run.current += 1;
    setState({ phase: "idle", qrPayload: "", error: "" });
  }, []);

  return { ...state, start, reset };
}
