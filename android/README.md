# AgentMux for Android

A native client for an AgentMux hub. It reaches the hub on its device port
(`[hub] addr`, usually behind the relay), which serves the API to phones when
the hub has `[hub] client_api = true`.

## How it connects

1. On a computer, a signed-in person opens the Console's **Settings → Phone
   app**, which shows `GET /api/v1/hub/client-link` as a QR code:
   `agentmux://hub?url=<hub address>&fp=<certificate SHA-256>`. The hub needs
   no public IP of its own: the address is `[hub] public_url` when set, so a
   relay, tunnel or forwarded port works. A loopback or `http://` address is
   refused with what to change; a LAN or VPN address works with a note that
   the phone must be on that network.
2. The app scans that code, or opens the link from the camera app, or takes it
   pasted. Only `https://` hub addresses are accepted.
3. Every connection is checked against `fp`: the hub's certificate is
   self-signed, so the app trusts exactly that certificate, as paired machines
   do ([`PinnedTls.kt`](app/src/main/java/com/agentmux/mobile/hub/PinnedTls.kt)).
   A link without `fp` falls back to the system's CA checks.
4. The person signs in with their AgentMux account. The app keeps the
   `agentmux_console` session cookie and sends it with the
   `X-AgentMux-Console: 1` header, as the Console does.

## What is here

Four tabs, after the layout of Persona's Android app (large-title list pages,
hairline rows, monogram avatars, status dots):

| Tab | What it does | Hub API |
| --- | --- | --- |
| Messages | Conversations with agents and people, a thread page to read and reply, and New conversation from the mailbox directory | `/api/v1/mailbox`, `/mailbox/messages`, `/mailbox/directory` |
| Agents (administrators) | Every machine's agents, filtered by machine, with a detail page; live Claude Code / Codex sessions with their transcript, and replies to sessions that run behind a channel | `/api/v1/remote/fleet/query` and `/execute` over `/agent-instances`, `/sessions`, `/sessions/messages` |
| Devices | Computers and servers with their state and hub role (members: their paired machines), and the phones signed in, which an administrator can sign out | `/api/v1/fleet` (or `/devices`), `/api/v1/devices/phones` |
| Me | Account, hub address and pinned certificate, this phone's name, sign out, use another hub | |

Pages poll while they are on screen (a thread every 4 s, lists every 15–30 s).

The app's User-Agent names it and the phone, `AgentMux-Android/0.2.0 (Xiaomi 14; Android 15)`
([`PhoneIdentity.kt`](app/src/main/java/com/agentmux/mobile/hub/PhoneIdentity.kt)),
which is how the Console's Devices page lists the phone under **Phones**.

| Part | File |
| --- | --- |
| Link parsing, pinning, API client | `hub/` |
| Theme, icons, shared components | `ui/theme/`, `ui/components/` |
| State and navigation | `ui/AppViewModel.kt`, `ui/AppRoot.kt` |
| Screens | `ui/screens/` |
| Launcher icon | made from `assets/branding/agentmux-logo.png` by `scripts/make_icons.py` |

Not yet: push notifications, approvals, and wrapping the stored session with a Keystore key.

## Build

Needs JDK 17 and the Android SDK with platform 35 and build-tools 35.0.0
(`sdk.dir` in `local.properties`, or `ANDROID_HOME`). On macOS with Homebrew:
`brew install openjdk@17` and `brew install --cask android-commandlinetools`,
then `sdkmanager "platforms;android-35" "build-tools;35.0.0"`.

```bash
cd android
JAVA_HOME=/opt/homebrew/opt/openjdk@17 ./gradlew testDebugUnitTest assembleDebug
```

The APK is `app/build/outputs/apk/debug/app-debug.apk`.
