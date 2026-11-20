# Super Agent turns across an AgentMux restart

Status: phase 1 (remote workers) implemented; phase 2 (local workers) proposed.

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
2. **Detached local Codex app-server (phase 2).** Start one app-server per
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
  queue with the recovery note, as before. Tickets without a `Turn`, which
  includes all local workers, keep the old recovery.

## Follow-ups

- Phase 2 (option 2) for local Codex workers, which the bot on
  bytedance-macmini uses as well as ecs_cn.
- A remote daemon that itself restarts still loses its turns. Phase 2 on that
  machine fixes it there too.
- Updates could wait a short grace period (option 4) for turns about to end
  before the swap. It is cheap and complements phase 2.
