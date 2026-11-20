# Agents

AgentMux has one kind of agent. An agent has an identity, a home with its
memory and Tickets, a **brain**, its **sources**, and a **worker policy**. The
brain answers directly when it can and starts workers, as Tickets, when it
cannot. Older versions had two kinds: regular Agents bound to channels, and
Super Agents, persistent routers. Both are now this one kind, and the rest of
this document still says "Super Agent" for the parts that only routed agents
used before. The design follows DeepSeek's
[dsh-superagent](https://github.com/deepseek-ai/dsh-superagent-prototype)
prototype, simplified to run inside the AgentMux daemon.

## One kind of agent

- **Record.** An agent is a `super_agents` row (`core.SuperAgent`).
  `router_agent_id` names its brain: an Agent instance (`agent_instances`) with
  the framework, model, approval mode, working directory, MCP servers and skills
  the brain runs with.
- **Creating an agent** in the Console or with `POST /api/v1/agent-instances`
  saves the brain and an agent record with **the same id**, in act mode and with
  no worker policy. Channel bindings, grants, `/api/v1/invocations` and
  `/v1/responses` keep naming that id. Nothing creates a regular agent any more.
- **Upgrading.** When the daemon starts, every Agent instance that is no agent's
  brain gets a record with its id: act mode, enabled as the instance was, and no
  workers. Router agents (`sar_*`) and auto workers (`saw_*`) belong to their
  agent and get none, and an ordinary Agent a Super Agent already routed with
  stays that Super Agent's brain. The step only inserts, so starting again
  changes nothing and no instance, channel binding, conversation or grant changes.
- **Channel sources.** Each channel bound to the brain (`channels.agent_id`) is a
  source of kind `channel`, with id `channel:<channel id>`. It is read from the
  binding every time and never stored; bind channels in the agent's settings. The
  channel's own reply scope decides who reaches the brain, exactly as before:
  there is no allowed list, and observe mode does not hold back the brain's reply.
- **Chats run through the channel engine.** A message on a bound channel is a
  normal channel turn, with streaming replies, the per-conversation queue,
  steering, approval cards and `/` commands. When the agent has a worker policy
  (worker frameworks or a roster), the engine puts a brief before the message: the
  roster, the conversation's Tickets, the operator's rules, the memory index and
  how to start a worker. The brain ends its reply with an action block to act; the
  block is hidden from the chat while the reply streams, and AgentMux carries it
  out after the turn completes, with the same checks as a router's. A turn that
  is stopped or times out starts nothing. An agent with no worker policy gets no
  brief, so its turns are what they were before agents could start workers.
- **Tickets from a chat** report back to that chat: when a worker finishes a
  turn, the brain gets a ticket update, as a router turn outside the chat's
  conversation, and its reply is posted in the chat's thread.
- **Deleting** an agent with `DELETE /api/v1/agent-instances` or
  `DELETE /api/v1/super-agents` stops its Tickets, removes its records and its own
  brain, and leaves its channels without an agent. Its home stays on disk.
- **The Console** has one Agents page. Each row shows what the agent does besides
  chatting, and opens its workers, triggers and Tickets. Agents that answer with a
  router agent, as Super Agents did, are rows of their own.

## Flow

```
Trigger source -> gate (allowed list) -> stored once -> router turn -> reply, and an action
  accept  -> new Ticket -> worker (local or remote) -> reply where it came from
  attach  -> input to a Ticket on the same thread
  stop    -> cancel a Ticket on the same thread
  none    -> only the router's reply, if it wrote one ("answered"), or nothing ("ignored")
```

An addressed event from a sender who is not allowed is recorded with its reason, and stops there. Ordinary traffic that addresses nobody and belongs to no Ticket is not stored.

When no available worker can do the work, the router tells the sender. An action the host rejects, such as a worker that is not on the roster, fails the event and also tells the sender.

## The router's turn

- **One conversation per thread.** Events on a thread go, one at a time, to the same
  router conversation, so it remembers what was said there. What should outlive a thread
  belongs in its memory.
- **The charter comes with every event**, not as a system prompt, which Cursor and Gemini
  ignore. It says what the router is for, what its directory holds, how to keep its
  memory, how to treat untrusted text, and how to answer. The operator's routing rules,
  the roster, the thread's Tickets with their folders, `memory/MEMORY.md` (up to 8,000
  characters) and the event follow it. The charter is built from prompt parts; see
  [Prompt parts](#prompt-parts).
- **The final message is the reply**, sent where the event came from. An empty message
  sends nothing. To have AgentMux act, the router ends the message with one block, which
  is removed before the reply is sent:

  ```
  <agentmux-action>
  {"action":"accept","worker_id":"auto:codex","title":"…","instructions":"…","reason":"…"}
  </agentmux-action>
  ```

  `accept` starts a Ticket, `attach` passes the event to a Ticket on the thread with the
  router's optional note, and `stop` stops one. An `accept` may say what done means, and
  `review` judges a Ticket whose worker finished a turn; see
  [Owning a Ticket](#owning-a-ticket). `follow_up` and `cancel_timer` set
  and withdraw a wake-up; see [Follow-ups](#follow-ups). An `accept` may say where it
  may run (`machine_policy`, see [Placement](#placement)), and `plan` proposes several
  Tickets for the requester's approval; see
  [Plans](#plans-cost-tiers-and-remembered-approvals). `reason` goes to the audit
  log. A JSON-only answer in the form routers used before is still read as an action.
- **Memory** is files in `memory/`, indexed by `memory/MEMORY.md`, which the router
  keeps. A read-only router can read it but not change it.

## Follow-ups

An agent can act because time passed. The router sets a follow-up for itself, and
AgentMux wakes it on the same thread when the follow-up is due:

```
<agentmux-action>
{"action":"follow_up","in":"18h","note":"Check whether Li approved the migration.","ticket_id":"tkt_…"}
</agentmux-action>
```

- **When.** `in` takes minutes, hours or days (`30m`, `18h`, `3d`, `1d12h`). `at`
  takes a time with an offset, such as `2026-11-15T09:00:00+08:00`; a time without
  one is read in the daemon's zone. Exactly one is given. Every event tells the
  router the daemon's time as `NOW`.
- **What.** `note` is required: it is what the router reads when it is woken.
  `ticket_id` is optional and names the Ticket the follow-up is about.
- **A due follow-up is an event**, never a turn of its own. AgentMux stores it as a
  Trigger of kind `timer` on the thread it was set on, so it runs in that thread's
  router conversation, in order with the thread's other events, and the router's
  reply goes where the thread's replies go. An empty reply sends nothing. A
  follow-up that fires more than two minutes late says how late it is.
- **Still set.** Each event lists the follow-ups still set on its thread under
  `TIMERS ON THIS THREAD`. `{"action":"cancel_timer","timer_id":"stm_…"}` withdraws
  one of them; a timer on another thread is refused, and one that already fired or
  was cancelled is left as it is.
- **Fired once.** A daemon that stops while a follow-up fires stores no second
  event when it starts again. Follow-ups that came due while the daemon was down,
  or while the agent was disabled, fire late.
- **Stopping a Ticket** cancels the follow-ups about it.
- **Observe mode** records "would follow_up" and sets nothing.
- **The Console** lists an agent's follow-ups between its Tickets and its events,
  once it has any: those still set, each with a cancel button, then those that
  fired or were cancelled in the last 72 hours.
- **When the router fails** on a follow-up after its retries, the event is recorded
  as failed. Nothing is sent, and the note is not passed to a worker.
- **In a chat**, a brain with a worker policy sets follow-ups the same way. The
  follow-up reaches it as a router turn outside the chat's conversation, like a
  ticket update, and its reply is posted in the chat's thread.

The host limits them, whatever the router asks for:

| Limit | Value |
| --- | --- |
| Follow-ups set on one thread | 5 |
| Follow-ups set by one agent | 50 |
| Shortest delay | 1 minute |
| Longest delay | 90 days |
| Note length | 2,000 characters; longer notes are cut |
| Follow-ups fired per agent in 24 hours | 200; further ones wait, and the activity response says so in `timer_warning` |

A follow-up may set another, which is how "check every hour until it merges"
works. The 24-hour limit is what bounds such a chain.

## Owning a Ticket

The brain owns the completion and quality of the Tickets it starts, instead of only
relaying a worker's last answer.

### Acceptance criteria

```
<agentmux-action>
{"action":"accept","worker_id":"auto:codex","title":"Decimal pricing","instructions":"…",
 "done_when":["go test ./pricing/... passes","no float64 left in pricing/"],"expect_minutes":30}
</agentmux-action>
```

- `done_when` is optional: up to 6 criteria, each one line of at most 300 characters,
  none repeated. `expect_minutes` is optional: how long the brain expects a turn to take,
  from 1 to 360 (the longest a worker's turn may run).
- Malformed values are refused before anything is carried out, like any other action the
  host rejects: the router is told why and answers again.
- The Ticket keeps them (`done_when`, `expect_minutes`, with the `instructions` it was
  accepted with), and `ticket.json` records them. The worker's first input lists them
  under `DONE WHEN`, with a request to end its report with one line per criterion:
  whether it is met, and the evidence. The brain sees them with the Ticket in OPEN TICKETS.
- An `accept` without them starts a Ticket exactly as before.

### Supervision

About every 30 seconds AgentMux looks at the running Tickets of every enabled agent and
derives **signals** from records it already has. It calls no model.

| Signal | When |
| --- | --- |
| `stalled` | The worker showed no progress (a tool call, or output, as `progress.md` records it) since its turn began or its last step, for longer than max(20 minutes, 1.5 × `expect_minutes`). |
| `orphaned` | The Ticket is marked running but nothing on this machine runs it, or its worker's machine does not answer a status check. Either must hold for a minute before it counts. |

- **Once per stretch.** A signal is recorded on the Ticket (`signals`: `kind`, `detail`,
  `since`, `at`, `woke_at`, `cleared_at`, and in `ticket.json`) when its condition begins,
  and closed when it ends: progress ends a stall, and whatever stops the Ticket running
  ends all of its signals. While it lasts it is told once. A retry of the same turn that
  finds the same condition reopens the same signal. The record is the dedupe state, so a
  daemon that restarts finds the signals it already told and does not tell them again.
  Recording a signal does not change the Ticket's `updated_at`.
- **Modes.** The agent's `supervision` setting: `observe` (the default, also when empty)
  records signals on the Ticket and wakes nothing, so an agent that does not choose a mode
  behaves as before; `act` also wakes the brain; `off` records nothing.
- **Waking the brain** (act). Signals are an event of kind `supervision` on the Tickets'
  thread, stored like a ticket update and taken by the router like any other event, in
  order with the thread's other events. All untold signals on a thread go in one event,
  and a thread gets at most one such event every 10 minutes; signals that come up in
  between wait and go together. The event's dedupe key names its signals, so a daemon
  that stops between storing it and marking them told stores no second one. In act mode
  OPEN TICKETS also lists each Ticket's open signals under `signals`.
- The brain decides what to do: ask the worker how it stands with `attach`, stop the
  Ticket and accept the work again, tell the people waiting, or nothing; an empty reply
  sends nothing. When the router fails on a supervision event after its retries, the
  event is recorded as failed and nothing is sent.

### Review

After a worker finishes a turn, the brain gives its verdict with a `review`:

```
<agentmux-action>
{"action":"review","ticket_id":"tkt_…","verdict":"rework",
 "checks":[{"criterion":"go test ./pricing/... passes","met":false,"evidence":"TestTax fails"},
           {"criterion":"no float64 left in pricing/","met":true,"evidence":"grep finds none"}],
 "note":"Also keep the old rounding behind a flag."}
</agentmux-action>
```

| Verdict | What the host does |
| --- | --- |
| `accepted` | Records it. The Ticket stays ready: its work is done. Refused while a check is unmet. |
| `rework` | Sends the itemized gaps (unmet checks with their evidence, then the note) to the same worker conversation and counts a round. Needs an unmet check or a note. A third `rework` of a Ticket is refused: it is recorded with `refused`, and the people on the thread are told, in AgentMux's words, that the Ticket needs a decision, with what is still open. |
| `needs_user` | Needs a `note` saying what must be decided. Records it, and tells the people on the thread, in AgentMux's words: "“<title>” needs a decision from you: <note>". |

- The host enforces it like any other action: the Ticket must be this agent's and on the
  event's thread, its worker must have finished a turn (the Ticket is ready; `needs_user`
  also takes a failed one), and an answer reviews each Ticket at most once. Otherwise the
  decision is refused before anything is carried out and the router is told why.
- At most 12 checks, each naming its criterion (at most 300 characters); evidence is cut
  at 1,000 characters and the note at 2,000.
- Every review is recorded on the Ticket (`reviews`: `verdict`, `checks`, `note`, `round`,
  `refused`, `trigger_id`, `at`; `rework_rounds` counts the rounds) and in `ticket.json`.
  OPEN TICKETS shows the latest under `review`.
- AgentMux's own message goes after the brain's reply, as a message of its own.
- A ticket update for a Ticket with `done_when` asks the brain to check the report against
  them and answer with a review, and says how many rework rounds are used. A Ticket
  without them is reported as before, and nothing requires a review.

| Limit | Value |
| --- | --- |
| Rework rounds per Ticket | 2 |
| Checks per review | 12 |

### Independent review

Optional, and off unless the agent's `review_runtime` names a framework. It must be
installed on the agent's machine and offer the read-only `plan` approval mode.

- **When.** A local worker whose Ticket has its own git worktree (a worker in worktree
  mode, or an auto worker in a repository) finishes a turn without failing. The review
  runs before the brain's ticket update, while the Ticket still holds its place among
  the running ones. Stopping the Ticket stops the review and sends no update.
- **The copy.** AgentMux stages the worker's files, uncommitted and untracked ones
  included (ignored ones not), into a separate index, commits that tree on top of the
  worker's `HEAD` as an unreferenced commit, and checks it out, detached, at
  `tickets/<id>/.review`. The worker's own index, branch and files are untouched. The
  copy is removed with `git worktree remove` when the review ends, whatever its outcome,
  and one left by a daemon that stopped mid-review is removed before the next.
- **The reviewer** is a fresh conversation of the Agent instance
  `saw_<agent id>_<framework>_review`, in `plan` mode, working in the copy. Like an auto
  worker, it takes the brain's model and environment when it shares its framework, and is
  removed with the agent. It is told only the Ticket's title, `instructions`, `done_when`
  and the diff from the base to the copy (cut at 60,000 characters; it can run `git diff`
  for the rest), never the worker's transcript or answer. The base is where the worker's
  branch left its base Ticket's work, or else the repository checkout it was cut from.
- **Findings** are one per line with a severity (P0 blocks, P1 likely wrong or untested,
  P2 minor) and `path:line` evidence, followed by a counts line that AgentMux reads and
  removes. They go with the ticket update, after the worker's report, cut at 6,000
  characters, and are recorded on the Ticket as `auto_review` (`runtime`, `findings`,
  `count`, `blocking`, `error`, `at`). A review that cannot run says why in the same place,
  and the update still goes out. The review is advice for the brain: AgentMux does not act
  on it.
- **Not yet:** remote workers, whose files are on another machine, and workers that share
  a directory with other Tickets are not reviewed. A review takes at most 30 minutes.

## Placement

Where a new Ticket runs takes each machine's [framework health](../docs/architecture/framework-health.md)
into account. Placement reads stored records; it never runs a check of its own.

- **The roster marks health.** A roster entry whose framework is `failing` or
  `usage_limit` on its machine gets `"health"`, such as `"usage-limited until
  2026-11-15T09:00:00Z: …"` or `"failing: not signed in"`. `ok` and `unknown` add
  nothing; `unknown` never holds work back. This machine's frameworks come from its own
  records, another machine's from that machine's `GET /api/v1/frameworks/health`,
  reached the way its workers are (SSH, the node link, or the hub relay, which carries
  this call). A machine's answer is reused for up to a minute. A roster worker's
  framework is the one its Agent reports. When an entry is marked, or work can go to
  another machine, the brain gets a short `PLACEMENT` note explaining the marks and
  `machine_policy`.
- **`machine_policy`** on an `accept`:

  ```
  {"action":"accept","worker_id":"auto:codex@studio","machine_policy":"flexible","title":"…","instructions":"…"}
  ```

  `fixed`, the default and what an accept without it means, runs the Ticket exactly where
  the brain named. If that machine cannot be reached, the framework is not installed
  there, or its health is `failing` or `usage_limit`, the action is refused before
  anything is carried out, with the reason, and the brain answers again. `flexible` lets
  AgentMux move the Ticket to another entry of the same roster that can take it: first
  the same directory with another framework, then the same framework elsewhere, then the
  rest in roster order. Nothing outside the roster is ever chosen. When no entry can
  take it, the action is refused with what was tried. Another value is refused.
- **A moved Ticket** records `placement` (`policy`, `requested_worker_id`, `worker_id`,
  `host_id`, `machine`, `reason`, `at`). The Trigger's reason says where it went, the
  brain sees it under `moved` in OPEN TICKETS, and the Console shows it on the Ticket.
- **Failed runs feed health.** When a turn fails for a reason the framework owns (see
  the classification in the health document), a worker on this machine stores it with
  `RecordFailure`, unless periodic checks are on, in which case the engine already
  learned it and confirms it with a recheck. A failure on another machine marks that
  framework here for up to an hour, or until that machine's own record is newer.
- **The Console** shows a mark beside each failing or usage-limited framework on the
  agent's roster (`runtime_health` in the activity response), and the move on a Ticket.

## Workspace claims

Two Tickets never write in the same directory at once.

- **What is claimed.** A Ticket records where its worker writes (`workspace`: `host_id`,
  `dir`, `isolated`, `held_since`) when it is accepted. A worker that gets its own copy
  holds nothing: an auto worker or roster Agent in a git repository (a per-conversation
  worktree), an auto worker given no directory (each Ticket's own `work/` folder), and a
  roster Agent whose workspace mode is `worktree`. On another machine, whether the
  directory is a git repository is what that machine says when it launches the worker.
  A roster Agent whose directory cannot be read holds nothing.
- **Keyed by machine and directory.** The machine is its machine UID when known, so a
  computer reached over SSH, the node link and the hub relay counts once; otherwise the
  way this machine reaches it. A directory here is compared expanded; one on another
  machine as the operator wrote it, and an empty one as that machine's default worker
  directory.
- **How long.** From the start of the Ticket's first turn until it is ready, failed or
  stopped. Retries, follow-ups during a turn, and a daemon restart keep the claim:
  claims are read from the stored Tickets, with no table of their own. A ready Ticket
  that gets new input claims the directory again when its next turn starts.
- **An accept for a held directory is refused** before anything is carried out, naming
  the holding Ticket (any agent's on that machine) and when it took the directory, so the
  brain can give the new Ticket `depends_on` that Ticket, choose another worker, or wait.
  An accept that depends on the holder, directly or through other Tickets, is allowed.
  Two accepts in one block for the same directory are refused unless the second depends
  on the first's `ref`. With `machine_policy` `flexible`, a held directory is one more
  reason to move the Ticket.
- **A queued Ticket whose directory is held waits** until it is free, as a Ticket with
  unmet dependencies does.
- **What the brain sees.** `DIRECTORIES IN USE` lists each held directory on the agent's
  machines (`dir`, `machine`, `ticket_id`, `title`, `agent` when another agent's, `since`),
  and a holding Ticket shows `holds` in OPEN TICKETS. The Console shows the same list as
  "Directories in use" (`workspace_claims` in the activity response) and the directory on
  the holding Ticket.
- **Not covered:** two different spellings of one directory on another machine (`~/code`
  and `/Users/me/code`) are two directories; claims of agents on other machines are not
  seen.

## Plans, cost tiers and remembered approvals

### Cost tiers

Every roster entry shows a `cost_tier`, `low`, `medium` or `high`: roughly what a turn
there costs. It comes from the framework and model: a model name with `haiku`, `mini`,
`nano`, `flash`, `lite` or `small` is low, one with `opus`, `-pro`, `ultra`, `max` or
`o3` is high, any other named model is medium, and without a model Claude Code is high
and other frameworks medium. An auto worker of the brain's own framework on this machine
uses the brain's model. The operator can set `cost_tier` on a roster worker to override
it. The Console shows the tier on each roster entry (`cost_tiers` in the activity
response).

### The plan action

```
<agentmux-action>
{"action":"plan","title":"Fix and document","text":"Why, in a sentence or two.",
 "tickets":[{"ref":"a","worker_id":"auto:codex","title":"Fix it","instructions":"…","done_when":["…"]},
            {"ref":"b","worker_id":"auto:claudecode","title":"Document it","instructions":"…","depends_on":["a"]}]}
</agentmux-action>
```

- A plan proposes up to 8 Tickets without starting any. Each has an accept's fields
  (`worker_id`, `title`, `instructions`, `done_when`, `expect_minutes`, `depends_on`,
  `base_ticket`, `machine_policy`, `ref`); refs are the plan's own and work in its
  `depends_on`. Each is checked as an accept would be, including
  [placement](#placement) and [workspace claims](#workspace-claims), and the host
  records its tier and where it would run now (`where`, and `placed_worker_id` when
  flexible placement would use another entry). The plan's `tier` is its highest.
- AgentMux stores the plan (`pln_…`), and after the brain's reply sends the thread it
  came from a text version: each Ticket with its worker, machine, tier and what it waits
  for, and that approval happens in the Console. The Console shows it as a card on the
  agent's page. Pending plans are listed to the brain under `PLANS WAITING FOR APPROVAL`
  so it does not propose them twice. A plan nobody decides expires after 7 days.
- **Approve** starts the Tickets through the normal accept path, in order, with refs
  resolved, as Tickets of the original request (its sender is their actor). The checks
  run again; when the Tickets cannot start now, the plan stays pending and the Console
  shows why. The thread is told how many started; the brain sees them with its next
  event, without a turn of its own.
- **Decline** starts nothing and wakes the brain with an event of kind `plan` on the
  plan's thread, with the person's note, so it can answer the requester.

### When a plan is required

The agent's `plan_approval` setting:

| Value | An accept |
| --- | --- |
| `off` (default, also empty) | starts at once, as before |
| `always` | needs a plan |
| `above_low` | of medium or high tier needs a plan |
| `above_medium` | of high tier needs a plan |

An accept that needs a plan is refused before anything is carried out, with the reason,
and the brain answers again, usually with a plan. While the setting is on, the brain gets
a `PLANS` note explaining the plan action. A plan may be proposed with the setting off
too. The requester is the event's sender; for AgentMux's own news about a Ticket, whoever
asked for that Ticket.

### Remembered approvals

Approving with "don't ask again" keeps (agent, requester, the plan's tier); a later
approval keeps the higher tier. Work for that requester at or under the tier starts
without a plan: an accept goes through, and a plan starts at once, recorded as approved
by `remembered trust`. The Console lists remembered approvals on the agent's page and
can revoke each. Plans and remembered approvals are stored as settings of this machine
(`super_agent_plans:<agent>`, `super_agent_trust:<agent>`), never in the agent's home,
which its brain can write, and are deleted with the agent.
## Helpers

Optional, and off unless the agent's `dispatch` setting is on. A worker running a Ticket
may then hand a self-contained sub-task to another of the agent's workers, a **helper**,
and read its result, so the brain is not the only thing that can split work.

- **Being told.** When the setting is on, a new Ticket gets a dispatch token, and its
  worker's first input ends with a `HELPERS` paragraph: the two calls with the token,
  the workers it may name and the limits below. When it is off, the input says nothing
  of them and no token exists. The paragraph is fixed text, not a prompt part. A worker on
  another machine is offered helpers only when this machine has a hub to reach it through.
- **Starting one.** `amux dispatch start --token <token> --worker <id> --title "…"
  --instructions "…"` (or `--instructions-file`, `--runtime <framework>` for
  `auto:<framework>`) prints the helper's id. It runs one turn, in a conversation of its
  own (`superagent-hlp_…`), on that worker in the worker's own directory, and is told
  only that it helps another worker of this agent on the Ticket's title, and the
  instructions. Its final message is its result.
- **Reading one.** `amux dispatch read --token <token> [--wait 10m] <helper id>` prints its
  state (`running`, `done`, `failed`, `stopped`) and, once done, its result.
- **The route.** Both commands call the worker's own machine:
  `POST /api/v1/super-agents/dispatch/start` and `/read`, with the token in the body.
  The routes take no account credential; the token is the credential. A worker on this
  machine whose `amux` cannot be found is given the same calls as `curl` lines. A machine
  that does not hold the Ticket passes the call up its hub link: through the worker relay
  to the agent's machine, or to `POST /uplink/v1/dispatch/start` and `/read` on the hub
  when the agent lives there.
- **Recorded.** Helpers are kept on their parent Ticket (`helpers`: `id`, `worker_id`,
  `worker`, `title`, `instructions`, `status`, `result`, `error`, `created_at`,
  `finished_at`). The Console shows them under the Ticket with their state and result,
  and OPEN TICKETS lists the helpers still running under `helpers`. The Ticket keeps only
  the SHA-256 of its token (`dispatch_key`).

The host enforces, whatever the worker asks for:

| Limit | Where |
| --- | --- |
| The agent's `dispatch` setting is on | Every call; turning it off refuses the next call (`403`) |
| The token is the Ticket's own; it names one Ticket, whose agent's setting and roster apply | Every call: an unknown or altered token is `401`; another Ticket's helper is `404` |
| One level: a helper is never given a token, and a token never names a helper | Token check (`401`) |
| The Ticket's turn is running | Start (`409`) |
| At most 3 helpers run at once for a Ticket | Start (`409`) |
| At most 12 helpers are started for a Ticket in all | Start (`409`) |
| The worker is on the agent's roster, or an `auto:` worker it may launch | Start (`403`) |
| Instructions are required, at most 20,000 characters | Start (`400`) |
| Helpers still running stop when the Ticket's turn ends, however it ends: finished, failed, stopped, or the daemon stopping | Recorded as `stopped` with the reason |

- A helper's result is cut at 60,000 characters. A read waits at most 30 seconds per call;
  `--wait` repeats it.
- Helpers do not count toward `max_concurrency`, are not reviewed, and do not wake the
  brain; the Ticket's worker reports what it used.
- **Not yet:** a worker on a machine reached only over SSH cannot reach the agent's
  machine, so it is not offered helpers. After a hub failover, a Ticket on the old
  primary is no longer `hub` to the new one.

## What the host enforces

The router reads untrusted text. What limits the harm a crafted event can do:

- **Allowed senders** are checked before the router sees an event, and an empty
  list admits nobody. `*` is accepted only for Feishu messages. This is the only hard
  limit on who can reach the router. Channel sources have no list: whoever the
  channel lets reach the bot reaches the brain, as with any bound channel.
- **A helper** a worker starts is held to the same roster, its Ticket's token and the
  limits under [Helpers](#helpers).
- **An action** can only name a worker on this Super Agent's roster, `auto:<framework>`
  or `auto:<framework>@<path id>` for one of its worker frameworks, and a Ticket already
  on the event's thread.
- **The router** is a [router agent](#router-agents): it runs on this machine in its
  framework's default router mode, `auto`, where the framework approves what looks safe
  and declines the rest. Its charter, not a sandbox, is what tells it to treat event text
  as data, so the triggers' allowed senders are still the limit on who can reach it.
- **Observe mode** is the default for a Super Agent saved through
  `POST /api/v1/super-agents` without a mode. It records the router's reply and action without
  running work or messaging anyone, and tells the router to change nothing; its approval
  mode still applies. Switch to Act when the decisions look right. Agents created as
  Agents, and Agents upgraded from before, act. In a chat, observe mode records the
  brain's actions without carrying them out; the chat still gets its reply.

Workers run unattended, and an approval nobody can answer is declined. Give them
a non-manual approval mode, and per-conversation worktrees so parallel Tickets
stay apart.

## Triggers

| Trigger | Connection | Who counts as the sender | Reply |
| --- | --- | --- | --- |
| Feishu message | A Feishu channel's long connection | `open_id` | In the thread of the message |
| Feishu document update | The same connection, plus a file subscription | `open_id` of an editor | The configured notify chat |
| Email | IMAP over TLS, polled every minute | Authenticated `From` address | SMTP over TLS, in the same thread |
| GitHub webhook | `POST /hook/super-agent/github/{source_id}` | `sender.login` | A comment, with a token |
| GitHub notifications (`delivery=poll`) | The agent account's notifications, read about once a minute | The comment's or assignment's author | A comment, with the same token |
| Custom callback | `POST /hook/super-agent/callback/{source_id}` | The request's `actor` (`callback` when empty) | The configured notify chat |

- **Feishu messages:** mentions and direct messages are claimed, so the channel's
  own Agent does not also answer. `/` commands stay with the channel. Follow-ups
  in a thread with a Ticket reach the router without another mention.
  A mention or direct message from an allowed sender gets the channel's ack
  reaction as soon as it is taken, and loses it once the router has answered
  (or chosen not to). Any other message is marked only when the router writes a
  reply, until that reply is sent. The channel's `ack_reaction_enabled` and
  `ack_reaction_emojis` apply, as for a channel turn.
- **Feishu documents:** the channel's app needs access to each document, and the
  app must subscribe to `drive.file.edit_v1` (the Console's second scan adds it
  with the document read scopes). Edits within the debounce window
  (10 minutes by default) are one event. An edit counts only when an allowed
  editor made it, which is also what stops a worker's own edits from looping.
- **Email:** a new source starts from now and never reads old mail. A sender counts
  only when the receiving server's topmost `Authentication-Results` shows DMARC
  or aligned DKIM passing. Set `trust_from_header=true` only if your mail server
  already rejects forged senders. Auto-replies and bulk mail are dropped.
- **GitHub:** in the repository's webhook settings use content type
  `application/json` and the source's secret, and send Issues, Issue comments,
  Pull requests and Pull request review comments. Asking means mentioning the
  agent's login or assigning it. Comments by the agent's own login and by bots are
  dropped. A webhook needs GitHub to reach the daemon; it listens on loopback by
  default. On a single machine, `gh webhook forward` (the `cli/gh-webhook`
  extension) relays one repository's deliveries to the loopback address. Otherwise
  use a reverse proxy that exposes only `/hook/`: with the bridge token off, the
  rest of the port is the unauthenticated Console API.
- **GitHub notifications:** with `delivery=poll` the source reads the agent
  account's notifications with `github_token` instead (a classic or OAuth token;
  fine-grained tokens cannot read notifications), so GitHub never has to reach
  the daemon and no webhook secret is needed. It sees what that account takes part
  in: mentions, assignments to it, and new comments on Issues and pull requests it
  has commented on, not every new Issue in the repository. It honors GitHub's
  `X-Poll-Interval`, starts from now, and reads on GitHub's clock with a minute of
  overlap; dedupe keys drop the repeats. It never marks notifications read. Give
  the agent its own account: its own comments never trigger it.

- **Custom callback:** an address for any system AgentMux has no trigger for, such as
  a script that watches a tracker. The body is JSON:
  `{"id","thread","actor","title","text"}`, and only `text` is required. `id` makes a
  repeated request harmless (it defaults to the body's digest), `thread` keeps events
  about one subject, and the Tickets they start, on one thread (it defaults to `id`),
  and `actor` must be on the trigger's allowed list, where `*` is refused. The caller
  proves itself with the trigger's secret: `X-AgentMux-Signature: sha256=` plus the
  hex HMAC-SHA256 of the body, or `Authorization: Bearer <secret>` when it cannot
  sign. The answer is `202 {"ok":true,"accepted":true}`; `accepted` is false for a
  repeat, and while the agent or the trigger is off. `400` means the body is not a
  request, `401` a wrong secret. `instructions` is added to every request's text.
  With `channel_id` and `notify_chat_id` the agent reports in that Feishu chat;
  without them results are only recorded. The daemon listens on loopback by default,
  so the caller runs on the same machine unless `/hook/` is exposed as for GitHub.
  [`integrations/meego-todo`](../integrations/meego-todo/README.md) is a caller that
  turns a person's new Meego to-dos into requests.

### Setting a trigger up in the Console

Each trigger in the Super Agent form is a list of steps, each with the fields it
fills in:

- **Feishu:** scanning with the Feishu app creates a bot in the scanner's tenant
  and saves it as a channel, so no App ID or secret is copied, and the scanner
  becomes an allowed sender. A second scan signs in to the Open Platform and turns
  on the scopes, long-connection events and card callbacks, then publishes the
  bot. Both scans work only from the local Console. Lark bots are configured on
  the Open Platform by hand, as the form describes. Choosing a bot checks its
  app's scopes (`GET /open-apis/application/v6/scopes`, with its own tenant
  token) against what the second scan enables, less the meeting scopes only
  AgentMux's meeting features use, for the app and for user identity, and shows what is missing with the console link that requests it and
  a QR code of that link; scopes requested but not yet granted are listed as
  waiting for an admin. Feishu counts a scope once a version with it is
  published. The bot picker names each channel with its bot's own name.
  Optionally, the operator then authorizes the bot's app once with their own
  Feishu account, through Feishu's device flow: they scan a code or open a link and
  approve, so no redirect address is configured. After that the group picker
  lists that person's groups most recently active first (the latest 300),
  searches all of them by group or member name, names chosen groups past those,
  and adds the bot to a group it is not in yet, as them. The sender picker lists
  the people of their recent 1:1 chats, most recent first, and searches everyone
  they can see by name, pinyin or email. The bot itself needs no scope to read those groups or people. The second
  scan also enables the four user-identity scopes this takes (`im:chat:read`,
  `im:chat.members:write_only`, `contact:user:search`, `offline_access`); an
  authorization that lacks one names it with a link to enable it. The daemon
  keeps the token per channel (`feishu_user_grants`), refreshes it before it
  expires and never sends it to the Console. Removing the authorization or the
  channel drops it; Feishu keeps an access token valid for up to two hours.
- **Email:** the address picks the provider's IMAP and SMTP servers, and the form
  links to where that provider issues app passwords.
- **GitHub:** the guide follows dsh-superagent's setup. The agent gets a GitHub
  user account of its own (a link and QR code to sign up). It signs in with the
  GitHub CLI's one-time code, shown with a QR code, in a gh profile of its own
  (`GH_CONFIG_DIR`): the operator's gh login and its environment tokens are not
  used, the token is stored in that profile in plain text instead of the keychain,
  and the profile is deleted once the token is handed to the trigger. The page that
  takes the code should be opened on a phone or in a private window, since the usual
  browser is signed in as the operator; signing in as the operator's own account is
  flagged. The operator's gh login then invites the agent to a repository they
  administer, with write access, and the agent's token accepts; for other
  repositories the invitation is sent by hand and accepted from the form. The
  operator's login is offered as the first allowed sender. Pasting a token is the
  fallback without gh. A token, signed in or pasted, is checked at once, and a saved
  one when the form opens; that fills in the login and lists what the account can
  reach. The routes that run gh answer only the local Console. A trigger added from
  a page GitHub cannot reach, such as the desktop app, reads notifications; one
  added from a reachable page uses a webhook. Either can be switched. For a
  webhook, the source id and secret are generated when the trigger is added, so its
  address is known before saving; a reachable page shows the values to paste into
  the repository's webhook settings, and a local one shows the `gh webhook forward`
  command.

## Workers

A worker is an Agent on this machine, or on a machine added under Remote Machines.
Remote workers run through the remote daemon's `/api/v1/invocations/stream` over
the verified SSH tunnel:

- A Ticket keeps one conversation id, so retries and follow-ups continue the same
  native session on the same worker.
- A lost tunnel or a busy conversation is retried up to three times. A failure of
  the work itself is not retried; it is reported, and can be retried from the Console.
- Stopping a Ticket closes the stream, and the remote daemon cancels the turn.
- Tickets running when the daemon stopped resume on the same conversation.

### Directories

Each Super Agent has a home, `~/.agentmux/super-agents/<id>/`. Its router agent works
there, and every Ticket has a folder below it:

```
~/.agentmux/super-agents/<id>/
├── CLAUDE.md, AGENTS.md     what the home holds; written once, then the operator's
├── memory/MEMORY.md         the router's memory index; the router keeps memory/
└── tickets/<ticket id>/
    ├── ticket.json          the Ticket's status, worker and working directory
    ├── inputs.md            everything AgentMux sent the worker, one section per turn
    ├── result.md            the worker's final answer after each turn
    ├── work/                the worker's working directory, or a link to it
    └── work.removed.json    where the work is, once a finished Ticket's worktree is removed
```

- A worker whose Agent uses per-conversation worktrees gets its worktree in `work/`, on
  an `agentmux/…` branch, instead of under `<repo>.agentmux-worktrees`. So does an auto
  worker in the default directory, as a plain directory.
- Any other local worker keeps the directory its Agent is configured with, and `work/`
  links there. A remote worker's directory is on its own machine; its folder has the
  record files only.
- The store stays the source of truth: AgentMux rewrites `ticket.json` on every change and
  only appends to the logs. Tickets created before homes existed have no folder and keep
  working where they started.
- A worktree in `work/` is removed once its Ticket has been quiet for a while: seven days
  after anything last happened to a ready or failed Ticket, one day after a Ticket was
  stopped. It goes only when nothing would be lost: it is on a branch and has no
  uncommitted changes or files of its own besides the ones AgentMux writes there. The
  branch stays in the repository, and `work.removed.json` beside `work/` names the
  repository, branch and commit. A follow-up or retry checks that branch out in `work/`
  again, and a Ticket that builds on this one still starts from its last commit. Files git
  ignores, such as build output and installed dependencies, go with the directory. A
  worktree with uncommitted work, or on a detached HEAD, is kept. A linked directory and a
  remote worker's directory are never removed.
- Deleting a Super Agent keeps its home, since worktrees there can hold work that was
  never committed.

### Auto workers

With auto workers on, the roster can be empty. `worker_runtimes` lists one or more
installed frameworks to launch workers with, and defaults to the router's. The roster
offers `auto:<framework>` for each; the router names one when no listed worker suits a
request, and the host starts a worker of that framework that was not configured in advance:

- Each Ticket gets its own session and directory. In a git repository the directory is a
  worktree in the Ticket's `work/` folder; in the default directory it is a new folder
  there. Another directory outside a repository is shared by the Tickets.
- An action supplies only the framework, the Ticket's title and instructions. A worker on
  the router agent's framework takes its provider, model and environment; another framework
  uses its active provider route. `auto:<framework>` works in the default directory,
  `~/.agentmux/super-agents/<id>/work` (a Super Agent that still routes with an ordinary
  Agent uses that Agent's directory). The approval mode is `auto_worker_approval_mode`,
  which every worker framework must support; left empty, each uses `auto`, or `auto_edit`
  where it lacks `auto`. MCP servers, skills and channels are not inherited.
- `max_concurrency` limits how many run at once. Workers also run in the directories
  `worker_machines` lists, here or on other machines; see below.
- Each worker framework has one Agent instance per directory here,
  `saw_<super agent id>_<framework>` for the default one and
  `saw_<super agent id>_<framework>_<path id>` for a listed one, removed with the Super
  Agent. The home is kept, and so is a Ticket's worktree until the Ticket has been quiet
  for a while (see above).

### Working directories

The default directory is always offered. `worker_machines` lists more directories the
agent may launch workers in, on this machine (an empty `host_id`) or on machines connected
to this AgentMux as linked devices or SSH hosts:

```json
"worker_machines": [
  { "id": "local", "host_id": "", "dir": "~/code/app", "capabilities": "The app repository." },
  { "id": "dev_23f0…", "host_id": "dev_23f0…", "dir": "~/code/app", "capabilities": "Has the GPU." },
  { "id": "dev_23f0…-2", "host_id": "dev_23f0…", "dir": "~/data" }
]
```

- A directory here must exist. One on another machine may be empty, giving the workers a
  directory of their own there; the same directory cannot be listed twice. Saving assigns
  each entry an `id`: the first one on another machine takes the machine's id, as entries
  did when each machine had one directory, and the others are numbered. `auto_worker_dir`,
  from before there could be several directories here, becomes the first entry when the
  agent is saved.
- The roster offers `auto:<framework>@<id>` for each directory and worker framework, with
  the directory, the operator's `capabilities` text, and for another machine its name and
  whether it is reachable and has that framework installed right now. A decision can only
  name an entry on the roster.
- Another machine itself creates the worker. The host sends the framework, `dir`,
  `auto_worker_approval_mode` and, for any but the first directory there, the entry's
  `path_id` to `POST /api/v1/super-agents/workers/launch` on that machine, which checks
  them as it would for its own agents: the framework must be installed, `dir` must exist
  (empty gives the worker `~/.agentmux/hosted-workers/<agent id>/work`), and the approval
  mode must be an unattended one. The worker is the Agent instance
  `sawr_<agent id>_<framework>`, or `sawr_<agent id>_<framework>_<path id>`, there. A
  machine whose AgentMux predates `path_id` keeps one worker per framework, which moves to
  whichever of its directories was launched last. It uses that machine's provider route,
  tools and environment; nothing is inherited from the agent's machine.
- The Ticket, its inputs, results and progress stay on the agent's machine. The worker's
  session and files stay on its own, so `base_ticket` only carries commits between Tickets
  on the same machine.
- A follow-up for a running Ticket is delivered during its turn through
  `POST /api/v1/invocations/steer` on the worker's machine. When that machine cannot take
  it, the follow-up waits for the next turn.
- An unreachable machine fails the turn as retryable, like any remote worker.
- Removing a machine from the list, or deleting the agent, removes its workers there
  (`POST /api/v1/super-agents/workers/release`). Their files are kept. A machine that is
  offline at that moment keeps an idle Agent instance.
- A machine whose AgentMux predates this answers the launch with an error naming the update.

`GET /api/v1/super-agents/tickets?limit=` lists the newest Tickets of every agent on a
machine, each with its agent's name and the machine its worker runs on. Reply addresses and
queued inputs are left out and long output is cut. Machines linked to a hub report this list
to it.

## Direct messages

Agents write to each other, and people write to agents, through AgentMux's own mailbox
rather than an outside channel.

**Addresses.** `agent:<id>` is an agent on this machine, `agent:<id>@<device id>` an agent on
a machine linked to this hub, and `person:<login>` someone with an account here. A linked
machine names its hub's parties `agent:<id>@hub` and `person:<login>@hub`. A conversation is
the messages between two addresses; there are no groups.

**For an agent.** A message to an agent is an event with `source` `mailbox` on the thread
`mail:<sender address>`, one continuing thread per sender. The router's reply to it is sent
back as a message, and so are a Ticket's updates when the message started one. The router's
PLACES list the agents and people it may write to, with each agent's machine, description
and whether its machine is online; `post` with `"to"` set to one of those addresses sends a
message. A `post` to an address that is not listed is refused before anything is carried out.
When the router posted on behalf of another thread, the answer's event lists that thread
under QUESTIONS YOU ASKED HERE, as for other places.

**Who may write to whom.**

- Parties with the same owner: the agents of one account, the agents on its devices, and the
  person. Administrators are one owner.
- An administrator, to any agent.
- Anyone, to a party that wrote to them first.
- Parties joined by an accepted contact. `POST /api/v1/mailbox/contacts {"a","b"}` makes one:
  accepted at once when the caller answers for both sides, otherwise `pending` with
  `waiting_for` naming the side whose owner (or an administrator) must call
  `POST /api/v1/mailbox/contacts/accept {"id"}`. Asking for a contact the other side already
  asked for accepts it. `DELETE /api/v1/mailbox/contacts?id=` ends, declines or withdraws one.
- People do not write to people.
- Two agents may trade at most 40 messages an hour; further messages are refused until a
  person has had time to look.

**Across machines.** Every machine keeps the messages its own parties sent and received.
A message for another machine goes through the hub:

- A linked machine sends it up its link (`POST /uplink/v1/mail/messages`). The link names
  the machine, so it can only send for its own agents. The hub applies the rules above,
  keeps the message and passes it on.
- The hub passes a message down the recipient's link to `POST /api/v1/mailbox/deliver`, an
  administrator's route on that machine. The machine accepts what its hub delivers.
- A message keeps its id on the way, and a machine that receives one twice keeps it once.
- Whichever machine holds an undelivered message retries it every 20 seconds and when the
  recipient's machine reconnects. After 7 days it gives up and records why (`failed_at`,
  `error`). A refusal, such as parties that are not contacts or an agent that no longer
  exists, fails the message at once and is recorded on the event that sent it.
- A linked machine learns who its agents may reach elsewhere from the hub
  (`GET /uplink/v1/mail/directory?agent=<id>`), and keeps the answer for a minute.
- People on a linked machine use the hub's Console to write to agents elsewhere.
- A hub or a machine whose AgentMux predates direct messages refuses them with a message
  naming the update.

**For the Console.**

| Route | Purpose |
| --- | --- |
| `GET /api/v1/mailbox` | The caller's conversations with agents, the conversations of their agents they may watch, the unread count and the addresses they answer for |
| `GET /api/v1/mailbox/messages?with=<address>` | The caller's conversation with an address, oldest first; marks it read |
| `GET /api/v1/mailbox/messages?with=<a>&and=<b>` | A conversation between two other parties, when the caller answers for one of them |
| `POST /api/v1/mailbox/messages {"to","text"}` | Send a message as the caller; `422` with the reason when it is refused |
| `GET /api/v1/mailbox/directory` | Every party the caller can name, and those they may write to now |
| `GET /api/v1/mailbox/contacts` | The contacts with a side the caller answers for |

A message has `delivered_at` once it reached its recipient's machine, or for a person their
inbox, and `read_at` once that person opened the conversation. Members reach these routes
for the parties they own. A caller without an account, such as the bridge token, is
`person:admin`. When a message reaches a person, their devices get a push notification of
kind `messages` that names the sender and never the text.

## Router agents

A router agent is an Agent made only to route, kept apart from ordinary Agents. The
Super Agent form chooses one, or creates one with a framework, a model, a reasoning effort,
a speed, and the MCP servers it may use. Each router agent belongs to one Super Agent and
works in its home, so it can read every Ticket's folder; another Super Agent cannot route
with it.

- It runs unattended in its framework's default router mode: `auto`, or, for a framework
  without it, `auto_edit`, then `yolo`, then `plan`. The Console always saves it in that
  mode, so it moves an older router onto it; the API still accepts any mode the framework
  offers except `manual`, and saving without one keeps the one it has. A framework with
  only `manual` cannot route. Its framework cannot change after it is created.
- Its MCP servers are the registered, enabled ones listed on it, none by default. AgentMux
  can give MCP servers only to Claude Code and Codex routers.
- It never has skills, CLIs, channels, schedules or a system prompt. Saving it restores
  these limits, and a mode its framework does not offer, whatever was changed elsewhere,
  and the Agents page shows it read-only.
- It is named after the Super Agent that routes with it, as `<name> router`, and follows
  that Super Agent's renames. An unnamed Super Agent is named `Super Agent (<framework>)`.
- Its provider is the framework's active provider route. The form offers that provider's
  models, reasoning efforts and service tiers, or else those of the framework's own
  sign-in. Empty values are the framework's defaults, and a `priority` or `fast` service
  tier is its fast mode.
- It works in its Super Agent's home, where AgentMux also writes its MCP configuration.
  Until a Super Agent routes with it, it has a scratch directory,
  `~/.agentmux/super-agents/routers/<id>`, removed once it moves.
- It is deleted with its Super Agent. Otherwise it can be deleted only when no Super Agent
  routes with it.
- Routers shared by several Super Agents before each belonged to one are copied when the
  daemon starts: the Super Agent created first keeps the original.

A Super Agent saved with an ordinary Agent as its router, before router agents existed,
keeps it until it switches to a router agent. `router_runtime` still names a framework
instead of a router agent for API clients: saving creates a router agent on that framework
for the Super Agent, or keeps the one it has there. The setup assistant, which saves
nothing, uses that framework's default router agent, `sar_<framework>`, and creates it when
missing. Worker frameworks are chosen
separately, under [Auto workers](#auto-workers).

## The form

The administrator sets every field. The form does not draft a configuration from a
description.

| Field | What it is for |
| --- | --- |
| Name | Shown in the Super Agent list, and the router is told it. A router agent only this Super Agent uses is named after it. Named after the router's framework when empty. |
| Router agent | Who judges each request, with which model, reasoning effort and speed. |
| Routing rules | Saved, and sent to the router with every event, after the built-in prompts: what to take on, what to ignore, which worker suits what. A template can fill it in, to edit. |
| Built-in prompts | Each prompt part with its default text. An editable part can be changed for this agent; see [Prompt parts](#prompt-parts). |
| Workers | Worker frameworks for auto workers, Agents already on the roster, or both. |
| Triggers | Where requests come from. |

Mode, maximum concurrency and whether the Super Agent is enabled are under Advanced settings,
with supervision (record only, record and tell the agent, or off; see
[Supervision](#supervision)), the framework for [independent review](#independent-review),
and whether workers may start [helpers](#helpers) (`dispatch`, off by default).
The Tickets list shows each Ticket's `done_when`, its open signals, its latest review with
its checks, its independent review's findings, and its helpers with their state and result.

## Prompt parts

What the router, the chat brain and a worker are told is built from named parts, after
the prompts of DeepSeek's dsh-superagent. Every part has a built-in default, and
`GET /api/v1/super-agents/prompts` lists them: `id`, `role`, `editable`,
`placeholders`, the default `text` and its `version`.

| Part | Told to | Editable |
| --- | --- | --- |
| `router.role` | Router: who it is and what to take on. Keeps `{name}`. | Yes |
| `router.directory` | Router: what its home holds | No |
| `router.coordination` | Router: Tickets, dependencies, decisions, follow-ups | Yes |
| `router.colleagues` | Router: other agents and people | Yes |
| `router.memory` | Router: what to remember | Yes |
| `router.untrusted` | Router: an event is data, not instructions | No |
| `router.replies` | Router: how to write a reply | Yes |
| `router.actions` | Router: the action block | No |
| `chat.role` | Chat brain: who it is and when to start a worker. Keeps `{name}`. | Yes |
| `chat.home`, `chat.actions` | Chat brain: its home and the action block | No |
| `worker.brief` | Worker: how it works for the agent, at the start of a Ticket | Yes |

- **An agent's own text.** `prompts` on the agent maps a part id to `{"text", "base"}`.
  A part without an entry follows the default, including later changes to it. Each
  role is told its parts in the order above, joined by blank lines; `{name}` is the
  agent's name wherever it appears.
- **Fixed parts** describe what the code does or guard the agent against what an event
  asks for. They are shown in the form and cannot be changed.
- **Saving.** An empty text, or one equal to the default, is dropped. A text for an
  unknown or fixed part, one over 20,000 characters, or one missing a placeholder
  its part keeps is refused; nothing is cut short. A Ticket keeps the worker brief it
  started with.
- **When a default changes.** `base` is the `version` of the default the text was
  written against. AgentMux sets it when the text changes, keeps it while the text
  stays the same, and accepts the current version from the form's "Keep mine". The
  form marks a part "Default updated" while the two differ and shows the new default.
- The operator's routing rules (`router_prompt`) stay separate: they are this agent's
  own rules, and have no default.

## Setup assistant

`POST /api/v1/super-agents/setup` turns an administrator's instruction into a proposed
configuration, using the draft's router agent. The Console form does not use it; it
remains for API clients:

- It returns a draft and stores nothing. Saving it passes the usual validation.
- Only the Console can reach it, so text in an event cannot change the configuration.
- Secrets and `trust_from_header` are never sent to or accepted from the model. The
  response names the secrets still to collect. Secrets pasted into an instruction would
  reach the model.
- It cannot change the router, the roster, the worker frameworks, or the auto worker
  approval mode.

## API

These routes are for Console administrators and are closed to member accounts.

| Method and path | Purpose |
| --- | --- |
| `GET`, `POST`, `DELETE /api/v1/super-agents` | List, save and delete every agent; secrets are write-only. The list includes each agent's channel sources. Saving an agent whose brain is its own renames and enables or disables the brain with it, and needs no workers; saving with channel sources sent back stores none. Deleting it deletes its own brain. |
| `GET /api/v1/super-agents/activity?id=` | Tickets, events, worker reachability, trigger health, `timers`: the follow-ups still set, then those that fired or were cancelled in the last 72 hours, `cost_tiers`, `plans` and `trust` (see [Plans](#plans-cost-tiers-and-remembered-approvals)), `runtime_health`: each roster framework's health per machine (`host_id`, `runtime`, `state`, `reason`, `resets_at`), and `workspace_claims`: the directories Tickets hold now on the agent's machines (`ticket_id`, `title`, `agent_id`, `host_id`, `machine`, `dir`, `since`) |
| `GET /api/v1/super-agents/prompts` | The built-in prompt parts with their default text |
| `GET`, `POST`, `DELETE /api/v1/super-agents/routers` | List router agents with the frameworks that can route and the MCP servers they may use; create or update one; delete an unused one |
| `POST /api/v1/super-agents/setup` | Propose a configuration from an instruction; stores nothing |
| `POST /api/v1/super-agents/trigger` | Send a request from the Console |
| `GET /api/v1/super-agents/github/setup` | The loopback address GitHub deliveries reach, and whether the API needs credentials |
| `GET /api/v1/super-agents/github/operator` | Local Console only: who is signed in to gh here, and the repositories they administer |
| `POST /api/v1/super-agents/github/login`, `/login/poll`, `/login/cancel` | Local Console only: sign the agent in with gh's one-time code; the first poll after success carries the token |
| `POST /api/v1/super-agents/github/invite` | Local Console only: invite the agent to a repository as the operator (unless `invite` is false), then accept as the agent |
| `POST /api/v1/super-agents/github/account` | The GitHub login, classic scopes and reachable repositories of a token in the body, or of a saved Source's token when the body sends it redacted with `super_agent_id` and `source_id` |
| `POST /api/v1/super-agents/tickets/stop`, `/retry` | Stop or retry a Ticket |
| `POST /api/v1/super-agents/timers/cancel` | Cancel a follow-up by `id`; `409` when it already fired or was cancelled |
| `GET /api/v1/super-agents/plans?agent_id=` | Beta. The agent's plans, newest first, and its remembered approvals (`{plans, trust}`) |
| `POST /api/v1/super-agents/plans/approve` | Beta. `{agent_id, plan_id, remember}`: start a pending plan's Tickets; `remember` keeps the requester's approval up to its tier; `400` with the reason when they cannot start now, `409` when the plan is no longer pending |
| `POST /api/v1/super-agents/plans/decline` | Beta. `{agent_id, plan_id, note}`: decline a pending plan and tell the brain |
| `GET`, `DELETE /api/v1/super-agents/trust?agent_id=[&requester=]` | Beta. List remembered approvals, or revoke a requester's |
| `POST /api/v1/super-agents/dispatch/start`, `/read` | A Ticket's worker starts and reads [helpers](#helpers). Not for the Console: the Ticket's token is the credential. |

## DeepSeek Harness runtime

Choose the `dsh` runtime for any Agent, including routers and workers. It runs
`dsh --profile headless --json` and resumes with `--session-id`. Headless cannot
ask for approval, so approval modes select a permission preset:

| Approval mode | DSH preset |
| --- | --- |
| `plan` | `read-only` |
| `auto_edit` | `workspace-write` |
| `yolo` | `danger-full-access` |

Headless ignores credentials saved by the DSH GUI. Provide `DEEPSEEK_API_KEY` in
the Agent's environment or in `$DSH_HOME/.env`.

## Current limits

- Workers are still Agent instances: auto workers keep their hidden
  `saw_<agent id>_<framework>` records, and roster workers are Agents.
- Only chats on bound channels run the brain through the channel engine. Feishu
  message, Feishu document, email and GitHub sources, the Console's "try it", and
  ticket updates run it as a router turn, with no streaming and in a conversation
  of their own per thread.
- A ticket update for a chat reaches the brain outside the chat's own conversation;
  the next chat turn sees the Ticket in its brief.

- One daemon owns a Super Agent's Tickets; running several against one database is
  not supported.
- An agent with no worker policy gets no brief in its chats, so it cannot set
  follow-ups there.
- Recurring schedules do not go through follow-ups: an Agent's cron schedules still
  run as chat turns.
- Images and attachments on incoming messages are not passed to the router or workers.
- Choosing a DSH model per Agent is not supported yet. DSH uses its configured default.
