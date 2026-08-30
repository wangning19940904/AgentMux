import type { ProviderModelHealth } from "../../api";

export type ProviderModelDisplayState = "healthy" | "unhealthy" | "unknown";

export interface ProviderModelHealthRow {
  model: string;
  state: ProviderModelDisplayState;
  message: string;
  statusCode?: number;
  offline: boolean;
}

// Keep every selectable model visible and append models that were discovered
// by the monitor but automatically taken offline. Unhealthy rows sort first so
// a collapsed card still exposes the reason for its provider warning.
export function providerModelHealthRows(
  supportedModels: string[],
  checkedModels: ProviderModelHealth[] = [],
): ProviderModelHealthRow[] {
  const supported = new Set(supportedModels);
  const healthByModel = new Map<string, ProviderModelHealth>();
  checkedModels.forEach((health) => {
    const model = health.model.trim();
    if (model) healthByModel.set(model, health);
  });

  const names: string[] = [];
  const seen = new Set<string>();
  const add = (model: string) => {
    model = model.trim();
    if (!model || seen.has(model)) return;
    seen.add(model);
    names.push(model);
  };
  supportedModels.forEach(add);
  checkedModels.forEach((health) => add(health.model));

  return names
    .map((model, index) => {
      const health = healthByModel.get(model);
      const state: ProviderModelDisplayState = !health
        ? "unknown"
        : health.state === "healthy"
          ? "healthy"
          : "unhealthy";
      return {
        index,
        model,
        state,
        message: health?.message?.trim() || "",
        statusCode: health?.status_code,
        offline: !supported.has(model),
      };
    })
    .sort((left, right) => {
      const leftPriority = left.state === "unhealthy" ? 0 : 1;
      const rightPriority = right.state === "unhealthy" ? 0 : 1;
      return leftPriority - rightPriority || left.index - right.index;
    })
    .map(({ index: _index, ...row }) => row);
}
