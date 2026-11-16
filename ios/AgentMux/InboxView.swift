import AgentMuxKit
import SwiftUI

/// The conversations beside the selected one on iPad; a list that pushes
/// the conversation on iPhone.
struct InboxView: View {
    @Environment(AppModel.self) private var model
    let link: HubLink
    let account: AccountIdentity
    @State private var selection: MailConversation.ID?

    private var conversations: [MailConversation] { model.mailbox?.conversations ?? [] }

    var body: some View {
        NavigationSplitView {
            List(conversations, selection: $selection) { conversation in
                NavigationLink(value: conversation.id) {
                    ConversationRow(conversation: conversation)
                }
            }
            .overlay {
                if model.mailbox != nil && conversations.isEmpty {
                    ContentUnavailableView("No conversations yet", systemImage: "tray")
                } else if model.mailbox == nil && model.busy {
                    ProgressView()
                }
            }
            .refreshable { await model.refresh() }
            .navigationTitle(account.shownName)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Text(link.url.absoluteString)
                        Button("Refresh", systemImage: "arrow.clockwise") { Task { await model.refresh() } }
                        Button("Sign out", systemImage: "rectangle.portrait.and.arrow.right") { model.signOut() }
                        Button("Use another hub", systemImage: "server.rack", role: .destructive) { model.forgetHub() }
                    } label: {
                        Label("Account", systemImage: "person.crop.circle")
                    }
                }
            }
        } detail: {
            if let selection, let conversation = conversations.first(where: { $0.id == selection }) {
                ThreadView(peer: conversation.peer).id(selection)
            } else {
                ContentUnavailableView("Select a conversation", systemImage: "bubble.left.and.bubble.right")
            }
        }
    }
}

private struct ConversationRow: View {
    let conversation: MailConversation

    var body: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Text(conversation.peer.shownName).font(.headline)
                    if let machine = conversation.peer.machine, !machine.isEmpty {
                        Text("· \(machine)").font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                Text(conversation.last.text)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                if let date = conversation.last.createdAt {
                    Text(date.formatted(.relative(presentation: .named)))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if let unread = conversation.unread, unread > 0 {
                    Text("\(unread)")
                        .font(.caption.bold())
                        .padding(.horizontal, 7)
                        .padding(.vertical, 2)
                        .background(.tint, in: Capsule())
                        .foregroundStyle(.white)
                }
            }
        }
        .padding(.vertical, 2)
    }
}
