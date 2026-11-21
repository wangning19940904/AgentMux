# Meego to-dos as Super Agent requests

Meego (Feishu Project) tells a person about work through its own bot, and no
other bot can read those messages. `poll.py` reads the same thing from the
other side: the signed-in person's to-do list, with `bytedcli meego todo list`.
Each new to-do becomes one request to a Super Agent's
[custom callback trigger](../../contract/SUPER_AGENTS.md#triggers).

| Event | When |
| --- | --- |
| `started` | A node you own is now in progress, whether or not it was announced as assigned. |
| `assigned` | Only with `announce_assigned`: a node you own appears in the list, and its turn has not come yet. |

- A new work item puts most of its nodes on its owner's list at once. One run
  sends at most one request per work item, naming every node of it that is
  news, so a new story with ten nodes is one request, not ten.
- By default a node waiting for its turn is news only once it starts. Set
  `announce_assigned` to hear about it as soon as it reaches your list; its
  nodes are then listed in the same request.
- The first run records what is already in the list and sends nothing.
- All events about one work item share a thread, so the agent sees its earlier
  Tickets for it.
- A node that is rolled back and reaches you again is announced again. A to-do
  is forgotten only after three polls in a row without it.
- If the list cannot be read, nothing changes. After five failed polls in a
  row the agent is told once, so it can tell you to sign in again.
- It needs no Meego admin rights and no address Meego can reach. The cost is
  the polling delay.

## Set up

1. In the Console, add a **Custom callback** trigger to the Super Agent. Put
   `meego` in its allowed callers, choose the chat for results, and write what
   the agent should do with a Meego event in "Added to every request". Copy
   the address and the secret.
2. On the same machine, sign in once: `bytedcli meego login`.
3. Write `~/.agentmux/meego-todo/config.json` (mode 600):

   ```json
   {
     "callback_url": "http://127.0.0.1:8765/hook/super-agent/callback/src_example",
     "secret": "the trigger's secret",
     "bytedcli": "/Users/me/.local/bin/bytedcli"
   }
   ```

   Optional keys: `actor` (`meego`), `site` (`https://meego.larkoffice.com`),
   `state` (`state.json` beside the config), `announce_assigned` (false),
   `max_events_per_run` (10 work items), `report_failures_after` (5).
4. Check what it would do, then let it take the baseline:

   ```sh
   python3 poll.py --dry-run
   python3 poll.py
   ```

5. Run it every two minutes. On macOS, save this as
   `~/Library/LaunchAgents/com.agentmux.meego-todo.plist` with your paths, then
   `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agentmux.meego-todo.plist`:

   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0"><dict>
     <key>Label</key><string>com.agentmux.meego-todo</string>
     <key>ProgramArguments</key><array>
       <string>/usr/bin/python3</string>
       <string>/Users/me/.agentmux/meego-todo/poll.py</string>
     </array>
     <key>EnvironmentVariables</key><dict>
       <key>PATH</key><string>/Users/me/.local/bin:/usr/bin:/bin</string>
     </dict>
     <key>StartInterval</key><integer>120</integer>
     <key>StandardOutPath</key><string>/Users/me/.agentmux/meego-todo/poll.log</string>
     <key>StandardErrorPath</key><string>/Users/me/.agentmux/meego-todo/poll.log</string>
   </dict></plist>
   ```

## Who can reach the agent this way

A work item's title and description are written by whoever filed it, and they
reach the agent as part of the request. Anyone who can assign you a Meego node
can therefore start work on your agent. Say in the trigger's instructions what
the agent may do on its own and what needs your word first, such as a
production release.

## Test

```sh
python3 test_poll.py
```
