import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { activeMachineScope, api } from "../../api";
import type { Framework, FrameworkHealthRecord, FrameworkHealthSettings, FrameworkSmokeResult } from "../../api";
import { targetKey } from "../../components/TargetBadge";

// Results per framework instance (targetKey), then per model ("" is the
// framework's default model).
export type FrameworkHealthResults = Record<string, Record<string, FrameworkSmokeResult>>;

const TEST_ALL_CONCURRENCY = 2;

// mergeHealthResults adds results, keeping the newest one per framework and model.
export function mergeHealthResults(current: FrameworkHealthResults, targetID: string | undefined, results: FrameworkSmokeResult[]): FrameworkHealthResults {
  const next = { ...current };
  for (const result of results) {
    const key = targetKey(targetID, result.kind);
    const model = result.model ?? "";
    const existing = next[key]?.[model];
    if (existing && Date.parse(existing.checked_at) >= Date.parse(result.checked_at)) continue;
    next[key] = { ...next[key], [model]: result };
  }
  return next;
}

// Health records per framework instance (targetKey).
export type FrameworkHealthRecords = Record<string, FrameworkHealthRecord>;

// mergeHealthRecords replaces the records of one machine.
export function mergeHealthRecords(current: FrameworkHealthRecords, targetID: string | undefined, records: FrameworkHealthRecord[]): FrameworkHealthRecords {
  const next = { ...current };
  for (const record of records) next[targetKey(targetID, record.kind)] = record;
  return next;
}

// latestHealthResult is the most recent test of a framework, whatever its model.
export function latestHealthResult(results?: Record<string, FrameworkSmokeResult>) {
  let latest: FrameworkSmokeResult | undefined;
  for (const result of Object.values(results ?? {})) {
    if (!latest || Date.parse(result.checked_at) > Date.parse(latest.checked_at)) latest = result;
  }
  return latest;
}

// The last results per machine selection, shown at once when the page opens
// again while they are reloaded.
const healthCache = new Map<string, { results: FrameworkHealthResults; records: FrameworkHealthRecords }>();

export function useFrameworkHealth(items: Framework[]) {
  const [results, setResults] = useState<FrameworkHealthResults>(() => healthCache.get(activeMachineScope())?.results ?? {});
  const [records, setRecords] = useState<FrameworkHealthRecords>(() => healthCache.get(activeMachineScope())?.records ?? {});
  useEffect(() => {
    healthCache.set(activeMachineScope(), { results, records });
  }, [results, records]);
  // Periodic check settings per machine ("" is the machine in view).
  const [checks, setChecks] = useState<Record<string, FrameworkHealthSettings>>({});
  const [testing, setTesting] = useState<Record<string, Record<string, boolean>>>({});
  const [testingAll, setTestingAll] = useState(false);
  const testingAllRef = useRef(false);
  const targets = useMemo(() => [...new Set(items.map((item) => item.target_id ?? ""))].sort(), [items]);
  // JSON, not join: [] and [""] (this machine) must differ.
  const targetsKey = JSON.stringify(targets);

  const load = useCallback(async (target: string, isActive: () => boolean = () => true) => {
    try {
      const health = await api.frameworkHealth(target || undefined);
      if (!isActive()) return;
      setResults((current) => mergeHealthResults(current, target || undefined, health.results));
      setRecords((current) => mergeHealthRecords(current, target || undefined, health.records ?? []));
      const settings = health.checks;
      if (settings) setChecks((current) => ({ ...current, [target]: settings }));
    } catch {
      // older machines have no stored results
    }
  }, []);

  useEffect(() => {
    let active = true;
    for (const target of targets) void load(target, () => active);
    return () => { active = false; };
  }, [targetsKey, load]);

  const setModelTesting = (key: string, model: string, value: boolean) => setTesting((current) => {
    const models = { ...current[key] };
    if (value) models[model] = true;
    else delete models[model];
    return { ...current, [key]: models };
  });

  const test = useCallback(async (item: Framework, model = "") => {
    const key = targetKey(item.target_id, item.spec.kind);
    setModelTesting(key, model, true);
    try {
      const result = await api.frameworkTest({ kind: item.spec.kind, ...(model ? { model } : {}) }, item.target_id);
      setResults((current) => mergeHealthResults(current, item.target_id, [{ ...result, model: result.model ?? model }]));
      // The test may have changed the framework's record.
      void load(item.target_id ?? "");
    } catch (error) {
      const failure: FrameworkSmokeResult = {
        kind: item.spec.kind, model, ok: false, latency_ms: 0, error_kind: "unknown",
        error_detail: error instanceof Error ? error.message : String(error), checked_at: new Date().toISOString(),
      };
      setResults((current) => mergeHealthResults(current, item.target_id, [failure]));
    } finally {
      setModelTesting(key, model, false);
    }
  }, [load]);

  const saveChecks = useCallback(async (target: string, settings: FrameworkHealthSettings) => {
    const saved = await api.saveFrameworkHealthSettings(settings, target || undefined);
    setChecks((current) => ({ ...current, [target]: saved }));
    return saved;
  }, []);

  const testAll = useCallback(async (candidates: Framework[]) => {
    if (testingAllRef.current) return;
    testingAllRef.current = true;
    setTestingAll(true);
    try {
      const queue = [...candidates];
      const worker = async () => {
        for (let item = queue.shift(); item; item = queue.shift()) await test(item);
      };
      await Promise.all(Array.from({ length: Math.min(TEST_ALL_CONCURRENCY, queue.length) }, worker));
    } finally {
      testingAllRef.current = false;
      setTestingAll(false);
    }
  }, [test]);

  return { results, records, checks, testing, testingAll, test, testAll, saveChecks };
}
