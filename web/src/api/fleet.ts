import { activeMachineScope, fleetAdminQuery, fleetExecute, fleetQuery } from "./client";
import type {
  FleetBatchResult,
  FleetOperation,
  FleetOperationResult,
  MachineTarget,
  TargetMetadata,
} from "./types";

export type Targeted<T> = T & TargetMetadata;
export { FLEET_WARNING_EVENT, currentFleetWarnings } from "./fleetWarnings";

export function fleetMode() {
  return activeMachineScope() === "all";
}

export async function fleetReadArray<T>(path: string, key = "data"): Promise<Targeted<T>[]> {
  const batch = await fleetQuery<T[]>([{ key, path }]);
	return fleetArrayFromBatch(batch, key);
}

export async function fleetAdminReadArray<T>(path: string, key = "data"): Promise<Targeted<T>[]> {
	const batch = await fleetAdminQuery<T[]>([{ key, path }], ["all"]);
	return fleetArrayFromBatch(batch, key);
}

function fleetArrayFromBatch<T>(batch: FleetBatchResult<T[]>, key: string): Targeted<T>[] {
  const out: Targeted<T>[] = [];
  let success = 0;
  const errors: string[] = [];
  for (const target of batch.targets) {
    const response = operationFor<T[]>(target.responses, key);
    if (response?.ok && response.data == null) {
      success += 1;
      continue;
    }
    if (!response?.ok || !Array.isArray(response.data)) {
      errors.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    for (const item of response.data) {
      if (item && typeof item === "object") {
        out.push({ ...item, target_id: target.target.id, target_name: target.target.name });
      }
    }
  }
  if (!success && errors.length) throw new Error(errors.join("; "));
  return out;
}

export async function fleetReadValues<T>(path: string, key = "data"): Promise<T[]> {
  const batch = await fleetQuery<T[]>([{ key, path }]);
  const values: T[] = [];
  let success = 0;
  const errors: string[] = [];
  for (const target of batch.targets) {
    const response = operationFor<T[]>(target.responses, key);
    if (response?.ok && response.data == null) {
      success += 1;
      continue;
    }
    if (!response?.ok || !Array.isArray(response.data)) {
      errors.push(`${target.target.name}: ${response?.error || "unavailable"}`);
      continue;
    }
    success += 1;
    values.push(...response.data);
  }
  if (!success && errors.length) throw new Error(errors.join("; "));
  return [...new Set(values)];
}

export async function fleetGet<T>(path: string, targetID: string, key = "data") {
  const batch = await fleetQuery<T>([{ key, path }], [targetID]);
  const result = batch.targets[0];
  const response = result && operationFor<T>(result.responses, key);
  if (!result || !response?.ok || response.data === undefined) {
    throw new Error(`${result?.target.name || targetID}: ${response?.error || "unavailable"}`);
  }
  const value = response.data;
  if (Array.isArray(value)) {
    return value.map((item) => item && typeof item === "object"
      ? { ...item, target_id: result.target.id, target_name: result.target.name }
      : item) as T;
  }
  return value && typeof value === "object"
    ? { ...value, target_id: result.target.id, target_name: result.target.name } as T & TargetMetadata
    : value;
}

export async function fleetWrite<T>(operation: FleetOperation, targetIDs: string[]): Promise<FleetBatchResult<T>> {
  return fleetExecute<T>([operation], targetIDs);
}

export async function fleetCall<T>(
  operation: FleetOperation,
  targetIDs: string[],
  options: { confirm?: boolean } = {},
) {
  const multiTarget = targetIDs.includes("all") || targetIDs.length > 1;
  if (options.confirm !== false && multiTarget && operation.method && operation.method !== "GET" && typeof window !== "undefined") {
    const chinese = document.documentElement.lang.toLowerCase().startsWith("zh");
    const accepted = window.confirm(chinese
      ? "确定把这个操作应用到所有选中的机器吗？每台机器会独立执行，并逐机报告失败。"
      : "Apply this operation to all selected machines? Each machine will run independently and partial failures will be reported.");
    if (!accepted) throw new Error("Fleet operation cancelled.");
  }
  const batch = await fleetWrite<T>(operation, targetIDs);
  const successes: Array<T & TargetMetadata> = [];
  const errors: string[] = [];
  for (const target of batch.targets) {
    const response = operationFor<T>(target.responses, operation.key);
    if (response?.ok && response.data !== undefined) {
      const value = response.data;
      successes.push(value && typeof value === "object"
        ? { ...value, target_id: target.target.id, target_name: target.target.name }
        : value as T & TargetMetadata);
    } else {
      errors.push(`${target.target.name}: ${response?.error || "operation failed"}`);
    }
  }
  if (!successes.length) throw new Error(errors.join("; ") || "Fleet operation failed.");
  return { first: successes[0], successes, errors, batch };
}

export function writeTargetIDs(targetID?: string) {
  if (targetID) return [targetID];
  return activeMachineScope() === "all" ? ["all"] : [activeMachineScope()];
}

export function singleTargetID(targetID?: string) {
  if (targetID) return targetID;
  const scope = activeMachineScope();
  if (scope === "all") throw new Error("Choose one machine for this interactive operation.");
  return scope;
}

export function operationFor<T>(responses: FleetOperationResult<unknown>[], key: string) {
  return responses.find((response) => response.key === key) as FleetOperationResult<T> | undefined;
}

export function successfulFleetData<T>(batch: FleetBatchResult<unknown>, key: string) {
  const out: Array<{ target: MachineTarget; data: T }> = [];
  for (const result of batch.targets) {
    const response = operationFor<T>(result.responses, key);
    if (response?.ok && response.data !== undefined) out.push({ target: result.target, data: response.data });
  }
  return out;
}

export function fleetErrors(batch: FleetBatchResult<unknown>, key: string) {
  return batch.targets.flatMap((result) => {
    const response = operationFor(result.responses, key);
    return response?.ok ? [] : [`${result.target.name}: ${response?.error || "unavailable"}`];
  });
}
