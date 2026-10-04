import Foundation

// Everything the app says to Linx in this slice (docs/PHASE2.md §4, build
// step 4), and nothing else:
//
//   POST /v1/enroll          the setup code and a certificate request
//   POST /v1/device-token    the certificate and a signed proof → a token
//   POST /api/v1/me/phone-line   that token → this phone's SIP login
//   POST /api/v1/me/phone-push   where Apple can reach this phone
//
// Always HTTPS, always with the certificate checked (Linx never talks
// plaintext, and the app never turns verification off). Everything is small,
// and nothing is cached.

/// What went wrong, in words the screens can show as they are.
enum LinxError: Error, Equatable {
    /// Linx answered with a problem document (RFC 9457).
    case server(status: Int, code: String, detail: String)
    /// Linx couldn't be reached at all.
    case unreachable(String)
    /// Linx answered something this app can't read.
    case unexpected

    var words: String {
        switch self {
        case .server(_, _, let detail): return detail
        case .unreachable(let why):
            return "Linx couldn't be reached (\(why)). Check the phone's internet connection and the address."
        case .unexpected: return "Linx answered something this app didn't understand. Try again."
        }
    }

    /// setUpAgain is true when Linx says this phone is no longer set up, so
    /// the only way on is a new setup code.
    var setUpAgain: Bool {
        if case .server(_, let code, _) = self {
            return code == "device_proof_invalid" || code == "device_inactive"
        }
        return false
    }
}

/// What a phone gets when it is set up.
struct Enrolled: Decodable, Sendable {
    let deviceID: UUID
    let deviceName: String
    let personName: String
    let extensionNumber: String
    let certificate: String
    let ca: String
    let certNotAfter: Date
    let setUpAgain: Date

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case deviceName = "device_name"
        case personName = "person_name"
        case extensionNumber = "extension"
        case certificate
        case ca
        case certNotAfter = "cert_not_after"
        case setUpAgain = "set_up_again"
    }
}

/// A 15-minute token, and a renewed certificate when the app asked for one.
struct DeviceToken: Decodable, Sendable {
    let token: String
    let expiresAt: Date
    let deviceID: UUID
    let certificate: String?
    let certNotAfter: Date
    let setUpAgain: Date

    enum CodingKeys: String, CodingKey {
        case token
        case expiresAt = "expires_at"
        case deviceID = "device_id"
        case certificate
        case certNotAfter = "cert_not_after"
        case setUpAgain = "set_up_again"
    }
}

/// This phone's SIP login. The password is never written down: it lives in
/// memory until the app stops, and the app asks for a new one next time.
struct PhoneLine: Decodable, Sendable {
    let deviceID: UUID
    let deviceName: String
    let sipUsername: String
    let password: String
    let sipURI: String
    let websocketPath: String
    let displayName: String
    let extensionNumber: String
    let turn: Turn

    struct Turn: Decodable, Sendable {
        let urls: [String]
        let username: String
        let credential: String
        let expiresAt: Date

        enum CodingKeys: String, CodingKey {
            case urls, username, credential
            case expiresAt = "expires_at"
        }
    }

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case deviceName = "device_name"
        case sipUsername = "sip_username"
        case password
        case sipURI = "sip_uri"
        case websocketPath = "websocket_path"
        case displayName = "display_name"
        case extensionNumber = "extension"
        case turn
    }
}

/// The app's side of the Linx API.
struct LinxClient: Sendable {
    let server: URL
    let session: URLSession

    init(server: URL, session: URLSession = .linx) {
        self.server = server
        self.session = session
    }

    /// What the app tells Linx about itself, so an admin's list can show it.
    static let appVersion =
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"

    func enroll(code: SetupCode, csr: Data, osVersion: String) async throws -> Enrolled {
        var body: [String: String] = [
            "csr": csr.base64EncodedString(),
            "app_version": Self.appVersion, "os_version": osVersion,
        ]
        if let token = code.token { body["token"] = token }
        if let typed = code.code { body["code"] = typed }
        return try await post("/v1/enroll", body: body)
    }

    func deviceToken(certificate: Data, proof: String, renewalCSR: Data?, osVersion: String) async throws
        -> DeviceToken
    {
        var body: [String: String] = [
            "certificate": certificate.base64EncodedString(), "proof": proof,
            "app_version": Self.appVersion, "os_version": osVersion,
        ]
        if let csr = renewalCSR { body["csr"] = csr.base64EncodedString() }
        return try await post("/v1/device-token", body: body)
    }

    func phoneLine(token: String) async throws -> PhoneLine {
        try await post("/api/v1/me/phone-line", body: nil, bearer: token)
    }

    /// Where Apple can reach this phone: the PushKit token for calls and,
    /// once the person allows notifications, the one for a missed call or a
    /// new voicemail (docs/PHASE2.md §5). Linx answers 204, so there is
    /// nothing to read back.
    func setPhonePush(_ tokens: PushTokens, token: String) async throws {
        _ = try await send(
            "/api/v1/me/phone-push",
            body: [
                "voip_token": tokens.voip, "alert_token": tokens.alert,
                "environment": tokens.environment, "call_alerts": tokens.callAlerts,
            ], bearer: token)
    }

    // MARK: - One request

    private func post<T: Decodable>(_ path: String, body: [String: String]?, bearer: String? = nil) async throws
        -> T
    {
        let data = try await send(path, body: body, bearer: bearer)
        do {
            return try JSONDecoder.linx.decode(T.self, from: data)
        } catch {
            throw LinxError.unexpected
        }
    }

    private func send(_ path: String, body: [String: Any]?, bearer: String?) async throws -> Data {
        var request = URLRequest(url: server.appending(path: path))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bearer { request.setValue("Bearer " + bearer, forHTTPHeaderField: "Authorization") }
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw LinxError.unreachable((error as NSError).localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else { throw LinxError.unexpected }
        guard (200..<300).contains(http.statusCode) else { throw problem(data, status: http.statusCode) }
        return data
    }

    /// problem reads Linx's answer to something that didn't work. Linx always
    /// says what to do about it in plain words, so the app shows that.
    private func problem(_ data: Data, status: Int) -> LinxError {
        struct Problem: Decodable {
            let code: String?
            let detail: String?
            let title: String?
        }
        let problem = try? JSONDecoder().decode(Problem.self, from: data)
        let detail = problem?.detail ?? problem?.title ?? "Linx refused that (\(status))."
        return .server(status: status, code: problem?.code ?? "", detail: detail)
    }
}

extension URLSession {
    /// The app's one session: nothing cached, no cookies, and a short wait
    /// before it gives up, because every one of these calls is small.
    static let linx: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 15
        config.waitsForConnectivity = false
        config.httpCookieAcceptPolicy = .never
        config.httpShouldSetCookies = false
        config.urlCache = nil
        return URLSession(configuration: config)
    }()
}

extension JSONDecoder {
    /// A decoder that reads Linx's times. It is made fresh each time: there
    /// are only a handful of these calls in the app's whole life.
    static var linx: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            guard let date = LinxTime.parse(text) else {
                throw DecodingError.dataCorrupted(
                    .init(codingPath: decoder.codingPath, debugDescription: "not a time: \(text)"))
            }
            return date
        }
        return decoder
    }
}

/// Linx writes times the way Go does: RFC 3339, in UTC, sometimes with
/// nanoseconds ("2026-10-03T12:00:00.123456789Z"). Only whole seconds matter
/// to the app, so the fraction is dropped before parsing.
enum LinxTime {
    static func parse(_ text: String) -> Date? {
        var trimmed = text
        if let dot = trimmed.firstIndex(of: ".") {
            let end =
                trimmed[trimmed.index(after: dot)...].firstIndex(where: { !$0.isNumber }) ?? trimmed.endIndex
            trimmed.removeSubrange(dot..<end)
        }
        return try? Date(trimmed, strategy: .iso8601)
    }
}
