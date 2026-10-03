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
}
