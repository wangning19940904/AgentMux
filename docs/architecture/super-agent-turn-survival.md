# Super Agent turns across an AgentMux restart

Status: phase 1 (remote workers) and phase 2 (local Codex workers) implemented;
local Claude Code (phase 2b) proposed.

## Problem

The user's Feishu bot runs on bytedance-macmini, whose headless `amux client`
is the primary hub. It updates by binary swap and restart, and it can crash.
Before this change, each restart cut every worker turn in flight:

- `Service.Stop` cancels every running turn. The Ticket stays `running` in the
  store, and the next `Start` re-queues it with `recoveryNote` in front of its
  inputs. The worker resumes the same conversation, so it keeps its context,
  but the turn starts over and the worker has to work out where it was.
- A **local** worker (Codex app-server, `claude -p`) is a child of the
  AgentMux process. Its turn is interrupted on shutdown and dies with a crash.
- A **remote** worker runs on another daemon, which ran the invocation under
  the HTTP request's context. When the hub's stream dropped, whether from a
  restart, a crash, a tunnel blip or the two-minute idle check, the remote
  daemon cancelled the turn. A `409` on the next attempt meant it was still
  winding down.

What a person sees in the Feishu thread today: the work keeps its 'on it'
reaction or status message. After the restart the Ticket briefly shows as
queued, then running again. The answer arrives later than it would have,
because the turn ran twice. The worker's reply can mention the restart because
the recovery note asked it to check its state. Nobody is told that anything
happened, which is right. They only notice the delay and, sometimes, the
wording. The progress status message (branch
`claude/task-progress-updates-5ac318`) lives on the Trigger and is rebuilt
from the store after a restart, so it survives either way. With the change
below, a rejoined Ticket never leaves `running`, so the message's phase does
not change at all.

## What the frameworks allow

| Path | Can the turn outlive AgentMux? | Can AgentMux rejoin it? |
|---|---|---|
| Remote worker, any framework | Yes, if the remote daemon stops tying the turn to the request | Yes, by invocation id: the remote daemon keeps the events |
| Local Codex (app-server) | Only if the app-server is not our child on stdio. `codex app-server --listen unix://…` or `ws://…`, or the managed `codex app-server daemon`, can run on its own | **Yes, verified with codex-cli 0.171.0**: after the client disconnected mid-command, a new client's `thread/resume` reported the turn `inProgress` and received its remaining notifications through `turn/completed` |
| Local Claude Code (`claude -p`) | Only if started detached (own session, stdout to a file rather than a pipe) | Partly: tail the spooled stream-json file and wait for the pid. No protocol to resume a live turn; `--resume` only continues after it ends |
| Terminal backend (tmux) | Yes, already (`DetachableAgentSession`) | Yes, but Super Agent workers use the structured backend, which this does not cover |

`core.DetachableAgentSession` is implemented only by the tmux terminal
runtime. It keeps the process alive. It does not rejoin a structured turn's
event stream, which is what a Ticket needs to get its answer.

## Options

1. **Detachable remote invocations (chosen, phase 1).** The remote daemon runs
   a turn the caller marks `detach` independently of the request, keeps its
   events by sequence number for 30 minutes after it ends, and offers
   `GET /api/v1/invocations/{id}/events?after=N` to rejoin and
   `POST /api/v1/invocations/{id}/cancel` to stop it. The hub records the
   invocation id on the Ticket when the turn starts. On restart it rejoins
   instead of re-queueing. Stopping a Ticket, or its deadline, cancels the
   turn explicitly. A daemon shutting down does not. This change is entirely
   within AgentMux, works for every framework, and also covers tunnel blips
   and idle-timeouts that used to restart turns.
2. **Detached local Codex app-server (chosen, phase 2).** Start one app-server per
   Codex Agent configuration on a unix socket in the state directory, in its
   own session, so it outlives AgentMux. Record the thread and turn ids on
   the Ticket. On restart, reconnect, `thread/resume` and follow the turn to
   `turn/completed`. Needs: app-server lifecycle (find or start, version
   checks, restart only when idle after a Codex update), a "rejoin" entry in
   the Codex adapter that does not start a turn, and a core invocation path
   that adopts a running turn rather than calling `Send`. It covers the
   bot's local Codex workers on bytedance-macmini.
3. **Spooled local Claude Code (phase 2b).** Run `claude -p` detached with
   stream-json going to a file in the Ticket's directory. Record the pid and
   process start time. On restart, tail the file from the start and wait for
   the process to exit. Simpler than option 2 but Claude-specific.
4. **Drain on planned restarts.** On an update, stop taking new work and wait
   for turns to end before the swap. Turns can last hours, so updates would
   wait just as long, and a crash gains nothing. Rejected as the main path.
   Still useful as a short grace period before a swap.
5. **Run local workers through a separate local daemon.** Treat the hub's own
   workers as remote workers of a second process on the same machine. This
   reuses phase 1 but adds a second long-lived process per machine to run,
   update and supervise. Rejected for now.

## Phase 1 design (implemented)

Remote daemon (`liveturns` package, wired into `server`):

- `POST /api/v1/invocations/stream?detach=1` starts the turn under
  `context.WithoutCancel(request)`, so request values such as telemetry are
  kept while its cancellation is ignored. The flag is a query parameter, like
  the steer route these routes are hub-to-node internals, so the public
  `InvocationRequest` contract does not change. The first event carries
  the invocation id. The stream is served from the turn's event log with SSE
  `id:` lines, so a client that leaves does not stop the turn.
- `GET /api/v1/invocations/{id}/events?after=N` replays events after `N`, then
  follows live events until the terminal one. The response header
  `X-AgentMux-Invocation-Seq` gives the last sequence number already logged
  when the client joined, so a client can tell replay from live events.
  Unknown or expired ids return `404`.
- `POST /api/v1/invocations/{id}/cancel` cancels the turn.
- A turn's log keeps the latest 4096 events. A finished turn is kept 30
  minutes for its caller to collect. Running turns end when the daemon stops.
- The worker relay through the hub allows both new routes.
- A daemon without this change ignores `detach` and returns 404 to a rejoin.
  A hub relay without it returns 403. The hub treats any 4xx except 409 on a
  rejoin as "turn gone" and falls back to the old recovery. Only machines that
  run worker turns need the new build for turns to survive; the hub needs it
  to rejoin them.

Hub (`superagent`):

- `Fleet.Run` for a remote worker asks for `detach` and reports the invocation
  id through `RunOptions.Started`. The service saves it on the Ticket as
  `Turn{InvocationID, HostID, Delivered, StartedAt}`. `Delivered` is how many
  queued inputs the turn was given, so inputs that arrive later stay queued.
- If the stream breaks while the turn context is live, `Fleet` rejoins the
  turn from the last sequence number for up to three minutes. It gives up on
  `404`, and the error is retryable as before.
- When the turn's context ends: if `RunOptions.Detach`, the daemon's context,
  has also ended, AgentMux is shutting down and the remote turn keeps
  running. Otherwise, because of a stop or the deadline, `Fleet` cancels it on
  the remote daemon.
- `Service.Start` keeps a running Ticket that has a `Turn` on a remote worker
  `running` and rejoins it through `TurnResumer.Resume`, with no recovery
  note, counted in the agent's concurrency, and with supervision's turn
  start restored. Replayed events do not go to the progress log twice. If the
  remote no longer has the turn (`ErrTurnGone`), the Ticket goes back to the
  queue with the recovery note, as before. Tickets without a `Turn` keep the
  old recovery.

## Phase 2 design (implemented): local Codex workers

Detached app-server (`agent/cliagents/codex_detached.go`):

- A Codex Agent created with `core.AgentConfigKeepTurns` runs its threads on a
  detached app-server: `codex app-server --listen unix://<dir>/sock`, started
  with `Setsid` so the signals that stop AgentMux, its process group or its
  launchd job do not reach it. Its output goes to `<dir>/server.log`. AgentMux
  reaps it while it lives; afterwards it belongs to init.
- Directory: `<UserConfigDir>/agentmux/codex-servers/<configuration>/<generation>/`
  with `sock`, `pid`, `used` and `server.log`. The configuration is a hash of
  the resolved binary and the Agent's environment. The generation is a hash
  of `codex --version` (cached 30 s; an npm-installed Codex is a script whose
  own file does not change on update) and the app-server arguments, which
  include the OTel settings. The OTel ingest token is persisted, so the
  generation is stable across AgentMux restarts.
- The unix socket speaks WebSocket (verified with codex-cli 0.171.0): one
  JSON-RPC message per text frame. Codex keeps the socket itself under
  `/tmp/codex-daemon-<uid>/` and links the requested path to it, so AgentMux
  dials the link's target and the path length limit does not apply.
- Finding or starting: under a process mutex and a `flock` on the
  configuration, AgentMux dials the current generation's socket and starts
  the app-server only when nothing listens (waiting up to a minute for it to
  listen). Each Codex Agent, which the invocation path creates per turn, opens
  its own connection; closing it leaves the server and its turns running.
- Restart only when idle: after a Codex update, new turns get a new
  generation. Every acquire stops idle app-servers of older generations, and
  those of configurations unused for a day. Idle means no loaded thread is
  `active` (`thread/loaded/list`, then `thread/read`). A server with a running
  turn is kept until a later acquire finds it idle. A stale pid is checked
  against the process's command line before it is signalled.
- Codex Desktop threads (`codex-app`), Trae and Windows keep the stdio child.

Codex adapter:

- A turn's context ending interrupts the turn (`turn/interrupt`) as before,
  except on a detached app-server when `core.TurnOwnerEnded(ctx)`: the turn's
  owner, the context `core.WithTurnDetach` attached (the Super Agent
  service's), has ended, so AgentMux is shutting down. A Ticket stop or
  deadline ends only the turn's context and still interrupts it.
- Events of a turn on a detached app-server carry the metadata
  `kept_turn=true` with the thread and turn ids.
- `RejoinTurn(ctx, workDir, {SessionID: thread, TurnID: turn})` finds the live
  app-server of the configuration that has the thread loaded, registers a
  session for the thread before calling `thread/resume {threadId}` (so no
  notification after the resume is lost), and looks up the turn in the
  response: `inProgress` is followed to `turn/completed` with the usual event
  mapping, the answer seeded from the turn's last agent message; `completed`
  (it finished while AgentMux was away) gives its answer at once. Anything
  else, or a thread no live server has loaded but whose record says
  `inProgress` (its server died), is `core.ErrInvocationTurnGone`.

Core invocation:

- `InvocationRequest.KeepTurn` (in-process only) creates the Agent with
  `AgentConfigKeepTurns`.
- `ConnectService.RejoinInvocation(ctx, {AgentID, ConversationID, Turn}, sink)`
  (`core.InvocationRejoiner`) creates the Agent the same way and calls its
  `RejoinTurn` (`core.TurnRejoiningAgent`). It holds the conversation's
  invocation slot, so a new turn on it waits, and tracks the session, so
  steering reaches a rejoined turn. Events and the outcome are reported as by
  `InvokeStream`, through the same `followInvocationTurn`. A framework that
  keeps no turns, or a turn that is gone, returns `ErrInvocationTurnGone`
  before any event.

Super Agent:

- `Fleet.Run` for a local worker sets `KeepTurn` (whenever `RunOptions.Started`
  is set, as the service does) and wraps the turn's context with
  `WithTurnDetach(RunOptions.Detach)`. The first event marked `kept_turn`
  reports `Turn{SessionID, TurnID}` through `Started`; the service saves it on
  the Ticket with `Delivered` and `StartedAt`, as for a remote turn. A
  framework that keeps no turns (Claude Code) never marks an event, so its
  Tickets record no `Turn` and keep the old recovery.
- `canRejoin` covers a local Ticket whose `Turn` has a session and turn id;
  `Fleet.Resume` rejoins it through `RejoinInvocation`. `ErrTurnGone` (the
  app-server or the turn is gone, for example after a reboot) sends the
  Ticket back to the queue with the recovery note.
- Tests: `TestLocalTurnContinuesAcrossARestartMidTurn`,
  `TestRestartFallsBackWhenTheLocalTurnIsGone` and
  `TestStoppingATicketInterruptsItsKeptLocalTurn` (superagent); a fake
  app-server, the test binary run as `codex`, in
  `agent/cliagents/codex_kept_turn_test.go` keeps a turn across a client
  reconnect, a Codex update and a stop. Verified once against codex-cli
  0.171.0: a turn running `sleep 8` was left running when its owner context
  ended, and a new Agent rejoined it and received the tool call and the
  final answer.

Known limits:

- A server request that needs a person (an approval) made while no AgentMux
  is connected: whether Codex sends it again to the rejoining client is not
  verified. Super Agent workers normally run without approvals.
- Usage of a rejoined turn counts only what was reported after the rejoin.
- The local `liveturns` of a remote daemon are in memory, so a remote daemon
  that itself restarts still loses the hub's turn even though its Codex
  app-server kept it (see follow-ups).

## Follow-ups

- A remote daemon that itself restarts still loses its turns: the hub knows
  only the invocation id, which lives in the daemon's memory. The detached
  invocation could set `KeepTurn` and send its `kept_turn` thread and turn ids
  to the hub, which would rejoin through the daemon's `RejoinInvocation` when
  the invocation id is unknown.
- Phase 2b (option 3) for local Claude Code workers.
- Updates could wait a short grace period (option 4) for turns about to end
  before the swap. It is cheap and complements phase 2.
