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
  characters) and the event follow it.
- **The final message is the reply**, sent where the event came from. An empty message
  sends nothing. To have AgentMux act, the router ends the message with one block, which
  is removed before the reply is sent:

  ```
  <agentmux-action>
  {"action":"accept","worker_id":"auto:codex","title":"…","instructions":"…","reason":"…"}
  </agentmux-action>
  ```

  `accept` starts a Ticket, `attach` passes the event to a Ticket on the thread with the
  router's optional note, and `stop` stops one. `reason` goes to the audit log. A
  JSON-only answer in the form routers used before is still read as an action.
- **Memory** is files in `memory/`, indexed by `memory/MEMORY.md`, which the router
  keeps. A read-only router can read it but not change it.

## What the host enforces

The router reads untrusted text. What limits the harm a crafted event can do:

- **Allowed senders** are checked before the router sees an event, and an empty
  list admits nobody. `*` is accepted only for Feishu messages. This is the only hard
  limit on who can reach the router. Channel sources have no list: whoever the
  channel lets reach the bot reaches the brain, as with any bound channel.
- **An action** can only name a worker on this Super Agent's roster, `auto:<framework>`
  for one of its worker frameworks, and a Ticket already on the event's thread.
- **The router** is a [router agent](#router-agents): it runs on this machine in the
  approval mode chosen for it, YOLO by default. With YOLO it can run any command as the
  daemon's user, including reading AgentMux's own configuration; its charter, not a
  sandbox, is what tells it to treat event text as data. Choose `auto`, `auto_edit` or
  `plan` for a Super Agent whose triggers carry text from people you do not trust.
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

- **Feishu messages:** mentions and direct messages are claimed, so the channel's
  own Agent does not also answer. `/` commands stay with the channel. Follow-ups
  in a thread with a Ticket reach the router without another mention.
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
    └── work/                the worker's working directory, or a link to it
```

- A worker whose Agent uses per-conversation worktrees gets its worktree in `work/`, on
  an `agentmux/…` branch, instead of under `<repo>.agentmux-worktrees`. So does an auto
  worker with no `auto_worker_dir`, as a plain directory.
- Any other local worker keeps the directory its Agent is configured with, and `work/`
  links there. A remote worker's directory is on its own machine; its folder has the
  record files only.
- The store stays the source of truth: AgentMux rewrites `ticket.json` on every change and
  only appends to the logs. Tickets created before homes existed have no folder and keep
  working where they started.
- Deleting a Super Agent keeps its home, since worktrees there can hold work that was
  never pushed.

### Auto workers

With auto workers on, the roster can be empty. `worker_runtimes` lists one or more
installed frameworks to launch workers with, and defaults to the router's. The roster
offers `auto:<framework>` for each; the router names one when no listed worker suits a
request, and the host starts a worker of that framework that was not configured in advance:

- Each Ticket gets its own session and directory. In a git repository the directory is a
  worktree in the Ticket's `work/` folder; with no `auto_worker_dir`, it is a new folder
  there. Another directory outside a repository is shared by the Tickets.
- An action supplies only the framework, the Ticket's title and instructions. A worker on
  the router agent's framework takes its provider, model and environment; another framework
  uses its active provider route. The directory is `auto_worker_dir`, or else `~/.agentmux/super-agents/<id>/work`
  (a Super Agent that still routes with an ordinary Agent uses that Agent's directory). The approval mode is `auto_worker_approval_mode`,
  which every worker framework must support; left empty, each uses `auto_edit`, or `auto`
  where it lacks `auto_edit`. MCP servers, skills and channels are not inherited.
- `max_concurrency` limits how many run at once. Auto workers run on this machine unless
  `worker_machines` lists others; see below.
- Each worker framework has one Agent instance, `saw_<super agent id>_<framework>`, removed
  with the Super Agent. Worktrees are kept after a Ticket ends, and so is the home.

### Workers on other machines

`worker_machines` lists machines connected to this AgentMux, as linked devices or SSH
hosts, that the agent may also launch workers on:

```json
"worker_machines": [{ "host_id": "dev_23f0…", "dir": "~/code/app", "capabilities": "Has the GPU." }]
```

- The roster offers `auto:<framework>@<host_id>` for each machine and worker framework,
  with the machine's name, the operator's `capabilities` text, and whether the machine is
  reachable and has that framework installed right now. A decision can only name an entry
  on the roster.
- The machine itself creates the worker. The host sends the framework, `dir` and
  `auto_worker_approval_mode` to `POST /api/v1/super-agents/workers/launch` on that machine,
  which checks them as it would for its own agents: the framework must be installed, `dir`
  must exist (empty gives the worker `~/.agentmux/hosted-workers/<agent id>/work`), and the
  approval mode must be an unattended one. The worker is the Agent instance
  `sawr_<agent id>_<framework>` there. It uses that machine's provider route, tools and
  environment; nothing is inherited from the agent's machine.
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
a speed, its permissions and the MCP servers it may use. Each router agent belongs to one Super Agent and
works in its home, so it can read every Ticket's folder; another Super Agent cannot route
with it.

- It runs unattended in any approval mode its framework offers except `manual`: `yolo` by
  default, or `auto`, `auto_edit` or `plan`. A framework with only `manual` cannot route.
  Saving without a mode keeps the one it has, so routers saved when routers were
  read-only stay in `plan` until the operator chooses otherwise. Its framework cannot
  change after it is created.
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
| Routing rules | Saved, and sent to the router with every event: what to take on, what to ignore, which worker suits what. A template can fill it in, to edit. |
| Workers | Worker frameworks for auto workers, Agents already on the roster, or both. |
| Triggers | Where requests come from. |

Mode, maximum concurrency and whether the Super Agent is enabled are under Advanced settings.

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
| `GET /api/v1/super-agents/activity?id=` | Tickets, events, worker reachability, trigger health |
| `GET`, `POST`, `DELETE /api/v1/super-agents/routers` | List router agents with the frameworks that can route and the MCP servers they may use; create or update one; delete an unused one |
| `POST /api/v1/super-agents/setup` | Propose a configuration from an instruction; stores nothing |
| `POST /api/v1/super-agents/trigger` | Send a request from the Console |
| `GET /api/v1/super-agents/github/setup` | The loopback address GitHub deliveries reach, and whether the API needs credentials |
| `GET /api/v1/super-agents/github/operator` | Local Console only: who is signed in to gh here, and the repositories they administer |
| `POST /api/v1/super-agents/github/login`, `/login/poll`, `/login/cancel` | Local Console only: sign the agent in with gh's one-time code; the first poll after success carries the token |
| `POST /api/v1/super-agents/github/invite` | Local Console only: invite the agent to a repository as the operator (unless `invite` is false), then accept as the agent |
| `POST /api/v1/super-agents/github/account` | The GitHub login, classic scopes and reachable repositories of a token in the body, or of a saved Source's token when the body sends it redacted with `super_agent_id` and `source_id` |
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
- Images and attachments on incoming messages are not passed to the router or workers.
- Choosing a DSH model per Agent is not supported yet. DSH uses its configured default.
