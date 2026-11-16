# AgentMux for iPhone and iPad

A SwiftUI client for an AgentMux hub, for iOS and iPadOS 17 or later. It
reaches the hub on its device port (`[hub] addr`, usually behind the relay),
which serves the API to phones when the hub has `[hub] client_api = true`. It
connects the same way as the [Android app](../android/README.md).

## How it connects

1. A signed-in person gets the hub's link from
   `GET /api/v1/hub/client-link`:
   `agentmux://hub?url=<hub address>&fp=<certificate SHA-256>`.
2. The app scans it as a QR code (VisionKit), opens it from the Camera app
   or Messages (the `agentmux` URL scheme), or takes it pasted. Only
   `https://` hub addresses are accepted.
3. Every connection is checked against `fp`: the hub's certificate is
   self-signed, so the app trusts exactly that certificate, as paired
   machines do (`PinnedTrust.swift`). A link without `fp` falls back to the
   system's trust evaluation.
4. The person signs in with their AgentMux account. The session goes into
   the Keychain (this device only, never in backups) and is sent as the
   `agentmux_console` cookie with the `X-AgentMux-Console: 1` header. The
   shared cookie jar is not used.

On iPad the inbox and the open conversation sit side by side; on iPhone the
conversation is pushed. A conversation shows its messages and lets the person
write back.

## Layout

| Part | Where |
| --- | --- |
| Link, pinning, Keychain session, API client | `AgentMuxKit/` (a Swift package, tested with `swift test`) |
| App, screens, QR scanner | `AgentMux/` |
| URL scheme | `AgentMux-Info.plist` |
| Chinese strings | `AgentMux/Localizable.xcstrings` |

The project uses folder-synchronized groups, so a new file in `AgentMux/` is
part of the app without editing the project.

## Build and test

```bash
cd ios/AgentMuxKit && swift test          # runs on the Mac, no simulator needed
cd ios && xcodebuild -project AgentMux.xcodeproj -scheme AgentMux \
  -destination 'platform=iOS Simulator,name=iPhone 17' build
```

Building the app needs the iOS platform installed in Xcode (Settings →
Components, or `xcodebuild -downloadPlatform iOS`). To run it on your own
iPhone or iPad, open `AgentMux.xcodeproj`, choose your team under Signing &
Capabilities, and run it on the connected device.

Not yet: push notifications, approvals, and the Console's QR code.
