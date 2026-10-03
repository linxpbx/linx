import CryptoKit
import Foundation

// What makes this phone this phone (docs/PHASE2.md §4). The app makes one
// P-256 key pair inside the Secure Enclave, and from then on everything it
// says to Linx is signed by that key: the certificate request when it is set
// up, and a short single-use proof every time it asks for a token. The
// private half can never be read, copied or backed up — not by the app,
// not by a backup, not by anyone who takes the phone apart — so a copied
// Keychain is worth nothing on another phone.

enum DeviceKeyError: Error, Equatable {
    /// There is no Secure Enclave (an old simulator). A real phone always
    /// has one, and the app refuses to pretend otherwise.
    case noSecureEnclave
    case keyUnusable
}

/// The phone's signing key.
struct DeviceKey: Sendable {
    /// Where the private half lives. `software` happens only in the
    /// simulator, which has no Secure Enclave on some Macs; a build on a
    /// phone never gets one.
    enum Kind: String, Codable, Sendable {
        case secureEnclave
        case software
    }

    let kind: Kind
    private let enclave: SecureEnclave.P256.Signing.PrivateKey?
    private let software: P256.Signing.PrivateKey?

    /// make is called once, when the phone is being set up.
    static func make() throws -> DeviceKey {
        if SecureEnclave.isAvailable {
            // Usable after the phone has been unlocked once since it was
            // switched on, and never needing Face ID: the app has to be able
            // to sign a proof when a call wakes it on the lock screen.
            var error: Unmanaged<CFError>?
            guard
                let access = SecAccessControlCreateWithFlags(
                    nil, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly, .privateKeyUsage, &error)
            else {
                throw DeviceKeyError.keyUnusable
            }
            return DeviceKey(
                kind: .secureEnclave,
                enclave: try SecureEnclave.P256.Signing.PrivateKey(accessControl: access),
                software: nil)
        }
        #if targetEnvironment(simulator)
            return DeviceKey(kind: .software, enclave: nil, software: P256.Signing.PrivateKey())
        #else
            throw DeviceKeyError.noSecureEnclave
        #endif
    }

    /// restore brings the key back after the app restarts, from what the
    /// Keychain kept. For a Secure Enclave key that is a wrapped blob only
    /// this phone's Enclave can unwrap.
    static func restore(kind: Kind, from stored: Data) throws -> DeviceKey {
        switch kind {
        case .secureEnclave:
            return DeviceKey(
                kind: .secureEnclave,
                enclave: try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: stored),
                software: nil)
        case .software:
            #if targetEnvironment(simulator)
                return DeviceKey(
                    kind: .software, enclave: nil,
                    software: try P256.Signing.PrivateKey(rawRepresentation: stored))
            #else
                throw DeviceKeyError.noSecureEnclave
            #endif
        }
    }

    /// stored is what goes in the Keychain. For a Secure Enclave key it is
    /// useless anywhere else.
    var stored: Data {
        if let enclave { return enclave.dataRepresentation }
        return software?.rawRepresentation ?? Data()
    }

    /// publicKeySPKI is the public half as a SubjectPublicKeyInfo, which is
    /// what a certificate request carries.
    var publicKeySPKI: Data {
        if let enclave { return enclave.publicKey.derRepresentation }
        return software?.publicKey.derRepresentation ?? Data()
    }

    private func signature(over data: Data) throws -> P256.Signing.ECDSASignature {
        if let enclave { return try enclave.signature(for: data) }
        guard let software else { throw DeviceKeyError.keyUnusable }
        return try software.signature(for: data)
    }

    /// derSignature signs `data` and answers in the form a certificate
    /// request wants (an ASN.1 r and s).
    func derSignature(over data: Data) throws -> Data {
        try signature(over: data).derRepresentation
    }

    /// rawSignature signs `data` and answers in the form a JWS wants (r and
    /// s, 32 bytes each, nothing around them).
    func rawSignature(over data: Data) throws -> Data {
        try signature(over: data).rawRepresentation
    }

    #if DEBUG
        /// A key in memory, for the unit tests. Never reachable in a release
        /// build, so a real phone always signs from its Secure Enclave.
        static func forTesting(_ key: P256.Signing.PrivateKey) -> DeviceKey {
            DeviceKey(kind: .software, enclave: nil, software: key)
        }
    #endif
}
