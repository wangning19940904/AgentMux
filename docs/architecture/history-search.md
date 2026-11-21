# History search: keywords over memory and past turns

Status: proposed · 2026-11-20 · branch `claude/history-search-7ca798`

The first version is built on this branch. §12 lists what it leaves out.

An agent cannot look up what it was told last week. Memory search is a
`LIKE` over `memory_entries` ([store/modules.go](../../store/modules.go)).
The turns themselves live only in observation traces, encrypted and with no
search. This proposal adds keyword search over an agent's memory entries
and its past turns, in Chinese and in English, for agents and the Console.

The ideas come from Linger's knowledge search (keywords only, several
wordings fused by reciprocal rank, two result lists, a time that only
boosts). None of its code is reused.

## 1. What stays out

- Embeddings and vector search. Agents reword a question better than a small
  embedding model matches one, and keywords need no model, no provider and no
  extension.
- Writing summaries. A memory loop
  ([agent-memory-loop.md](agent-memory-loop.md), proposed in another branch)
  may write them. This search indexes them when they exist (§5) and works
  without them.
- Super Agent routers and workers calling search. They reach AgentMux by
  other routes (§8, question 3).
- Other machines' history. A search runs on one machine; §12 says how a hub
  could fan out.

## 2. What is indexed

One table, `history_search_docs`, holds one row per searchable text:

| Kind | Source | Owner | List |
| --- | --- | --- | --- |
| `memory` | a `memory_entries` row | its memory scope (`global`, `agent:<id>`, …) | memory |
| `summary` | reserved for the memory loop | an agent | memory |
| `turn_input` | what a person sent: the `agent.turn` start envelope | the agent | originals |
| `turn_answer` | the agent's final answer: the `agent.turn` end envelope | the agent | originals |

Turns come from the observation bus. `historysearch.Service.Observe`
subscribes beside the recorder and takes only `agent.turn` envelopes with
source `agentmux.internal` and an agent id. A person's own Claude Code
sessions, which AgentMux only watches, belong to no agent and are not
indexed. A failed turn's end carries its error, not an answer, so it is
skipped.

The input text is what the person wrote. The engine sends the agent the
message wrapped twice: the memory block (`withMemoryContext`) and, on
Feishu and Lark, the channel contract with a JSON envelope. `core.StripMemoryContext`
and `core.ChannelPromptUserText` undo both, next to the code that adds them,
so the format lives in one place. Attachment references stay.

Each row keeps the channel, chat, conversation key, person and trace id,
so a match can be traced back to its conversation.

Memory entries reach the index two ways. `Service.WrapMemory` wraps the
memory store, so a `Put` or `Delete` through the API or the engine updates
the index at once. An hourly reconcile (`StaleHistoryMemory`) catches writes
that bypass the wrapper, such as fleet sync or a restore: entries whose row is
missing or older, and rows whose entry is gone.

Turns recorded before this version are backfilled at start and hourly
(`BackfillTurns`). The backfill reads recorded `agent.turn` events within
the content retention that have no row yet, decrypts their payloads with the
recorder, and indexes them 200 at a time. A payload that cannot be read
still gets an empty row, so it is not decrypted again at every start.

## 3. The index

`terms TEXT[]` with a plain GIN index. The Go tokenizer
(`historysearch.Terms`) fills it, and the same tokenizer reads every query:

- A run of letters or digits in an alphabetic script is one lower-cased
  word: `Migration` → `migration`, `k8s` → `k8s`. Common English stop words
  are dropped.
- A run of Han, kana or Hangul characters is split into overlapping pairs:
  `数据库迁移` → `数据 据库 库迁 迁移`. A single character stays as it is.
- A script change ends a run, so mixed text splits cleanly: `部署k8s集群` →
  `部署 k8s 集群`.

The store adds two scope keys to the array: `@list:memory` or
`@list:originals`, and `@agent:<id>`. Words never start with `@`. The GIN
index can then intersect "this agent's originals" with "holds one of these
words" before reading any row.

Why not `pg_trgm`, the first idea:

- **Locale.** pg_trgm counts only characters the database's `LC_CTYPE`
  calls alphanumeric. On a `C` or `POSIX` database, Chinese yields no
  trigrams at all. The Linux setup (`internal/postgressetup/linux.sh`)
  creates the cluster with the system locale, which is `C` or `POSIX` on a
  minimal server or container. This Mac's Homebrew cluster is
  `en_US.UTF-8`, where it works. The search must not depend on how a
  machine's cluster was initialized.
- **Two-character words.** Most Chinese words have two characters. A
  trigram index cannot serve `LIKE '%迁移%'`: the pattern holds no complete
  trigram, so the planner reads every row. Pairs index those words directly.
- **The extension.** pg_trgm ships with Homebrew's PostgreSQL (checked:
  18.4 here) and Debian's `postgresql-16`, and is a trusted extension, so the
  database owner can create it. On Fedora and RHEL it is in
  `postgresql-contrib`, which the setup does not install. And an extension
  is shared by the whole database: it lives in one schema, and the
  integration tests' per-test `search_path` could not see it. A failed
  `CREATE EXTENSION` would also fail the migration and stop the daemon.
  Arrays need nothing.

The cost: English words match whole. `migrat` does not find `migration`, and
`DB` does not find `database`. The agent's job is to give such variants as
extra wordings (§4). A trigram signal could be added later as one more
ranked list where the extension exists.

The table is plaintext. The observation payloads it is derived from are
encrypted at rest, so this is a deliberate trade, bounded four ways. It holds
only the person's message and the final answer, not tool calls, reasoning or
tool results. Everything is redacted before indexing (§7). Rows expire with
the payloads (§9). The table exists only where content capture is `full`
with a secure key, which is where the content is kept anyway. The same
messages are already in plain text in the channel logs
(`~/.agentmux/logs/channels/*.jsonl`) and in the agent CLIs' own
transcripts.

Migration **38** creates the table. 37 is claimed by other open proposals,
so this one skips it. The runner applies versions in list order and checks
each one on its own, so a 37 merged later still applies.

## 4. Several wordings, one ranking

A request carries up to eight wordings of one question. Each wording is
tokenized and matched on its own:

- A document qualifies if it holds the whole wording as a phrase, or enough
  of its terms: all of one or two, three in five of more. That covers a
  natural question and Chinese pairs that cross word boundaries (`库迁`).
- Of the newest 1,000 qualifying documents, phrase matches rank first, then
  documents holding more terms, then newer ones. The cap keeps a very common
  word from reading the whole table. Such a word is answered from recent
  history, which is where its best match usually is.

The lists are merged with reciprocal rank fusion: rank *r* in a list scores
1/(60 + *r*). A document that several wordings find beats one that a single
wording put first. Each match reports which wordings found it.

The wordings of both result lists run side by side, at most four queries at
a time, so a search costs about one query.

## 5. Two result lists

| List | Holds | For |
| --- | --- | --- |
| `memory` | memory entries, and summaries once a memory loop writes them | an overview: who owns what, what was decided |
| `originals` | turn inputs and answers | exact details: the wording, the number, the date |

Each list has its own fusion and limit (8 by default, at most 30). A match
carries a snippet: the text around its first occurrence of a wording, or
else of a term. That is about 90 characters on each side, never splitting a
character, with `…` where it was cut. It also carries the time, kind, agent,
memory scope, person, chat and conversation key.

The memory loop adds its documents through `Service.Put` with kind
`summary`. They are redacted and tokenized like the rest. Nothing here
depends on that loop.

## 6. Time boost

A question that names a time ("what did we decide about the migration last
Tuesday?") passes `around: {from, to}`. The CLI also accepts a day, a range
of days or an instant. The window becomes one more ranked list, holding the
candidates inside it, the ones nearest its middle first. It is fused with
half the weight of a wording. A match inside the window rises, a match
outside keeps its score, and nothing is removed. A window nothing falls into
changes nothing. Matches inside the window say `in_time_range`.

## 7. Scope

The scope never comes from the request body. It comes from who is calling:

| Caller | Sees |
| --- | --- |
| An agent's running turn in a one-to-one chat with person P | its own group and system turns, its own private chats with P, and the memory scopes `global` and its own (`agent:<id>` or the configured `memory_scope`) |
| An agent's running turn in a group chat | its own group and system turns and those memory scopes. **No one's** private chats, since everyone in the group reads the answer. |
| The Console administrator, one agent | all of that agent's turns and its memory scopes |
| The Console administrator, all agents | everything on this machine |
| A member account | nothing: the route is not in `memberRoutePolicy` |

An agent never sees another agent's turns, private or not. A turn is
private when its chat type is not a known group type (`group`,
`topic_group`, …) and it has a person. An unknown chat type therefore counts
as private, and a new platform's naming never widens who sees a chat.

"The people it works for" is the person of the running turn. A one-to-one
chat may recall that person's earlier one-to-one chats with the same agent,
and nothing more. Memory scopes shared on purpose (`global`, or one
`memory_scope` set on several agents) stay shared, as they already are
in every prompt.

The store enforces the scope twice: the `@agent:` key in the GIN condition,
and the row condition on `agent_id` and `private_to`. An agent scope
without an agent id returns nothing.

## 8. How agents call it

Agents already reach AgentMux one way during a turn: the `delivery_cli`
named in every Feishu and Lark message, which is the `amux` binary, run as
`amux send --channel-id <id> --conversation-key <key>`. The daemon takes
that call only on loopback, and only for a conversation with a running turn
(`Engine.SendToChannel`). Search uses the same route:

```bash
amux history search --channel-id <channel_id> --conversation-key <conversation_key> \
  --query "数据库迁移" --query "database migration" --query "Postgres upgrade" \
  --around 2026-11-12
```

It calls `POST /api/v1/history/search` with `channel_id` and
`conversation_key`. The handler requires loopback and asks
`Engine.ActiveTurnIdentity` for the agent, memory scope, chat type and
person of the running turn. A conversation without a running turn is
refused with 409, so a key copied from an old message is no use. The output
is plain text, with the two lists, snippets and notes; `--json` gives the
raw answer.

The channel contract every Feishu or Lark turn receives
(`channelInteractionContract`) gains one line. It says when to search, to
give synonyms and both languages as separate `--query` wordings, that
`--around` only boosts, and that the agent must search the originals before
saying something was never recorded.

The Console calls the same endpoint without a conversation and with an
optional `agent_id`; the handler then requires an administrator. The
search box sits on the Memory page: an agent picker, one wording per line,
an optional date range, and the two lists with their snippets.

Questions for later:

1. **An MCP tool.** Frameworks that load MCP servers could get a `search_history`
   tool. That needs a per-turn credential in the MCP server's environment, which
   AgentMux does not have today.
2. **Other channel types.** Only Feishu and Lark messages carry the
   contract and the ids. Agents on other channels can call the command if
   their prompt tells them the ids.
3. **Super Agents.** A router or worker turn runs as an agent instance and
   is indexed under it. A ticket-scoped route, like the dispatch token, would
   let workers search the history of the agent they work for.

## 9. Retention

- A turn row expires when its payload does: occurred time plus
  `observability.content_retention_days` (30 by default). Searches ignore
  expired rows. The hourly maintenance deletes them in batches.
- Memory rows have no expiry. They follow their entries through the wrapper
  and the reconcile.
- When content capture is not `full`, or the recorder has no secure key, no
  turns are indexed. Every search then says so in `notes`, and the memory
  list still works. Rows indexed earlier still expire on their own clock.
- Every search notes how far back the originals reach, so an agent that
  finds nothing knows whether the turn could have expired.
- Turning capture off does not delete existing rows. Deleting the table's
  rows by hand is safe: the backfill indexes again whatever payloads are
  still within retention.

## 10. Redaction

Everything is redacted before it is indexed, with
`store.RedactObservationContent`, the recorder's redactor, and its secret
list: configured tokens, agent and MCP environment values, the ingest
token, and the per-turn known secrets of the envelope. Memory entries are
redacted the same way, although they are stored plain in `memory_entries`.
A redacted value is gone from the terms, so searching for it finds nothing.

Snippets are redacted again on the way out, with the secrets known at that
moment. A secret configured after a text was indexed never comes back
either.

## 11. Latency

Measured on this Mac (Apple Silicon, Homebrew PostgreSQL 18.4, default
settings) with `TestPostgresSearchLatency`. It loads 200,000 turn documents
from 20 agents and 60 people over 30 days, plus 2,000 memory entries. Each
document holds 8–68 words from a Zipf-distributed vocabulary of 4,000
Chinese and 2,000 English words, with the queried words planted at fixed
rates. The table and its indexes take 227 MB. Times are the median of seven
searches, each returning both lists:

| Search | Median |
| --- | --- |
| Agent, one Chinese word (in 3% of documents) | 24 ms |
| Agent, one word in 40% of documents | 39 ms |
| Agent, two English words | 21 ms |
| Agent, a rare word (0.1%) | 17 ms |
| Agent, a long Chinese phrase | 59 ms |
| Agent, three wordings and a time | 40 ms |
| Console, all agents, three wordings | 138 ms |
| Console, all agents, the 40% word | 255 ms |

The test runs only with `AGENTMUX_HISTORY_LATENCY=<documents>`.
`AGENTMUX_HISTORY_KEEP_SCHEMA=<schema>` keeps the loaded data for another run.

## 12. Later

- A pg_trgm list for partial English words, where the extension exists.
- Hub fan-out: the Console asks each machine and fuses their lists. The
  hub's fleet read already reaches every machine's memory.
- The memory loop's summaries and topic memory as `summary` rows.
- Opening a match: the trace id is stored, and the Console's trace view
  could link to it.
