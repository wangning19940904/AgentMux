# Timers for agents: follow-ups and schedules as inbox events

Status: follow-ups (§8, step 1) implemented; schedules proposed · 2026-11-14 · branch `claude/always-on-agents-review-634ac8`

[contract/SUPER_AGENTS.md](../../contract/SUPER_AGENTS.md#follow-ups) describes
follow-ups as built. Where the build differs from this draft, §10 says how.

An agent today acts only when something arrives: a message, a webhook, a
worker's report. This proposal lets it act because time passed.

- A **follow-up** is a one-off wake-up the brain sets for itself: "check
  tomorrow at 9 whether Li approved the migration".
- A **schedule** is a recurring wake-up the operator configures: "every weekday
  at 9, post the open-ticket digest".

Neither starts a brain turn directly. A due timer becomes an ordinary row in
`super_agent_triggers`, so it gets the per-thread ordering, retries, observe
mode and audit trail that every other event has. This is the timer primitive
from Anthropic's
[Building always-on agents](https://claude.com/resources/articles/building-always-on-agents),
fitted to the trigger inbox AgentMux already has.

## 1. What stays out

- The cron triggers of the channel engine ([core/scheduler.go](../../core/scheduler.go),
  `core.Trigger`, `AgentSchedule`) keep working as they do. They run a prompt
  as a chat turn and never touch the trigger inbox. §8 says how the two could
  merge later.
- A durable outbox, batching of triggers and memory consolidation are separate
  proposals. Consolidation needs this one first.
- Schedules the brain creates for itself. See §9, question 1.

## 2. The timer record

One new table, in migration 30, shaped like the two beside it:

```sql
CREATE TABLE IF NOT EXISTS super_agent_timers (
	id TEXT PRIMARY KEY,
	super_agent_id TEXT NOT NULL,
	thread_key TEXT NOT NULL,
	status TEXT NOT NULL,
	due_at TEXT NOT NULL,
	data TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_super_agent_timers_due ON super_agent_timers(status,due_at);
CREATE INDEX IF NOT EXISTS idx_super_agent_timers_thread ON super_agent_timers(super_agent_id,thread_key);
```

`core.SuperAgentTimer`:

| Field | Meaning |
| --- | --- |
| `id` | `stm_<uuid>` for a follow-up, `stm_<agent id>_<schedule id>` for a schedule |
| `kind` | `follow_up` or `schedule` |
| `status` | `active`, `done` or `cancelled` |
| `thread_key`, `source_id`, `source_kind`, `reply` | Where its event lands and where the reply goes. A follow-up copies them from the trigger that set it. |
| `ticket_id` | Optional: the Ticket a follow-up is about |
| `note` | What to do when it fires. The brain writes it for its later self; for a schedule it is the operator's prompt. |
| `due_at` | The next time it fires, in UTC |
| `cron`, `timezone` | Schedules only |
| `set_by` | The trigger id that created a follow-up, for the audit trail |
| `fired`, `last_fired_at` | How often it has fired, and when |

`DeleteSuperAgent` also deletes the agent's timers.

## 3. The scheduler

`Pass` ([superagent/service.go:257](../../superagent/service.go)) already runs
every two seconds and on every wake. It gains a first step, `fireDueTimers`:

1. Read up to 100 `active` timers with `due_at <= now`, oldest first.
2. For each, insert a trigger, as `wakeRouter` does for a worker's report:
   - `kind` is `timer`, the actor is `agentmux`, and it is addressed.
   - The thread, source and reply address are the timer's.
   - The dedupe key is `timer:<timer id>:<due_at>`.
3. Update the timer. A follow-up becomes `done`. A schedule gets the next
   occurrence after *now*, so a daemon that was down for a weekend fires a
   daily schedule once, not three times.
4. Wake the evaluation step.

A crash between steps 2 and 3 is harmless: after the restart the timer is still
due, the insert hits the unique `(super_agent_id, source_id, dedupe_key)` and
does nothing, and step 3 completes.

Three cases need a rule:

- **Disabled agent.** Its timers are not read. They fire late when it is
  enabled again.
- **Late timers.** The event says when the timer was due and how late it is.
  The brain decides whether a stale follow-up still matters; the host does not
  drop it.
- **Stopped Ticket.** `StopTicket` cancels the follow-ups tied to that Ticket.

Cron expressions are parsed with `robfig/cron/v3`, which the module already
uses, in the schedule's time zone.

## 4. What the brain sees

**Every event** gains two things in its context
([superagent/router.go](../../superagent/router.go), `writeRouterContext`):

- `NOW`, with the date, weekday, time and zone. Models compute dates badly
  without it.
- `TIMERS ON THIS THREAD`, listing each active timer's id, due time and note.

**A timer event** reads like a ticket update:

```
A follow-up you set on 14 Nov 16:02 is due (due 15 Nov 09:00, 3 minutes late).
Your note: Check whether Li approved the migration. If not, ask again and tell the thread.
Ticket: tkt_… "Migrate billing tables" is ready.
```

It arrives on the thread's own router conversation, so the brain still has
what was said there. Its final message is the reply on that thread, and an
empty message sends nothing.

**The charter** gains one paragraph under COORDINATING WORK: set a follow-up
when you are waiting on someone or promised to check back; cancel it when the
answer arrives first; never use one to poll a worker, since ticket updates
already arrive on their own.

## 5. Two new actions

```json
{"action":"follow_up","in":"18h","note":"…","ticket_id":"tkt_…","reason":"…"}
{"action":"follow_up","at":"2026-11-15T09:00:00+08:00","note":"…"}
{"action":"cancel_timer","timer_id":"stm_…","reason":"…"}
```

- `in` takes minutes, hours or days (`90m`, `18h`, `3d`). `at` takes an
  RFC 3339 time. Exactly one is given.
- `validateDecision` checks the shape. `preflight` checks the limits in §6,
  that `ticket_id` is this agent's, and that `timer_id` is an active timer on
  this event's thread.
- `apply` creates or cancels the timer. The trigger's disposition is unchanged
  by these actions: a turn that only sets a follow-up is `answered` or
  `ignored` as before, with the follow-up named in its reason.
- In observe mode nothing is created, and the summary reads "would follow up
  at …", which falls out of the existing early return in `evaluate`.

Chats on bound channels need nothing extra. `BrainFinished`
([superagent/brain.go:80](../../superagent/brain.go)) already passes the
brain's action blocks to `apply`. As with ticket updates, the timer event
reaches the brain as a router turn outside the chat's conversation, and its
reply is posted in the chat's thread.

## 6. What the host enforces

A follow-up is set by a turn that an allowed sender started, so it needs no
gate of its own. The limits stop a crafted or confused turn from creating a
loop that spends tokens unattended:

| Limit | Proposed default |
| --- | --- |
| Active follow-ups per thread | 5 |
| Active follow-ups per agent | 50 |
| Shortest delay | 1 minute |
| Longest delay | 90 days |
| Note length | 2,000 characters |
| Timer events per agent per day | 200; past that, timers wait and the agent's activity shows a warning |

A timer event may set another follow-up, which is how "check every hour until
it merges" works. The daily budget is what bounds that chain.

## 7. Schedules

The agent record gains `schedules`, set in the form and through
`POST /api/v1/super-agents`:

```json
"schedules": [{
  "id": "digest", "name": "Morning digest", "cron": "0 9 * * 1-5",
  "timezone": "Asia/Shanghai", "prompt": "Post the open-ticket digest.",
  "to": "<a place from PLACES>", "enabled": true
}]
```

- Saving the agent reconciles its schedules into timer rows: new ones are
  inserted, changed ones get a new `due_at`, removed ones are cancelled.
- Each schedule has its own thread, `schedule:<schedule id>`, and so its own
  continuing router conversation. The brain remembers what it reported
  yesterday without a separate log.
- `to` is resolved with `resolveDestination` when the timer fires, so a place
  that was removed fails the event visibly. With no `to`, the reply stays in
  the agent's activity, and the brain can still `post`.

The Console lists an agent's timers beside its Tickets, with a cancel button:

| Method and path | Purpose |
| --- | --- |
| `GET /api/v1/super-agents/activity?id=` | Also returns `timers`: active ones, and those fired or cancelled recently |
| `POST /api/v1/super-agents/timers/cancel` | Cancel a timer by id |

## 8. Order of work

1. **Follow-ups.** The table, `fireDueTimers`, the two actions, the context and
   charter changes, and timers in the activity response. Update
   [contract/SUPER_AGENTS.md](../../contract/SUPER_AGENTS.md) and
   `contract/openapi.yaml`.
2. **Schedules.** The `schedules` field, reconciliation on save, and the form.
3. **Later.** Route the channel engine's cron triggers through the inbox for
   agents with a worker policy, so there is one scheduling path. Add the
   consolidation pass as a built-in schedule.

Tests for step 1: a timer fires once across a restart between insert and
update; a late timer says so; a disabled agent's timers wait; limits reject in
`preflight`; observe mode creates nothing; stopping a Ticket cancels its
follow-ups; a follow-up set from a chat replies in that chat's thread; cron
advance across a daylight-saving change.

## 9. Open questions

The follow-ups build took the draft's answer to each of these.

1. **May the brain create schedules?** "Send me this every Monday" in a chat
   is a natural request, but a recurring action set from event text is a
   standing rule. The draft says no for now: the brain asks the operator to add
   it, or chains follow-ups within the daily budget.
2. **Should a human reply cancel a follow-up automatically?** The draft says
   no. The brain sees the thread's timers with every event and cancels the one
   that no longer applies. Automatic rules would guess wrong when the reply is
   about something else.
3. **Should asking a question set a follow-up by default?** A `post` on behalf
   of a thread is remembered for 72 hours ([superagent/asks.go](../../superagent/asks.go))
   and nothing chases it. The draft leaves it to the charter; a host default,
   such as 24 hours, would be more reliable and less flexible.
4. **Which time zone is the default?** The draft uses the daemon's zone for
   `NOW` and for a schedule with no `timezone`. A per-agent zone is the
   alternative when the operator and the team are in different places.
5. **Are the limits in §6 right?** They are guesses, not measurements.

## 10. How the build differs from the draft

- **Table.** The second index is on `(super_agent_id, status, updated_at)`,
  which serves the per-agent queries; nothing reads timers by thread alone. The
  time columns hold whole seconds in UTC, so they compare correctly as text.
- **Record.** It has `fired_at` and no `fired` count, `cron` or `timezone`:
  those belong to schedules.
- **Batch.** A pass reads up to 1,000 due timers, not 100. The timers of a
  disabled agent stay due, and a small batch would let them crowd out the rest.
- **`cancel_timer`** on a timer that already fired or was cancelled does
  nothing and is not an error, since the router may race the scheduler.
- **A failed timer event** is recorded and nothing else. Without this rule the
  existing fallback would pass the note to the thread's worker or tell the
  thread the router is unavailable.
- **Chats.** Only a brain with a worker policy is briefed, so only it can set
  follow-ups from a chat.
- **Console.** The agent page lists follow-ups with a cancel button now, ahead
  of schedules, since the brain sets them unattended.
