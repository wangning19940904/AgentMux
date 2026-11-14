import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../../api";
import type { Framework, FrameworkSmokeResult } from "../../api";
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

// latestHealthResult is the most recent test of a framework, whatever its model.
export function latestHealthResult(results?: Record<string, FrameworkSmokeResult>) {
  let latest: FrameworkSmokeResult | undefined;
  for (const result of Object.values(results ?? {})) {
    if (!latest || Date.parse(result.checked_at) > Date.parse(latest.checked_at)) latest = result;
  }
  return latest;
}

export function useFrameworkHealth(items: Framework[]) {
  const [results, setResults] = useState<FrameworkHealthResults>({});
  const [testing, setTesting] = useState<Record<string, Record<string, boolean>>>({});
  const [testingAll, setTestingAll] = useState(false);
  const testingAllRef = useRef(false);
  const targets = useMemo(() => [...new Set(items.map((item) => item.target_id ?? ""))].sort(), [items]);
  const targetsKey = targets.join("\n");

  useEffect(() => {
    let active = true;
    for (const target of targets) {
      api.frameworkHealth(target || undefined)
        .then((list) => { if (active) setResults((current) => mergeHealthResults(current, target || undefined, list)); })
        .catch(() => undefined); // older machines have no stored results
    }
    return () => { active = false; };
  }, [targetsKey]);

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
    } catch (error) {
      const failure: FrameworkSmokeResult = {
        kind: item.spec.kind, model, ok: false, latency_ms: 0, error_kind: "unknown",
        error_detail: error instanceof Error ? error.message : String(error), checked_at: new Date().toISOString(),
      };
      setResults((current) => mergeHealthResults(current, item.target_id, [failure]));
    } finally {
      setModelTesting(key, model, false);
    }
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

  return { results, testing, testingAll, test, testAll };
}
