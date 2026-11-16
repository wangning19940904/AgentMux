import CryptoKit
import Foundation
import Security

/// A hub without a domain presents its own self-signed certificate. Machines
/// that pair with it trust exactly that certificate by its SHA-256
/// fingerprint (link.Fingerprint in the Go code); the app does the same.
public enum PinnedTrust {
    /// SHA-256 of a certificate's DER encoding, in lowercase hex.
    public static func fingerprint(der: Data) -> String {
        SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
    }

    public static func fingerprint(_ certificate: SecCertificate) -> String {
        fingerprint(der: SecCertificateCopyData(certificate) as Data)
    }

    /// Whether the leaf certificate of `trust` is the pinned one. The
    /// comparison does not stop early, so its timing reveals nothing.
    public static func matches(_ trust: SecTrust, fingerprint expected: String) -> Bool {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate], let leaf = chain.first else {
            return false
        }
        return constantTimeEqual(fingerprint(leaf), HubLink.normalizeFingerprint(expected))
    }

    static func constantTimeEqual(_ a: String, _ b: String) -> Bool {
        let x = Array(a.utf8), y = Array(b.utf8)
        guard x.count == y.count else { return false }
        return zip(x, y).reduce(0) { $0 | ($1.0 ^ $1.1) } == 0
    }
}

/// Answers server-trust challenges for one hub. With a fingerprint it
/// accepts exactly that certificate, whatever host name it names (the hub is
/// often reached by IP address); without one the system decides.
final class PinningDelegate: NSObject, URLSessionDelegate, @unchecked Sendable {
    private let fingerprint: String?
    private let lock = NSLock()
    private var mismatched = false

    init(fingerprint: String?) {
        self.fingerprint = fingerprint
    }

    /// True once a server has shown a certificate other than the pinned one.
    var sawMismatch: Bool {
        lock.withLock { mismatched }
    }

    func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust, let fingerprint
        else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        if PinnedTrust.matches(trust, fingerprint: fingerprint) {
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else {
            lock.withLock { mismatched = true }
            completionHandler(.cancelAuthenticationChallenge, nil)
        }
    }
}
