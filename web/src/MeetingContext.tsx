import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api, streamMeetingEvents } from "./api";
import type { MeetingOverview, MeetingStreamEvent } from "./api";

const EMPTY_OVERVIEW: MeetingOverview = { channels: [], invitations: [], meetings: [] };

type MeetingState = {
  overview: MeetingOverview;
  lastEvent: MeetingStreamEvent | null;
  refresh: (silent?: boolean) => Promise<void>;
  error: string;
};

const MeetingContext = createContext<MeetingState | null>(null);

export function MeetingProvider({ children }: { children: ReactNode }) {
  const [overview, setOverview] = useState<MeetingOverview>(EMPTY_OVERVIEW);
  const [lastEvent, setLastEvent] = useState<MeetingStreamEvent | null>(null);
  const [error, setError] = useState("");
  const [scopeVersion, setScopeVersion] = useState(0);

  useEffect(() => {
    const changed = () => setScopeVersion((version) => version + 1);
    window.addEventListener("agentmux:machine-scope-changed", changed);
    return () => window.removeEventListener("agentmux:machine-scope-changed", changed);
  }, []);

  const refresh = useCallback(async (silent = true) => {
    try {
      const result = await api.meetingOverview();
      setOverview({ ...result, channels: result.channels ?? [], invitations: result.invitations ?? [], meetings: result.meetings ?? [] });
      if (!silent) setError("");
    } catch (reason) {
      if (!silent) setError(reason instanceof Error ? reason.message : String(reason));
    }
  }, []);

  useEffect(() => {
    let active = true;
    const controller = new AbortController();
    let reconnectTimer = 0;
    let reconnectDelay = 1_500;
    void refresh();
    const stream = async () => {
      while (active) {
        const connectedAt = Date.now();
        try {
          await streamMeetingEvents(controller.signal, (name, payload) => {
            if (!active) return;
            if (name !== "ready") setLastEvent({ ...payload, type: name });
            if (name === "meeting.changed") void refresh();
          });
        } catch {
          if (!active || controller.signal.aborted) return;
        }
        if (Date.now() - connectedAt >= 15_000) reconnectDelay = 1_500;
        await new Promise<void>((resolve) => { reconnectTimer = window.setTimeout(resolve, reconnectDelay); });
        reconnectDelay = Math.min(reconnectDelay * 2, 30_000);
      }
    };
    void stream();
    return () => { active = false; controller.abort(); window.clearTimeout(reconnectTimer); };
  }, [refresh, scopeVersion]);

  const value = useMemo(() => ({ overview, lastEvent, refresh, error }), [overview, lastEvent, refresh, error]);
  return <MeetingContext.Provider value={value}>{children}</MeetingContext.Provider>;
}

export function useMeetings() {
  const state = useContext(MeetingContext);
  if (!state) throw new Error("useMeetings must be used inside MeetingProvider");
  return state;
}
