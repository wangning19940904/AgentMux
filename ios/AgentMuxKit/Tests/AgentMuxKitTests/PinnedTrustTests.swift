@testable import AgentMuxKit
import Security
import XCTest

/// A self-signed P-256 certificate, as `[hub] tls = "auto"` makes, and its
/// SHA-256 as `openssl dgst -sha256` prints it.
final class PinnedTrustTests: XCTestCase {
    private func fixture(_ name: String) throws -> Data {
        let url = try XCTUnwrap(Bundle.module.url(forResource: name, withExtension: nil, subdirectory: "Fixtures"))
        return try Data(contentsOf: url)
    }

    private func trust(for der: Data) throws -> SecTrust {
        let certificate = try XCTUnwrap(SecCertificateCreateWithData(nil, der as CFData))
        var trust: SecTrust?
        XCTAssertEqual(SecTrustCreateWithCertificates(certificate, SecPolicyCreateBasicX509(), &trust), errSecSuccess)
        return try XCTUnwrap(trust)
    }

    func testFingerprintMatchesOpenSSL() throws {
        let der = try fixture("hub-cert.der")
        let expected = String(decoding: try fixture("hub-cert.sha256"), as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        XCTAssertEqual(PinnedTrust.fingerprint(der: der), expected)
    }

    func testOnlyThePinnedCertificateMatches() throws {
        let der = try fixture("hub-cert.der")
        let trust = try trust(for: der)
        let pinned = PinnedTrust.fingerprint(der: der)
        XCTAssertTrue(PinnedTrust.matches(trust, fingerprint: pinned))
        XCTAssertTrue(PinnedTrust.matches(trust, fingerprint: pinned.uppercased()))
        XCTAssertFalse(PinnedTrust.matches(trust, fingerprint: String(repeating: "0", count: 64)))
        XCTAssertFalse(PinnedTrust.matches(trust, fingerprint: String(pinned.dropLast())))
    }
}
