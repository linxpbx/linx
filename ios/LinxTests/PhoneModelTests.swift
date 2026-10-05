import Foundation
import Testing

@testable import Linx

// The phone as the screens see it: the line comes up, a call goes out and
// comes back, and the last few calls are remembered.

@MainActor
struct PhoneModelTests {
    private static func line(path: String = "/sip") -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: path, displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600),
                    maxBitrateBps: 1_280_000)),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    @Test("the websocket is always wss, on this phone's own Linx")
    func buildsTheAccount() throws {
        let account = try #require(PhoneModel.account(for: Self.line()))
        #expect(account.websocket.absoluteString == "wss://pbx.example.com/sip")
        #expect(account.domain == "sip.example.com")
        #expect(account.username == "d_Ab12Cd34")
        #expect(account.token == "a-device-token")
        // A server behind a front door on another port keeps it.
        let other = PhoneModel.account(for: Self.line(path: "/sip?x=1"))
        #expect(other?.websocket.absoluteString == "wss://pbx.example.com/sip?x=1")
    }

    @Test("a call goes out, is answered, and ends up in the recent list")
    func aCallOutAndBack() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media })
        await phone.start()

        // Asterisk signs the line in.
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        #expect(phone.status == .ready)

        phone.typed = "1024"
        phone.dial()
        #expect(phone.typed.isEmpty)
        #expect(phone.call?.phase == .calling)
        #expect(await eventually { transport.last("INVITE") != nil })

        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(phone.call?.ringingThere == true)

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = "v=0\r\ntheir-answer\r\n"
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })

        phone.toggleMute()
        #expect(phone.call?.muted == true)
        #expect(media.muted)

        phone.hangUp()
        #expect(phone.call == nil)
        #expect(phone.recent.count == 1)
        #expect(phone.recent.first?.kind == .outgoing)
        #expect(phone.recent.first?.peer.number == "1024")
    }

    @Test("the test-sound call isn't kept in the recent list")
    func echoTestIsNotACall() async throws {
        let transport = FakeTransport()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in FakeMedia() })
        await phone.start()
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))

        phone.callNumber(PhoneModel.echoTest)
        #expect(phone.call?.peer.name == "Test sound")
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        #expect(invite.requestURI == "sip:*43@sip.example.com")
        transport.asterisk(SIPMessage.response(486, "Busy Here", to: invite).withTag("x"))
        #expect(phone.call == nil)
        #expect(phone.recent.isEmpty)
        #expect(phone.problem == "They're on another call.")
    }

    @Test("no line, and the phone says so instead of pretending")
    func noLine() async {
        let phone = PhoneModel(line: { nil }, transport: { _ in FakeTransport() }, media: { _ in FakeMedia() })
        await phone.start()
        if case .unavailable(let why) = phone.status {
            #expect(why.contains("try again"))
        } else {
            Issue.record("the phone should say it has no line")
        }
        phone.callNumber("1024")
        #expect(phone.call == nil)
        phone.stop()
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }
}
