import Foundation
import Network
import Security
import Synchronization
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
///
/// **The check never goes to the internet while a call is up.** WebRTC hands
/// over the relay's own certificate and nothing else, and it asks on the
/// one thread that sends everything every connection of the call sends. Left
/// to itself iOS fetches the missing issuer from Let's Encrypt over plain
/// HTTP, and that thread — the sound, the picture, both relays — waited for
/// it: on Etisalat's mobile data the iPhone sent nothing for 2.5 s every
/// 4 s through the first 40 s of every call, on any route, while Safari on
/// the same phone was clean (2026-10-17). So the issuers are fetched once,
/// ahead of the call, over TLS from the relay itself (`warm`), and each check
/// is made from what is already here, with fetching switched off; a
/// certificate already found good is remembered.
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
        if Self.known.withLock({ known in
            hosts.contains { known.trusted.contains(Trusted(host: $0, certificate: derCertificate)) }
        }) {
            return true
        }
        let began = Date()
        for host in hosts {
            let issuers = Self.known.withLock { $0.issuers[host] } ?? []
            if !issuers.isEmpty, Self.trusts(certificate, issuers: issuers, as: host, fetching: false) {
                Self.remember(derCertificate, for: host)
                Self.tell("Relay certificate checked here in \(Self.ms(since: began))")
                return true
            }
        }
        // Nothing fetched yet, or a renewed certificate from a new issuer:
        // iOS's own way, which may go to the internet — this once.
        for host in hosts where Self.trusts(certificate, issuers: [], as: host, fetching: true) {
            Self.remember(derCertificate, for: host)
            Self.tell("Relay certificate checked over the internet in \(Self.ms(since: began))")
            Self.known.withLock { $0.issuers[host] = nil }
            return true
        }
        Self.tell("Relay certificate refused (\(Self.ms(since: began)))")
        return false
    }

    /// Whether iOS trusts this certificate for that hostname, with the
    /// issuers given and, when `fetching` is off, nothing from the network.
    static func trusts(
        _ certificate: SecCertificate, issuers: [Data], as host: String, fetching: Bool
    ) -> Bool {
        var trust: SecTrust?
        let chain = [certificate] + issuers.compactMap { SecCertificateCreateWithData(nil, $0 as CFData) }
        let policy = SecPolicyCreateSSL(true, host as CFString)
        guard SecTrustCreateWithCertificates(chain as CFArray, policy, &trust) == errSecSuccess,
            let trust
        else { return false }
        SecTrustSetNetworkFetchAllowed(trust, fetching)
        return SecTrustEvaluateWithError(trust, nil)
    }

    // MARK: - What is known ahead of the call

    private struct Trusted: Hashable {
        let host: String
        let certificate: Data
    }

    private struct Known {
        /// Each relay's issuers, as iOS built the chain from what the relay
        /// sent (DER).
        var issuers: [String: [Data]] = [:]
        var fetching: Set<String> = []
        var trusted: Set<Trusted> = []
    }

    private static let known = Mutex(Known())

    private static func remember(_ certificate: Data, for host: String) {
        known.withLock { known in
            if known.trusted.count > 16 { known.trusted.removeAll() }
            known.trusted.insert(Trusted(host: host, certificate: certificate))
        }
    }

    /// Fetches each relay's issuers over TLS, from the relay itself, unless
    /// they are here already: when the line opens and whenever new relay
    /// credentials arrive, so the call's own checks find them waiting.
    static func warm(_ urls: [String]) {
        for (host, port) in tlsEndpoints(in: urls) {
            let go = known.withLock { known in
                guard known.issuers[host] == nil, !known.fetching.contains(host) else { return false }
                known.fetching.insert(host)
                return true
            }
            if go { fetchIssuers(host: host, port: port) }
        }
    }

    /// The host and port of each `turns:` URL: where the relay speaks TLS.
    static func tlsEndpoints(in urls: [String]) -> [(host: String, port: UInt16)] {
        var out: [(host: String, port: UInt16)] = []
        for url in urls where url.lowercased().hasPrefix("turns:") {
            guard let host = hosts(in: [url]).first, !out.contains(where: { $0.host == host }) else { continue }
            var rest = url.dropFirst("turns:".count)
            if let question = rest.firstIndex(of: "?") { rest = rest[..<question] }
            var port: UInt16 = 443
            if let colon = rest.lastIndex(of: ":"), !rest.hasSuffix("]"),
                let given = UInt16(rest[rest.index(after: colon)...])
            {
                port = given
            }
            out.append((host, port))
        }
        return out
    }

    private static func fetchIssuers(host: String, port: UInt16) {
        let queue = DispatchQueue(label: "linx.relay-issuers", qos: .utility)
        let tls = NWProtocolTLS.Options()
        sec_protocol_options_set_tls_server_name(tls.securityProtocolOptions, host)
        sec_protocol_options_set_verify_block(
            tls.securityProtocolOptions,
            { _, peer, complete in
                let trust = sec_trust_copy_ref(peer).takeRetainedValue()
                SecTrustSetPolicies(trust, SecPolicyCreateSSL(true, host as CFString))
                SecTrustEvaluateAsyncWithError(trust, queue) { trust, ok, _ in
                    if ok, let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate], chain.count > 1 {
                        let issuers = chain.dropFirst().map { SecCertificateCopyData($0) as Data }
                        known.withLock { $0.issuers[host] = issuers }
                    }
                    complete(ok)
                }
            }, queue)
        let connection = NWConnection(
            host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port) ?? 443, using: NWParameters(tls: tls))
        connection.stateUpdateHandler = { state in
            switch state {
            case .ready, .failed, .cancelled:
                known.withLock { _ = $0.fetching.remove(host) }
                if case .cancelled = state { return }
                connection.cancel()
            default:
                return
            }
        }
        connection.start(queue: queue)
        queue.asyncAfter(deadline: .now() + 15) { connection.cancel() }
    }

    // MARK: - Saying how long it took

    private static func ms(since began: Date) -> String {
        "\(Int((Date().timeIntervalSince(began) * 1000).rounded())) ms"
    }

    /// Onto the call's timeline (`WebRTCMedia.relayChecked`).
    private static func tell(_ words: String) {
        Task { @MainActor in WebRTCMedia.relayChecked(words) }
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

extension RTCPeerConnectionFactory {
    /// A connection whose relay certificate is checked by `verifier` when
    /// there is one: every connection that may use Linx's relay over TLS
    /// needs it — the call's sound, its picture and a group call.
    func peerConnection(
        with configuration: RTCConfiguration, constraints: RTCMediaConstraints,
        verifier: RTCSSLCertificateVerifier?, delegate: RTCPeerConnectionDelegate
    ) -> RTCPeerConnection? {
        guard let verifier else {
            return peerConnection(with: configuration, constraints: constraints, delegate: delegate)
        }
        return peerConnection(
            with: configuration, constraints: constraints, certificateVerifier: verifier, delegate: delegate)
    }
}
