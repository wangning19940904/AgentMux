# AgentMux 3.0: Hub, Nodes and Super Agents

Status: proposal · 2026-11-12 · branch `claude/project-architecture-refactor-ca965e`

This proposal makes three changes:

1. **Super Agents replace regular Agents.** Each existing Agent becomes a Super
   Agent in solo mode. What used to be an `AgentInstance` becomes a **worker
   profile** that a Super Agent runs. The Console, API and data model will have
   one agent concept.
2. **Hub and outbound nodes replace the SSH controller.** One hub is the central
   meeting point. Every machine runs the AgentMux it already has, and that
   daemon *dials out* to the hub. Only the hub needs to be reachable. Laptops
   behind NAT, servers without a domain and phones all work.
3. **Agents and people message each other.** The hub keeps durable
   conversations, with one mailbox per recipient. Super Agents on any machine,
   owned by any account, can ask, delegate to and notify each other. People
   reach them from IM, the web or a phone.

The ideas come from Bearisbug/imbo, Agent Relay and OpenAgents (§3). The
migration reuses AgentMux's existing seams so that each phase ships on its own
(§9).

---

## 1. Where AgentMux is today

| Area | Today | Problem |
| --- | --- | --- |
| Agent model | Two parallel models. `AgentInstance` ([core/agent_instance.go](../../core/agent_instance.go)) is bound to channels, schedules and invocations. `SuperAgent` ([core/superagent.go](../../core/superagent.go)) has its own router, workers, sources and Tickets. Router agents (`sar_*`) and auto workers (`saw_*`) are hidden, read-only AgentInstances. | Users must learn two kinds of agent. Super Agent Sources overlap with Channels and Triggers: Feishu message claims ([superagent/claims.go](../../superagent/claims.go)) and GitHub webhooks are both separate from `/hook/{id}`. Orchestrations are a DAG ([core/orchestration.go](../../core/orchestration.go)), and Ticket `depends_on` is a second DAG. |
| Multi-machine | A controller dials each machine over SSH ([remote/](../../remote/)). It imports `~/.ssh/config`, follows ProxyJump chains, pins host keys and uploads the binary. It reads the remote bridge token by running a script against launchd plists or `/proc/<pid>/environ` ([remote/token.go](../../remote/token.go)), then tunnels HTTP. The fleet query, execute and sync features fan out over those tunnels ([server/fleet.go](../../server/fleet.go), [server/fleet_sync.go](../../server/fleet_sync.go)). | The controller must reach every machine by SSH, so a NAT'd laptop, a phone or a teammate's machine cannot join. Each controller is its own island. The token it extracts is the *full admin* credential of the remote machine. "One daemon owns a Super Agent's Tickets" ([contract/SUPER_AGENTS.md:332](../../contract/SUPER_AGENTS.md)). |
| Identity | (When written) tenants were host *applications* such as the Web UI, the desktop app or a service. The admin is "whoever holds the bridge token". Tenancy has since been removed (contract 3.0): there are only admins and member accounts ([core/principal.go](../../core/principal.go)). | There is no concept of a *person*, so agents of different users cannot be told apart, contacts are impossible and so is per-user consent. |
| Clients | The Console binds to `127.0.0.1:8765`, plus the desktop app, the menu bar app and the CLI. | No mobile client and no push notifications. The Console reaches another machine only over its SSH tunnel. |
| Layering | `server/` is about 17k lines and holds business logic: fleet sync (1,300 lines), Feishu Open Platform automation (700 lines) and OpenAI Responses (840 lines). `core/` is about 13.5k lines and mixes the channel runtime, meetings, invocation and guard. | The README calls `server/` a thin transport, but it is not. The hub needs the same services without the HTTP layer. |

**What is already good and should stay:** the single Go binary;
PostgreSQL as the single source of truth; adapter registries; the Super Agent
safety model (host-enforced allowed senders, roster-checked actions, observe
mode, untrusted-text charter); durable channel tasks; encrypted observability;
Guard. (Tenancy grant levels `read`/`use`/`manage` were listed here; they were
removed with tenancy in contract 3.0, and agent sharing will need its own model.)

**The seam that makes the migration cheap:** Super Agent remote workers already
go through `superagent.HostDialer` ([superagent/workers.go:58](../../superagent/workers.go)),
an interface that only resolves a host and opens a `net.Conn` to that host's
daemon. The fleet and channel peer clients work the same way. If the hub link
implements that dialer, everything that runs over SSH today runs over the hub
on day one (§4.3).

---

## 2. Goals and non-goals

**Goals**

- A machine joins with one command and one approval, with no inbound port and no domain name.
- From any client, including a phone, you can see every machine, agent, Ticket and conversation you are allowed to see.
- A Super Agent can start and steer work on any machine its owner has, and keeps the Ticket's state and context in one place.
- Agents and people message each other across machines and across accounts, with consent, auditing and delivery guarantees.
- Single-machine users notice nothing new: `amux` alone still works.

**Non-goals for 3.0**

- Federation between *separate* hubs, such as two companies. §6.6 sketches it as a later phase.
- Moving full transcripts, secrets or worktrees to the hub. They stay on the nodes (§4.4).
- Replacing IM channels. Feishu, Slack and the others remain entry points.

---

## 3. What we borrow

| Project | Idea | How we apply it |
| --- | --- | --- |
| **imbo** | One daemon per computer keeps an *outbound* WebSocket to the server: `hello` with the session list, then `presence`, `deliver`, `ack`, `nack`, `cancel` and `wake`. | The node link (§4.3). |
| imbo | A delivery state machine per recipient session (`pending → pushed → delivered`), with ack timeouts, backoff (30 s, 1 m, 2 m, 5 m, then every 10 m) and ordered delivery that stops at the first message still backing off. | The mailbox (§6.2). |
| imbo | **Wake**: a message from the phone reopens a closed session on its computer. | Starting an idle worker or router session (§6.3). |
| imbo | Contacts by account ID with mutual consent; groups whose members choose which of their sessions receive messages; block, report and ban. | Cross-user contacts and rooms (§6.5). |
| imbo | The receiving agent may reply on its own, but must ask its owner before sending files or data out, deleting, spending money or touching production. | The default policy for cross-user requests (§6.5). |
| **Agent Relay** | Two planes. The *messaging fabric* is made of flat, equal agents. The *compute layer* is made of nodes that advertise capabilities such as `spawn:codex`. | Agents and Tickets sit above; nodes and placement sit below (§5.3). |
| Agent Relay | Placement: `spawn {capability, node? \| "self", session_ref}`. **Resume is a targeted spawn on the origin node.** Idempotent invocations with at-least-once delivery and reconciliation when a node reconnects. | Ticket placement and recovery (§5.3). |
| Agent Relay | A bounded, durable mailbox: per-recipient `seq`, a cumulative ack (`up_to_seq`), a TTL, and dead-lettering when the session cannot be resumed. | §6.2. |
| Agent Relay | The **observer plane**: an append-only event log for each workspace with a monotonic `seq`, `GET /events?since=` backfill and a live WebSocket stamped with the same `seq`. UIs never pose as agents. | The client API for web and mobile (§7). |
| Agent Relay | Headless servers enroll with a **device code** approved on a phone or Mac. A teammate can start work on your machine only after you opt in (`spawn_allow_teammates`). | `amux hub join` and node policy (§4.3). |
| **OpenAgents** | A workspace as a persistent URL hub. Devices pair with a code, and **a device belongs to the person who connected it**. Agents are private by default and shared with named teammates. | Ownership and sharing (§4.5). |
| OpenAgents | One roster for people and agents; approvals as cards in the thread, with a policy that decides who signs off; issues and tasks; a shared, bounded thread context (30 messages, 16k characters). | People as participants, approvals and context bundles (§5.4, §6, §10). |
| OpenAgents | Tunnels that expose a dev server so its result can be previewed from any device. | Preview through the hub (§10). |

---

## 4. Target topology

```
                       ┌──────────────────────── Hub (amux hub) ────────────────────────┐
  phone / PWA ──HTTPS──▶  client API · observer event log (seq) · push (APNs/FCM/WebPush) │
  web Console ──HTTPS──▶  accounts · devices · agent directory · conversations · mailbox  │
  IM (Feishu…) ─────────▶ Tickets + leases · placement · approvals · audit · blob store   │
                       │  PostgreSQL                                                     │
                       └───────▲────────────────▲──────────────────▲────────────────────┘
                               │ WSS (outbound) │                  │
                     ┌─────────┴───┐    ┌───────┴─────┐     ┌──────┴──────┐
                     │ node: Mac   │    │ node: GPU   │     │ node: bob's │
                     │ (NAT, no IP)│    │ server      │     │ laptop      │
                     │ routers,    │    │ codex,      │     │ (other      │
                     │ workers,    │    │ claude      │     │  account)   │
                     │ channels    │    │ workers     │     │             │
                     └─────────────┘    └─────────────┘     └─────────────┘
```

- **One binary, three roles:** `amux` (a node, as today), `amux hub serve` (a
  hub), and an embedded hub. A single-machine user runs the node and the hub in
  one process on loopback. That is the default, so nothing changes for them.
- **The hub is the only component that must be reachable.** Nodes never accept
  inbound connections from the hub, and the node Console stays on loopback.
- The hub is a coordinator, not a compute node. Routers and workers always run
  on nodes. A hub *process* may also be a node, as in the embedded case.

### 4.1 Reaching the hub without a domain or public IP

| Option | When to use it | What AgentMux ships |
| --- | --- | --- |
| **A. Small VPS or cloud VM** (recommended for teams) | Any team; a $5 machine is enough | `amux hub serve --domain hub.example.com` uses automatic TLS from Let's Encrypt, as imbo does. Without a domain it gets a self-signed certificate, and nodes pin its fingerprint at enrollment, so no CA is needed. |
| **B. Hub at home or the office, behind NAT** | No VPS available | `amux hub expose cloudflare\|tailscale-funnel\|frp` starts and supervises a tunnel and records the public URL in the enrollment links. |
| **C. Private network only** | Everything is on Tailscale, Headscale, ZeroTier or a corporate VPN | The hub binds to the tailnet address and phones join the tailnet. Optionally, embed `tsnet` so the binary joins the tailnet itself (decision D2). |
| **D. Embedded, single machine** | A single user | This is the default. Nothing is exposed. |

A node only ever needs *outbound* HTTPS to the hub URL, and it honors the proxy
variables AgentMux already imports from the login shell.

### 4.2 Hub storage and a standby hub

The hub starts as a single process, and node links reconnect with jitter and
resume from cursors, so restarting it is safe. A second machine can run as a
**standby hub** that takes over when the primary shuts down or loses its
network. The code is in [store/hubsync.go](../../store/hubsync.go),
[hubha/](../../hubha) and [hubrelay/](../../hubrelay); the rules follow.

**One PostgreSQL instance per machine.** Every machine, hub or not, runs exactly
one PostgreSQL instance holding its own data. Hub data is a fixed list of
ordinary tables in that same database (`store.HubTables`): accounts and their sessions, devices, pairing codes, what devices
report, and Web Push keys and subscriptions. Every other table is the machine's
own and is never copied. Hub data does not get its own instance or schema,
because on a hub the Console already reads these tables where they are.

The mailbox tables are not in the list yet. A node keeps its own outgoing mail
in the same tables, so a standby could not send mail of its own while they
followed the primary. Copying the hub's mailbox needs separate hub and node
mail tables first; until then, messages waiting on the hub do not survive a
failover.

**Only the hub service writes hub data.** Nodes never connect to a hub's
PostgreSQL. They send reports over the node link (§4.3), and the active hub
process validates them and writes hub data:

- No database port or password leaves the hub machine. Each node authenticates
  with its own device token and can only write its own records.
- The hub tables can change without upgrading every node; only the link
  protocol has to stay compatible.
- Reports are reconciled against hashes of what the hub holds, so after a
  reconnect or a failover a node simply resends whatever the hub lacks.
- With a single writer, fencing a stale primary (below) is a check in one place.

**The standby copies hub data through the hub service, not through PostgreSQL.**
PostgreSQL's own replication was rejected for three reasons:

- Physical streaming replication copies a whole instance, local data included,
  and leaves the copy read-only, so the standby could no longer write its own
  local data. It also needs the same major version on both ends, and the first
  two hubs run PostgreSQL 18 and 16.
- Logical replication of the hub tables avoids both problems, but the standby's
  subscription is an ordinary PostgreSQL client connection to the primary. The
  two hubs would need a direct network path and a database port open between
  them, which defeats the outbound-only design. It also needs
  `wal_level=logical` and a restart, and copies neither schema changes nor
  sequences.
- Each hub would have to manage the database's publications and subscriptions,
  and swap their direction on every failover.

Instead, the standby replicates over the same path as any node:

1. The standby pairs with the primary like any device, through the relay, and
   the primary's administrator allows it to follow (`amux hub standby
   <device-id>`, which sets `devices.hub_role`). No extra port or network path
   is needed.
2. While a hub is the primary, a trigger on every hub table appends each row
   change to `hub_changes` (`seq`, `epoch`, `tbl`, `op`, `row_data jsonb`) in the
   same transaction as the write itself. Writers take an advisory lock, so
   sequence order is commit order. A hub without a standby has the role `none`
   and logs nothing.
3. The standby long-polls `/uplink/v1/hub/changes?after=<seq>` up its link and
   applies each batch to its own tables in one transaction. Asking for changes
   after N acknowledges everything up to N. It starts from a snapshot of every
   hub table (`/uplink/v1/hub/snapshot`), taken in one repeatable-read
   transaction, and loads another whenever its cursor is older than the log the
   primary keeps (7 days).
4. The standby also copies the primary's TLS certificate and key
   (`/uplink/v1/hub/identity`). Its hub listener reads the certificate on each
   handshake, so devices' pinned fingerprints keep working after a failover.
5. Each hub runs its own schema migrations at startup, as AgentMux does today.
   A standby at a different schema version than the primary stops applying
   changes and reports the mismatch rather than writing the wrong columns.

This works between any PostgreSQL versions and wherever both machines can reach
the relay.

**Failover and fencing**

- **Relay and lease:** `amux hub relay <config.toml>` runs on the public VPS.
  Each hub keeps its own outbound tunnel there, and the relay forwards the
  public port (443) as plain TCP to whichever hub holds the lease. It renews
  that hub's lease (15 seconds by default) every 5 seconds through
  `POST /hub/v1/lease` on the hub's device listener, authenticated with the
  shared `[hub] lease_secret` and pinned to the hub certificate. The other hub
  is told `active=false`, which makes it follow.
- **Failover:** when the primary has not answered for the lease's length plus
  one renewal, so its lease has certainly expired on its own, the relay
  increments the epoch, records it, grants the lease to the standby if it holds
  a copy, switches forwarding and closes the devices' connections to the old
  hub. Devices reconnect through the relay to the new primary.
- **Epoch and fencing:** a primary that hears nothing for the lease's length
  fences itself. It refuses devices, drops their connections, and its trigger
  rejects any write to hub data. A hub refuses a lease older than the epoch it
  has seen. A primary that restarts starts fenced until the relay renews its
  lease, which takes seconds.
- **Not losing a new device:** pairing waits up to 10 seconds for an active
  standby to hold the new device before returning its token. Without that, a
  device paired just before a failover would be refused by the new primary and
  stop reconnecting. Other hub data needs no wait: nodes resend reports, and
  the rest is changed rarely by people.
- **Return of the old primary:** the relay never moves the lease back on its
  own. It tells the old primary `active=false`, so the old primary becomes a
  standby, discards its position and loads a fresh snapshot. That drops
  whatever it wrote and never copied. To follow, it links to the new primary
  with the device membership it gave itself when its standby was set up
  (`hub-peer.json` in the hub state directory). Moving the primary back is
  manual, on the relay's local control port (`POST /relay/v1/switch?to=<name>`).
- **While a machine is the standby,** hub data on it is read-only. Its Console
  cannot pair devices, change accounts, or sign in with an account
  (sign-in writes a session); use the primary for those.

Live frames still come from one hub process at a time. Running several active
hub processes on one database (with `LISTEN/NOTIFY` fan-out and sticky routing)
remains out of scope.

**First deployment (2026-11-13):** this Mac is the primary and
`bytedance-macmini` the standby. Its AgentMux database runs on the bundled
PostgreSQL 16, while this Mac runs 18; copying through the hub service does not
care. On the relay, `agentmux-relay.service` runs `amux hub relay` with
`/etc/agentmux-relay/relay.toml`, replacing the earlier `systemd-socket-proxyd`
units. Each hub reaches it through its own reverse tunnel: 18766 for this Mac
and 18767 for the standby.

Setting up a standby has one manual step: copy the primary's `hub-cert.pem` and
`hub-key.pem` into its hub state directory first. The relay pins that
certificate, so it cannot tell a hub that still presents its own certificate to
follow. Once the hub follows, it keeps the copy current itself.

### 4.3 The node link

**Enrollment**

```
amux hub join https://hub.example.com        # prints a code and a QR code; approve on the phone or in the Console
amux hub join https://hub… --token <one-time> # headless or automation: an enrollment token from the Console
amux hub join … --via-ssh my-server           # bootstrap: install AgentMux over SSH, then enroll; SSH is not used afterwards
```

- Enrollment uses the device flow (RFC 8628). The person who approves the code
  **owns** the node. The hub issues a node token, stores only its SHA-256 hash
  (as device tokens do today), and returns its own TLS fingerprint for pinning.
- The node keeps its token in the OS keychain where one is available, otherwise
  in a 0600 file. Revoking the node in the Console closes the link with code 4401
  and the node stops reconnecting (imbo's `CLOSE_UNAUTHORIZED`). A second link
  from the same node replaces the first (4409).
- The existing `remote/` SSH import becomes the `--via-ssh` bootstrap. The token
  extraction script ([remote/token.go](../../remote/token.go)) is retired: the
  hub never holds a node's admin token.

**Transport**

One WebSocket (`wss://hub/v1/node/link`, using gorilla/websocket, which is
already a dependency) carries a **yamux** session:

- **Stream 0 is the control channel**, with JSON frames as described below.
- **Streams opened by the hub towards the node** are reverse-tunnelled HTTP
  requests to the node's own API. This is what implements `superagent.HostDialer`
  and the fleet dialer, so `/api/v1/invocations/stream`, fleet query and execute,
  directory listing and terminal snapshots all work unchanged over the hub.
  **The node checks every tunnelled request against a node RPC route policy**,
  just as `memberRoutePolicy` ([server/auth_policy.go](../../server/auth_policy.go)) does today for member accounts. The hub reaches only what node policy
  allows (§4.5), never the full Console API.
- **Streams opened by the node towards the hub** carry uploads such as
  artifacts, ticket result files and usage batches.

```jsonc
// node → hub
{"type":"hello","node_id":"…","version":"3.0.0","capabilities":{"runtimes":["claude","codex"],"labels":["gpu","repo:github.com/acme/api"],"max_concurrency":4},"sessions":[…],"cursors":{"mailbox":1234}}
{"type":"presence","sessions":[{"agent_id":"…","session":"…","state":"busy|idle|closed"}]}
{"type":"report","stream":"usage|metrics|ticket|trace_meta","seq":88,"items":[…]}   // outbox, at least once
{"type":"ack","stream":"mailbox","up_to_seq":1240}
{"type":"woken","request_id":"…","error":null}
// hub → node
{"type":"welcome","account":{…},"node_id":"…","min_version":"3.0.0","acks":{"report":87}}
{"type":"deliver","seq":1241,"recipient":"agent:…","message":{…}}
{"type":"invoke","invocation_id":"…","op":"ticket.run|ticket.steer|ticket.stop|session.wake","args":{…}}   // idempotent by invocation_id
{"type":"cancel","invocation_id":"…"}
```

**Reliability:** every report stream has its own `seq` and the receiver acks
cumulatively. The node keeps an outbox in its own Postgres, so it keeps working
while the hub is down and replays its reports afterwards. Invocations are
idempotent by `invocation_id`. When a node reconnects, it re-announces its live
Tickets and sessions, and the hub reconciles them, applying Agent Relay's rule
that the first `completed` wins.

### 4.4 What is stored where

| Data | Hub | Node |
| --- | --- | --- |
| Accounts, devices, contacts, rooms, grants | ✔ (source of truth) | cache |
| Agent directory (name, owner, entry points, policy, worker profiles) | ✔ | the owning node runs it |
| Tickets: state, brief, inputs log, results, links, cost | ✔ (source of truth; leases) | runtime copy plus `tickets/<id>/` folder |
| Conversations, messages, mailbox | ✔ | — |
| Agent memory (`MEMORY.md` and `memory/`) | ✔ versioned document | working copy, synced |
| Provider keys, channel secrets, OAuth grants | ✗ (never, unless explicitly synced) | ✔ |
| Native session transcripts, worktrees, repos | ✗ | ✔ |
| Observability | metadata and summaries | full encrypted payloads, fetched on demand through the link |
| Usage | aggregated rows | raw records |

Each node chooses a **report level**: `metadata`, `summaries` (the default) or
`full`. Nothing with secrets is reported, and the existing redaction runs before
any report.

### 4.5 Identity, ownership and permissions

```
Principal = Account (person) | Node (device token) | Agent (acting for its owner) | App (an API client)
```

- **Account:** a person. The hub signs accounts in with an email code or OIDC.
  Feishu SSO fits well here because the Feishu device-flow plumbing already
  exists. Today's bridge-token admin becomes the hub's first account.
- **Node:** owned by the account that approved it. Only the owner can manage it.
  A hub admin can only unpair it (as in OpenAgents).
- **Agent:** owned by an account and private by default. Sharing would use
  grant levels (the earlier tenancy grants were removed in contract 3.0):
  `read` (see the agent and its Tickets) · `use` (message it and open Tickets) ·
  `manage` (edit it).
- **App:** an API client that acts for an account. Tenants, which used to fill
  this role, were removed in contract 3.0; integrations use the bridge token.
- **Node policy** decides what the hub may ask a node to do. Each item is a
  separate opt-in: `accept_owner_work` (on by default), `accept_shared_agent_work`
  (off by default; teammates' agents cannot run on your machine unless you opt in),
  and `expose_terminal`, `expose_fs_browse` and `max_concurrency`.

---

## 5. Super Agents as the only agent

### 5.1 Model

```
Agent  (what the user creates; today's SuperAgent, renamed)
├── identity: id, handle @name, owner account, avatar, visibility and grants
├── charter: routing rules, memory, default policy (approval mode, budget, allowed senders)
├── router: a worker profile with role=router (optional, see solo mode)
├── workers: [ WorkerProfile × placement constraints ]   ← today's AgentInstance
├── entry points: channels, schedules, webhooks, email, GitHub, Feishu docs, direct API, messages from agents and people
└── tickets: units of work, each placed on a node

WorkerProfile  (today's AgentInstance, internal; no longer called an "agent" in the UI)
  runtime, provider, model, effort, approval mode, MCP, skills, env, workspace mode, base dir
  placement: node = <id> | "home" | any where {runtime ∈ caps, labels ⊇ required}
```

- **Entry points replace three overlapping concepts.** Channels bind to *Agents*,
  and Super Agent Sources, cron and webhook Triggers become entry point kinds.
  The Feishu claim mechanism goes away, because a channel no longer has another
  agent to claim messages from.
- **Tickets replace Orchestrations.** An orchestration DAG is a Ticket graph
  (`depends_on` and `base_ticket` already exist), created by an API call instead
  of a router decision. `/api/v1/orchestrations` stays as a compatibility shim.

### 5.2 Solo mode and routing fast paths

Sending every message through a router turn costs latency and tokens. That is
the main reason a plain Agent is still useful today. Three changes keep that
benefit inside the single concept:

1. **`routing: solo`.** The Agent has one worker profile and no router. Each
   conversation maps straight to a worker session, which is exactly today's
   regular Agent. The router runs only when it is needed, for example for a
   Ticket update or an escalation. **Every existing AgentInstance migrates to a
   solo Agent with no change in behavior.**
2. **`routing: auto`** (the default for new Agents). Deterministic paths come
   first: slash commands, an explicit `@worker` mention, and a follow-up on a
   thread that has a live Ticket (the existing `follow_up` disposition) skip the
   router. Only a new or ambiguous request takes a router turn.
3. **`routing: router`.** Every event goes to the router, as Super Agents work today.

An Agent can switch modes at any time, and the Console shows "upgrade to a
router" for solo Agents. Direct APIs keep working: `/v1/responses` with
`model=<agent id>` goes straight through for a solo Agent. For an Agent with a
router, it opens a Ticket and streams that Ticket's events.
`X-AgentMux-Worker` pins a worker profile.

### 5.3 Sessions on any device: placement, Tickets and context

- **Placement**, adapted from Agent Relay:
  `place(ticket) = pinned node | node of base_ticket or session_ref (sticky resume) | least-loaded node where runtime ∈ caps ∧ labels ⊇ required ∧ policy allows`.
  If no node is eligible, the Ticket is queued with a deadline, and the router
  is told why. A pinned node that is offline is queued, never silently moved.
- **A Ticket lease** lives on the hub. The owning node holds a lease with a
  heartbeat. If that node disappears, the hub marks the Ticket `orphaned`. A
  resumable Ticket waits for the same node (session continuity, as in Agent
  Relay §8.2). Otherwise the router is woken to decide whether to retry
  elsewhere. This removes the "one daemon owns a Super Agent's Tickets" limit.
- **The router runs on the Agent's home node**, which the owner chooses. Router
  and workers do not need to share a machine.
- **Context bundle.** Each worker turn receives, assembled by the hub:
  - the Ticket brief and its inputs log;
  - the results of its `depends_on` Tickets;
  - the thread transcript, bounded as in OpenAgents (30 messages or 16k characters);
  - the Agent's memory index;
  - artifacts, as hub blob URLs downloaded by the node.

  Code moves through git. A worktree on node B starts from the branch that
  `base_ticket` pushed. When there is no shared remote, the hub relays a
  `git bundle`, with a size cap.
- **Steering a remote worker:** `ticket.steer` is an invocation sent over the
  link. The node applies it with the existing steer path
  (`core.InvocationSteerer`). Today remote steer waits for the next turn; this
  closes that gap.

### 5.4 People inside a Ticket

A Ticket has participants: people and agents. Approvals from Guard
(`ask`) and from cooperative `request_approval` calls appear as **approval
cards** in the Ticket thread and in the owner's phone inbox. A policy decides who
may sign off, for example deploy needs an admin and spending needs the owner. A
Ticket waiting on a decision moves to *Needs approval* on the board.

---

## 6. Messaging: agents, people, and across accounts

### 6.1 Addressing

| Address | Meaning |
| --- | --- |
| `@reviewer` | Your own Agent |
| `@bob/api` | Bob's Agent; requires that you are contacts or that Bob shared it with you |
| `@bob` | Bob, the person (his web, phone and IM) |
| `#launch` | A room: people, plus the agents each member opted in |
| `ticket:T-123` | A Ticket's thread |

### 6.2 Conversations and the mailbox

The tables are `conversations` (direct, room, ticket), `messages` (idempotent per
sender `client_msg_id`, with attachments in the blob store) and `deliveries`,
one row per recipient endpoint:

```
queued ──push(seq)──▶ pushed ──ack──▶ delivered
  ▲          │ ack timeout / nack (backoff 30s,1m,2m,5m,10m…)
  └──────────┘
queued ── TTL / unresumable session ──▶ dead_letter (sender is told)
any    ── sender recalls / ban ──▶ canceled (cancel frame if pushed)
```

Delivery is per recipient and in order: it stops at the first message that is
still backing off, as in imbo. Nodes ack cumulatively. The history is read from
the hub, and the delivery state comes from the node; the ack connects them, so an
inbox read never shows a delivered message again.

### 6.3 Getting a message into an agent

The node is the location of the agents it hosts. Applying Agent Relay's rule,
an agent has exactly one location, so a message is never delivered twice.

| Session state | What the node does |
| --- | --- |
| Busy, and the runtime supports steering | Inject the message into the running turn (existing steer). |
| Busy, no steering | Queue it for the end of the turn, then start a follow-up turn. |
| Idle or closed | **Wake:** start a turn on the same conversation, which resumes the native session. |
| Native sessions AgentMux did not start (a terminal running Claude Code or Codex) | Optional, later: an observer plugin or hook delivery like imbo's and Agent Relay's. AgentMux's `integrations/native` plugins are the place for it. |

Agents get a small MCP toolset served by the node (MCP is already managed per
agent): `amux_send`, `amux_inbox`, `amux_ask` (a request with a timeout that
returns the answer), `amux_delegate` (open a Ticket on another Agent),
`amux_contacts`, `amux_rooms`, and `amux_request_approval`. Router agents use
the same verbs in `<agentmux-action>` blocks (`post`, `link` and the others
already exist there).

### 6.4 Super Agent to Super Agent

There are three verbs, each a typed message whose state the hub tracks:

- **ask:** a question that expects an answer. The receiver's router or solo
  worker answers within a deadline.
- **delegate:** "please take this work". The receiving Agent's router accepts
  or declines it. Its **host policy** still applies: allowed senders, a budget,
  and whether delegated work needs the owner's approval. On accept, a linked
  Ticket is created on the receiver's side, and its status changes are mirrored
  to the sender's Ticket (`links` exist already).
- **notify:** for information; no reply is expected.

On the receiving side, an incoming agent message is just another entry point
kind (`agent_message`). The sender's identity is the actor `account/agent`, and
it goes through the existing `Ingest` gate with `AllowedActors`. **The Super
Agent safety model carries over unchanged.**

### 6.5 Across accounts: consent and safety

- **Contacts need mutual consent:** a request by account ID or email, and an
  acceptance. Sharing an agent with a teammate goes through grants. Rooms let
  members choose which of their agents receive messages.
- **Text from another account is untrusted data.** The charter already says so.
  Delegated work runs with the receiver's policy and on the receiver's machines,
  and the sender gains no capability on them.
- **Default cross-user policy, adopted from imbo:** the agent may answer and
  work in its sandbox. It must get its owner's approval before it sends files or
  data out, deletes anything, spends money or touches production. Where Guard
  can see the action, such as attachments on an outbound message or an
  `external_send` tool, Guard enforces this. Elsewhere the charter states it.
- **Abuse controls:** rate limits per sender, block, report, an admin ban that
  closes links and cancels deliveries, and a budget cap per delegation.
- **Every cross-account action is recorded in the audit log** (§10).

### 6.6 Federation between hubs (later)

Two organizations each run a hub. Hubs exchange signed server-to-server
envelopes. Addresses gain a host part (`@bob/api@hub.acme.com`). The design keeps
this possible: account IDs are namespaced by hub ID from the start, and every
message carries its origin hub. It is not part of 3.0.

---

## 7. Clients: web, phone and observers

- **The observer plane:** the hub appends every visible event to `hub_events`
  (`seq`, `type`, `scope`, `payload`, `created_at`, with bounded retention).
  Clients call `GET /v1/events?since=<seq>` and open `WS /v1/events/live`. Live
  frames carry the same `seq`, so the client keeps a single cursor, as in Agent
  Relay's observer plane. Observers never get an agent identity and never get
  deliveries.
- **Web:** the Console is served by the hub as well as by each node. In hub
  mode it shows all nodes, agents, Tickets and conversations the account may
  see. Node-local pages, such as provider setup on a given machine, are proxied
  over the link and limited by node policy.
- **Phone, in two steps:** (1) Make the Console an installable **PWA** with Web
  Push. This is cheap, because the React and Vite Console already exists. (2) If
  needed, build a native app with Expo or React Native (imbo's route) for APNs
  and FCM, lock-screen replies and approve and deny actions.
- **Push events:** a message to you, an approval request, a Ticket finished or
  failed, a node offline, a budget threshold.

---

## 8. Codebase refactor

```
cmd/amux            node CLI (+ hub subcommands)
hub/                NEW: link server, directory, mailbox, tickets, placement, events, push, accounts
  hub/link          WSS + yamux, frames, outbox/ack, reverse-tunnel dialer
  hub/mailbox       conversations, messages, deliveries state machine
  hub/placement     scheduler, leases, reconcile
nodelink/           NEW: node side of the link; implements superagent.HostDialer + fleet dialer
agentsvc/           Agent (= Super Agent) application service; absorbs superagent/ + orchestration/
runtime/            worker profile execution (today's core invocation/session parts)
channels/           channel runtime moved out of core/ (dispatch, queue, modes, prompts)
core/               domain types + interfaces only
server/             HTTP transport only; fleet_sync, feishu_setup_automation, openai_* logic → services
remote/             reduced to SSH bootstrap (install + enroll)
```

Rules:

- `server/` handlers only decode, authorize and encode, so the hub and the node
  mount the same services.
- Each service declares which **plane** it lives on: hub, node or both.
- Time columns: the control tables store `TEXT` timestamps (22 `created_at TEXT`
  columns in [store/postgres_schema.sql](../../store/postgres_schema.sql)). Use
  `timestamptz` for new hub tables, and migrate the old ones when they are next
  touched.
- `super_agents.data` is a JSON blob. That is fine for configuration, but the
  hub's Ticket and mailbox tables must have real columns and indexes, because
  they are queried and leased.

---

## 9. Migration and phases

Each phase ships on its own and keeps the previous behavior working.

| Phase | Ships | Exit criterion |
| --- | --- | --- |
| **P0: groundwork** (about 1–2 weeks) | Accounts and principals (the bridge-token admin becomes account #1); the `hub_events` log locally; move logic out of `server/` into services; Contract 3.0 draft | Unchanged behavior; the event log is filled locally |
| **P1: hub and node link** | `amux hub serve`, `amux hub join` (device code, token, `--via-ssh`), WSS and yamux link, the node RPC route policy, presence and metrics; `nodelink` implements `HostDialer` and the fleet dialer; Machine status shows hub nodes; SSH hosts stay as a fallback | A Super Agent on a Mac runs a worker on a laptop behind NAT **without SSH**; fleet query and execute work over the hub |
| **P2: Super Agents by default** | `routing: solo/auto/router`; a data migration from AgentInstance to a solo Agent plus a worker profile, rebinding channels and schedules; entry points unify Channels, Triggers and Sources; the claim mechanism removed; Console "Agents" = Super Agents, "Workers" tab = profiles; API shims (`agent-instances`, `orchestrations`) | No regular Agent can be created; all existing channels answer as before; the API golden tests pass through the shims |
| **P3: Tickets on the hub** | The hub owns Tickets and leases, placement, cross-node context bundles, `ticket.steer` over the link, the Ticket board, the PWA with the event cursor, and Web Push | Start, steer and stop a Ticket on another node from a phone; a hub restart and a node reconnect lose nothing |
| **P4: messaging fabric** | Conversations, messages, the mailbox, the MCP tools, agent-to-agent ask, delegate and notify, people in conversations, approval cards on the phone | Two agents on two nodes delegate and report back; a person answers an approval from the lock screen |
| **P5: across accounts** | Contacts, rooms, sharing, the cross-user policy, the audit log, abuse controls; optionally a native mobile app; a federation design | Alice's agent delegates to Bob's agent with Bob's policy applied, and every step is audited |

**Data migration (P2):** for each `AgentInstance` that is not a router or auto
worker, create `Agent{routing: solo, workers: [profile=instance]}`. Move its
`channel_bindings` and `schedules` to entry points, and keep the old ID as an
alias so `model=<old id>` and existing conversations still resolve. Router
(`sar_*`) and auto worker (`saw_*`) instances become profiles with
`role=router` or `role=worker`. Run it as `--dry-run` first, then `--apply`,
like `amux database import-config`. Record each legacy surface in
[LEGACY.md](../../LEGACY.md) with its removal version.

---

## 10. Other features worth adding

Ordered by value relative to effort, given what already exists.

1. **Approvals inbox on the phone.** Guard already decides allow, deny or ask.
   Route `ask` to push notifications with Approve and Deny buttons and a timeout
   policy. This is the most useful thing to have on a phone.
2. **Ticket board and audit log.** A Kanban view (queued, running, needs
   approval, ready, failed) with human assignees, plus an append-only audit of
   who did what on which node: approvals, delegations, grants, node pairing.
   Export to CSV.
3. **Budgets and cost attribution.** Ledger already prices usage. Attribute it
   to the Ticket, Agent, account and delegation, and enforce hard caps that
   pause a Ticket and ask the owner.
4. **Session handoff ("teleport").** Continue a session on another device:
   resume on the origin node, or push the worktree branch and resume on the new
   node with a context bundle.
5. **Live view from the phone.** Stream Ticket progress, the tmux terminal
   snapshot (it exists already) and diffs over the link.
6. **Preview tunnels.** Expose a node's dev server through the hub
   (`https://hub/preview/<ticket>`, with authentication) so you can check an
   agent's work on a phone.
7. **Playbooks: enforceable workflows** (the idea behind Agent Relay Flows).
   Required steps and gates, such as tests must pass or a person must review,
   attached to an Agent's Tickets. They are built on the Ticket graph and checked
   by the host, not just prompted.
8. **Shared memory and a decision log**, kept on the hub and versioned, with
   scopes (agent, account, team). The router writes decisions; people can edit
   them.
9. **Routing evaluation.** Replay recorded triggers against new routing rules or
   a new router model in observe mode, and diff the decisions before switching
   to Act. Observe mode and the trigger log already exist.
10. **Fleet operations.** Version skew policy, staged node auto-updates from the
    hub, health alerts, and draining a node.
11. **Voice to an Agent from the phone.** TTS and meetings support already
    exists. Add push-to-talk that produces a message or a Ticket.
12. **Templates and sharing.** Publish an Agent (charter, workers, entry point
    kinds, without secrets) as a template that a teammate can instantiate with
    their own credentials.

---

## 11. Decisions needed

| # | Decision | Options | Recommendation |
| --- | --- | --- | --- |
| D1 | Where the team hub lives | Self-hosted VPS / embedded only / also offer a hosted hub | Self-hosted plus embedded first; a hosted hub later if wanted |
| D2 | Link transport | **WSS plus yamux** (no external dependency, works through HTTP proxies) / embed Tailscale `tsnet` (a real network address per node, but needs a Tailscale or Headscale account) | WSS plus yamux; tsnet as an optional mode later |
| D3 | Mobile | PWA first / native first | PWA in P3, native in P5 if needed |
| D4 | How long to keep the `AgentInstance` API | One minor release / until Contract 4.0 | Shims until Contract 4.0, following the [LEGACY.md](../../LEGACY.md) policy |
| D5 | Account sign-in on the hub | Email code / OIDC (Feishu, GitHub, Google) | Email code plus Feishu OIDC |
| D6 | Hub storage and standby replication | A separate hub instance / PostgreSQL replication / copy through the hub service | **Decided (2026-11-13):** one instance per machine, with hub data as a fixed list of tables in it; only the hub service writes them; the standby copies them through the hub service over the link (§4.2) |

## 12. Risks

- **A larger attack surface.** The hub is a new internet-facing service. Mitigations:
  a minimal route set; node RPC limited by node policy; tokens stored as hashes;
  certificate pinning; rate limits; no secrets on the hub.
- **Cost and latency of router turns.** Mitigated by solo and auto routing (§5.2).
- **Cross-node git state.** This is the hardest part of "work on any device".
  Start with sticky placement and shared remotes, and add `git bundle` relay after.
- **Migration risk to existing channels.** Mitigated by the dry-run migration,
  ID aliases and the golden contract tests.
