import Foundation

/// Where a hub is and which certificate to expect there, as the hub's
/// `GET /api/v1/hub/client-link` describes it:
/// `agentmux://hub?url=https://203.0.113.7:443&fp=<sha-256 hex>`.
///
/// `fingerprint` is nil when the hub has a certificate a public CA signed;
/// the system's trust evaluation checks it then.
public struct HubLink: Codable, Equatable, Sendable {
    public static let scheme = "agentmux"

    public let url: URL
    public let fingerprint: String?

    public init(url: URL, fingerprint: String?) {
        self.url = url
        self.fingerprint = fingerprint
    }

    public enum ParseError: LocalizedError, Equatable {
        case notAHubLink, noAddress, badAddress, notHTTPS, badFingerprint

        public var errorDescription: String? {
            switch self {
            case .notAHubLink: "This is not an AgentMux hub link."
            case .noAddress: "The link has no hub address."
            case .badAddress: "The hub address is malformed."
            case .notHTTPS: "The hub address must use https://."
            case .badFingerprint: "The link's certificate fingerprint is malformed."
            }
        }
    }

    /// Reads a scanned, opened or pasted link.
    public static func parse(_ text: String) throws -> HubLink {
        guard let components = URLComponents(string: text.trimmingCharacters(in: .whitespacesAndNewlines)),
              components.scheme?.lowercased() == scheme, components.host?.lowercased() == "hub"
        else { throw ParseError.notAHubLink }
        let items = components.queryItems ?? []
        guard let raw = items.first(where: { $0.name == "url" })?.value, !raw.isEmpty else { throw ParseError.noAddress }
        var trimmed = raw.trimmingCharacters(in: .whitespaces)
        while trimmed.hasSuffix("/") { trimmed.removeLast() }
        guard let url = URL(string: trimmed), let host = url.host(), !host.isEmpty else { throw ParseError.badAddress }
        // The phone sends a password and a session to this address.
        guard url.scheme?.lowercased() == "https" else { throw ParseError.notHTTPS }

        var fingerprint: String?
        if let fp = items.first(where: { $0.name == "fp" })?.value.map(normalizeFingerprint), !fp.isEmpty {
            guard fp.count == 64, fp.allSatisfy({ $0.isHexDigit }) else { throw ParseError.badFingerprint }
            fingerprint = fp
        }
        return HubLink(url: url, fingerprint: fingerprint)
    }

    /// Accepts the hex form the hub prints and the colon-separated form.
    public static func normalizeFingerprint(_ raw: String) -> String {
        raw.trimmingCharacters(in: .whitespaces).lowercased().replacingOccurrences(of: ":", with: "")
    }
}
