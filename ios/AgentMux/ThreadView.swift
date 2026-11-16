import AgentMuxKit
import SwiftUI

/// One conversation, oldest message first, with a field to write back.
struct ThreadView: View {
    @Environment(AppModel.self) private var model
    let peer: MailParty
    @State private var messages: [MailMessage] = []
    @State private var draft = ""
    @State private var sending = false
    @State private var loaded = false

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 8) {
                    ForEach(messages) { message in
                        Bubble(message: message, mine: message.from != peer.address)
                            .id(message.id)
                    }
                }
                .padding()
            }
            .defaultScrollAnchor(.bottom)
            .overlay {
                if !loaded { ProgressView() }
            }
            .onChange(of: messages.last?.id) { _, last in
                if let last { withAnimation { proxy.scrollTo(last, anchor: .bottom) } }
            }
        }
        .safeAreaInset(edge: .bottom) {
            HStack(alignment: .bottom) {
                TextField("Message", text: $draft, axis: .vertical)
                    .lineLimit(1...5)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task { await send() }
                } label: {
                    Image(systemName: "arrow.up.circle.fill").font(.title2)
                }
                .disabled(sending || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityLabel("Send")
            }
            .padding()
            .background(.bar)
        }
        .navigationTitle(peer.shownName)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .refreshable { await load() }
    }

    private func load() async {
        do {
            messages = try await model.messages(with: peer.address)
        } catch {
            model.report(error)
        }
        loaded = true
    }

    private func send() async {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        sending = true
        defer { sending = false }
        do {
            try await model.send(text, to: peer.address)
            draft = ""
            await load()
            await model.refresh()
        } catch {
            model.report(error)
        }
    }
}

private struct Bubble: View {
    let message: MailMessage
    let mine: Bool

    var body: some View {
        HStack {
            if mine { Spacer(minLength: 48) }
            VStack(alignment: mine ? .trailing : .leading, spacing: 2) {
                Text(message.text)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(mine ? AnyShapeStyle(.tint) : AnyShapeStyle(.fill.tertiary), in: RoundedRectangle(cornerRadius: 16))
                    .foregroundStyle(mine ? .white : .primary)
                    .textSelection(.enabled)
                if message.failedAt != nil {
                    Label("Not delivered", systemImage: "exclamationmark.circle")
                        .font(.caption)
                        .foregroundStyle(.red)
                } else if let date = message.createdAt {
                    Text(date, format: .dateTime.hour().minute())
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            if !mine { Spacer(minLength: 48) }
        }
    }
}
