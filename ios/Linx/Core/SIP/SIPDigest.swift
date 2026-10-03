import CryptoKit
import Foundation

// Answering Asterisk's challenge (RFC 3261 §22, which is HTTP digest, RFC
// 2617). The phone's SIP password lives in memory only, for as long as the
// app is open: it comes from POST /api/v1/me/phone-line and is new every time
// (docs/PHASE2.md §12, step 4a).

/// What Asterisk asked for in a 401 or 407.
struct SIPChallenge: Equatable, Sendable {
    var realm: String
    var nonce: String
    var opaque: String?
    var qop: String?
    var algorithm: String
    var stale: Bool
    /// Which header the challenge came in, so the answer goes in its pair.
    var proxy: Bool

    /// parse reads a WWW-Authenticate or Proxy-Authenticate header. Anything
    /// but Digest, and anything but MD5, is refused: Asterisk's pjsip offers
    /// MD5, and guessing at the rest would only fail later, less clearly.
    static func parse(_ header: String, proxy: Bool) -> SIPChallenge? {
        let trimmed = header.trimmingCharacters(in: .whitespaces)
        guard trimmed.lowercased().hasPrefix("digest ") else { return nil }
        var values: [String: String] = [:]
        for part in splitParameters(String(trimmed.dropFirst("digest ".count))) {
            let pair = part.split(separator: "=", maxSplits: 1)
            guard pair.count == 2 else { continue }
            let key = pair[0].trimmingCharacters(in: .whitespaces).lowercased()
            var value = pair[1].trimmingCharacters(in: .whitespaces)
            if value.hasPrefix("\"") && value.hasSuffix("\"") && value.count > 1 {
                value = String(value.dropFirst().dropLast())
            }
            values[key] = value
        }
        guard let realm = values["realm"], let nonce = values["nonce"] else { return nil }
        let algorithm = (values["algorithm"] ?? "MD5").uppercased()
        guard algorithm == "MD5" else { return nil }
        return SIPChallenge(
            realm: realm, nonce: nonce, opaque: values["opaque"], qop: qop(values["qop"]),
            algorithm: algorithm, stale: (values["stale"] ?? "").lowercased() == "true", proxy: proxy)
    }

    /// Asterisk may offer several qop values; the app does "auth" and nothing
    /// else ("auth-int" would mean hashing the body too, which nothing asks of
    /// a phone).
    private static func qop(_ offered: String?) -> String? {
        guard let offered else { return nil }
        return offered.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
            .contains("auth") ? "auth" : nil
    }

    /// Splits on commas that aren't inside quotes.
    private static func splitParameters(_ text: String) -> [String] {
        var parts: [String] = []
        var current = ""
        var quoted = false
        for character in text {
            if character == "\"" { quoted.toggle() }
            if character == ",", !quoted {
                parts.append(current)
                current = ""
                continue
            }
            current.append(character)
        }
        parts.append(current)
        return parts
    }
}

/// The Authorization header that answers a challenge.
enum SIPDigest {
    static func authorization(
        challenge: SIPChallenge, username: String, password: String, method: String, uri: String,
        nonceCount: Int, cnonce: String = SIPRandom.token(16)
    ) -> String {
        let ha1 = md5("\(username):\(challenge.realm):\(password)")
        let ha2 = md5("\(method):\(uri)")
        var fields = [
            "username=\"\(username)\"", "realm=\"\(challenge.realm)\"", "nonce=\"\(challenge.nonce)\"",
            "uri=\"\(uri)\"", "algorithm=MD5",
        ]
        let response: String
        if let qop = challenge.qop {
            let nc = String(format: "%08x", nonceCount)
            response = md5("\(ha1):\(challenge.nonce):\(nc):\(cnonce):\(qop):\(ha2)")
            fields += ["qop=\(qop)", "nc=\(nc)", "cnonce=\"\(cnonce)\""]
        } else {
            response = md5("\(ha1):\(challenge.nonce):\(ha2)")
        }
        fields.append("response=\"\(response)\"")
        if let opaque = challenge.opaque { fields.append("opaque=\"\(opaque)\"") }
        return "Digest " + fields.joined(separator: ", ")
    }

    /// Digest's own hash. MD5 is what the scheme says and all it is used for
    /// here: nothing is kept secret by it, and the password itself never
    /// leaves the phone's memory.
    static func md5(_ text: String) -> String {
        Insecure.MD5.hash(data: Data(text.utf8)).map { String(format: "%02x", $0) }.joined()
    }
}
