import AgentMuxKit
import XCTest

/// Answers the client's requests from a queue, as a fake hub.
final class StubHub: URLProtocol {
    struct Reply {
        var status = 200
        var headers: [String: String] = [:]
        var body = ""
    }

    nonisolated(unsafe) static var replies: [Reply] = []
    nonisolated(unsafe) static var requests: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var recorded = request
        if let stream = request.httpBodyStream {
            stream.open()
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let read = stream.read(&buffer, maxLength: buffer.count)
                if read <= 0 { break }
                data.append(buffer, count: read)
            }
            stream.close()
            recorded.httpBody = data
        }
        Self.requests.append(recorded)
        let reply = Self.replies.isEmpty ? Reply(status: 500) : Self.replies.removeFirst()
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1", headerFields: reply.headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(reply.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

final class HubClientTests: XCTestCase {
    private let sessions = MemorySessionStore()
    private lazy var hub = HubClient(
        link: HubLink(url: URL(string: "https://203.0.113.7:443")!, fingerprint: nil),
        sessions: sessions, protocolClasses: [StubHub.self]
    )

    override func setUp() {
        StubHub.replies = []
        StubHub.requests = []
    }

    func testSignsInKeepsTheSessionAndSendsItBack() async throws {
        StubHub.replies = [
            .init(headers: ["Set-Cookie": "agentmux_console=ams_secret; Path=/; Max-Age=604800; HttpOnly; Secure; SameSite=Strict"],
                  body: #"{"account":{"id":"a1","login":"bob","display_name":"Bob","role":"member"}}"#),
            .init(body: #"{"me":{"address":"person:bob"},"unread":2,"conversations":[{"peer":{"address":"agent:builder","name":"Builder"},"self":{"address":"person:bob"},"last":{"id":"m1","from":"person:bob","text":"hi","created_at":"2026-11-15T14:05:03.123456Z"},"unread":2}]}"#),
        ]
        let account = try await hub.signIn(login: "bob", password: "pw")
        XCTAssertEqual(account.shownName, "Bob")
        XCTAssertEqual(sessions.sessionToken, "ams_secret")
        let mailbox = try await hub.mailbox()
        XCTAssertEqual(mailbox.unread, 2)
        XCTAssertEqual(mailbox.conversations?.first?.peer.shownName, "Builder")
        XCTAssertNotNil(mailbox.conversations?.first?.last.createdAt)

        let signIn = StubHub.requests[0]
        XCTAssertEqual(signIn.url?.path(), "/api/v1/auth/sign-in")
        XCTAssertEqual(signIn.value(forHTTPHeaderField: HubClient.consoleHeader), "1")
        let body = try JSONSerialization.jsonObject(with: XCTUnwrap(signIn.httpBody)) as? [String: String]
        XCTAssertEqual(body, ["login": "bob", "password": "pw"])
        XCTAssertNil(signIn.value(forHTTPHeaderField: "Cookie"))
        XCTAssertEqual(StubHub.requests[1].value(forHTTPHeaderField: "Cookie"), "agentmux_console=ams_secret")
    }

    func testMessagesAskForOnePeer() async throws {
        StubHub.replies = [.init(body: #"[{"id":"m1","from":"agent:builder","text":"done","created_at":"2026-11-15T14:05:03Z"}]"#)]
        let messages = try await hub.messages(with: "agent:builder@dev 1")
        XCTAssertEqual(messages.map(\.text), ["done"])
        let url = try XCTUnwrap(StubHub.requests.first?.url)
        XCTAssertEqual(URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first?.value, "agent:builder@dev 1")
    }

    func testAnEndedSessionIsForgotten() async {
        sessions.sessionToken = "ams_old"
        StubHub.replies = [.init(status: 401, body: "unauthorized")]
        do {
            _ = try await hub.mailbox()
            XCTFail("expected unauthorized")
        } catch {
            XCTAssertEqual(error as? HubError, .unauthorized("unauthorized"))
            XCTAssertNil(sessions.sessionToken)
        }
    }

    func testSignOutForgetsTheSessionEvenWhenTheHubIsGone() async {
        sessions.sessionToken = "ams_old"
        StubHub.replies = [.init(status: 500)]
        await hub.signOut()
        XCTAssertNil(sessions.sessionToken)
    }

    func testHubErrorsCarryTheirMessage() async {
        StubHub.replies = [.init(status: 403, body: #"{"error":"reserved for administrators"}"#)]
        do {
            _ = try await hub.mailbox()
            XCTFail("expected forbidden")
        } catch {
            XCTAssertEqual(error as? HubError, .forbidden("reserved for administrators"))
        }
    }
}
