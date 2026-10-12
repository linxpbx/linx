import Foundation

// The same low-bandwidth settings the browser sends (web/src/phone/sdp.ts,
// CLAUDE.md "Low-resource and low-bandwidth rule"), so a bad network degrades
// the same way on a phone as it does in a tab: Opus with in-band FEC (it
// rebuilds lost packets), DTX (next to nothing goes out during silence), mono,
// and a ceiling of OPUS_MAX_BITRATE. WebRTC still goes lower by itself on a
// poor link.

enum SDPTweaks {
    /// Opus's most, in bits per second: full-quality mono voice.
    static let opusMaxBitrate = 24000

    /// letThemStartTheEncryption answers an offer that lets either side start
    /// the DTLS handshake (`actpass`) as the side that waits (`passive`).
    /// Asterisk drops a handshake that arrives before its own side of the
    /// route is confirmed, and WebRTC then waits one, two, four seconds
    /// before trying again: a call answered on mobile data had its sound
    /// start seconds late (2026-10-11). As the side that starts, Asterisk
    /// begins the moment it is ready, and nothing is thrown away.
    static func letThemStartTheEncryption(_ answer: String, offer: String) -> String {
        guard offer.contains("a=setup:actpass") else { return answer }
        return answer.replacingOccurrences(of: "a=setup:active", with: "a=setup:passive")
    }

    /// preferOpusFecDtx writes the Opus settings into an SDP this phone is
    /// about to send. An SDP without Opus is left exactly as it was.
    static func preferOpusFecDtx(_ sdp: String) -> String {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        var lines = sdp.components(separatedBy: eol)
        guard
            let rtpmap = lines.firstIndex(where: {
                $0.hasPrefix("a=rtpmap:") && $0.lowercased().contains(" opus/48000/2")
            })
        else { return sdp }
        let payload = lines[rtpmap].dropFirst("a=rtpmap:".count).prefix { $0.isNumber }
        guard !payload.isEmpty else { return sdp }
        let prefix = "a=fmtp:\(payload) "

        let wanted = [
            ("useinbandfec", "1"), ("usedtx", "1"), ("stereo", "0"), ("sprop-stereo", "0"),
            ("maxaveragebitrate", String(opusMaxBitrate)),
        ]
        guard let fmtp = lines.firstIndex(where: { $0.hasPrefix(prefix) }) else {
            let parameters = wanted.map { "\($0.0)=\($0.1)" }.joined(separator: ";")
            lines.insert(prefix + parameters, at: rtpmap + 1)
            return lines.joined(separator: eol)
        }
        var parameters = lines[fmtp].dropFirst(prefix.count).split(separator: ";").map(String.init)
        for (key, value) in wanted {
            if let at = parameters.firstIndex(where: { $0.trimmingCharacters(in: .whitespaces).hasPrefix(key + "=") }) {
                parameters[at] = "\(key)=\(value)"
            } else {
                parameters.append("\(key)=\(value)")
            }
        }
        lines[fmtp] = prefix + parameters.joined(separator: ";")
        return lines.joined(separator: eol)
    }

    /// everyStreamHasRoutes says whether every open stream of an offer
    /// carries at least one route candidate. Asterisk turns ICE off for a
    /// stream whose offer has none (res_pjsip_sdp_rtp's check_ice_support),
    /// for the rest of the call: a move to the relay sent the instant its
    /// gathering began went out with none, and the call fell silent
    /// (2026-10-12).
    ///
    /// Streams bundled onto one route share its candidates, which may sit in
    /// the first of them only, so a bundle needs them once.
    static func everyStreamHasRoutes(_ sdp: String) -> Bool {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        let bundled = Set(
            sdp.components(separatedBy: eol).first { $0.hasPrefix("a=group:BUNDLE ") }
                .map { $0.dropFirst("a=group:BUNDLE ".count).split(separator: " ").map(String.init) } ?? [])
        let open = sections(sdp, eol: eol).filter { $0[0].split(separator: " ").dropFirst().first != "0" }
        let hasRoutes = { (section: [String]) in section.contains(where: { $0.hasPrefix("a=candidate:") }) }
        let inBundle = { (section: [String]) in
            section.contains { $0.hasPrefix("a=mid:") && bundled.contains(String($0.dropFirst("a=mid:".count))) }
        }
        let alone = open.filter { !inBundle($0) }
        let together = open.filter(inBundle)
        return alone.allSatisfy(hasRoutes) && (together.isEmpty || together.contains(where: hasRoutes))
    }

    /// The first ICE username in an SDP: when the other side's changes, it
    /// has started looking for routes afresh (an ICE restart).
    static func iceUser(_ sdp: String) -> String? {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        return sdp.components(separatedBy: eol).first { $0.hasPrefix("a=ice-ufrag:") }
            .map { String($0.dropFirst("a=ice-ufrag:".count)) }
    }

    /// Which streams of an offer carry ICE details, in a few words for Call
    /// details: "audio ice, video no ice".
    static func iceWords(_ sdp: String) -> String {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        return sections(sdp, eol: eol).map { section in
            let kind = section[0].dropFirst(2).prefix { $0 != " " }
            let ice = section.contains(where: { $0.hasPrefix("a=ice-ufrag:") })
            return "\(kind) \(ice ? "ice" : "no ice")"
        }.joined(separator: ", ")
    }

    /// The SDP cut into its media sections, each starting with its own m=
    /// line. Lines before the first m= belong to the session and are left
    /// out: every question here is about one stream.
    private static func sections(_ sdp: String, eol: String) -> [[String]] {
        var out: [[String]] = []
        for line in sdp.components(separatedBy: eol) {
            if line.hasPrefix("m=") {
                out.append([line])
            } else if !out.isEmpty {
                out[out.count - 1].append(line)
            }
        }
        return out
    }
}
