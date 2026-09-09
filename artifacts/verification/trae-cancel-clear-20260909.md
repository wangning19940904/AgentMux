# TRAE cancellation and Feishu command repair — 2026-09-09

## Confirmed incident

- ecs_cn was running `0.1.9-timeout60.1`.
- At 09:21:54 China time, the controller interrupted native turn
  `01a083bd-11b4-7161-ace2-5e99d2ec9598`.
- At 09:22:30, new turn `01a083c2-6627-7c22-a57c-44e5b23e883e`
  started, but AgentMux consumed the old turn's interrupted notification and
  failed task `task-10f0f5e5173736870bf39911` with `context canceled`.
- At 09:23:10, the channel received `@_user_1 /clear`. The mention was not
  removed before exact command matching, so the command reached TRAE as text.
- TRAE's native rollout completed at 09:24:13, but AgentMux retained task
  `task-e24cc68b8248e770f7b02e02` as running with delivery pending.
- A later 09:47 request, task `task-965fb04941fc299103a1eefb`, blocked on
  `model/list` before obtaining a native session or starting an agent turn.
  No new native rollout existed when the deployment was prepared.

Sources: ecs_cn channel JSONL, PostgreSQL channel tasks and observation events,
TRAE native rollout and app-server logs; cross-checked against source code.

## Changes

- Give each turn a fresh notification inbox; discard notifications for idle,
  closed, or mismatched turns. Decline stale server approval requests.
- Check turn identity before mapping events, including notifications received
  before the turn/start response.
- Keep notification routing independent of the shared RPC reader. The inbox
  has an explicit 4,096-event limit; overflow interrupts that turn with an
  explicit error rather than blocking all sessions.
- Allow cancellation to release a producer blocked on channel output, and
  interrupt the backend before releasing its turn state.
- Normalize leading bot mentions for slash commands and 停止 using verified
  Feishu mention metadata. Preserve ordinary prose, other recipients and
  unverified mentions; support plain text and rich-text posts.

## Validation

- `go test -race ./agent/cliagents ./platform/feishu ./core ./server` passed in
  both the working tree and the isolated deployment source.
- The focused app-server regressions passed 10 consecutive runs with the race
  detector: cancelled turn followed by a new turn, stale errors/approvals,
  1,024 notifications before the RPC response, inbox overflow, and blocked
  output cancellation. Both Codex and TRAE adapters are exercised.
- Feishu tests cover `/clear`, `/reset`, `/new`, `/model`, 停止, Unicode
  whitespace, repeated bot mentions, rich text, and preservation of human or
  other-bot mentions and ordinary prose.
- The focused Linux test binaries also passed on ecs_cn without sending chat
  messages or invoking a real model.
- `git diff --check` passed.

## Build

- Version: `0.1.9-cancel-clear.1`.
- Baseline: main `dd81b6b` plus the already-deployed timeout60 patch. Only the
  eight repair source/test files were added. The deployed WebUI was reused.
- Unrelated working-tree changes are excluded from the deployment.
- Linux amd64 SHA-256:
  `ce49ec4acae058c1871991436cb91c29c0596e1eea72d269b18a43863503534b`.
- Build and patch: `/tmp/agentmux-cancel-clear-20260909/`.

## Deployment verified

- Deployed on ecs_cn at 09:50 China time, then verified at 09:50:46.
- `/api/v1/status` reports healthy, version `0.1.9-cancel-clear.1`.
- All three Feishu channels report `running` and connected. Channel agent
  bindings and full configurations match their pre-deployment database values;
  all three retain their 60-minute timeout.
- Both known blocked tasks are now interrupted; there are no active tasks.
  The 09:47 request never started a native turn and must be resubmitted by the
  user. No request was automatically replayed.
- Existing chat cards were not edited as part of verification. The historical
  conversation was not retroactively cleared; the repaired command path handles
  the user's next `/clear` normally.
- Rollback binary:
  `/home/tiger/.agentmux/bin/amux.before-cancel-clear-20260909T015025Z`.
- Deployment was initially deferred when the 09:47 request appeared. Its lack
  of a native session/turn and the app-server's blocked model-list request were
  verified before restarting. Subsequent checks allowed only these two known
  blocked tasks, so other work would defer deployment.
