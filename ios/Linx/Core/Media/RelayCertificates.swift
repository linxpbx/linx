import Foundation
import Security
@preconcurrency import WebRTC

/// Who the app trusts for the TLS connection to Linx's call relay: **iOS**.
///
/// WebRTC carries its own frozen list of certificate authorities, built into
/// the library and as old as the library is. The owner's relay certificate is
/// newer than that list — Let's Encrypt began issuing from a 2026 chain (ISRG
/// Root YE) in September 2026 — so WebRTC refused the certificate, every
/// `turns:` connection failed before it began, and a call made anywhere the
/// relay is the only way through (a mobile network, a hotel, anything behind
/// NAT) connected and carried no sound at all. Safari, Linx's own web client
/// and every other part of iOS trusted the same certificate perfectly, which
/// is exactly why the browser worked where the app didn't (owner, 2026-10-05).
///
/// Nothing is skipped here and nothing is weakened. WebRTC asks this verifier
/// only when its own list has failed, and the answer comes from **the system's
/// trust store** — kept current by Apple, which a bundled list never is — with
/// the certificate held to the relay's own hostname, the one Linx itself named
/// in the credentials it issued. A certificate iOS doesn't trust, or one for
/// another name, is still refused and the call still goes without a relay.
final class RelayCertificates: NSObject, RTCSSLCertificateVerifier {
    /// The relay's hostnames, taken from the URLs Linx handed this phone.
    private let hosts: [String]

    init(relay: PhoneLine.Turn?) {
        hosts = Self.hosts(in: relay?.urls ?? [])
    }

    func verify(_ derCertificate: Data) -> Bool {
        guard !hosts.isEmpty,
            let certificate = SecCertificateCreateWithData(nil, derCertificate as CFData)
        else { return false }
        return hosts.contains { trusts(certificate, as: $0) }
    }

    /// Whether iOS trusts this certificate for that hostname. The system is
    /// allowed to fetch a missing intermediate the ordinary way; in practice
    /// it already has them, because the app has just spoken HTTPS to the same
    /// Linx server to get its phone line.
    private func trusts(_ certificate: SecCertificate, as host: String) -> Bool {
        var trust: SecTrust?
        let policy = SecPolicyCreateSSL(true, host as CFString)
        guard SecTrustCreateWithCertificates(certificate, policy, &trust) == errSecSuccess,
            let trust
        else { return false }
        return SecTrustEvaluateWithError(trust, nil)
    }

    /// The host out of each `turn:`/`turns:` URL. They are not ordinary URLs —
    /// `turns:host:port?transport=tcp` has no `//` — so the host is read out
    /// rather than parsed by URLComponents.
    static func hosts(in urls: [String]) -> [String] {
        var out: [String] = []
        for url in urls {
            guard let colon = url.firstIndex(of: ":") else { continue }
            let scheme = url[..<colon].lowercased()
            guard scheme == "turn" || scheme == "turns" || scheme == "stun" || scheme == "stuns" else {
                continue
            }
            var rest = String(url[url.index(after: colon)...])
            if let question = rest.firstIndex(of: "?") { rest = String(rest[..<question]) }
            // An address in brackets is IPv6; otherwise the host ends at the
            // port's colon.
            if rest.hasPrefix("["), let close = rest.firstIndex(of: "]") {
                rest = String(rest[rest.index(after: rest.startIndex)..<close])
            } else if let port = rest.lastIndex(of: ":") {
                rest = String(rest[..<port])
            }
            guard !rest.isEmpty, !out.contains(rest) else { continue }
            out.append(rest)
        }
        return out
    }
}
