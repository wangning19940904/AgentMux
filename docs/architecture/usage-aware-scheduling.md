# Usage-aware turn scheduling: one queue per framework account

Status: proposed · 2026-11-20 · branch `claude/usage-scheduler-0d8976` ·
phase 1 (§9) built, off by default

Every agent turn on a machine spends the same thing: the subscription of the
framework account it runs on, one Claude Code login or one Codex login. A
person's chat with an agent, a Super Agent's workers, orchestration tasks,
cron triggers and, soon, memory passes and LLM Guard reviews all draw on it.
Today nothing orders them. When an account is busy, a person's message waits
for whichever process the CLI or the provider happens to serve next, and when
it reaches its usage limit every queued Ticket starts, fails and is retried
or reported as failed.

This proposal puts one admission queue in front of each account. A turn waits
there for a place, by priority, and work that cannot succeed while the account
is at its usage limit waits for the reset instead of failing.

The idea is borrowed from Linger's model scheduling (one priority queue per
model endpoint, adaptive concurrency, preemption). AgentMux's resource is
different: not a GPU or an API endpoint but a subscription with a rolling
usage window, which says when it resets.

## 1. What stays out

- **Moving work to another framework.** A turn runs on the account its Agent
  names. The scheduler never switches a turn to another framework or another
  login, at a limit or otherwise. Choosing a different worker is the brain's
  decision, and placement already refuses `usage_limit` frameworks for new
  accepts ([framework-health.md](framework-health.md)).
- **Reordering a conversation.** The channel queue (`core/channel_queue.go`)
  and the per-conversation invocation lock keep turns of one conversation in
  order. The scheduler only decides when the next turn of a conversation may
  start.
- **Token budgets.** Phase 1 counts turns, not tokens.
- **Accounts on other machines.** Each daemon schedules its own accounts. A
  remote worker's turn is admitted by the machine it runs on (§4).

## 2. Where turns start

Every model turn on a machine passes through one function:
`Engine.observeSendWith` (`core/observation_runtime.go`), reached from

| Caller | Path | Origin today |
| --- | --- | --- |
| Channel chat (Feishu, Slack…), including a Super Agent brain bound to a channel | `channel_dispatch.go` → `streamTurnCard` / `streamTurnMessage` / `streamTurn` | `channel` |
| Meeting assistant | `meeting_commands.go` | `meeting` |
| Project runtimes from `config.toml` | `engine.go` → `streamTurn` | `channel` |
| Cron and webhook triggers | `trigger_exec.go` → `streamTurn` | `cron`, `webhook` |
| Agent-to-agent send | `sender.go` → `streamTurn` | the sender's |
| Direct invocations: HTTP API, OpenAI-compatible API, and in process | `invocation.go` → `invokeRuntime` | `api` |

The in-process invocation callers are where most automated load comes from:

- Super Agent router turns (`superagent/router.go`, `Service.route`), one per
  event: a person's message from a non-channel Source, a Ticket update, a
  timer, a supervision signal, a plan.
- Super Agent worker turns (`superagent/workers.go`, `Fleet.Run`), locally or
  on another machine through `POST /api/v1/invocations/stream`.
- Helpers a worker starts through bounded dispatch
  (`superagent/dispatch.go`, `runHelper`). The worker's turn stays running and
  polls for the helper's result.
- Orchestration tasks (`orchestration/service.go`).
- Setup of a Super Agent from the Console (`superagent/setup.go`).
- Later: memory passes and LLM Guard reviews.

Admitting at `observeSendWith` covers all of them with one hook. The session
is already started there (the CLI process may be running), but no model call
has been made.

## 3. What an account is

An account is a framework kind plus the sign-in that kind uses on this
machine:

- The kind is the Agent's `runtime_id` (`claudecode`, `codex`, `cursor`…).
  Framework health keys its records by the same value.
- The profile is what selects a different sign-in for that kind. An Agent's
  `env` can point a framework at another configuration or key:
  `CLAUDE_CONFIG_DIR`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
  `ANTHROPIC_BASE_URL` for Claude Code; `CODEX_HOME`, `OPENAI_API_KEY`,
  `OPENAI_BASE_URL` for Codex. When an Agent sets any of them, its profile is
  a short hash of those values (`core.AccountProfile`); the values themselves
  never leave the process. Without them the profile is empty: the machine's
  default sign-in.

So the account key is `claudecode` for the default login and
`claudecode#3f9a…` for an Agent with its own `CLAUDE_CONFIG_DIR`. Framework
health speaks for the default profile only, because its checks run in the
machine's own environment.

Tmux-backed sessions (`session_backend = tmux`) still run the Agent's
framework, so they count against the same account.

## 4. Priority classes

| Class | Turns |
| --- | --- |
| `interactive` | A person's chat turn on a channel or in a meeting; a direct API invocation (the Console's chat, the OpenAI-compatible API); a Super Agent router turn for a person's message or plan decision; Super Agent setup. Approvals are not turns in AgentMux (they are answers to a running turn), so they need no class. |
| `ticket` | Super Agent worker, helper and reviewer turns; router turns for Ticket updates and agent mail; orchestration tasks. |
| `scheduled` | Cron and webhook triggers; router turns for timers and supervision signals. |
| `background` | Memory passes and LLM Guard reviews, once they exist. |

A caller in process names the class with `core.WithTurnClass(ctx, class)`.
Without one, the class follows the message origin: `cron` and `webhook` are
`scheduled`, everything else is `interactive`, which keeps an unclassified
turn exactly as fast as today.

Over HTTP, a caller may name a class with the header `X-AgentMux-Turn-Class`
on `POST /api/v1/invocations[/stream]`. A header can only lower a turn from
the default `interactive`, which costs the caller and nobody else, so it needs
no extra permission. A Super Agent's remote worker sends `ticket`.

## 5. Admission

Each account has a queue per class and a count of running turns. A turn is
admitted when all of these hold:

1. **Priority.** No turn of a higher class is waiting on the account, and no
   earlier turn of its own class is. Within a class, turns start in arrival
   order.
2. **Limit.** Running turns on the account are fewer than the class's limit:
   `max_concurrent` for every class but `interactive`, and
   `max_concurrent + interactive_headroom` for `interactive`. The headroom is
   Linger's reserved last place, the other way round: automated work fills at
   most `max_concurrent` places and a person can always take one more, even
   when `max_concurrent` is 1. A `max_concurrent` of 0 means no limit.
3. **Hold.** The account is not held at a usage limit (§7), or the class is
   `interactive`.

A turn that waits keeps nothing: no place, no lock the scheduler knows of.
Cancelling its context removes it from the queue, and the next turn is
considered at once.

**Awaited turns.** A turn that a running turn waits for is admitted past the
limit, because making it wait for a place the waiting turn holds would
deadlock at a limit of one. Callers mark such turns with
`core.WithAwaitedTurn(ctx)`:

- a helper, which its worker's running turn polls for;
- a Super Agent router turn. The brain decides for every Ticket of its agent:
  it steers running workers, answers what they report and starts what depends
  on them, and a worker on the same account can be stuck until it does.

Both are bounded elsewhere: at most three running helpers per Ticket, and at
most `maxRouterEvals` (4) router turns at once per daemon. An awaited turn
still counts as running, so it delays other work, and it still respects a
usage-limit hold. This is Linger's "an approval borrows the reviewed Run's
place".

**Existing caps.** A Super Agent's `max_concurrency` limits its running
Tickets, and an orchestration's `max_concurrency` its running tasks. They stay
as they are and apply first: a Ticket counts against its agent from the moment
it is claimed, including while its turn waits for the account. The account
limit then decides, across all agents and orchestrations, which of the turns
that passed their own cap starts. The effective limit for one agent is the
smaller of the two; the account limit is what keeps two agents from together
exceeding what one login sustains.

**Time limits.** Phase 1 does not stop the clock while a turn waits. A
router's 10-minute timeout and a worker's 6-hour timeout include time in the
queue. When a turn's context ends while it waits, its error says what it
waited for ("waited for claudecode's usage limit to reset at 16:20"), not a
bare deadline. Phase 2 starts these timers at admission (§9).

## 6. Adaptive concurrency

A subscription signals overload in two ways: a usage-limit message with a
reset time, which §7 handles, and transient rate limiting or overload (`429`,
`overloaded_error`, "rate limit"), which a retry later survives. Phase 2 adds
an adaptive limit per account under `max_concurrent`:

- The adaptive limit starts at `max_concurrent`.
- A turn that fails with a rate-limit or overload error halves it (to at
  least 1) and freezes increases for one minute.
- While turns wait and finish without such errors, it rises by one per
  successful turn after the freeze, back to `max_concurrent`.
- A limit the account had to come down from is retried only after five
  minutes, doubling up to thirty, as Linger does, so an account at the edge
  does not flap.

Latency is a poor signal for subscriptions (turns run minutes and vary with
the task), so unlike Linger the limit does not react to slower turns.
`interactive_headroom` stays on top of the adaptive limit: a person is never
the one who has to wait for the account to recover from automated work.

The classifier for rate limits belongs in `framework/health`, beside
`ClassifyRunError`, which today deliberately does not count a generic 429. The
scheduler takes it as an injected function, as it does the usage-limit
classifier, so `core` keeps importing nothing else from the module.

## 7. At a usage limit

The scheduler learns that an account is at its limit from two places:

- **Framework health.** A `usage_limit` record with a `resets_at` in the
  future holds the kind's default profile. The scheduler reads it when a
  holdable turn asks for admission.
- **Its own turns.** A turn whose error is a usage-limit message
  (`framework.IsUsageLimit`) holds its account until the reset the message
  names (`framework.UsageLimitReset`), or for 15 minutes when it names none.
  This works with periodic health checks off, and for profiles health does
  not check.

While an account is held:

- `interactive` turns are admitted as usual. The person gets the framework's
  own limit message, as today, and can decide to wait or switch agents.
  Nothing is moved for them.
- Every other class waits in its queue. At the reset the hold ends and the
  queue drains in priority order, within the limit.
- A turn that finishes without error on the account ends the hold early: the
  limit has evidently lifted.
- A hold with no known reset ends after 15 minutes; the next turn finds out
  whether the limit is still there and, if so, holds again.

Nothing is silently moved to another framework. A Super Agent's brain still
sees the framework as `usage_limit` in its roster health and may accept new
work on another worker; that is a visible decision in the Ticket's history.

## 8. Console visibility

`GET /api/v1/frameworks/scheduling` (beta, administrator) returns this machine's
settings and, per account in use:

```json
{
  "settings": {"enabled": true, "max_concurrent": 2, "interactive_headroom": 1},
  "accounts": [{
    "account": "claudecode", "runtime_id": "claudecode",
    "limit": 2, "running": 2,
    "running_by_class": {"interactive": 1, "ticket": 1},
    "held_until": "2026-11-20T16:20:00Z", "hold_reason": "usage limit",
    "queued": [{"class": "ticket", "agent_id": "…", "conversation_id": "…",
                "waiting_since": "2026-11-20T14:02:11Z", "held": true}]
  }]
}
```

`PUT /api/v1/frameworks/scheduling/settings` changes the settings, stored in the
setting `turn_scheduler_v1`. Both routes are reached on other machines
through the fleet query and execute routes, like framework health.

Phase 2 shows this in the Console:

- On the frameworks page, per account: running and waiting turns by class,
  and a "held until 16:20 (usage limit)" badge.
- On a Ticket: "waiting for claudecode (held until 16:20)" instead of a
  running spinner with no progress, so supervision does not report a stall
  either. Supervision's stall clock should start at admission.
- On an orchestration task: the same waiting state.

## 9. Phases

**Phase 1 (this branch).**

- `core.TurnScheduler`: per-account queues, the admission rule of §5,
  awaited turns, usage-limit holds and their release at the reset (§7), and
  a snapshot for §8.
- The hook in `Engine.observeSendWith`; account keys from `runtime_id` and
  the Agent's env.
- Classes set by Super Agent routers, workers, helpers and remote workers,
  orchestration tasks and setup; the HTTP header.
- Settings and the two routes. No migration: the settings are one row in the
  settings table.
- **Off by default.** With `enabled` false every turn is admitted at once and
  nothing is held, so behaviour is unchanged. Turning it on changes nothing
  either until the account is contended or at a limit.

**Phase 2.**

- Adaptive concurrency (§6).
- Time limits and supervision counting from admission (§5, §8).
- The Console views of §8.
- Per-account `max_concurrent` overrides, once accounts can be named in the
  Console.
- Classes for memory passes and LLM Guard reviews, as those land
  (`background`, below scheduled work).

**Phase 3, open: yielding running turns.** Linger lets high-priority work
make a lower-priority run yield and resume in the same session. For AgentMux
that means interrupting a worker's turn when a person is waiting, then
continuing its native session when a place frees. Questions before building
it:

1. Can every framework resume an interrupted turn without losing work?
   Claude Code and Codex resume sessions; whether a turn interrupted mid-tool
   call resumes cleanly needs testing per framework.
2. How does a yield look to the brain and to supervision: a Ticket update, a
   new state, nothing?
3. Is yielding needed at all once `interactive_headroom` exists? A person
   waits only when the headroom itself is full, which is rare with a few
   people per account.
4. What a remote worker's yield means when its turn runs on another daemon.

## 10. Open questions

1. Should a direct API invocation default to `interactive`? It does in phase 1
   so the Console's chat and the OpenAI-compatible API stay as fast as
   today. Automation that calls the API can send the header.
2. Should `max_concurrent` default to something when `enabled` is first
   turned on? Phase 1 uses 2: one automated turn per account is too
   cautious for a Max plan, more invites rate limiting on a Pro plan.
3. Should held Tickets be visible to the brain, so it can tell the person
   "your Ticket waits for the limit to reset at 16:20"? Probably, through the
   roster health it already reads.
