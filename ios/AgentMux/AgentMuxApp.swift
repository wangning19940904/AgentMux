import SwiftUI

@main
struct AgentMuxApp: App {
    @State private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                // agentmux://hub?url=…&fp=… from the camera app or a message.
                .onOpenURL { url in
                    if url.scheme == "agentmux" { model.useLink(url.absoluteString) }
                }
        }
    }
}
