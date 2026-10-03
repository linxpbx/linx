import CryptoKit
import Foundation
import Testing

@testable import Linx

/// The certificate request the app writes by hand (`Core/DER.swift`). Linx's
/// own certificate authority parses this with Go's x509, which refuses
/// anything that isn't exactly right, so the bytes are checked here rather
/// than trusted.
struct DERTests {
    @Test("lengths under 128 are one byte, and longer ones say how many follow")
    func lengths() {
        #expect(DER.encode(tag: 0x04, Data([1, 2, 3])) == Data([0x04, 0x03, 1, 2, 3]))
        let long = DER.encode(tag: 0x04, Data(repeating: 7, count: 200))
        #expect(long.prefix(3) == Data([0x04, 0x81, 200]))
        #expect(long.count == 203)
        let longer = DER.encode(tag: 0x04, Data(repeating: 7, count: 300))
        #expect(longer.prefix(4) == Data([0x04, 0x82, 0x01, 0x2C]))
    }

    @Test("object identifiers are packed the way DER says")
    func oids() {
        // 1.2.840.113549.1.9.14 (extensionRequest) and 2.5.4.3 (commonName).
        #expect(DER.OID.extensionRequest == Data([0x06, 0x09, 0x2A, 0x86, 0x48, 0x86, 0xF7, 0x0D, 0x01, 0x09, 0x0E]))
        #expect(DER.OID.commonName == Data([0x06, 0x03, 0x55, 0x04, 0x03]))
        #expect(DER.OID.subjectAltName == Data([0x06, 0x03, 0x55, 0x1D, 0x11]))
        #expect(DER.OID.ecdsaWithSHA256 == Data([0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x04, 0x03, 0x02]))
    }

    @Test("a certificate request holds the name, the key and a signature over itself")
    func certificateRequest() throws {
        let key = P256.Signing.PrivateKey()
        let csr = try CertificateRequest.der(
            commonName: "linx-phone", publicKeySPKI: key.publicKey.derRepresentation,
            sign: { try key.signature(for: $0).derRepresentation })

        // CertificationRequest ::= SEQUENCE { info, signatureAlgorithm, signature }
        let parts = try ASN1.children(of: csr)
        #expect(parts.count == 3)
        let info = parts[0]
        #expect(info.tag == 0x30)

        // CertificationRequestInfo ::= SEQUENCE { version, subject, spki, attributes }
        let inside = try ASN1.children(of: info.whole)
        #expect(inside.count == 4)
        #expect(inside[0].whole == Data([0x02, 0x01, 0x00]))  // version 0
        #expect(inside[2].whole == key.publicKey.derRepresentation)
        #expect(inside[3].tag == 0xA0)  // the attributes, [0] constructed

        // The name, and the same name again as a subject alternative name.
        let name = "linx-phone".data(using: .utf8)!
        #expect(inside[1].whole.range(of: DER.utf8String("linx-phone")) != nil)
        #expect(inside[3].whole.range(of: Data([0x82, UInt8(name.count)]) + name) != nil)

        // ecdsa-with-SHA256, and a signature the phone's own key made over
        // exactly the bytes of the request's contents.
        #expect(parts[1].whole == DER.sequence(DER.OID.ecdsaWithSHA256))
        // A slice's indices start where it was cut, so copy before counting.
        let bits = Data(parts[2].whole)
        #expect(bits.first == 0x03)
        let signature = try P256.Signing.ECDSASignature(derRepresentation: Data(parts[2].body).dropFirst())
        #expect(key.publicKey.isValidSignature(signature, for: info.whole))
    }
}

/// Just enough ASN.1 reading for the tests to look inside what the app wrote.
enum ASN1 {
    struct Value {
        let tag: UInt8
        let body: Data
        /// The whole thing, tag and length included.
        let whole: Data
    }

    enum Problem: Error { case truncated }

    static func children(of der: Data) throws -> [Value] {
        let outer = try read(der, at: der.startIndex)
        var out: [Value] = []
        var index = outer.body.startIndex
        while index < outer.body.endIndex {
            let value = try read(outer.body, at: index)
            out.append(value)
            index += value.whole.count
        }
        return out
    }

    private static func read(_ data: Data, at start: Data.Index) throws -> Value {
        guard start + 1 < data.endIndex else { throw Problem.truncated }
        let tag = data[start]
        var index = start + 1
        var length = Int(data[index])
        index += 1
        if length & 0x80 != 0 {
            let count = length & 0x7F
            guard index + count <= data.endIndex else { throw Problem.truncated }
            length = 0
            for byte in data[index..<(index + count)] { length = length << 8 | Int(byte) }
            index += count
        }
        guard index + length <= data.endIndex else { throw Problem.truncated }
        return Value(tag: tag, body: data[index..<(index + length)], whole: data[start..<(index + length)])
    }
}
