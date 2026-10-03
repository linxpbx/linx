import CryptoKit
import Foundation
import Testing

@testable import Linx

/// The proof the phone signs to get a token (`Core/Proof.swift`). Linx checks
/// it with go-jose, which accepts ES256 and nothing else, so the shape
/// matters: two base64url parts, a raw 64-byte signature, and claims that
/// last exactly a minute.
struct ProofTests {
    @Test("a proof is a compact ES256 JWS over its own header and claims")
    func proof() throws {
        let key = P256.Signing.PrivateKey()
        let device = UUID(uuidString: "0199c0de-0000-7000-8000-00000000abcd")!
        let id = UUID(uuidString: "0199c0de-0000-7000-8000-00000000ee11")!
        let now = Date(timeIntervalSince1970: 1_791_028_800)
        let raw = try Proof.make(
            device: device.uuidString.lowercased(), key: DeviceKey.forTesting(key), now: now, id: id)

        let parts = raw.split(separator: ".", omittingEmptySubsequences: false)
        #expect(parts.count == 3)
        let header = try #require(base64URL(String(parts[0])))
        #expect(String(decoding: header, as: UTF8.self) == #"{"alg":"ES256","typ":"JWT"}"#)

        let claims = try JSONSerialization.jsonObject(with: try #require(base64URL(String(parts[1]))))
        let body = try #require(claims as? [String: Any])
        #expect(body["sub"] as? String == device.uuidString.lowercased())
        #expect(body["aud"] as? String == "linx-device")
        #expect(body["jti"] as? String == id.uuidString.lowercased())
        #expect(body["iat"] as? Int == 1_791_028_800)
        #expect(body["exp"] as? Int == 1_791_028_860)

        // The signature is r and s, 32 bytes each, over "header.claims".
        let signature = try #require(base64URL(String(parts[2])))
        #expect(signature.count == 64)
        let over = Data("\(parts[0]).\(parts[1])".utf8)
        #expect(
            key.publicKey.isValidSignature(
                try P256.Signing.ECDSASignature(rawRepresentation: signature), for: over))
    }

    @Test("each proof is its own: a new id every time")
    func singleUse() throws {
        let key = DeviceKey.forTesting(P256.Signing.PrivateKey())
        let first = try Proof.make(device: UUID().uuidString, key: key)
        let second = try Proof.make(device: UUID().uuidString, key: key)
        #expect(first != second)
    }

    private func base64URL(_ text: String) -> Data? {
        var padded = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while padded.count % 4 != 0 { padded += "=" }
        return Data(base64Encoded: padded)
    }
}
