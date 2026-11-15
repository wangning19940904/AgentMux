# Framework health

Agent frameworks on a machine break silently: a sign-in expires, a CLI goes
missing or falls behind, a gateway rejects the model, or a subscription hits
its usage limit. Before health records, nobody noticed until a Ticket failed
on its first message. `framework/health` keeps one record per framework on
each machine so the rest of AgentMux can find out before it sends work.

The package owns the stored state. `server/framework_health.go` only serves
it. Worker placement and the brain's roster will read these records in a
later change; nothing blocks on them yet.

## Records

A record holds:

| Field | Meaning |
| --- | --- |
| `state` | `ok`, `failing`, `usage_limit` or `unknown`. `unknown` means never checked, or a usage limit whose `resets_at` has passed with no check since. |
| `reason` | A short label and the framework's own message, with secrets redacted and the text shortened. |
| `error_kind` | The smoke-test classification. It adds `usage_limit`, and `cli_outdated` (from runs only). |
| `source` | `test` (the Console's Test), `periodic`, `recheck` (including the check that confirms a failed run), or `run`. |
| `checked_at` | When the evidence was produced. |
| `probed_at` | When a check last ran. The periodic interval and the 30-second recheck throttle count from this time. A failed run does not change it. |
| `resets_at` | When a usage limit ends, if the framework said so. |
| `consecutive_failures`, `last_ok_at` | Failure history for later placement decisions. |

Go callers use `Status(ctx, kind)`, `Records(ctx)`, `RecordFailure(ctx, kind, text)`
and `Recheck(ctx, kind)`. Everything is stored in the setting
`framework_health_v2` as `{records, results}`. `results` keeps the latest test
result per framework and model, as the old `framework_health_v1` did, and
that setting is imported the first time records load.

## Checks

A check is `framework.SmokeTest` with the framework's default model: the
same "reply ok" run as the Test button. It runs with the `health_check`
agent option. Claude Code and Codex honour it and leave no session or
transcript. Frameworks that cannot honour it leave their usual session
behind.

- **Periodic checks are off by default.** The setting
  `framework_health_checks` (`{enabled, interval_minutes}`; the interval
  defaults to 30 minutes and is clamped to 5–1440) turns them on. It is set
  per machine with `PUT /api/v1/frameworks/health/settings` or the checkbox
  on the Console's frameworks page.
- **When they run.** The loop looks for due checks every minute. The first
  look is 30 seconds after the daemon starts, so a restart checks every
  framework whose last check is older than the interval. Due frameworks are
  checked one at a time, 5 seconds apart. They include the installed,
  registered and catalogued frameworks, plus any framework that already has a
  record, so a CLI that disappears gets noticed.
- **Timeouts.** A check that times out is retried once right away. Only a
  second timeout in a row is recorded as `failing`. Other failures are
  recorded the first time.
- **With checks off, nothing runs on its own.** There are no rounds and no
  startup check, and failed runs are neither recorded nor confirmed. Records
  change only through a Test or a recheck.

## Learning from real runs

`Service.EngineSink` is subscribed to the engine
(`core.Engine.SubscribeEventSink`). It receives every `HookError` with the
run's `runtime_id`. Session start failures of invocations and of project
runtimes include `runtime_id` too, so a missing CLI is caught. A dynamic
channel's start failure does not yet name its runtime, so it is not counted.

`ClassifyRunError` is stricter than the smoke-test classifier. A smoke test
asks only for "ok", so any failure is the framework's. A real run fails for
many reasons, and only these count:

- a missing CLI
- not signed in or bad credentials
- a model the framework or its gateway rejects
- a CLI too old for the options AgentMux passes
- the framework's own usage-limit message

These do not count: cancellations, timeouts, network errors, a generic 429 or
"quota", tool output, and any other failure of the work itself.

When periodic checks are on, a counted failure is recorded with source `run`
and confirmed by one recheck. If that check passes, the record goes back to
`ok`, so one blip does not leave a framework marked `failing`. A usage limit
from a run is recorded but not checked, because the check would also be
refused.

## Rechecks

`Recheck` (`POST /api/v1/frameworks/health/recheck`):

- **Single flight per framework.** Concurrent callers wait for the same
  check, which runs on the service's own context, so a caller that leaves
  does not cancel it for the others.
- **Throttled.** A recheck within 30 seconds of the last check returns that
  record without running another.
- **Held for usage limits.** A `usage_limit` record with a known `resets_at`
  is returned unchecked until the reset. Without a known reset, it is checked
  like any other record. Periodic rounds skip a held record and check it in
  the first round after the reset, whatever the interval.

The reset time is read from the framework's own words:

- a Unix timestamp after `usage limit reached|`
- `resets_in_seconds`
- an RFC 3339 time after "resets"
- a clock time with its time zone, such as `resets 4:20pm (Asia/Shanghai)`
- "try again in 2 hours"

A time already past, or more than eight days ahead, is not trusted.

## Tests

The Console's Test (`POST /api/v1/frameworks/test`) still stores its result
per framework and model. A passing test marks the framework `ok`. A failing
test updates the record only when it tested what a check tests: the
framework's default model in this machine's own environment. A failing test
of a typed model or of an Agent's environment does not mark the framework
`failing`.

## API

`GET /api/v1/frameworks/health[?kind=]` returns `{results, records, checks}`.
`results` keeps its earlier shape. Like `GET /api/v1/frameworks`, it is an
administrator route that other machines read through a fleet query
(`POST /api/v1/fleet/query`). The recheck and settings routes are reached
through `POST /api/v1/fleet/execute`. All four routes are beta in
`contract/openapi.yaml`.
