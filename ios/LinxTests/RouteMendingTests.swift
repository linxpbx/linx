import Foundation
import Testing

@testable import Linx

// Calls whose sound stops getting through (owner, 2026-10-08): an iPhone on
// 5G with Tailscale and an exit node abroad chose a direct route through the
// Synology that carried the connectivity checks but not the encrypted sound,
// and its own lookups of the relay's name failed, so there was nothing to
// fall back to. Three things answer it, and each is checked here:
//
// - the relay is looked up by iOS and given to WebRTC by address, its
//   certificate still held to its name (`RelayAddresses`);
// - a call with no sound two seconds after the answer, or whose sound stops
//   for three, is moved onto the relay without hanging up
//   (`PhoneModel.mendIfSilent`, `SIPUserAgent.moveToRelay`);
// - the call keeps a timeline of how long setting it up took (Call details).
//
// Nothing here touches WebRTC or a network, except one lookup of
// "localhost" through the system's own resolver.

@MainActor
struct RouteMendingTests {
    // MARK: - Looking the relay up

    @Test("a relay's URLs are given by address, each server keeping its name for TLS")
    func relayByAddress() {
        let urls = [
            "turn:turn.home.example:443?transport=udp", "turns:turn.home.example:443?transport=tcp",
            "stun:turn.home.example:443",
        ]
        let servers = RelayAddresses.servers(for: urls, addresses: ["turn.home.example": ["203.0.113.7"]])
        #expect(servers.count == 1)
        #expect(servers[0].hostname == "turn.home.example")
        #expect(
            servers[0].urls == [
                "turn:203.0.113.7:443?transport=udp", "turns:203.0.113.7:443?transport=tcp",
                "stun:203.0.113.7:443",
            ])
        // The certificate check still sees the name, because it reads the
        // URLs Linx issued, not these.
        #expect(RelayCertificates.hosts(in: urls) == ["turn.home.example"])
    }

    @Test("a name nobody could look up is left for WebRTC to try itself")
    func unknownNameIsLeftAlone() {
        let urls = ["turns:turn.home.example:443?transport=tcp"]
        let servers = RelayAddresses.servers(for: urls, addresses: [:])
        #expect(servers.count == 1)
        #expect(servers[0].hostname == nil)
        #expect(servers[0].urls == urls)
    }

    @Test("an IPv6 address goes in brackets, and two addresses give two URLs")
    func ipv6AndSeveral() {
        let urls = ["turns:turn.home.example:443?transport=tcp"]
        let servers = RelayAddresses.servers(
            for: urls, addresses: ["turn.home.example": ["203.0.113.7", "2001:db8::7"]])
        #expect(
            servers[0].urls == [
                "turns:203.0.113.7:443?transport=tcp", "turns:[2001:db8::7]:443?transport=tcp",
            ])
        #expect(RelayAddresses.isAddress("2001:db8::7"))
        #expect(RelayAddresses.isAddress("203.0.113.7"))
        #expect(!RelayAddresses.isAddress("turn.home.example"))
    }

    @Test("the system's own resolver answers")
    func systemLookup() {
        #expect(RelayAddresses.systemLookup("localhost").contains("127.0.0.1"))
        #expect(RelayAddresses.systemLookup("no-such-name.invalid").isEmpty)
    }

    // MARK: - When a call is moved onto the relay

    @Test("silence after the answer, silence mid-call, and never too often")
    func theRule() {
        let answered = Date(timeIntervalSinceReferenceDate: 1000)
        func should(_ after: TimeInterval, sound: TimeInterval? = nil, moves: Int = 0, lastMove: TimeInterval? = nil)
            -> Bool
        {
            PhoneModel.shouldMoveToRelay(
                now: answered.addingTimeInterval(after), answeredAt: answered,
                soundLastArrived: sound.map { answered.addingTimeInterval($0) }, moves: moves,
                lastMove: lastMove.map { answered.addingTimeInterval($0) })
        }
        #expect(!should(1.5))  // the first reading may not be in yet
        #expect(should(2.0))  // nothing at all two seconds after the answer
        #expect(!should(10, sound: 8))  // sound two seconds ago is fine
        #expect(should(10, sound: 7))  // three seconds without it is not
        #expect(!should(30, moves: 2))  // twice a call at most
        #expect(!should(30, moves: 1, lastMove: 25))  // and ten seconds apart
        #expect(should(30, moves: 1, lastMove: 19))
    }

    /// Waits for something the app does in a Task of its own.
    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }

    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600),
                    maxBitrateBps: nil)),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    private func inACall() async throws -> (PhoneModel, FakeTransport, FakeMedia) {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media }, calls: FakeSystemCalls())
        await phone.start()
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))

        phone.callNumber("0504564177")
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:0504564177@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        return (phone, transport, media)
    }

    @Test("no sound after the answer moves the call onto the relay, once, without hanging up")
    func silentCallIsMoved() async throws {
        let (phone, transport, media) = try await inACall()
        let answeredAt = try #require(phone.call?.answeredAt)
        let invites = transport.count("INVITE")

        // A reading with nothing arriving, right after the answer: too soon.
        media.onConnection?(MediaConnection(route: .direct, roundTripMs: 28, relayProtocol: nil, audioBytesIn: 0))
        #expect(media.relayMoves == 0)

        // Still nothing two and a half seconds later: moved.
        phone.mendIfSilent(at: answeredAt.addingTimeInterval(2.5))
        #expect(await eventually { transport.count("INVITE") == invites + 1 })
        #expect(media.relayMoves == 1)
        #expect(transport.last("INVITE")?.body == FakeMedia.relayOffer)
        #expect(phone.call?.phase == .active)

        // Asked again at once: not twice in ten seconds.
        phone.mendIfSilent(at: answeredAt.addingTimeInterval(3.5))
        try await Task.sleep(for: .milliseconds(100))
        #expect(media.relayMoves == 1)
    }

    @Test("a move Asterisk turns down leaves the call as it was, and says nothing about video")
    func refusedMoveIsQuiet() async throws {
        let (phone, transport, media) = try await inACall()
        let answeredAt = try #require(phone.call?.answeredAt)
        media.onConnection?(MediaConnection(route: .direct, roundTripMs: 28, relayProtocol: nil, audioBytesIn: 0))
        phone.mendIfSilent(at: answeredAt.addingTimeInterval(2.5))
        #expect(await eventually { media.relayMoves == 1 })
        let asked = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(488, "Not Acceptable Here", to: asked).withTag("theirs"))
        #expect(await eventually { media.rolledBack })
        try await Task.sleep(for: .milliseconds(100))
        #expect(phone.call?.phase == .active)
        #expect(phone.problem == nil)
    }

    @Test("sound that is arriving leaves the call alone")
    func soundArriving() async throws {
        let (phone, _, media) = try await inACall()
        let answeredAt = try #require(phone.call?.answeredAt)
        media.onConnection?(MediaConnection(route: .direct, roundTripMs: 28, relayProtocol: nil, audioBytesIn: 4_000))
        phone.mendIfSilent(at: answeredAt.addingTimeInterval(2.5))
        try await Task.sleep(for: .milliseconds(100))
        #expect(media.relayMoves == 0)
    }

    @Test("the call's timeline: started, sent, ringing, answered")
    func timeline() async throws {
        let (_, _, media) = try await inACall()
        #expect(media.steps.first == "began")
        #expect(media.steps.contains("Call sent"))
        #expect(media.steps.contains("Ringing"))
        #expect(media.steps.contains(CallDiagnostics.answered))
        let sent = try #require(media.steps.firstIndex(of: "Call sent"))
        let ringing = try #require(media.steps.firstIndex(of: "Ringing"))
        let answered = try #require(media.steps.firstIndex(of: CallDiagnostics.answered))
        #expect(sent < ringing && ringing < answered)
    }
}
