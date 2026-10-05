import Foundation
import Security

// What a set-up phone keeps (docs/PHASE2.md §4): the certificate Linx issued,
// which is what tells this phone from any other, and the little that goes
// with it. No SIP password and no Linx password is ever stored — the app asks
// for a phone line every time it runs. The private key isn't here either: it
// is in the Secure Enclave, and only the blob that points at it is kept.

/// Who this phone is, as the app last heard it from Linx.
struct PhoneIdentity: Codable, Equatable, Sendable {
    /// The server, "https://host".
    let server: URL
    let deviceID: UUID
    let deviceName: String
    let personName: String
    let extensionNumber: String
    /// The phone's certificate and the CA's intermediate, PEM.
    var certificate: String
    /// Linx's own certificate authority, PEM, as it was when this phone was
    /// set up. Nothing is checked against it, on purpose: the phone's own
    /// certificate is only ever *presented* to Linx, which knows it by the
    /// fingerprint of the one it issued, and the connection itself is
    /// already proved by the server's public certificate. Refusing a
    /// renewal that came from a different internal CA would break every
    /// phone on a server whose CA was legitimately replaced and stop
    /// nothing, since an attacker able to answer for this server would have
    /// its certificate anyway (docs/THREAT_MODEL.md, the phone lifetime
    /// review). It is kept because it is Linx's own root, which a later
    /// step may have to trust directly.
    let ca: String
    var certNotAfter: Date
    /// When the phone has to be set up again if it stops being in touch.
    var setUpAgain: Date
    /// Which kind of key signs for this phone.
    let keyKind: DeviceKey.Kind

    /// certificateDER is the leaf certificate's bytes, which is what Linx
    /// wants with every proof.
    var certificateDER: Data? { PEM.first(in: certificate) }

    /// renewSoon is true once the certificate has less than a month left:
    /// the app renews it on its next token, long before it could run out.
    func renewSoon(now: Date = Date()) -> Bool {
        certNotAfter.timeIntervalSince(now) < 30 * 24 * 60 * 60
    }
}

/// The PEM the app has to read: the first certificate in a bundle.
enum PEM {
    static func first(in text: String) -> Data? {
        let begin = "-----BEGIN CERTIFICATE-----"
        let end = "-----END CERTIFICATE-----"
        guard let from = text.range(of: begin), let to = text.range(of: end, range: from.upperBound..<text.endIndex)
        else {
            return nil
        }
        let body = text[from.upperBound..<to.lowerBound].filter { !$0.isWhitespace }
        return Data(base64Encoded: String(body))
    }
}

/// Where the phone's certificate and key blob are kept. The real one is the
/// Keychain; the unit tests use their own, because an unsigned build in the
/// simulator has no Keychain at all.
protocol PhoneStorage: Sendable {
    @discardableResult func save(_ data: Data, as account: String) -> Bool
    func read(_ account: String) -> Data?
    func delete(_ account: String)
}

/// The Keychain, with only what this app puts in it. Everything is
/// `ThisDeviceOnly`, so nothing follows a backup or an iCloud restore onto
/// another phone — a phone that is restored elsewhere has to be set up again,
/// which is the point.
struct Keychain: PhoneStorage, Sendable {
    let service: String

    static let shared = Keychain(service: "com.linxpbx.app")

    private func query(_ account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    @discardableResult func save(_ data: Data, as account: String) -> Bool {
        var attributes = query(account)
        SecItemDelete(attributes as CFDictionary)
        attributes[kSecValueData as String] = data
        attributes[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        return SecItemAdd(attributes as CFDictionary, nil) == errSecSuccess
    }

    func read(_ account: String) -> Data? {
        var attributes = query(account)
        attributes[kSecReturnData as String] = true
        attributes[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(attributes as CFDictionary, &item) == errSecSuccess else { return nil }
        return item as? Data
    }

    func delete(_ account: String) {
        SecItemDelete(query(account) as CFDictionary)
    }
}

/// Where the two things a set-up phone keeps are kept.
enum PhoneStore {
    static let identityAccount = "identity"
    static let keyAccount = "device-key"

    static func load(from keychain: some PhoneStorage = Keychain.shared) -> (PhoneIdentity, DeviceKey)? {
        guard let identityData = keychain.read(identityAccount),
            let identity = try? JSONDecoder().decode(PhoneIdentity.self, from: identityData),
            let keyData = keychain.read(keyAccount),
            let key = try? DeviceKey.restore(kind: identity.keyKind, from: keyData)
        else {
            return nil
        }
        return (identity, key)
    }

    static func save(_ identity: PhoneIdentity, key: DeviceKey?, to keychain: some PhoneStorage = Keychain.shared) {
        if let data = try? JSONEncoder().encode(identity) {
            keychain.save(data, as: identityAccount)
        }
        if let key {
            keychain.save(key.stored, as: keyAccount)
        }
    }

    /// forget is "sign out of this phone": the certificate and the key go,
    /// and the app is back to its setup screen. The phone itself is stopped
    /// at Linx's end separately, which is what makes it safe for a phone
    /// someone has lost.
    static func forget(_ keychain: some PhoneStorage = Keychain.shared) {
        keychain.delete(identityAccount)
        keychain.delete(keyAccount)
    }
}
