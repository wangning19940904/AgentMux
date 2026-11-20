import { useCallback, useEffect, useRef, useState } from "react";
import {
  activeMachineScope, api, mergeFleetUsage, usageFilterActive, usagePath,
  type FleetTargetResult, type UsageReport, type UsageReportFilter, type UsageTarget,
} from "../../api";
import { usePolling } from "../../hooks/usePolling";
import type { Period } from "./usageQuery";

export type UsageRequest = {
  period: Period;
  from: string;
  to: string;
  filter: UsageReportFilter;
  // Machine IDs to read; empty reads every machine.
  machines: string[];
};

export type UsageLoadResult = { report: UsageReport; targets: UsageTarget[] };
export type UsagePartial = { report: UsageReport; targets: UsageTarget[]; pending: UsageTarget[] };

function failedTarget(target: UsageTarget, error: string): FleetTargetResult<UsageReport> {
  return {
    target: { id: target.id, name: target.name, kind: target.id === "local" ? "local" : "link", trusted: true, online: false },
    responses: [{ key: "usage", status: 0, ok: false, error, duration_ms: 0 }],
  };
}

// loadUsage reads a usage report from every selected machine. Each machine is
// asked on its own, so the report fills in as machines answer and one slow or
// unreachable machine does not hold back the rest. onPartial receives the
// report so far while machines are still pending.
export async function loadUsage(request: UsageRequest, onPartial?: (partial: UsagePartial) => void): Promise<UsageLoadResult> {
  if (activeMachineScope() !== "all") {
    return { report: await api.usage(request.period, request.from, request.to, request.filter), targets: [] };
  }
  let targets: UsageTarget[];
  try {
    targets = await api.usageTargets();
  } catch {
    // Without the machine list, one fleet-wide read still answers.
    return { report: await api.usage(request.period, request.from, request.to, request.filter), targets: [] };
  }
  const wanted = new Set(request.machines);
  const selected = wanted.size ? targets.filter((target) => wanted.has(target.id)) : targets;
  if (selected.length === 0) throw new Error("None of the selected machines is available.");
  const path = usagePath(request.period, request.from, request.to, request.filter);
  const merge = (targets: FleetTargetResult<UsageReport>[]) => mergeFleetUsage({ targets }, { filtered: usageFilterActive(request.filter) });
  const answers = new Map<string, FleetTargetResult<UsageReport>>();
  const ordered = () => selected.flatMap((target) => answers.get(target.id) ?? []);
  await Promise.all(selected.map(async (target) => {
    let answer: FleetTargetResult<UsageReport>;
    try {
      const batch = await api.usageFromTarget(path, target.id);
      answer = batch.targets.find((item) => item.target.id === target.id) ?? batch.targets[0] ?? failedTarget(target, "unavailable");
    } catch (error) {
      answer = failedTarget(target, error instanceof Error ? error.message : String(error));
    }
    answers.set(target.id, answer);
    if (!onPartial || answers.size === selected.length) return;
    try {
      onPartial({ report: merge(ordered()), targets, pending: selected.filter((item) => !answers.has(item.id)) });
    } catch {
      // No machine has answered with data yet.
    }
  }));
  return { report: merge(ordered()), targets };
}

// --- last answers ------------------------------------------------------------
// Reopening the dashboard, or returning to a range already viewed, shows the
// last answer at once while a fresh one loads.

type Cached = UsageLoadResult & { updatedAt: number };
const lastAnswers = new Map<string, Cached>();
const MAX_CACHED_ANSWERS = 24;

function remember(key: string, value: Cached) {
  lastAnswers.delete(key);
  lastAnswers.set(key, value);
  while (lastAnswers.size > MAX_CACHED_ANSWERS) lastAnswers.delete(lastAnswers.keys().next().value as string);
}

export function clearUsageAnswers() {
  lastAnswers.clear();
}

export function usageRequestKey(request: UsageRequest) {
  return JSON.stringify([activeMachineScope(), request.period, request.from, request.to,
    request.filter.models ?? [], request.filter.runtimes ?? [], request.filter.agents ?? [], request.machines]);
}

// prefetchUsage warms the default view before the dashboard opens.
export function prefetchUsage(request: UsageRequest, staleMs = 30_000) {
  const key = usageRequestKey(request);
  const cached = lastAnswers.get(key);
  if (cached && Date.now() - cached.updatedAt < staleMs) return;
  void loadUsage(request).then((result) => remember(key, { ...result, updatedAt: Date.now() })).catch(() => undefined);
}

export type UsageState = {
  report: UsageReport | null;
  // The request the shown report answers; it trails the current request
  // while a new range or filter is loading.
  reportKey: string;
  targets: UsageTarget[];
  // Machines that have not answered the current request yet.
  pending: UsageTarget[];
  loading: boolean;
  error: string | null;
  updatedAt: number;
};

export function useUsageReport(request: UsageRequest, options: { pollMs: number; live: boolean }) {
  const key = usageRequestKey(request);
  const requestRef = useRef(request);
  requestRef.current = request;
  const sequence = useRef(0);
  const [state, setState] = useState<UsageState>(() => {
    const cached = lastAnswers.get(key);
    return {
      report: cached?.report ?? null, reportKey: cached ? key : "", targets: cached?.targets ?? [],
      pending: [], loading: true, error: null, updatedAt: cached?.updatedAt ?? 0,
    };
  });

  const load = useCallback(async () => {
    const current = ++sequence.current;
    const cached = lastAnswers.get(key);
    setState((previous) => cached && previous.reportKey !== key
      ? { ...previous, report: cached.report, reportKey: key, targets: cached.targets, updatedAt: cached.updatedAt, loading: true, error: null, pending: [] }
      : { ...previous, loading: true, error: null });
    // A background refresh replaces the report in one step; machine-by-machine
    // totals would make every number dip and climb again.
    let fillIn = !cached;
    try {
      const result = await loadUsage(requestRef.current, (partial) => {
        if (sequence.current !== current) return;
        setState((previous) => {
          if (!fillIn && previous.reportKey === key) return previous;
          fillIn = true;
          return { ...previous, report: partial.report, reportKey: key, targets: partial.targets, pending: partial.pending };
        });
      });
      const updatedAt = Date.now();
      remember(key, { ...result, updatedAt });
      if (sequence.current !== current) return;
      setState({ report: result.report, reportKey: key, targets: result.targets, pending: [], loading: false, error: null, updatedAt });
    } catch (error) {
      if (sequence.current !== current) return;
      setState((previous) => ({ ...previous, pending: [], loading: false, error: error instanceof Error ? error.message : String(error) }));
    }
  }, [key]);

  useEffect(() => {
    void load();
    return () => { sequence.current += 1; };
  }, [load]);
  usePolling(load, options.pollMs, { enabled: options.live });

  return { ...state, stale: state.reportKey !== key, reload: load };
}
