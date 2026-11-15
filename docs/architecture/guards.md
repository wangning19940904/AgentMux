# Architecture guards

Status: in force · 2026-11-15 · package [`internal/archguard`](../../internal/archguard)

[hub-and-super-agents.md](hub-and-super-agents.md) describes where AgentMux is
going: `core/` holds domain types only, `server/` is a thin HTTP transport, and
the hub is a meeting point rather than a place for business logic. §1 and §8 of
that document admit that today's code is not there yet. The architecture
guards keep it from drifting further away while the hub and the layering are
being built.

Each guard is a Go test that compares the code with a hand-edited list in
`internal/archguard/testdata/`. Growth in the wrong direction fails the test,
and the only way to make it pass is to change a line in a list, which a
reviewer then sees in the diff. No list has an update flag or environment
variable that rewrites it: every change is a hand edit.

The tests run with everything else (`go test ./...`, so `make test`,
`make check` and CI). To run just them:

```
go test ./internal/archguard/
```

They need the `go` tool on `PATH` (the boundary guard runs `go list`) and no
database.

## Package boundaries

`boundaries_test.go`, list `boundary_allowed.txt`.

The rules read the import graph of production code (`go list`, test files
excluded: a test may import whatever it needs to check its package). Paths are
relative to the module (`core`, `cmd/amux`); a path outside the module is
written in full (`net/http`).

| Rule | What it says | Why |
| --- | --- | --- |
| `core-leaf` | `core/` imports no other package of this module. | `core/` is the bottom of the graph: domain types and interfaces every layer shares (§8). |
| `core-no-transport` | `core/` imports none of `net`, `net/http`, `os/exec`, `database/sql`, pgx, SQLite, gorilla/websocket or yamux. | Domain types do not open connections, start processes or reach a database. |
| `link-below-services` | `link`, `hubrelay` and `hubha` depend, even indirectly, on none of `server`, `bootstrap`, `cmd/…`, `superagent`, `orchestration`, `mailbox`, `push`, `framework`, `agent` and `platform`. | The link packages only carry bytes between machines and hubs. A service reaches them through an interface it owns (`superagent.HubRelay`, `superagent.HostDialer`), so the hub and the standby can use them without the node's application. |
| `only-roots-import-server` | Nothing but a composition root (`cmd/…`, `bootstrap`) or `server/` itself depends on `server`, even indirectly. | `server/` is the transport. Services must not need it, so the hub and the node can mount the same services (§8). |

`bootstrap` counts as a composition root: it assembles the daemon for both
`cmd/amux` and the desktop shell, and wiring `server` is its job. The desktop
shell (`desktop/`, build tag `desktop`) is one too, but `go list` skips it by
default, so the rules never see it.

`boundary_allowed.txt` lists today's violations, one per line as
`<rule> <package> <dependency>`, each with a comment saying what would remove
it. Today it holds two, both from `core/hooks.go`, which runs webhook hooks
over HTTP and command hooks as processes:

```
core-no-transport core net/http
core-no-transport core os/exec
```

The list only shrinks. A line that no longer matches a violation fails the
test with "which no longer happens: delete the line".

**Reading a failure.** "`X` depends on `Y`, which rule `R` forbids" names the
package, the dependency and the rule, followed by what to do instead. For a
transitive rule, `Y` may arrive through a package in between. To find the
import that brought it in, list which packages import `Y` directly:
`go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./... | grep Y`.

**When adding a line is legitimate.** Almost never. The intended fix is the
one in the message: an interface in the lower package, or the code moved to
the layer that may own it. Adding a line is acceptable only when a change
*moves* existing code and with it an existing violation into another package,
so the total does not grow. Say so in the PR, and delete the old line in the
same change.

## The hub's surface

`hub_surface_test.go`, lists `uplink_routes.txt`, `relay_allowed.txt` and
`hub_tables.txt`.

Three lists pin what crosses the hub. Each is compared with the code in both
directions: an entry in code without a line fails ("is not in testdata/…"),
and so does a line the code no longer has ("which the code no longer has:
delete the line").

| List | What it pins | How the test reads the code |
| --- | --- | --- |
| `uplink_routes.txt` | Every route the hub serves up the node link: anything under `/uplink/`. | Parses every non-test Go file in the module and evaluates the patterns of `Handle` and `HandleFunc` calls, `strings.HasPrefix(….URL.Path, …)` dispatches (written `prefix /uplink/…`) and `….URL.Path == …` comparisons (written `path /uplink/…`). Patterns may be built from string literals, `+` and string constants of any package, exported or not. |
| `relay_allowed.txt` | The worker API calls the hub's worker relay carries between one owner's machines: the cases of `superagent.RelayAllowed`. | Parses the switch in `RelayAllowed` from source, then calls the real function for every listed line to check that it lets it through. |
| `hub_tables.txt` | The tables that make up hub data: `store.HubTables`. A standby copies them, and a fenced primary refuses writes to them. | Reads the exported variable. |

The source parsing exists so that no production file has to change to be
checked. If the code changes shape (for instance `RelayAllowed` stops being a
switch, or an uplink pattern is built from a variable), the test fails and says
the guard needs teaching. Update `hub_surface_test.go` in the same change; do
not weaken it. One gap remains: a pattern that is nothing but a variable
(`mux.HandleFunc(p, h)`) is not seen. Review has to catch that.

**Reading a failure.** "new uplink route `…` (file:line)" points at the
registration. "lists … which the code no longer has" means a route, call or
table went away: delete the line.

**When adding a line is legitimate.** When the new entry really is the hub's
own communication. For uplink routes that is pairing, reports, mail, relays
between machines and the standby feed; business features belong on the nodes,
reached through the relay or the link's reverse tunnel. A relayed call must be
part of the worker API one owner's machines need from each other. A hub table
is one a standby must hold to take over, and that no node writes for itself
(the mailbox tables are not hub tables for that reason, see §4.2). Name the
reason in the line's comment.

## `server/` size ratchet

`server_size_test.go`, list `server_size.txt`.

Some files in `server/` hold business logic that §8 wants moved into services.
The ratchet records their non-test line counts as `<glob> <lines>`, with one
`total` line summing every file the globs match (each file counted once):

| Entry | Lines | What is in it |
| --- | --- | --- |
| `fleet_sync.go` | 1041 | Fleet sync over SSH tunnels |
| `fleet.go` | 395 | Fleet query and execute fan-out |
| `feishu_setup_automation.go` | 706 | Feishu Open Platform automation |
| `openai_*.go` | 2222 | The OpenAI Responses implementation |
| `keep_awake.go` | 463 | macOS power assertions and activity simulation |
| `total` | 4827 | |

A count must equal the code. Above it, the business logic grew: put the new
code in a service package and call it from the handler. Below it, the file
shrank: lower the ceiling (and the total by the same amount) so the gain
cannot be spent again. A glob that matches nothing fails too. A new file that
matches a glob, such as another `openai_*.go`, counts against that line's
ceiling and the total.

The ratchet is deliberately modest: a few named files, not every file in
`server/`. Handlers that only decode, authorize and encode are what `server/`
is for and are not limited.

**When raising a ceiling is legitimate.** When a change moves code *into* one
of these files from another `server/` file on its way out, or fixes a bug in
place where the fix cannot wait for the move. Say which in the PR. Otherwise
the ceilings only go down.

## Merging work from parallel branches

The lists are where parallel work meets. Two branches that each add an uplink
route, a relayed call, a hub table or a boundary change will both edit the
same list, and the second one to merge sees a conflict or a failing guard. That
is the guard working: resolve it by hand, keep both entries if both are
intended, and let the reviewer read the result. Expect this in particular
while the `superagent/`, `link/`, `remote/`, `framework/` and `core/` work in
flight lands.
