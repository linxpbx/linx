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

    /// The ceiling written into an SDP this phone sends, in bits per second:
    /// the most a picture could ever use here (`VideoQuality.hd`). What it
    /// *does* use is decided call by call from the link and the route
    /// (ADR-081) and set on the sender itself, which is where a moving ceiling
    /// belongs — an SDP is negotiated once and would otherwise hold a good
    /// link down to what the first seconds of the call could manage.
    static let videoMaxBitrate = VideoQuality.hd.bitrate

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

    /// capVideo writes that ceiling into an SDP this phone is about to send
    /// (RFC 3556 `b=AS`, in kilobits), so the other side and Asterisk both
    /// know it before a single frame is sent. An SDP with no video in it is
    /// left exactly as it was.
    static func capVideo(_ sdp: String) -> String {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        var lines = sdp.components(separatedBy: eol)
        guard let video = lines.firstIndex(where: { $0.hasPrefix("m=video ") }) else { return sdp }
        // b= goes straight after c=, and there is at most one of each per
        // media section (RFC 4566 §5).
        var at = video + 1
        while at < lines.count, lines[at].hasPrefix("i=") || lines[at].hasPrefix("c=") { at += 1 }
        let cap = "b=AS:\(videoMaxBitrate / 1000)"
        if at < lines.count, lines[at].hasPrefix("b=AS:") {
            lines[at] = cap
        } else {
            lines.insert(cap, at: at)
        }
        return lines.joined(separator: eol)
    }

    /// hasVideo reports whether an SDP offers or answers a picture at all: a
    /// video section that hasn't been turned down (port 0) or switched off.
    static func hasVideo(_ sdp: String) -> Bool {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        for section in sections(sdp, eol: eol) where section.first?.hasPrefix("m=video ") == true {
            let port = section[0].dropFirst("m=video ".count).prefix { $0.isNumber }
            guard port != "0" else { continue }
            if section.contains(where: { $0.hasPrefix("a=inactive") }) { continue }
            return true
        }
        return false
    }

    /// theySendVideo reports whether the other side's SDP says *they* will
    /// send a picture — their `sendrecv` or `sendonly`, which is what
    /// decides whether this phone shows a window for them.
    static func theySendVideo(_ sdp: String) -> Bool {
        let eol = sdp.contains("\r\n") ? "\r\n" : "\n"
        for section in sections(sdp, eol: eol) where section.first?.hasPrefix("m=video ") == true {
            let port = section[0].dropFirst("m=video ".count).prefix { $0.isNumber }
            guard port != "0" else { continue }
            if section.contains(where: { $0.hasPrefix("a=sendrecv") || $0.hasPrefix("a=sendonly") }) {
                return true
            }
            // No direction at all means sendrecv (RFC 4566).
            if !section.contains(where: {
                $0.hasPrefix("a=recvonly") || $0.hasPrefix("a=inactive")
            }) {
                return true
            }
        }
        return false
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
