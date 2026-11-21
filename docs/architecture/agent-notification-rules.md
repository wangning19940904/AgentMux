# Delivery rules for messages an agent sends on its own

Status: proposed · 2026-11-20 · branch `claude/notify-rules-e1e5dc` (built there, not merged)

An agent writes to people when nobody asked it to in that turn: a Ticket's
report hours after the request, a follow-up it set for itself, a supervision
note about a stalled Ticket, a reply to a document edit. Today each goes out
the moment the brain answers, at 3 a.m. as at noon, as often as the events
come, and in the middle of whatever the chat is discussing.

This proposal adds per-agent delivery rules for those messages: quiet hours, a
daily cap per chat, a wait for the chat to go quiet, and one message a day about
one thing. It borrows the idea from the "Another Me" agent of Linger
(simplexai-labs/linger), which records app events at night, sends at most five
reminders a day, waits for three quiet minutes and judges one item in one state
once a day. Nothing is copied from Linger's code.

Every rule is off until the operator sets it, so an agent saved before this
change behaves as before.

## 1. Which messages count

Every message an agent sends goes through `Service.sendReply` or the `post`
action in `apply` ([superagent/service.go](../../superagent/service.go)). The
paths, and whether a person asked in that turn:

| Path | Where | Unprompted? |
| --- | --- | --- |
| The brain's reply to a person's message (Feishu mention or DM, bound channel, email to the agent, mailbox, Console) | `apply` → reply | No |
| A reply on a thread to a message that did not address the agent (GitHub comment without a mention, Feishu message without a mention on a Ticket's thread) | `apply` → reply | Yes |
| A reply to a Feishu document edit (`notify_chat_id`) | `apply` → reply | Yes |
| The brain's report when a Ticket's worker finishes a turn (`ticket_update`) | `apply` → reply | Yes |
| The worker's own report when the brain cannot be woken or fails (`relayReport`, the fallback in `runTicket`) | [coordination.go](../../superagent/coordination.go), service.go | Yes |
| A follow-up the brain set for itself fires (`timer`) | `apply` → reply | Yes |
| A supervision signal in `act` mode (`supervision`) | `apply` → reply | Yes |
| `post` to another chat, person or agent | `apply` → post | Same as the event it answers |
| AgentMux's notices after the reply (a plan to approve, a review outcome) | `apply` → notices | Same as the event |
| "Plan approved" after a person approves in the Console or a card | [plans.go](../../superagent/plans.go) | No: a person just acted |
| The brain's answer to a plan decision (`plan`) | `apply` → reply | No |
| "Could not carry out what I asked for" in a chat turn | [brain.go](../../superagent/brain.go) | No |
| "My router is unavailable" after retries | `routerFailed` | Same as the event |

The signal is the Trigger: `kind` is set only on events AgentMux raises
itself (`ticket_update`, `timer`, `supervision`, `plan`), `addressed` says
whether a person addressed the agent, and `source_kind` tells a document edit
from a message. `triggerDelivery` in
[superagent/delivery.go](../../superagent/delivery.go) turns those into a class:

- **Direct**: a source event that addressed the agent and is not a document
  edit, and every `plan` event. Never held.
- **Unprompted**: `ticket_update`, `timer`, `supervision`, a document edit, and
  a source event that did not address the agent.

A GitHub comment that mentions the agent or assigns it, and an email to its
mailbox, are a person asking, so their replies are direct. The Ticket report that
follows hours later is not.

Out of scope: the channel engine's cron schedules (`core/scheduler.go`), which
run a prompt as a chat turn at a time the operator chose, and channel Agents
that are not Super Agents.

## 2. Settings

One field on the agent, stored with the rest of its record:

```go
type SuperAgentDelivery struct {
	QuietStart       string // "HH:MM"; both empty is off
	QuietEnd         string // a start after the end crosses midnight
	TimeZone         string // IANA; empty is this machine's zone
	DailyCap         int    // per chat or recipient per day; 0 is none
	QuietChatMinutes int    // 0 sends at once; at most 60
	Held             string // queue (empty) or digest
	OncePerDay       bool
}
```

`core.SuperAgent.Delivery`, JSON `delivery`, omitted when zero. `Save` checks it
(`normalizeDelivery`): both quiet times or neither, valid times that differ, a
known time zone, a cap of 0 to 100, a delay of 0 to 60 minutes.

**A chat or recipient** is where the message lands: a Feishu chat
(`channel:chat`), a GitHub Issue or pull request, an email address, a mailbox
address. The cap and the quiet-chat delay count per recipient, so a busy
GitHub thread never holds back a Feishu chat.

## 3. The gate

`deliver(agent, source, target, text, class)` replaces the body of
`sendReply`. A direct message, or any message of an agent with no rule on,
goes at once, as today. An unprompted message under rules takes the
service's delivery lock, which only such messages take, and, in order:

1. **Once per day.** With `OncePerDay`, a message about an item in a state
   already sent to this recipient today is dropped and recorded. A held
   message about the same item and state is replaced by the newer text.
2. **Quiet hours**: held until they end.
3. **Daily cap**: held until the next day, counted in the agent's time zone.
4. **Messages already held for this recipient**: held behind them, so order
   is kept.
5. **Quiet-chat delay**: held while anything was said in the chat within
   the delay.
6. Otherwise sent, and counted.

Chat activity is every inbound message the engine sees (`InterceptMessage`, for
all channels, including bound ones), every event a Source ingests, and every
message the agent sends. It is kept in memory; after a restart every chat
counts as quiet.

The note `deliver` returns goes into the Trigger's reason ("reply held: quiet
hours"), so the event list says why nothing went out. A held message is not a
failure: the Ticket's `reply_error` stays empty.

### Items and states

| Event | Item | State |
| --- | --- | --- |
| `timer` | its Ticket, else its thread | `follow_up` |
| `supervision` | its Ticket, else its thread | `supervision` |
| Document edit | the document's thread | `edited` |
| Unaddressed message | its thread | `message` |
| `ticket_update`, worker reports | none | never a repeat |

A Ticket report is news by construction: each one follows a finished turn, and
a report after rework is the one the person waits for. Reports are
therefore never dropped as repeats, only held.

## 4. Held messages

Held messages are kept per agent as a setting, `super_agent_delivery:<agent>`,
beside `super_agent_plans:` and `super_agent_trust:`, with today's counts and the
last 20 dropped messages. No migration is needed. They are deleted with the
agent. At most 200 are kept per agent; past that the oldest is dropped and
recorded.

Every 15 seconds the service looks at agents with held messages and, per
recipient, releases them when quiet hours are over, the cap allows and the chat
is quiet:

- **queue** (the default): each held message goes on its own, oldest first,
  while the cap allows. If more are held than the cap leaves, the last
  allowed message is a digest of the rest, so a night of reports never
  spills into a second day.
- **digest**: everything held for a recipient goes as one message, "N
  messages I held back:", each line with the time it was held.

A digest goes where the newest held message would have gone. A message that
fails to send is tried three times, then dropped and recorded. Turning every
rule off releases what is held at the next look. On start the service finds
agents that held messages before the restart.

Why not drop at night, as Linger records app events and nothing more? An
AgentMux report is the result of work a person asked for, so losing it is
worse than delivering it late. A digest is offered for those who prefer one
message in the morning.

## 5. Urgent overrides

None in this version. Every unprompted message obeys the rules. The obvious
candidates are a failed Ticket, a plan or review that needs a person's decision,
and a follow-up the person asked for at a set hour ("remind me at 23:00"). The
brain is the one that knows, so the natural design is an `urgent` flag on its reply
and `post`. Such a message would skip quiet hours and the cap, but not the
quiet-chat delay, with an allowance of its own per day so the flag cannot become
the default. That needs a change to the action contract and the router prompts,
so it is left for a later step (§8).

## 6. Console

- **Agent form**, under advanced settings, "Messages it sends on its own":
  quiet hours on/off with from/until, time zone (placeholder: the browser's),
  daily cap per chat, minutes to wait for a quiet chat, held messages
  (queue or digest), and "one message a day about the same thing".
- **Agent activity** gains `delivery`: held messages with recipient, reason,
  when held and when they go; the last dropped messages with why; today's count
  per recipient. The agent page lists held and dropped messages under "Held
  messages".
- The event list shows the note in each Trigger's reason.

Agent records and activity are not in `contract/openapi.yaml` (the plans and
dispatch endpoints are), so the API contract does not change.

## 7. What was built

- `core.SuperAgentDelivery` and `SuperAgent.Delivery`.
- [superagent/delivery.go](../../superagent/delivery.go): classification,
  recipient keys, the window, the gate, release, Console activity, validation.
- Call sites: `apply` (reply, posts, notices), `routerFailed`, `relayReport`,
  the runTicket fallback. `sendReply` stays for direct messages.
- Tests in [superagent/delivery_test.go](../../superagent/delivery_test.go):
  quiet hours across midnight and in two time zones (one with daylight
  saving), validation, a direct reply going at once while the report waits
  for the morning, the cap per recipient and its release the next day with
  a digest for overflow, the quiet-chat delay (GitHub thread and Feishu chat),
  digest mode, once per day and replacement while held, a restart with held
  messages, and the classification of every Trigger kind.
- Console form and activity list, with tests.

## 8. Open questions

1. **Default for held messages.** Queue keeps each report as it was; digest
   is quieter. The build defaults to queue.
2. **Urgent overrides** (§5): add the brain's `urgent` flag, or exempt fixed
   kinds such as failed Tickets?
3. **Email and GitHub mentions** count as a person asking, so their direct
   replies are never held. If a mailbox should keep quiet hours for
   everything, that is a per-source setting, not built.
4. **Judging less, not only sending less.** Linger's once-a-day rule saves a
   model run. Here it applies at delivery, after the brain's turn. Applying it
   at intake to supervision signals and document edits would also save the
   turn, but could hide an event the brain needs. Worth doing once the
   delivery rule has been used.
5. **Learned rules.** The memory-loop proposal (phase 5, rules learned from
   feedback) could set these per recipient or kind ("tell me client replies
   right away"). This design keeps the rules per agent, which is where the
   learned ones would plug in.
6. **Console actions** on a held message ("send now", "discard") need two
   endpoints and an OpenAPI entry. Not built.
