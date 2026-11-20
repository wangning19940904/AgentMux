import { fleetExecute, fleetMonitorQuery } from "./client";
import { operationFor } from "./fleet";
import type { FrameworkInstallResult, FrameworkOperationJob, OperationProgress } from "./types";

// Framework installs and updates on a chosen machine run as background
// operations on that machine. The fleet relay cannot stream, so the Console
// starts one and then polls it for its phase, the command's latest output
// line and its log until it settles.

export class FrameworkOperationsUnsupported extends Error {
  constructor() {
    super("This machine runs an AgentMux version without background framework operations.");
  }
}

export interface FrameworkOperationBody {
  kind: string;
  action: "install" | "update" | "uninstall";
  acknowledge_internal?: boolean;
  confirmed_command?: string;
}

const POLL_INTERVAL_MS = 1200;
// Polls ride the relay; a node can miss several in a row while its link
// reconnects without the operation itself being in trouble.
const MAX_POLL_FAILURES = 25;

export async function startFrameworkOperationJob(body: FrameworkOperationBody, targetID: string): Promise<FrameworkOperationJob> {
  const batch = await fleetExecute<FrameworkOperationJob>([{ key: "operation", method: "POST", path: "/api/v1/frameworks/operations", body }], [targetID]);
  const target = batch.targets[0];
  const response = target && operationFor<FrameworkOperationJob>(target.responses, "operation");
  if (response?.ok && response.data) return response.data;
  // An older machine has no such route: a 404/405, or the web app's HTML
  // fallback, which the relay reports as an invalid JSON answer.
  if (response && (response.status === 404 || response.status === 405
    || (response.status === 502 && /invalid JSON/i.test(response.error || "")))) {
    throw new FrameworkOperationsUnsupported();
  }
  throw new Error(`${target?.target.name || targetID}: ${response?.error || "operation failed"}`);
}

export async function readFrameworkOperationJob(id: string, targetID: string): Promise<FrameworkOperationJob> {
  const batch = await fleetMonitorQuery<FrameworkOperationJob>([{ key: "operation", path: `/api/v1/frameworks/operations?id=${encodeURIComponent(id)}` }], [targetID]);
  const target = batch.targets[0];
  const response = target && operationFor<FrameworkOperationJob>(target.responses, "operation");
  if (response?.ok && response.data) return response.data;
  const error = new Error(`${target?.target.name || targetID}: ${response?.error || "unavailable"}`) as Error & { status?: number };
  error.status = response?.status;
  throw error;
}

export function jobProgress(job: FrameworkOperationJob): OperationProgress {
  const started = Date.parse(job.started_at);
  return {
    phase: job.progress.phase,
    detail: job.progress.detail,
    percent: job.progress.percent,
    started_at: Number.isFinite(started) ? started : undefined,
    log: job.log,
  };
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// followFrameworkOperationJob polls an operation until it settles and
// resolves with its result.
export async function followFrameworkOperationJob(
  job: FrameworkOperationJob,
  targetID: string,
  onProgress?: (progress: OperationProgress) => void,
): Promise<FrameworkInstallResult> {
  let current = job;
  let failures = 0;
  let lastError = "";
  onProgress?.(jobProgress(current));
  while (current.state === "running") {
    await sleep(POLL_INTERVAL_MS);
    try {
      current = await readFrameworkOperationJob(job.id, targetID);
      failures = 0;
      onProgress?.(jobProgress(current));
    } catch (error) {
      if ((error as { status?: number }).status === 404) {
        throw new Error("The machine no longer knows this operation (AgentMux restarted while it ran). Check the framework's version to see whether it finished.");
      }
      failures += 1;
      lastError = error instanceof Error ? error.message : String(error);
      onProgress?.({ ...jobProgress(current), detail: `Waiting for the machine to answer (${failures}/${MAX_POLL_FAILURES}): ${lastError}` });
      if (failures >= MAX_POLL_FAILURES) {
        throw new Error(`Lost contact with the machine while the operation was running: ${lastError}`);
      }
    }
  }
  return current.result ?? {
    kind: current.kind,
    action: current.action,
    ok: current.state === "succeeded",
    log: current.log,
    error: current.state === "failed" ? "operation failed" : undefined,
  };
}
