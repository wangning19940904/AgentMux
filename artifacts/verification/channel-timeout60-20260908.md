# Channel timeout update — 2026-09-08

The channel default is now 60 minutes in both the shared App form and the
server. Explicit channel overrides and the legacy timeout key remain supported.

Timeout replies identify the configured total execution limit and the interrupted
Agent turn. They explain that interruption does not roll back completed actions
and that submitted builds/deployments must be checked before retrying. Network
and model-service troubleshooting appears only when an observed model request
failure has not recovered. Existing tool progress remains visible.

## Build and validation

- Isolated baseline: `7c187b8fc3c1a88be123ce5fd5481edb972f8ca9`, matching the
  previously deployed `0.1.9-main.7c187b8` runtime. Its tree matches the current
  main merge commit. Unrelated local changes were excluded from the build.
- Version: `0.1.9-timeout60.1`.
- `go test -race ./core ./server` passed.
- Focused timeout tests passed with configured limits of 35/60/90 minutes,
  default and legacy configuration, model retry recovery, retained tool progress,
  and final delivery after context expiry.
- `npm run build` passed.
- Linux amd64/arm64, macOS amd64/arm64 CLI and native macOS App builds passed.
- Native App and bundled executables installed; bundle signature verified.
- Native “New channel” form visibly shows a timeout of `60`; the verification
  form was closed without creating a channel. Local daemon and channel healthy.
- Linux amd64 SHA-256:
  `6aa1872649e0cb51b8f624b4f3ba298dd466f3eb9c4f9715074f32f9e5216653`.
- Original App backup:
  `/Users/bytedance/Library/Application Support/AgentMux/backups/AgentMux.before-timeout60-20260908.app`.

## Remote rollout

Completed on `ecs_cn` at 16:39 China time. All three existing channels now have
both `turn_timeout_minutes` and `codex_turn_timeout_minutes` set to `60`:

| Bound agent | Channel | Timeout | Connection |
| --- | --- | --- | --- |
| Codex | `channel-4c84c8d7af20` | 60 minutes | running, connected |
| TRAE CLI | `channel-829d00b0d089` | 60 minutes | running, connected |
| Cursor | `channel-b65e4f691be8` | 60 minutes | running, connected |

The App default, server default, live remote configuration, version and binary
checksum were verified. The service is active and reports `0.1.9-timeout60.1`.

The rollout initially waited for active work. The user subsequently explicitly
authorized immediate deployment with interruption of running tasks. The remaining
TRAE task `task-531b3dab2fafcec9575a02ef` is recorded as `interrupted`, with reason
`AgentMux restarted while task was active`. The earlier Cursor task had already
ended before deployment.

Remote backups:

- Binary: `/home/tiger/.agentmux/bin/amux.before-timeout60-20260908T083916Z`
- Previous timeout fields:
  `/home/tiger/.agentmux/backups/channel-timeouts-20260908T083916Z.json`

The configuration update only merged the two timeout keys and refreshed
`updated_at`; agent bindings and other channel settings were preserved.
