import Foundation
import Testing

@testable import Linx

// What the Calls, Team and Voicemail screens read, against a stand-in Linx
// inside the test process (docs/PHASE2.md §12, step 7). The JSON in here is
// written exactly as the server writes it (api/openapi.yaml), so a change at
// one end that the other doesn't expect fails here rather than on somebody's
// phone.

/// A stand-in Linx of these tests' own. It is a second one on purpose: the
/// enrollment tests have theirs, and two suites sharing one would answer
/// each other's requests whenever the test runner ran them side by side.
final class FakeScreensLinx: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var answers: [String: (status: Int, body: String, type: String)] = [:]
    nonisolated(unsafe) private static var calls:
        [(path: String, body: [String: Any], bearer: String?, method: String, query: String?, type: String?)] = []

    static func answer(_ path: String, _ status: Int, _ body: String, type: String = "application/json") {
        lock.withLock { answers[path] = (status, body, type) }
    }

    /// Everything the app asked of one path, in order.
    static func asked(_ path: String) -> [(
        path: String, body: [String: Any], bearer: String?, method: String, query: String?, type: String?
    )] {
        lock.withLock { calls.filter { $0.path == path } }
    }

    static func reset() {
        lock.withLock {
            answers = [:]
            calls = []
        }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url?.path ?? ""
        var body: [String: Any] = [:]
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
        let asked = (
            path, body,
            request.value(forHTTPHeaderField: "Authorization")?.replacingOccurrences(of: "Bearer ", with: ""),
            request.httpMethod ?? "GET", request.url?.query, request.value(forHTTPHeaderField: "Content-Type")
        )
        let answer = Self.lock.withLock { () -> (status: Int, body: String, type: String) in
            Self.calls.append(asked)
            return Self.answers[path] ?? (404, #"{"code":"not_found","detail":"no such path"}"#, "application/json")
        }
        let response = HTTPURLResponse(
            url: request.url!, statusCode: answer.status, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": answer.type])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(answer.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static var session: URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FakeScreensLinx.self]
        return URLSession(configuration: config)
    }
}

@MainActor
@Suite(.serialized) struct ScreensDataTests {
    private static var access: LinxAccess {
        LinxAccess(
            client: LinxClient(server: URL(string: "https://pbx.example.com")!, session: FakeScreensLinx.session),
            server: URL(string: "https://pbx.example.com")!, token: { "a-device-token" })
    }

    @Test("the team list, as Linx sends it")
    func readsTheTeam() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer(
            "/api/v1/team", 200,
            """
            {"items":[
              {"extension":"101","name":"Sara Haddad","status":"available"},
              {"extension":"1024","name":"Omar Khalil","status":"on_call","since":"2026-10-04T09:12:00Z"},
              {"extension":"1047","name":"Chen Wei","status":"offline"}
            ],"voicemail":3,"calls":7}
            """
        )
        let list = try await Self.access.client.team(token: "a-device-token")
        #expect(list.items.count == 3)
        #expect(list.items[1].status == .onCall)
        #expect(list.items[1].words == "On a call")
        #expect(list.items[1].since != nil)
        #expect(list.voicemail == 3 && list.calls == 7)
        #expect(FakeScreensLinx.asked("/api/v1/team").first?.method == "GET")
        #expect(FakeScreensLinx.asked("/api/v1/team").first?.bearer == "a-device-token")
    }

    @Test("my status is a PUT, and nothing else")
    func setsPresence() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/me/presence", 204, "")
        try await Self.access.client.setPresence(.dnd, token: "a-device-token")
        let asked = try #require(FakeScreensLinx.asked("/api/v1/me/presence").last)
        #expect(asked.method == "PUT")
        #expect(asked.body["presence"] as? String == "dnd")
    }

    @Test("my call history, newest first, with who the other end was")
    func readsCallHistory() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer(
            "/api/v1/me/calls", 200,
            """
            {"items":[
              {"id":"0199c0de-0000-7000-8000-00000000c001","direction":"inbound",
               "started_at":"2026-10-04T08:00:00Z","ended_at":"2026-10-04T08:00:20Z","talk_seconds":0,
               "result":"missed","missed":true,"rang_unanswered":true,"placed_by_me":false,
               "from":{"number":"+97145550147","name":"Al Noor Trading"},
               "to":{"number":"101","name":"Sara Haddad"},"steps":["Rang your phones"]},
              {"id":"0199c0de-0000-7000-8000-00000000c002","direction":"outbound",
               "started_at":"2026-10-04T07:00:00Z","answered_at":"2026-10-04T07:00:05Z",
               "ended_at":"2026-10-04T07:02:19Z","talk_seconds":134,"result":"answered","missed":false,
               "rang_unanswered":false,"placed_by_me":true,
               "from":{"number":"101","name":"Sara Haddad"},"to":{"number":"1024","name":"Omar Khalil"},
               "steps":["You called 1024"]}
            ],"keep_days":365,"next":"2026-10-04T07:00:00Z"}
            """
        )
        let page = try await Self.access.client.myCalls(token: "a-device-token")
        #expect(page.items.count == 2)
        #expect(page.next == "2026-10-04T07:00:00Z")
        // The other end of each call is who the row names and who the call
        // button rings.
        #expect(page.items[0].other.name == "Al Noor Trading")
        #expect(page.items[0].outgoing == false)
        #expect(page.items[1].other.number == "1024")
        #expect(page.items[1].outgoing)
        #expect(FakeScreensLinx.asked("/api/v1/me/calls").first?.query?.contains("limit=50") == true)
    }

    @Test("the badges are this person's own counts, asked for by name")
    func readsBadges() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/me/missed-calls", 200, #"{"missed":2}"#)
        FakeScreensLinx.answer("/api/v1/me/voicemail-count", 200, #"{"new":1}"#)
        #expect(try await Self.access.client.missedCalls(token: "t") == 2)
        #expect(try await Self.access.client.newVoicemail(token: "t") == 1)
    }

    @Test("opening Calls clears the badge with a DELETE")
    func clearsTheBadge() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/me/missed-calls", 204, "")
        try await Self.access.client.clearMissedCalls(token: "t")
        #expect(FakeScreensLinx.asked("/api/v1/me/missed-calls").last?.method == "DELETE")
    }

    @Test("voicemail: the list, then one message's audio, then heard, then gone")
    func voicemail() async throws {
        FakeScreensLinx.reset()
        let id = "0199c0de-0000-7000-8000-00000000d001"
        FakeScreensLinx.answer(
            "/api/v1/voicemail", 200,
            """
            {"boxes":[{"id":"0199c0de-0000-7000-8000-00000000e001","kind":"person","owner":"Sara Haddad (101)",
              "mine":true,"member":false,"removed":false,"enabled":true,"email":false,"messages":2,"new":1,"bytes":1}],
             "items":[{"id":"\(id)","box_id":"0199c0de-0000-7000-8000-00000000e001",
              "caller_number":"+971501234567","caller_name":"","received_at":"2026-10-04T06:00:00Z",
              "duration_ms":23000,"heard_by_me":false}],
             "keep_days":90}
            """
        )
        let list = try await Self.access.client.voicemail(token: "t")
        let message = try #require(list.items.first)
        #expect(message.who == "+971501234567")
        #expect(list.boxes.first?.mine == true)

        FakeScreensLinx.answer("/api/v1/voicemail/\(id)/audio", 200, "RIFF....WAVE")

        let wav = try await Self.access.client.voicemailAudio(message.id, token: "t")
        #expect(wav.count > 0)

        FakeScreensLinx.answer("/api/v1/voicemail/\(id)", 204, "")
        try await Self.access.client.markVoicemail(message.id, heard: true, token: "t")
        let patch = try #require(FakeScreensLinx.asked("/api/v1/voicemail/\(id)").last)
        #expect(patch.method == "PATCH")
        // Every change in the Linx API is a JSON Merge Patch, and the
        // server takes it as nothing else.
        #expect(patch.type == "application/merge-patch+json")
        #expect(patch.body["heard"] as? Bool == true)

        try await Self.access.client.deleteVoicemail(message.id, token: "t")
        #expect(FakeScreensLinx.asked("/api/v1/voicemail/\(id)").last?.method == "DELETE")
    }

    // Searching call history (owner, 2026-10-04). These live in this suite
    // because they use the same stand-in Linx, and two suites resetting it
    // at once would answer each other's requests.
    private static let onePage = """
        {"items":[{"id":"0199c0de-0000-7000-8000-00000000c001","direction":"inbound",
         "started_at":"2026-10-04T08:00:00Z","ended_at":"2026-10-04T08:00:20Z","talk_seconds":0,
         "result":"missed","missed":true,"rang_unanswered":true,
         "from":{"number":"+97145550147","name":"Al Noor Trading"},
         "to":{"number":"101","name":"Sara Haddad"},"steps":[]}],"keep_days":365}
        """

    @Test("a number is searched for at the server, so it finds calls off this page")
    func searchingANumber() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/me/calls", 200, Self.onePage)
        let calls = CallsModel(access: Self.access)
        await calls.load()
        await calls.search("4555 0147")
        let asked = try #require(FakeScreensLinx.asked("/api/v1/me/calls").last)
        #expect(asked.query?.contains("number=%2B") == false)
        #expect(asked.query?.contains("number=45550147") == true)
    }

    @Test("a name is not sent to Linx: there is nothing there to search by name")
    func searchingAName() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/me/calls", 200, Self.onePage)
        let calls = CallsModel(access: Self.access)
        await calls.load()
        let before = FakeScreensLinx.asked("/api/v1/me/calls").count
        await calls.search("Al Noor")
        #expect(FakeScreensLinx.asked("/api/v1/me/calls").count == before)
    }

    @Test("Linx's own words are what the screen shows when something goes wrong")
    func problemsAreShownAsTheyAre() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer(
            "/api/v1/team", 403, #"{"code":"insufficient_scope","detail":"This needs the team:read scope."}"#
        )
        let home = HomeModel(access: Self.access, watching: false)
        await home.loadOnce()
        #expect(home.problem == "This needs the team:read scope.")
    }

    @Test("the tab badges come from Linx, and opening Calls clears the one on Calls")
    func badgesAndClearing() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer("/api/v1/team", 200, #"{"items":[]}"#)
        FakeScreensLinx.answer("/api/v1/me/missed-calls", 200, #"{"missed":4}"#)
        FakeScreensLinx.answer("/api/v1/me/voicemail-count", 200, #"{"new":2}"#)
        let home = HomeModel(access: Self.access, watching: false)
        await home.refreshBadges()
        #expect(home.missedCalls == 4)
        #expect(home.newVoicemail == 2)

        FakeScreensLinx.answer("/api/v1/me/missed-calls", 204, "")
        await home.openedCalls()
        #expect(home.missedCalls == 0)
        #expect(FakeScreensLinx.asked("/api/v1/me/missed-calls").last?.method == "DELETE")
    }

    @Test("a status that Linx refuses goes back to what it was")
    func presenceThatDidntTake() async throws {
        FakeScreensLinx.reset()
        FakeScreensLinx.answer(
            "/api/v1/me/presence", 400, #"{"code":"presence_invalid","detail":"Choose available, away or dnd."}"#
        )
        let home = HomeModel(access: Self.access, watching: false)
        await home.setPresence(.dnd)
        #expect(home.presence == .available)
        #expect(home.problem == "Choose available, away or dnd.")
    }
}
