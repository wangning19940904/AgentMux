import Foundation

// The hub's JSON, as server/accounts.go and server/mailbox.go write it.
// Only the fields the app shows are declared; the rest are ignored.

public struct AccountIdentity: Codable, Equatable, Sendable {
    public let id: String
    public let login: String
    public let displayName: String?
    public let role: String

    public var shownName: String {
        if let displayName, !displayName.isEmpty { return displayName }
        return login
    }
}

public struct AuthSession: Codable, Equatable, Sendable {
    public var accountsEnabled: Bool?
    public var signInRequired: Bool?
    public var authenticated: Bool?
    public var admin: Bool?
    public var account: AccountIdentity?
}

struct SignInResponse: Codable {
    let account: AccountIdentity
}

public struct MailParty: Codable, Equatable, Hashable, Sendable {
    public let address: String
    public var kind: String?
    public var name: String?
    public var machine: String?

    public var shownName: String {
        if let name, !name.isEmpty { return name }
        return address
    }
}

public struct MailMessage: Codable, Equatable, Identifiable, Sendable {
    public let id: String
    public var from: String
    public var fromName: String?
    public var to: String?
    public var text: String
    public var createdAt: Date?
    public var failedAt: Date?
    public var error: String?
}

public struct MailConversation: Codable, Equatable, Identifiable, Sendable {
    public let peer: MailParty
    public let last: MailMessage
    public var unread: Int?
    public var messages: Int?

    public var id: String { peer.address }
}

public struct MailOverview: Codable, Equatable, Sendable {
    public let me: MailParty
    public var admin: Bool?
    public var conversations: [MailConversation]?
    public var unread: Int?
}

struct APIError: Codable {
    let error: String
}
