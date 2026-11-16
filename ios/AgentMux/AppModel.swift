import AgentMuxKit
import Foundation
import Observation

/// What the app shows.
enum Screen: Equatable {
    /// No hub yet: scan or paste its link.
    case connect
    case signIn(HubLink)
    case inbox(HubLink, AccountIdentity)
}

@MainActor
@Observable
final class AppModel {
    private(set) var screen: Screen = .connect
    private(set) var mailbox: MailOverview?
    private(set) var busy = false
    var error: String?

    private let sessions: SessionStore
    private let defaults: UserDefaults
    private var client: HubClient?
    private static let linkKey = "hub.link"

    init(sessions: SessionStore = KeychainSessionStore(), defaults: UserDefaults = .standard) {
        self.sessions = sessions
        self.defaults = defaults
        if let data = defaults.data(forKey: Self.linkKey),
           let link = try? JSONDecoder().decode(HubLink.self, from: data) {
            connect(link)
        }
    }

    /// A link from the QR scanner, a deep link or the paste field.
    func useLink(_ text: String) {
        do {
            let link = try HubLink.parse(text)
            defaults.set(try JSONEncoder().encode(link), forKey: Self.linkKey)
            sessions.sessionToken = nil
            connect(link)
        } catch {
            self.error = error.localizedDescription
        }
    }

    func signIn(login: String, password: String) {
        perform { client in
            let account = try await client.signIn(login: login.trimmingCharacters(in: .whitespaces), password: password)
            try await self.showInbox(client, account)
        }
    }

    func refresh() async {
        guard let client, case .inbox = screen else { return }
        await run { self.mailbox = try await client.mailbox() }
    }

    func signOut() {
        perform { client in
            await client.signOut()
            self.mailbox = nil
            self.screen = .signIn(client.link)
        }
    }

    func forgetHub() {
        defaults.removeObject(forKey: Self.linkKey)
        sessions.sessionToken = nil
        client = nil
        mailbox = nil
        screen = .connect
    }

    func messages(with peer: String) async throws -> [MailMessage] {
        guard let client else { return [] }
        return try await client.messages(with: peer)
    }

    func send(_ text: String, to peer: String) async throws {
        guard let client else { return }
        try await client.send(text: text, to: peer)
    }

    /// Reports a failure from a view's own call the way the model's are.
    func report(_ error: Error) {
        if case HubError.unauthorized = error, let client { screen = .signIn(client.link) }
        self.error = error.localizedDescription
    }

    // MARK: -

    private func connect(_ link: HubLink) {
        let client = HubClient(link: link, sessions: sessions)
        self.client = client
        mailbox = nil
        screen = .signIn(link)
        // Open on the inbox when the stored session is still good.
        perform(quiet: true) { client in
            let state = try await client.authSession()
            if state.authenticated == true, let account = state.account {
                try await self.showInbox(client, account)
            }
        }
    }

    private func showInbox(_ client: HubClient, _ account: AccountIdentity) async throws {
        screen = .inbox(client.link, account)
        mailbox = try await client.mailbox()
    }

    private func perform(quiet: Bool = false, _ body: @escaping @MainActor (HubClient) async throws -> Void) {
        guard let client else { return }
        Task { await run(quiet: quiet) { try await body(client) } }
    }

    private func run(quiet: Bool = false, _ body: @MainActor () async throws -> Void) async {
        busy = true
        defer { busy = false }
        do {
            try await body()
        } catch HubError.unauthorized(let message) {
            if let client { screen = .signIn(client.link) }
            if !quiet { error = message }
        } catch {
            self.error = error.localizedDescription
        }
    }
}
