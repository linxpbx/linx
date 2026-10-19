import Foundation

// Being signed in, from the app's side (docs/PHASE2.md §4). There is no
// password and no session cookie: the phone signs a one-minute proof with the
// key in its Secure Enclave and gets a 15-minute token back, and does that
// again whenever the token is nearly up. Every time it is in touch, Linx also
// pushes the six-month "set this phone up again" date further out, and the
// certificate is renewed with a month to spare, so a phone in use never
// notices either.

/// The phone's side of being set up and signed in. An actor, because the app
/// asks for a token from several places at once and only one of those asks
/// should reach Linx.
actor PhoneSession {
    private(set) var identity: PhoneIdentity?
    private var key: DeviceKey?
    private var token: DeviceToken?
    /// The SIP login, kept in memory only and never written anywhere.
    private(set) var line: PhoneLine?

    /// Which of this phone's accounts this session is (`docs/MULTI_ACCOUNT.md`
    /// §3): a phone on two Linx servers has a session for each. Nil until it
    /// is restored or set up, and then the account it holds.
    private(set) var account: UUID?

    private let keychain: any PhoneStorage
    private let makeClient: @Sendable (URL) -> LinxClient
    private let now: @Sendable () -> Date

    init(
        account: UUID? = nil,
        keychain: any PhoneStorage = Keychain.shared,
        client: @escaping @Sendable (URL) -> LinxClient = { LinxClient(server: $0) },
        now: @escaping @Sendable () -> Date = { Date() }
    ) {
        self.account = account
        self.keychain = keychain
        self.makeClient = client
        self.now = now
    }

    /// How restoring went, when nothing was restored.
    struct Missing: Sendable {
        var identity: OSStatus
        var key: OSStatus
        /// The phone hasn't been unlocked since it started, so the Keychain
        /// can't be read yet: it is worth waiting, not setting up again.
        var locked: Bool { identity == errSecInteractionNotAllowed || key == errSecInteractionNotAllowed }
        var why: String {
            if identity == errSecSuccess && key == errSecSuccess { return "both there, but one wouldn't load" }
            return "sign-in \(SignInTrail.words(identity)), key \(SignInTrail.words(key))"
        }
    }

    /// restore picks up where the app left off, if this phone has been set
    /// up, and says what it found when it couldn't.
    @discardableResult
    func restore() -> Missing? {
        // The account this session was made for, or — for the first one, as
        // the app starts — whichever account a call is made from.
        let id = account ?? PhoneStore.accounts(in: keychain).current
        guard let id, let (identity, key) = PhoneStore.load(id, from: keychain) else {
            let how = PhoneStore.status(id, in: keychain)
            return Missing(identity: how.identity, key: how.key)
        }
        self.account = id
        self.identity = identity
        self.key = key
        return nil
    }

    /// setUp is the one-time part: make the key, ask for a certificate with
    /// the setup code, and keep both. The code can only be used once, so
    /// anything that goes wrong here means asking for a new one.
    func setUp(with code: SetupCode) async throws -> PhoneIdentity {
        let key = try DeviceKey.make()
        let csr = try CertificateRequest.der(
            commonName: Self.certificateName, publicKeySPKI: key.publicKeySPKI,
            sign: { try key.derSignature(over: $0) })
        let enrolled = try await makeClient(code.server).enroll(
            code: code, csr: csr, osVersion: Self.osVersion)
        let identity = PhoneIdentity(
            server: code.server, deviceID: enrolled.deviceID, deviceName: enrolled.deviceName,
            personName: enrolled.personName, extensionNumber: enrolled.extensionNumber,
            certificate: enrolled.certificate, ca: enrolled.ca, certNotAfter: enrolled.certNotAfter,
            setUpAgain: enrolled.setUpAgain, keyKind: key.kind)
        PhoneStore.save(identity, key: key, to: keychain)
        self.account = identity.deviceID
        self.identity = identity
        self.key = key
        self.token = nil
        return identity
    }

    /// accessToken is the token every other call carries. The one in hand is
    /// reused until it is nearly up.
    func accessToken() async throws -> String {
        if let token, token.expiresAt.timeIntervalSince(now()) > 60 {
            return token.token
        }
        // One request at a time: everything that wants a token while one is
        // on its way waits for that one. Two at once, each renewing the
        // certificate, left the phone holding one Linx no longer knew, and
        // Linx asked for it to be set up again (2026-10-13).
        if let inFlight { return try await inFlight.value }
        let request = Task { try await self.fetchToken() }
        inFlight = request
        defer { inFlight = nil }
        return try await request.value
    }

    private var inFlight: Task<String, Error>?

    private func fetchToken() async throws -> String {
        guard var identity = identity, let key, let certificate = identity.certificateDER else {
            throw LinxError.server(
                status: 401, code: "device_inactive",
                detail: "This phone isn't set up yet. Scan a setup code to start.")
        }
        let proof = try Proof.make(device: identity.deviceID.uuidString.lowercased(), key: key, now: now())
        // The certificate is renewed for the same key, in the same breath,
        // once it has less than a month left. A new key would mean a new
        // phone, so the key never changes here.
        var renewal: Data?
        if identity.renewSoon(now: now()) {
            renewal = try CertificateRequest.der(
                commonName: Self.certificateName, publicKeySPKI: key.publicKeySPKI,
                sign: { try key.derSignature(over: $0) })
        }
        let answer = try await makeClient(identity.server).deviceToken(
            certificate: certificate, proof: proof, renewalCSR: renewal, osVersion: Self.osVersion)
        if let renewed = answer.certificate {
            identity.certificate = renewed
        }
        identity.certNotAfter = answer.certNotAfter
        identity.setUpAgain = answer.setUpAgain
        PhoneStore.save(identity, key: nil, to: keychain)
        self.identity = identity
        self.token = answer
        return answer.token
    }

    /// phoneLine asks for this phone's SIP login, which the app needs before
    /// it can make or take a call. The password stays in memory: a restarted
    /// app asks again and gets a new one.
    func phoneLine() async throws -> PhoneLine {
        guard let identity else {
            throw LinxError.server(
                status: 401, code: "device_inactive", detail: "This phone isn't set up yet.")
        }
        let client = makeClient(identity.server)
        do {
            let line = try await client.phoneLine(token: try await accessToken())
            self.line = line
            return line
        } catch LinxError.server(401, "auth_invalid", _) {
            // The token in hand was refused: usually because this phone was
            // stopped. Only a new token can say so ("set it up again"), so
            // ask for one rather than retrying the refused one for ever
            // (owner, 2026-10-18: an iPad marked lost sat on "No phone line"
            // until the app was restarted).
            token = nil
            let line = try await client.phoneLine(token: try await accessToken())
            self.line = line
            return line
        }
    }

    /// forget is "sign out of this phone", locally: the certificate and the
    /// key go, and nothing of this Linx is left on the phone.
    func forget() {
        // This account alone: every other account on this phone stays.
        if let account {
            PhoneStore.forget(account, from: keychain)
        } else {
            PhoneStore.forget(keychain)
        }
        account = nil
        identity = nil
        key = nil
        token = nil
        line = nil
    }

    /// certificateName is the only name Linx will ever sign (the server's
    /// enroll.CertName): a phone is told apart by its key, not by a name.
    static let certificateName = "linx-phone"

    /// What the app tells Linx about the phone, for the admin's list.
    static var osVersion: String {
        let version = ProcessInfo.processInfo.operatingSystemVersion
        return "\(version.majorVersion).\(version.minorVersion).\(version.patchVersion)"
    }
}
