import AgentMuxKit
import XCTest

final class HubLinkTests: XCTestCase {
    private let fp = String(repeating: "ab", count: 32)

    func testReadsTheLinkTheHubPrints() throws {
        // As url.Values.Encode writes it in server/hub_clients.go.
        let link = try HubLink.parse("agentmux://hub?fp=\(fp)&url=https%3A%2F%2F203.0.113.7%3A443")
        XCTAssertEqual(link, HubLink(url: URL(string: "https://203.0.113.7:443")!, fingerprint: fp))
    }

    func testAcceptsColonFingerprintsAndCAURLs() throws {
        let colons = stride(from: 0, to: 64, by: 2).map { i -> String in
            let start = fp.index(fp.startIndex, offsetBy: i)
            return String(fp[start..<fp.index(start, offsetBy: 2)])
        }.joined(separator: ":").uppercased()
        XCTAssertEqual(try HubLink.parse("agentmux://hub?url=https://h.example.com/&fp=\(colons)").fingerprint, fp)
        let ca = try HubLink.parse("agentmux://hub?url=https://hub.example.com/")
        XCTAssertEqual(ca.url.absoluteString, "https://hub.example.com")
        XCTAssertNil(ca.fingerprint)
    }

    func testRefusesWhatIsNotASafeHubLink() {
        let cases: [(String, HubLink.ParseError)] = [
            ("https://hub.example.com", .notAHubLink),
            ("agentmux://other?url=https://h", .notAHubLink),
            ("agentmux://hub", .noAddress),
            ("agentmux://hub?url=http://10.0.0.2:8766", .notHTTPS),
            ("agentmux://hub?url=https://h&fp=1234", .badFingerprint),
            ("not a link at all", .notAHubLink),
        ]
        for (text, expected) in cases {
            XCTAssertThrowsError(try HubLink.parse(text), text) { error in
                XCTAssertEqual(error as? HubLink.ParseError, expected, text)
            }
        }
    }
}
