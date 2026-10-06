# Agent channel binding and cache repair

## Data repair

On `aliyun-swas-sg`, channel `channel-a754134c4fe5` (Feishu bot 宁宝)
had an empty `agent_id`. Restored it through the normal channel API to
`agent-eb05f8ad4fc6` at 2026-09-22 17:22:43 +08:00. Read back the binding
and confirmed all three channels returned `running`, `connected=true`.

The channel's prior update time was 16:32:39. Logs confirm a channel reload
at that time, but do not identify which request cleared its binding.
Code inspection found a possible destructive race: opening an Agent editor
before channel data arrived initialized the selections as empty; a subsequent
save could unbind channels that arrived in the meantime. This path is now
blocked until channels and triggers finish loading successfully.

## Changes

- Agent page data: 30-second in-memory cache, partitioned by machine and tenant;
  retain the previous result during revalidation and failures. Deduplicate
  in-flight reads and invalidate/revalidate after API writes.
- Loading/error states no longer appear as a confirmed absence of bindings.
  Editing and saving bindings require successfully loaded data.
- Server bot identity and avatar caches: 5-minute freshness, coalesced requests,
  background refresh with the previous good value retained for up to one hour,
  and 30-second failure backoff. Credential changes select a new identity key.
  Avatar requests still authorize the channel before reading cached bytes.
- Frontend avatar blobs: shared, machine/tenant-scoped 5-minute cache; aborting
  one viewer does not cancel the image fetch needed by another.
- Agent cards display configured reasoning effort and speed mode.
- Error notices use explicit error state, including `signal: killed` failures.

## Verification

- Frontend suite: 39 files, 159 tests passed.
- Focused final frontend rerun: 35 tests passed; TypeScript/Vite build passed.
- `go test ./server -count=1` passed.
- Focused server cache/channel/avatar tests passed with `-race`.
- Native UI verified: three bound channels, all portraits, and
  `grok-4.7 · xhigh · 快速` on all three cards. Navigation retained the cards
  while stale data refreshed in the background.
- Remote channel list timings after deployment: cold 1104.06 ms;
  subsequent reads 7.79 ms and 3.97 ms (daemon-local measurements).

## Applied builds

Version: `0.1.14-channel-cache.1`.

- Updated and restarted `/Applications/AgentMux.app`; checked its code signature
  and local status version. Built with the installed Xcode 26.5 SDK because
  the default Command Line Tools 27 SDK is incompatible with the current linker.
- Updated the `aliyun-swas-sg` user service; verified the uploaded binary SHA-256,
  checked that no channel tasks were active before restarting, and read back
  the version, all channel bindings, and connected states afterwards.
- Desktop backup:
  `/Users/bytedance/Library/Application Support/agentmux/backups/AgentMux-before-channel-cache-20260922-173716.app`
- Remote binary backup:
  `/root/.agentmux/backups/amux-before-channel-cache-20260922-173627`
- Other remote services were not updated.
