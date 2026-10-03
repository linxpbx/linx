import Foundation
import Testing

@testable import Linx

// The whole chain the app walks when it starts (docs/PHASE2.md §4), against a
// stand-in Linx that answers exactly what the real one answers (the shapes
// come from services/control-plane/enroll.go and api/webphone.go): a setup
// code becomes a certificate, the certificate and a proof become a token, and
// the token becomes this phone's SIP login. Nothing here touches a network.

/// What the phone keeps, in memory for the test.
final class FakeStore: PhoneStorage, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]

    @discardableResult func save(_ data: Data, as account: String) -> Bool {
        lock.withLock { items[account] = data }
        return true
    }

    func read(_ account: String) -> Data? { lock.withLock { items[account] } }

    func delete(_ account: String) { lock.withLock { items[account] = nil } }
}

/// A stand-in Linx inside the test process.
final class FakeLinx: URLProtocol, @unchecked Sendable {
    /// What each path answers, by path. Set before a test runs.
    nonisolated(unsafe) static var answers: [String: (status: Int, body: String)] = [:]
    /// What the app asked for, in order, as (path, body, bearer token).
    nonisolated(unsafe) static var asked: [(path: String, body: [String: Any], bearer: String?)] = []

    static func reset() {
        answers = [:]
        asked = []
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url?.path ?? ""
        var body: [String: Any] = [:]
        // URLProtocol hides the body on the request itself; the stream is
        // where it is.
        if let stream = request.httpBodyStream {
            stream.open()
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let read = stream.read(&buffer, maxLength: buffer.count)
                if read <= 0 { break }
                data.append(buffer, count: read)
            }
            stream.close()
            body = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        }
        let bearer = request.value(forHTTPHeaderField: "Authorization")?
            .replacingOccurrences(of: "Bearer ", with: "")
        Self.asked.append((path, body, bearer))

        let answer = Self.answers[path] ?? (404, #"{"code":"not_found","detail":"no such path"}"#)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: answer.status, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(answer.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    /// session is a URLSession that talks only to this.
    static var session: URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FakeLinx.self]
        return URLSession(configuration: config)
    }
}

/// Serialized: both tests talk to the one stand-in Linx, which keeps what it
/// was asked in a single list.
@Suite(.serialized) struct PhoneSessionTests {
    private static let deviceID = "0199c0de-0000-7000-8000-0000000000aa"
    private static let certificate = """
        -----BEGIN CERTIFICATE-----
        MIIBlzCCATygAwIBAgIUNtjcSNky5xoBCQzAlHFb9AL5ncQwCgYIKoZIzj0EAwIw
        FTETMBEGA1UEAwwKbGlueC1waG9uZTAeFw0yNjEwMDMxNzQxNTNaFw0yNzA0MDQx
        NzQxNTNaMBUxEzARBgNVBAMMCmxpbngtcGhvbmUwWTATBgcqhkjOPQIBBggqhkjO
        PQMBBwNCAASQpC3dygoPrxCVLAdYxIKt+VD1TosU1WA5bdwNWAz/+Ed2gYpMAkqU
        /QldfaF9M6UtoYhMQW2TrJb61CzuXhZco2owaDAdBgNVHQ4EFgQUH1ng9tNzWFc0
        w4C7eD3Rbf0YIgUwHwYDVR0jBBgwFoAUH1ng9tNzWFc0w4C7eD3Rbf0YIgUwDwYD
        VR0TAQH/BAUwAwEB/zAVBgNVHREEDjAMggpsaW54LXBob25lMAoGCCqGSM49BAMC
        A0kAMEYCIQDljlmDkJDHjF+uePrRe5MinJERu5aA/Z2T3OPlWHFc7gIhAI8M5pQn
        JrAbagcRG9G7RWOYgeXliGjMXNiwmwE0xLAn
        -----END CERTIFICATE-----
        """

    /// A session whose Linx is the stand-in and whose store is in memory: an
    /// unsigned build in the simulator has no Keychain, so the real one is
    /// checked on a phone instead (`ios/README.md`).
    private func session(_ store: FakeStore) -> PhoneSession {
        PhoneSession(keychain: store, client: { LinxClient(server: $0, session: FakeLinx.session) })
    }

    @Test("a setup code becomes a certificate, a token and a phone line")
    func wholeChain() async throws {
        FakeLinx.reset()
        FakeLinx.answers["/v1/enroll"] = (
            201,
            """
            {"device_id":"\(Self.deviceID)","device_name":"Sara's iPhone","person_name":"Sara Haddad",
             "extension":"101","certificate":"\(Self.certificate.replacingOccurrences(of: "\n", with: "\\n"))",
             "ca":"-----BEGIN CERTIFICATE-----\\nca\\n-----END CERTIFICATE-----\\n",
             "cert_not_after":"2027-04-03T00:00:00Z","set_up_again":"2027-04-03T00:00:00Z"}
            """
        )
        FakeLinx.answers["/v1/device-token"] = (
            200,
            """
            {"token":"a-device-token","expires_at":"2099-01-01T00:00:00Z","device_id":"\(Self.deviceID)",
             "cert_not_after":"2027-04-03T00:00:00Z","set_up_again":"2027-04-03T00:00:00Z"}
            """
        )
        FakeLinx.answers["/api/v1/me/phone-line"] = (
            200,
            """
            {"device_id":"\(Self.deviceID)","device_name":"Sara's iPhone","sip_username":"d_Ab12Cd34",
             "password":"a-password-in-memory-only","sip_uri":"sip:d_Ab12Cd34@sip.example.com",
             "websocket_path":"/sip","display_name":"Sara Haddad","extension":"101",
             "turn":{"urls":["turns:pbx.example.com:443?transport=tcp"],"username":"1791028800:sara",
             "credential":"a-relay-password","expires_at":"2026-10-03T13:00:00Z"}}
            """
        )

        let store = FakeStore()
        let phone = session(store)
        let code = try #require(SetupCode.link("https://pbx.example.com/set-up-phone#a-ticket"))

        let identity = try await phone.setUp(with: code)
        #expect(identity.personName == "Sara Haddad")
        #expect(identity.extensionNumber == "101")
        #expect(identity.deviceID.uuidString.lowercased() == Self.deviceID)
        #expect(identity.certificateDER != nil)

        let line = try await phone.phoneLine()
        #expect(line.sipUsername == "d_Ab12Cd34")
        #expect(line.password == "a-password-in-memory-only")
        #expect(line.websocketPath == "/sip")
        #expect(line.turn.urls.count == 1)

        // What it asked for, in order: a certificate request with the
        // ticket, then a proof with the certificate, then the line with the
        // token it was given.
        #expect(FakeLinx.asked.count == 3)
        #expect(FakeLinx.asked[0].path == "/v1/enroll")
        #expect(FakeLinx.asked[0].body["token"] as? String == "a-ticket")
        #expect((FakeLinx.asked[0].body["csr"] as? String)?.isEmpty == false)
        #expect(FakeLinx.asked[0].body["os_version"] != nil)
        #expect(FakeLinx.asked[1].path == "/v1/device-token")
        let proof = try #require(FakeLinx.asked[1].body["proof"] as? String)
        #expect(proof.split(separator: ".").count == 3)
        // The certificate lasts six months here, so nothing is renewed yet.
        #expect(FakeLinx.asked[1].body["csr"] == nil)
        #expect(FakeLinx.asked[2].path == "/api/v1/me/phone-line")
        #expect(FakeLinx.asked[2].bearer == "a-device-token")

        // The token in hand is reused rather than asked for again…
        _ = try await phone.phoneLine()
        #expect(FakeLinx.asked.count == 4)
        // …and what the phone keeps comes back after a restart, with no
        // setup code and no password anywhere.
        let again = session(store)
        await again.restore()
        #expect(await again.identity?.deviceID == identity.deviceID)
        #expect(await again.line?.sipUsername == nil)

        // Signing out leaves nothing behind.
        await again.forget()
        #expect(await again.identity?.deviceID == nil)
        #expect(store.read(PhoneStore.identityAccount) == nil)
        #expect(store.read(PhoneStore.keyAccount) == nil)
    }

    @Test("a phone Linx no longer knows is told to be set up again")
    func stoppedPhone() async throws {
        FakeLinx.reset()
        FakeLinx.answers["/v1/enroll"] = (
            400, #"{"code":"enrollment_invalid","detail":"This setup code can't be used any more. Ask for a new one."}"#
        )
        let phone = session(FakeStore())
        let code = try #require(SetupCode.byHand(server: "pbx.example.com", code: "ABCD2345"))
        do {
            _ = try await phone.setUp(with: code)
        } catch let error as LinxError {
            #expect(error.words.contains("Ask for a new one"))
            #expect(!error.setUpAgain)  // a code, not a phone
        }
        // The token endpoint's refusals are the ones that mean "set it up
        // again", and the app says so.
        #expect(LinxError.server(status: 401, code: "device_inactive", detail: "x").setUpAgain)
        #expect(LinxError.server(status: 401, code: "device_proof_invalid", detail: "x").setUpAgain)
    }
}
