import Foundation

/// Why a call to the hub failed, in terms the screens can act on.
public enum HubError: LocalizedError, Equatable {
    /// No session, or it ended: sign in again.
    case unauthorized(String)
    case forbidden(String)
    case rateLimited(String)
    /// Whatever answered is not the hub in the link.
    case wrongCertificate
    case unreachable(String)
    case http(Int, String)

    public var errorDescription: String? {
        switch self {
        case let .unauthorized(message), let .forbidden(message), let .rateLimited(message), let .http(_, message):
            message
        case .wrongCertificate:
            "The server's certificate does not match this hub's link. Scan the link again."
        case let .unreachable(message):
            "Cannot reach the hub: \(message)"
        }
    }
}

/// The hub's API on its client port ([hub] client_api). A signed-in app sends
/// the account session as the Console does: the `agentmux_console` cookie
/// together with the `X-AgentMux-Console` header, without which the hub
/// ignores the cookie.
public final class HubClient: Sendable {
    public static let sessionCookie = "agentmux_console"
    public static let consoleHeader = "X-AgentMux-Console"

    public let link: HubLink
    private let sessions: SessionStore
    private let session: URLSession
    private let pinning: PinningDelegate

    /// `protocolClasses` lets tests answer requests without a network.
    public init(link: HubLink, sessions: SessionStore, protocolClasses: [AnyClass]? = nil) {
        self.link = link
        self.sessions = sessions
        let configuration = URLSessionConfiguration.ephemeral
        // The session cookie is handled here, so the shared cookie jar never
        // stores it or sends it to another host.
        configuration.httpShouldSetCookies = false
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpCookieStorage = nil
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 30
        if let protocolClasses { configuration.protocolClasses = protocolClasses }
        pinning = PinningDelegate(fingerprint: link.fingerprint)
        session = URLSession(configuration: configuration, delegate: pinning, delegateQueue: nil)
    }

    deinit { session.invalidateAndCancel() }

    public func authSession() async throws -> AuthSession {
        try await send(request("/api/v1/auth/session"))
    }

    public func signIn(login: String, password: String) async throws -> AccountIdentity {
        var request = request("/api/v1/auth/sign-in", method: "POST")
        request.httpBody = try JSONEncoder().encode(["login": login, "password": password])
        let response: SignInResponse = try await send(request)
        return response.account
    }

    public func signOut() async {
        defer { sessions.sessionToken = nil }
        _ = try? await data(for: request("/api/v1/auth/sign-out", method: "POST"))
    }

    public func mailbox() async throws -> MailOverview {
        try await send(request("/api/v1/mailbox"))
    }

    /// The messages between the signed-in person and `peer`, oldest first.
    public func messages(with peer: String) async throws -> [MailMessage] {
        try await send(request("/api/v1/mailbox/messages", query: [URLQueryItem(name: "with", value: peer)]))
    }

    @discardableResult
    public func send(text: String, to peer: String) async throws -> MailMessage {
        var request = request("/api/v1/mailbox/messages", method: "POST")
        request.httpBody = try JSONEncoder().encode(["to": peer, "text": text])
        return try await send(request)
    }

    // MARK: - Transport

    private func request(_ path: String, method: String = "GET", query: [URLQueryItem] = []) -> URLRequest {
        var components = URLComponents(url: link.url.appending(path: path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { components.queryItems = query }
        var request = URLRequest(url: components.url!)
        request.httpMethod = method
        request.setValue("1", forHTTPHeaderField: Self.consoleHeader)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if method != "GET" { request.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        if let token = sessions.sessionToken {
            request.setValue("\(Self.sessionCookie)=\(token)", forHTTPHeaderField: "Cookie")
        }
        return request
    }

    private func send<T: Decodable>(_ request: URLRequest) async throws -> T {
        let (body, response) = try await data(for: request)
        rememberSession(response)
        guard (200..<300).contains(response.statusCode) else { throw failure(response.statusCode, body) }
        do {
            return try Self.decoder.decode(T.self, from: body)
        } catch {
            throw HubError.http(response.statusCode, "The hub sent an answer this app does not understand.")
        }
    }

    private func data(for request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let body: Data, response: URLResponse
        do {
            (body, response) = try await session.data(for: request)
        } catch {
            if pinning.sawMismatch { throw HubError.wrongCertificate }
            throw HubError.unreachable(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else { throw HubError.unreachable("no HTTP response") }
        return (body, http)
    }

    private func rememberSession(_ response: HTTPURLResponse) {
        guard let header = response.value(forHTTPHeaderField: "Set-Cookie") else { return }
        let url = response.url ?? link.url
        for cookie in HTTPCookie.cookies(withResponseHeaderFields: ["Set-Cookie": header], for: url)
        where cookie.name == Self.sessionCookie {
            let expired = cookie.expiresDate.map { $0 <= Date() } ?? false
            sessions.sessionToken = cookie.value.isEmpty || expired ? nil : cookie.value
        }
    }

    private func failure(_ status: Int, _ body: Data) -> HubError {
        let decoded = (try? JSONDecoder().decode(APIError.self, from: body))?.error
        let text = String(data: body, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines)
        let message = [decoded, text].compactMap { $0 }.first { !$0.isEmpty } ?? "HTTP \(status)"
        switch status {
        case 401:
            sessions.sessionToken = nil
            return .unauthorized(message)
        case 403: return .forbidden(message)
        case 429: return .rateLimited(message)
        default: return .http(status, message)
        }
    }

    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            // Go writes RFC 3339 with or without fractional seconds.
            let precise = ISO8601DateFormatter()
            precise.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = precise.date(from: text) ?? ISO8601DateFormatter().date(from: text) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "bad date \(text)"))
        }
        return decoder
    }()
}
