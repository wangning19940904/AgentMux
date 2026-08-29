import type { FrameworkAuthStatus, FrameworkLoginResult } from "../../api";

export type FrameworkLoginFlowState = "waiting" | "succeeded" | "failed" | "cancelled";

export type FrameworkLoginFlow = FrameworkLoginResult & {
  state: FrameworkLoginFlowState;
  error?: string;
  codeSubmitted?: boolean;
  reAuthenticate?: boolean;
};

export interface FrameworkLoginPoll {
  auth: FrameworkAuthStatus;
  lifecycleActive: boolean;
  lifecycleState: string;
  lifecycleError: string;
  endedMessage: string;
}

// resolveFrameworkLoginFlow is the single state transition table used by the
// polling hook. Stale sessions and terminal flows are immutable.
export function resolveFrameworkLoginFlow(
  flow: FrameworkLoginFlow | undefined,
  sessionID: string,
  poll: FrameworkLoginPoll,
): FrameworkLoginFlow | undefined {
  if (!flow || flow.session_id !== sessionID || flow.state !== "waiting") return flow;
  if (poll.lifecycleState === "cancelled") {
    return { ...flow, state: "cancelled", error: "" };
  }
  if (poll.lifecycleState === "failed") {
    return { ...flow, state: "failed", error: poll.lifecycleError || poll.endedMessage };
  }
  if (poll.lifecycleState === "succeeded" || poll.auth.state === "authenticated" && !flow.reAuthenticate) {
    return { ...flow, state: "succeeded", error: "" };
  }
  if (!poll.lifecycleActive && poll.lifecycleState === "unknown") {
    return { ...flow, state: "failed", error: poll.endedMessage };
  }
  return flow;
}
