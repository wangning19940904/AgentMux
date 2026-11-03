# Super Agents

A Super Agent is a persistent router. Events arrive from its triggers, a router
Agent decides what each one means, and AgentMux applies that decision to local
or remote workers. The design follows DeepSeek's
[dsh-superagent](https://github.com/deepseek-ai/dsh-superagent-prototype)
prototype, simplified to run inside the AgentMux daemon.

## Flow

```
Trigger source -> gate (allowed list) -> stored once -> router Agent decision
  accept  -> new Ticket -> worker (local or remote) -> reply where it came from
  attach  -> input to a Ticket on the same thread
  stop    -> cancel a Ticket on the same thread
  ignore  -> recorded, and the router's reply is sent if it wrote one
```

An addressed event from a sender who is not allowed is recorded with its reason, and stops there. Ordinary traffic that addresses nobody and belongs to no Ticket is not stored.

When no available worker can do the work and auto workers are off, the router ignores the event and tells the sender. A decision the host rejects, such as a worker that is not on the roster, fails the event and also tells the sender.

## What the host enforces

The router reads untrusted text, so nothing depends on the model behaving:

- **Allowed senders** are checked before the router sees an event, and an empty
  list admits nobody. `*` is accepted only for Feishu messages.
- **A decision** can only name a worker on this Super Agent's roster, `auto` when
  auto workers are on, and a Ticket already on the event's thread.
- **The router Agent** must run on this machine and cannot use the `yolo` approval
  mode. Choosing a framework creates a router in `plan` mode, so a framework without
  a read-only mode, such as Kimi, cannot route. An existing Agent can be chosen instead.
- **Observe mode** is the default. It records what the router would do, without
  running work or messaging anyone. Switch to Act when the decisions look right.

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

- **Feishu messages:** mentions and direct messages are claimed, so the channel's
  own Agent does not also answer. `/` commands stay with the channel. Follow-ups
  in a thread with a Ticket reach the router without another mention.
- **Feishu documents:** the channel's app needs access to each document, and the
  app must subscribe to `drive.file.edit_v1`. Edits within the debounce window
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
  dropped. The daemon must be reachable from GitHub; it listens on loopback by
  default, so this needs a tunnel or reverse proxy that exposes only `/hook/`.

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

### Auto workers

With auto workers on, the roster can be empty. The router names `auto` when no listed
worker suits a request, and the host starts a worker that was not configured in advance:

- Each Ticket gets its own session and, in a git repository, its own worktree under
  `<repo>.agentmux-worktrees`. Outside a repository, Tickets share the directory.
- A decision supplies only the Ticket's title and instructions. The runtime, provider,
  model and environment come from the router Agent. The directory is `auto_worker_dir`,
  or an existing router Agent's, or else `~/.agentmux/super-agents/<id>/work`. The approval
  mode is `auto_worker_approval_mode`, by default `auto_edit`, or `auto` on a framework
  without it. MCP servers, skills and channels are not inherited.
- `max_concurrency` limits how many run at once. Auto workers run on this machine only.
- They share one Agent instance, `saw_<super agent id>`, removed with the Super Agent.
  Worktrees are kept after a Ticket ends.

## Choosing a framework

`router_runtime` names an installed framework, and no Agent has to exist first. The host
keeps one router Agent per framework, `sar_<framework>`, and uses the framework's active
provider route. It is shared between Super Agents and stays when one is deleted. Its
provider or model can be changed on the Agents page, and those changes are kept.

## Setup assistant

`POST /api/v1/super-agents/setup` turns an administrator's instruction into a proposed
configuration, using the router Agent:

- It returns a draft and stores nothing. Saving it passes the usual validation.
- Only the Console can reach it, so text in an event cannot change the configuration.
- Secrets and `trust_from_header` are never sent to or accepted from the model. The
  response names the secrets the form must collect. Secrets pasted into an instruction
  would reach the model, so the form asks people not to.
- It cannot change the router, the roster, or the auto worker approval mode.

## API

These routes are for Console administrators and are closed to tenant credentials.

| Method and path | Purpose |
| --- | --- |
| `GET`, `POST`, `DELETE /api/v1/super-agents` | List, save and delete; secrets are write-only |
| `GET /api/v1/super-agents/activity?id=` | Tickets, events, worker reachability, trigger health |
| `POST /api/v1/super-agents/setup` | Propose a configuration from an instruction; stores nothing |
| `POST /api/v1/super-agents/trigger` | Send a request from the Console |
| `POST /api/v1/super-agents/tickets/stop`, `/retry` | Stop or retry a Ticket |

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

- One daemon owns a Super Agent's Tickets; running several against one database is
  not supported.
- Images and attachments on incoming messages are not passed to the router or workers.
- Choosing a DSH model per Agent is not supported yet. DSH uses its configured default.
