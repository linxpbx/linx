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

    /// renewSoon is true once the certificate has less than three days left:
    /// the app renews it on its next token, long before it could run out.
    /// Linx's certificates for phones last a week, so a month's margin (as
    /// it was) meant a new certificate at every token (2026-10-13).
    func renewSoon(now: Date = Date()) -> Bool {
        certNotAfter.timeIntervalSince(now) < 3 * 24 * 60 * 60
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
    /// How a read of this went: found, not there, or locked.
    func status(_ account: String) -> OSStatus
}

extension PhoneStorage {
    func status(_ account: String) -> OSStatus { read(account) == nil ? errSecItemNotFound : errSecSuccess }
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

    func status(_ account: String) -> OSStatus {
        var attributes = query(account)
        attributes[kSecReturnData as String] = true
        attributes[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        return SecItemCopyMatching(attributes as CFDictionary, &item)
    }
}

/// The accounts this phone is set up on (`docs/MULTI_ACCOUNT.md` §4). One
/// phone may be on several Linx servers at once — a live system and a
/// staging one, two companies — and each is a separate enrolment with its
/// own key, its own certificate and its own line. This is the list of them.
struct PhoneAccounts: Codable, Equatable, Sendable {
    /// Each account's id: the device id Linx issued, which is already
    /// unique across servers. Oldest first, which is the order they show in.
    private(set) var ids: [UUID] = []
    /// The account a call is made from unless another is chosen.
    private var preferred: UUID?
    /// Names the person gave their accounts ("Work"), by account id. An
    /// account with no name of its own is shown by its person and server.
    private var names: [String: String] = [:]

    /// The most accounts one phone may hold (§2): more open lines than this
    /// is a battery bill, and nobody has four phone systems.
    static let most = 4

    var isEmpty: Bool { ids.isEmpty }
    var isFull: Bool { ids.count >= Self.most }

    /// Which account a new call is made from: the chosen one while it is
    /// still here, else the first.
    var current: UUID? {
        if let preferred, ids.contains(preferred) { return preferred }
        return ids.first
    }

    func name(for id: UUID) -> String? { names[Self.key(id)] }

    mutating func add(_ id: UUID) {
        guard !ids.contains(id) else { return }
        ids.append(id)
    }

    mutating func remove(_ id: UUID) {
        ids.removeAll { $0 == id }
        names[Self.key(id)] = nil
        if preferred == id { preferred = nil }
    }

    /// prefer picks the account a call is made from; an id this phone
    /// doesn't hold is ignored.
    mutating func prefer(_ id: UUID) {
        guard ids.contains(id) else { return }
        preferred = id
    }

    /// name gives an account the name the person typed; an empty one takes
    /// the name away again.
    mutating func name(_ name: String, for id: UUID) {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        names[Self.key(id)] = trimmed.isEmpty ? nil : trimmed
    }

    private static func key(_ id: UUID) -> String { id.uuidString.lowercased() }
}

/// Where the things a set-up phone keeps are kept: the list of accounts,
/// and for each of them the certificate and the key blob that points at its
/// key in the Secure Enclave. Each account's two slots are its own: removing
/// one account leaves every other account untouched.
enum PhoneStore {
    /// The list itself (`PhoneAccounts`).
    static let accountsAccount = "accounts"
    /// The two slots a phone set up before there could be more than one
    /// account. They are moved into an account the first time this version
    /// runs, and then removed (`docs/MULTI_ACCOUNT.md` §4).
    static let oldIdentityAccount = "identity"
    static let oldKeyAccount = "device-key"

    static func identityAccount(_ id: UUID) -> String { oldIdentityAccount + "." + slug(id) }
    static func keyAccount(_ id: UUID) -> String { oldKeyAccount + "." + slug(id) }

    private static func slug(_ id: UUID) -> String { id.uuidString.lowercased() }

    /// accounts is the list, oldest first, moving a phone set up by an older
    /// app into it on the way — once, and never again afterwards.
    ///
    /// A Keychain that can't be read yet — a phone woken by a call before it
    /// has been unlocked since it started — is left completely alone:
    /// nothing is moved, nothing is written and nothing is deleted, because
    /// "I can't read it" must never be taken for "there is nothing there".
    static func accounts(in keychain: some PhoneStorage = Keychain.shared) -> PhoneAccounts {
        if let data = keychain.read(accountsAccount),
            let list = try? JSONDecoder().decode(PhoneAccounts.self, from: data)
        {
            return list
        }
        return moveTheOldSlotsIn(keychain)
    }

    private static func moveTheOldSlotsIn(_ keychain: some PhoneStorage) -> PhoneAccounts {
        // A locked Keychain reads as nothing, so this leaves at once and
        // touches none of it.
        guard let identityData = keychain.read(oldIdentityAccount),
            let identity = try? JSONDecoder().decode(PhoneIdentity.self, from: identityData)
        else {
            return PhoneAccounts()
        }
        let id = identity.deviceID
        keychain.save(identityData, as: identityAccount(id))
        if let keyData = keychain.read(oldKeyAccount) {
            keychain.save(keyData, as: keyAccount(id))
        }
        var list = PhoneAccounts()
        list.add(id)
        save(list, to: keychain)
        keychain.delete(oldIdentityAccount)
        keychain.delete(oldKeyAccount)
        return list
    }

    static func save(_ accounts: PhoneAccounts, to keychain: some PhoneStorage = Keychain.shared) {
        if let data = try? JSONEncoder().encode(accounts) {
            keychain.save(data, as: accountsAccount)
        }
    }

    /// load reads one account. Both halves have to be there: a certificate
    /// without its key is no use, and the phone is set up again.
    static func load(_ id: UUID, from keychain: some PhoneStorage = Keychain.shared) -> (PhoneIdentity, DeviceKey)? {
        guard let identityData = keychain.read(identityAccount(id)),
            let identity = try? JSONDecoder().decode(PhoneIdentity.self, from: identityData),
            let keyData = keychain.read(keyAccount(id)),
            let key = try? DeviceKey.restore(kind: identity.keyKind, from: keyData)
        else {
            return nil
        }
        return (identity, key)
    }

    /// load with no account named is the one a call is made from.
    static func load(from keychain: some PhoneStorage = Keychain.shared) -> (PhoneIdentity, DeviceKey)? {
        guard let id = accounts(in: keychain).current else { return nil }
        return load(id, from: keychain)
    }

    /// How a read of an account's two slots went, for the sign-in trail
    /// (`SignInTrail`): found, not there, or locked. With no account named
    /// it is the old slots, which is what a phone that has never been set
    /// up — or one that can't be read yet — has.
    static func status(_ id: UUID?, in keychain: some PhoneStorage = Keychain.shared)
        -> (identity: OSStatus, key: OSStatus)
    {
        guard let id else {
            // No account to name, which has two very different causes. The
            // list itself is asked first: on a phone that has already been
            // moved to accounts the old slots are gone, so they would read
            // as "nothing there" even while the Keychain is locked, and the
            // app would ask to be set up again instead of waiting for the
            // unlock (found 2026-10-19, building step 2).
            let list = keychain.status(accountsAccount)
            if list == errSecInteractionNotAllowed { return (list, list) }
            return (keychain.status(oldIdentityAccount), keychain.status(oldKeyAccount))
        }
        return (keychain.status(identityAccount(id)), keychain.status(keyAccount(id)))
    }

    /// save keeps an account's certificate, and its key the one time there
    /// is one, adding it to the list if it is new.
    static func save(_ identity: PhoneIdentity, key: DeviceKey?, to keychain: some PhoneStorage = Keychain.shared) {
        let id = identity.deviceID
        if let data = try? JSONEncoder().encode(identity) {
            keychain.save(data, as: identityAccount(id))
        }
        if let key {
            keychain.save(key.stored, as: keyAccount(id))
        }
        var list = accounts(in: keychain)
        guard !list.ids.contains(id) else { return }
        list.add(id)
        save(list, to: keychain)
    }

    /// forget is "sign out of this account": its certificate and its key go,
    /// and it leaves the list. Every other account stays exactly as it was.
    /// The phone itself is stopped at Linx's end separately, which is what
    /// makes it safe for a phone someone has lost.
    static func forget(_ id: UUID, from keychain: some PhoneStorage = Keychain.shared) {
        keychain.delete(identityAccount(id))
        keychain.delete(keyAccount(id))
        var list = accounts(in: keychain)
        list.remove(id)
        save(list, to: keychain)
    }

    /// forget with no account named clears what a phone set up by an older
    /// app left behind, for a sign-out that happens before the list has been
    /// read (nothing else writes those two slots any more).
    static func forget(_ keychain: some PhoneStorage = Keychain.shared) {
        keychain.delete(oldIdentityAccount)
        keychain.delete(oldKeyAccount)
    }
}
