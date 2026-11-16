// swift-tools-version: 5.10
import PackageDescription

// What the iPhone and iPad app share and what `swift test` checks on a Mac:
// the hub link, certificate pinning, the session store and the API client.
let package = Package(
    name: "AgentMuxKit",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "AgentMuxKit", targets: ["AgentMuxKit"]),
    ],
    targets: [
        .target(name: "AgentMuxKit"),
        .testTarget(name: "AgentMuxKitTests", dependencies: ["AgentMuxKit"], resources: [.copy("Fixtures")]),
    ]
)
