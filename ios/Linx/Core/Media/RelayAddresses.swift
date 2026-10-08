import Darwin
import Foundation

/// Where Linx's call relay is, looked up by **iOS** rather than by WebRTC.
///
/// WebRTC looks a relay's name up with a resolver of its own, and on some
/// networks that lookup fails where every other part of the phone succeeds.
/// The owner's iPhone on 5G with Tailscale (an exit node abroad) showed it:
/// "701 TURN host lookup received error" for `turn.home.mym.ae`, a name the
/// public DNS answered perfectly (2026-10-08). With the relay's address
/// unknown the phone has no relay route at all, so a call whose direct
/// route fails has nowhere to fall back to — and the offer waits for a
/// relay route that never comes, which is time the person spends looking
/// at "Calling…".
///
/// So the app asks the system (`getaddrinfo`, which follows the phone's
/// VPN and DNS settings the way Safari does), keeps the last answer that
/// worked, and gives WebRTC the address. Nothing about trust changes: a
/// `turns:` relay given by address still has its certificate checked
/// against the relay's own name, which WebRTC is told separately (the ICE
/// server's `hostname`) and which `RelayCertificates` holds the certificate
/// to.
@MainActor enum RelayAddresses {
    /// The last addresses each relay name looked up to, and when.
    private static var known: [String: (addresses: [String], at: Date)] = [:]
    private static var looking: [String: Task<[String], Never>] = [:]

    /// A lookup newer than this is used without asking again.
    static let fresh: TimeInterval = 10 * 60
    /// How long a call waits for a lookup before going with what it has.
    static let patience: Duration = .milliseconds(1500)

    /// Starts looking these names up now, so a call made in a moment finds
    /// them already known (the line opening, the relay's credentials
    /// arriving, the app coming to the front).
    static func warm(_ hosts: [String]) {
        for host in hosts where !isAddress(host) { _ = lookUp(host) }
    }

    /// The addresses for each name: a fresh answer straight away; otherwise a
    /// new lookup, waited for no longer than `patience`; otherwise the last
    /// answer that worked. A name nobody could look up is left out, and its
    /// URLs keep the name for WebRTC to try itself.
    static func addresses(for hosts: [String]) async -> [String: [String]] {
        var out: [String: [String]] = [:]
        for host in hosts where !isAddress(host) {
            if let last = known[host], Date().timeIntervalSince(last.at) < fresh {
                out[host] = last.addresses
                continue
            }
            let lookup = lookUp(host)
            let answer = await withTaskGroup(of: [String]?.self) { group in
                group.addTask { await lookup.value }
                group.addTask {
                    try? await Task.sleep(for: patience)
                    return nil
                }
                let first = await group.next() ?? nil
                group.cancelAll()
                return first
            }
            if let answer, !answer.isEmpty {
                out[host] = answer
            } else if let last = known[host] {
                out[host] = last.addresses
            }
        }
        return out
    }

    private static func lookUp(_ host: String) -> Task<[String], Never> {
        if let running = looking[host] { return running }
        let task = Task<[String], Never> {
            let found = await Task.detached(priority: .userInitiated) { systemLookup(host) }.value
            if !found.isEmpty { known[host] = (found, Date()) }
            looking[host] = nil
            return found
        }
        looking[host] = task
        return task
    }

    /// `getaddrinfo`: the phone's own resolver, through its VPN and DNS
    /// settings. IPv4 first, then IPv6, each address once.
    nonisolated static func systemLookup(_ host: String) -> [String] {
        var hints = addrinfo()
        hints.ai_family = AF_UNSPEC
        hints.ai_socktype = SOCK_DGRAM
        var list: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(host, nil, &hints, &list) == 0, let first = list else { return [] }
        defer { freeaddrinfo(first) }
        var v4: [String] = []
        var v6: [String] = []
        var cursor: UnsafeMutablePointer<addrinfo>? = first
        while let entry = cursor {
            var buffer = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(
                entry.pointee.ai_addr, entry.pointee.ai_addrlen, &buffer, socklen_t(buffer.count),
                nil, 0, NI_NUMERICHOST) == 0
            {
                let address = String(cString: buffer)
                if entry.pointee.ai_family == AF_INET6 {
                    if !v6.contains(address) { v6.append(address) }
                } else if !v4.contains(address) {
                    v4.append(address)
                }
            }
            cursor = entry.pointee.ai_next
        }
        return v4 + v6
    }

    /// Whether a URL's host is already an address (nothing to look up).
    nonisolated static func isAddress(_ host: String) -> Bool {
        var v4 = in_addr()
        var v6 = in6_addr()
        return inet_pton(AF_INET, host, &v4) == 1 || inet_pton(AF_INET6, host, &v6) == 1
    }

    /// The relay's URLs written with addresses instead of names: one ICE
    /// server per name, carrying that name as its `hostname` so a TLS relay
    /// is still checked against it (and asked for it by SNI, which is how a
    /// front door in front of Linx routes it). Names with no address keep
    /// their URLs as they are.
    nonisolated static func servers(for urls: [String], addresses: [String: [String]])
        -> [(urls: [String], hostname: String?)]
    {
        var byName: [(host: String?, urls: [String])] = []
        func add(_ url: String, under host: String?) {
            if let i = byName.firstIndex(where: { $0.host == host }) {
                byName[i].urls.append(url)
            } else {
                byName.append((host, [url]))
            }
        }
        for url in urls {
            guard let host = RelayCertificates.hosts(in: [url]).first, let found = addresses[host], !found.isEmpty
            else {
                add(url, under: nil)
                continue
            }
            for address in found {
                add(replace(host, with: address, in: url), under: host)
            }
        }
        return byName.map { ($0.urls, $0.host) }
    }

    /// `turns:turn.example.com:443?transport=tcp` with the name swapped for an
    /// address (in brackets if it's IPv6).
    nonisolated static func replace(_ host: String, with address: String, in url: String) -> String {
        guard let colon = url.firstIndex(of: ":") else { return url }
        let scheme = url[...colon]
        var rest = String(url[url.index(after: colon)...])
        let written = address.contains(":") ? "[\(address)]" : address
        if rest.hasPrefix(host) {
            rest = written + rest.dropFirst(host.count)
        } else if rest.hasPrefix("[\(host)]") {
            rest = written + rest.dropFirst(host.count + 2)
        }
        return scheme + rest
    }
}
