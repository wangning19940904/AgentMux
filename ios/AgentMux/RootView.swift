import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        Group {
            switch model.screen {
            case .connect:
                NavigationStack { ConnectView() }
            case let .signIn(link):
                NavigationStack { SignInView(link: link) }
            case let .inbox(link, account):
                InboxView(link: link, account: account)
            }
        }
        .alert("AgentMux", isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.error ?? "")
        }
    }
}
