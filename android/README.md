# AgentMux for Android

A native client for an AgentMux hub. It reaches the hub on its device port
(`[hub] addr`, usually behind the relay), which serves the API to phones when
the hub has `[hub] client_api = true`.

## How it connects

1. On a computer, a signed-in person opens `GET /api/v1/hub/client-link` (the
   Console will show it as a QR code). It returns
   `agentmux://hub?url=<hub address>&fp=<certificate SHA-256>`.
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

| Part | File |
| --- | --- |
| Link parsing | `hub/HubLink.kt` |
| Certificate pinning | `hub/PinnedTls.kt` |
| API client and errors | `hub/HubClient.kt`, `hub/Models.kt` |
| Stored hub and session | `hub/HubStore.kt` |
| Screens: connect, sign in, inbox | `ui/AppViewModel.kt`, `ui/Screens.kt` |

Not yet: push notifications, approvals, sending messages, the Console's QR
code, and wrapping the stored session with a Keystore key.

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
