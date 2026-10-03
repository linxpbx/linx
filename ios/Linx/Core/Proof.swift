import Foundation

// How the phone proves who it is after it has been set up (docs/PHASE2.md
// §4). Not with a TLS client certificate: a front door that decrypts and
// re-encrypts (Caddy, Nginx Proxy Manager) would swallow it, and Linx has to
// work behind every front door and inside China. Instead the app signs a
// short statement with its Secure Enclave key and sends it with the
// certificate Linx issued. The statement is good for one minute and can be
// used once, so a copy of it is worthless, and it can only be made on that
// phone.

extension Data {
    /// base64url, as every part of a JWS is written.
    var base64URL: String {
        base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

/// One signed statement: `{"alg":"ES256"}` . claims . signature.
enum Proof {
    /// How long a proof says it is good for. Linx refuses a longer one.
    static let lifetime: TimeInterval = 60

    private struct Claims: Encodable {
        let sub: String
        let aud: String
        let jti: String
        let iat: Int
        let exp: Int
    }

    /// make signs a proof for this phone. `device` is the id Linx gave it
    /// when it was set up, and the audience is fixed: a proof is good for
    /// asking for a device token and for nothing else.
    static func make(device: String, key: DeviceKey, now: Date = Date(), id: UUID = UUID()) throws -> String {
        let header = Data(#"{"alg":"ES256","typ":"JWT"}"#.utf8).base64URL
        let claims = Claims(
            sub: device, aud: "linx-device", jti: id.uuidString.lowercased(),
            iat: Int(now.timeIntervalSince1970), exp: Int(now.timeIntervalSince1970 + lifetime))
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.withoutEscapingSlashes]
        let payload = try encoder.encode(claims).base64URL
        let signed = "\(header).\(payload)"
        let signature = try key.rawSignature(over: Data(signed.utf8))
        return "\(signed).\(signature.base64URL)"
    }
}
