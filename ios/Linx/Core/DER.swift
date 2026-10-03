import Foundation

// The small amount of ASN.1 (DER) the app has to write itself. Apple has no
// API for a certificate request, and a certificate request is the only way to
// ask Linx's own certificate authority for a certificate for the key inside
// the Secure Enclave (docs/PHASE2.md §4). Nothing here parses anything: the
// app only ever writes these few shapes, and the unit tests check the bytes.

/// DER writing: a tag, a length, and the value.
enum DER {
    /// A value with its tag and length. Lengths under 128 are one byte; the
    /// rest say how many length bytes follow.
    static func encode(tag: UInt8, _ body: Data) -> Data {
        var out = Data([tag])
        let n = body.count
        if n < 0x80 {
            out.append(UInt8(n))
        } else {
            var length = Data()
            var rest = n
            while rest > 0 {
                length.insert(UInt8(rest & 0xFF), at: 0)
                rest >>= 8
            }
            out.append(UInt8(0x80 | length.count))
            out.append(length)
        }
        out.append(body)
        return out
    }

    static func sequence(_ parts: Data...) -> Data { sequence(parts) }

    static func sequence(_ parts: [Data]) -> Data {
        encode(tag: 0x30, parts.reduce(into: Data()) { $0.append($1) })
    }

    static func set(_ parts: Data...) -> Data {
        encode(tag: 0x31, parts.reduce(into: Data()) { $0.append($1) })
    }

    /// A small non-negative integer (the only ones the app writes).
    static func integer(_ value: UInt8) -> Data { encode(tag: 0x02, Data([value])) }

    static func utf8String(_ text: String) -> Data { encode(tag: 0x0C, Data(text.utf8)) }

    static func bitString(_ bytes: Data) -> Data {
        // No unused bits in anything the app writes.
        encode(tag: 0x03, Data([0x00]) + bytes)
    }

    static func octetString(_ bytes: Data) -> Data { encode(tag: 0x04, bytes) }

    /// A context-specific tag, e.g. `[0]` for a request's attributes or
    /// `[2]` for a DNS name inside a subject alternative name.
    static func contextSpecific(_ number: UInt8, constructed: Bool, _ body: Data) -> Data {
        encode(tag: 0x80 | (constructed ? 0x20 : 0x00) | number, body)
    }

    /// An object identifier, written from its numbers (1.2.840… ). The first
    /// two are packed into one byte; the rest are base-128, high bit set on
    /// every byte but the last.
    static func objectIdentifier(_ numbers: [UInt]) -> Data {
        var body = Data()
        guard numbers.count >= 2 else { return encode(tag: 0x06, body) }
        body.append(UInt8(numbers[0] * 40 + numbers[1]))
        for number in numbers.dropFirst(2) {
            var chunks = [UInt8(number & 0x7F)]
            var rest = number >> 7
            while rest > 0 {
                chunks.insert(UInt8(rest & 0x7F) | 0x80, at: 0)
                rest >>= 7
            }
            body.append(contentsOf: chunks)
        }
        return encode(tag: 0x06, body)
    }

    /// The object identifiers this app needs, and nothing else.
    enum OID {
        /// id-at-commonName
        static let commonName = objectIdentifier([2, 5, 4, 3])
        /// ecdsa-with-SHA256
        static let ecdsaWithSHA256 = objectIdentifier([1, 2, 840, 10045, 4, 3, 2])
        /// pkcs-9-at-extensionRequest
        static let extensionRequest = objectIdentifier([1, 2, 840, 113549, 1, 9, 14])
        /// id-ce-subjectAltName
        static let subjectAltName = objectIdentifier([2, 5, 29, 17])
    }
}

/// A PKCS#10 certificate request for the phone's own key, signed by that key
/// so Linx knows the phone holds it.
enum CertificateRequest {
    /// der builds and signs the request. `publicKeySPKI` is the key's
    /// SubjectPublicKeyInfo (CryptoKit's `derRepresentation`), and `sign`
    /// returns a DER ECDSA signature over what it is given — on a real phone,
    /// from inside the Secure Enclave.
    static func der(commonName: String, publicKeySPKI: Data, sign: (Data) throws -> Data) throws -> Data {
        let subject = DER.sequence(DER.set(DER.sequence(DER.OID.commonName, DER.utf8String(commonName))))
        // One subject alternative name, the same name again: Linx signs that
        // one name and nothing else, for every phone.
        let names = DER.sequence(DER.contextSpecific(2, constructed: false, Data(commonName.utf8)))
        let extensions = DER.sequence(DER.sequence(DER.OID.subjectAltName, DER.octetString(names)))
        let attributes = DER.contextSpecific(
            0, constructed: true,
            DER.sequence(DER.OID.extensionRequest, DER.set(extensions)))
        let info = DER.sequence(DER.integer(0), subject, publicKeySPKI, attributes)
        let signature = try sign(info)
        return DER.sequence(info, DER.sequence(DER.OID.ecdsaWithSHA256), DER.bitString(signature))
    }
}
