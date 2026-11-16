import Foundation
import Security

/// Keeps the account session a sign-in returns.
public protocol SessionStore: AnyObject, Sendable {
    var sessionToken: String? { get set }
}

/// A session kept only in memory, for tests and previews.
public final class MemorySessionStore: SessionStore, @unchecked Sendable {
    private let lock = NSLock()
    private var token: String?

    public init(_ token: String? = nil) { self.token = token }

    public var sessionToken: String? {
        get { lock.withLock { token } }
        set { lock.withLock { token = newValue } }
    }
}

/// The session in the Keychain, readable after the first unlock and never
/// moved to another device by a backup or migration.
public final class KeychainSessionStore: SessionStore, @unchecked Sendable {
    private let service: String
    private let account: String

    public init(service: String = "com.agentmux.mobile.hub", account: String = "session") {
        self.service = service
        self.account = account
    }

    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }

    public var sessionToken: String? {
        get {
            var lookup = query
            lookup[kSecReturnData as String] = true
            lookup[kSecMatchLimit as String] = kSecMatchLimitOne
            var item: CFTypeRef?
            guard SecItemCopyMatching(lookup as CFDictionary, &item) == errSecSuccess, let data = item as? Data else {
                return nil
            }
            return String(data: data, encoding: .utf8)
        }
        set {
            SecItemDelete(query as CFDictionary)
            guard let newValue, let data = newValue.data(using: .utf8) else { return }
            var add = query
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            SecItemAdd(add as CFDictionary, nil)
        }
    }
}
