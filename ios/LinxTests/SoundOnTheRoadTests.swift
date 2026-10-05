import AVFoundation
import Foundation
import Testing
@preconcurrency import WebRTC

@testable import Linx

// What a real iPhone found that a simulator never could (owner, 2026-10-05,
// `docs/TEST_MATRIX.md`): AirPods connected in the middle of a call, a camera
// that took the sound away with it, a picture that stopped and could never
// come back, a video button that looked dead while it worked, and a call that
// connected on a mobile network and carried no sound at all.
//
// Nothing here touches a network, a camera or a microphone.

@MainActor
struct SoundOnTheRoadTests {
    // MARK: - The camera and the sound of the call

    @Test("the camera borrows the call's audio session and never configures it")
    func theCameraLeavesTheSoundAlone() {
        let camera = Camera(factory: RTCPeerConnectionFactory())
        // WebRTC's own capturer gives its capture session a session of its
        // own, and starting one of those takes the sound hardware off the
        // call: the owner's AirPods went the moment the camera came on.
        #expect(camera.usesTheCallsAudioSession)
    }

    // MARK: - Who says the relay's certificate is good

    @Test("the relay is trusted by iOS, for the hostname Linx named, or not at all")
    func relayCertificates() {
        // The hostnames come out of the URLs Linx issued, which are not
        // ordinary URLs: turns:host:port?transport=tcp has no "//".
        #expect(
            RelayCertificates.hosts(in: [
                "turn:turn.example.com:443?transport=udp",
                "turns:turn.example.com:443?transport=tcp",
                "stun:stun.example.com:3478",
                "turns:[2001:db8::1]:443?transport=tcp",
                "turn:relay.example.net",
                "https://example.com",
            ]) == ["turn.example.com", "stun.example.com", "2001:db8::1", "relay.example.net"])
        // Nothing that isn't a certificate is let through, and neither is
        // anything at all when Linx named no relay.
        #expect(!RelayCertificates(relay: nil).verify(Data([1, 2, 3])))
        let relay = PhoneLine.Turn(
            urls: ["turns:turn.example.com:443?transport=tcp"], username: "u", credential: "c",
            expiresAt: Date(timeIntervalSinceNow: 3600))
        #expect(!RelayCertificates(relay: relay).verify(Data()))
        #expect(!RelayCertificates(relay: relay).verify(Data(repeating: 0x30, count: 64)))
    }

    // MARK: - Their picture coming and going

    @Test("a picture that stops arriving comes back when it starts again")
    func theirPictureComesBack() {
        var watch = PictureWatch()
        // It is announced before any frame has arrived, which is the ordinary
        // way round: nothing is said until frames stop coming.
        #expect(watch.reading(framesIn: 0, showing: true) == nil)
        #expect(watch.reading(framesIn: 0, showing: true) == false)
        // The echo test's picture starts the moment this phone's camera does.
        #expect(watch.reading(framesIn: 12, showing: false) == true)
        #expect(watch.reading(framesIn: 30, showing: true) == nil)
        // It stops. One quiet reading is a hiccup; two mean it has gone.
        #expect(watch.reading(framesIn: 30, showing: true) == nil)
        #expect(watch.reading(framesIn: 30, showing: true) == false)
        // And it comes back, which is the half that was missing.
        #expect(watch.reading(framesIn: 31, showing: false) == true)
    }

    @Test("their camera is asked about once in a call, however often it comes and goes")
    func askedOnlyOnce() async throws {
        let (phone, _, media, _) = try await inACall()
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.askAboutTheirVideo != nil })
        phone.answeredAboutTheirVideo(turningMineOn: false)
        // A thin link takes the picture away and brings it back; the question
        // is not asked again.
        media.pretendTheirVideo(false)
        media.pretendTheirVideo(true)
        #expect(phone.askAboutTheirVideo == nil)
    }

    // MARK: - Where the sound comes out

    @Test("AirPods connected during a call are a choice there and then")
    func airPodsMidCall() async throws {
        let (phone, _, media, _) = try await inACall()
        // Nothing else connected: the plain Speaker switch.
        media.soundComesOut(of: AudioRoute(name: "Speaker", speaker: false, builtIn: true))
        #expect(phone.whereTheSoundGoes == nil)
        // AirPods paired in the middle of the call, the sound still in the
        // phone: the button becomes the system's picker all the same, so they
        // can be chosen without making another call.
        media.soundComesOut(
            of: AudioRoute(name: "Speaker", speaker: false, builtIn: true, others: true))
        #expect(phone.whereTheSoundGoes != nil)
        // And once the sound has moved, it wears their name.
        media.soundComesOut(
            of: AudioRoute(name: "Sara's AirPods", speaker: false, builtIn: false, others: true))
        #expect(phone.whereTheSoundGoes == "Sara's AirPods")
    }

    // MARK: - A picture going into a call takes a moment

    @Test("the video button says it is working, and one press is one press")
    func videoSaysItIsWorking() async throws {
        let (phone, transport, _, _) = try await inACall()
        let invites = transport.count("INVITE")
        phone.toggleVideo()
        #expect(phone.changingVideo)
        // A second press while the first is still in the air changes nothing.
        phone.toggleVideo()
        #expect(await eventually { !phone.changingVideo })
        #expect(transport.count("INVITE") == invites + 1)
    }

    // MARK: - A call that connects and carries no sound

    @Test("a relay that won't have this phone is said out loud")
    func theRelayRefused() async throws {
        let (phone, _, media, _) = try await inACall()
        media.found(
            CallDiagnostics(
                found: ["this network"],
                relayTrouble: [
                    CallDiagnostics.RelayTrouble(url: "turns:turn.example.com:443", code: 401, said: "Unauthorized")
                ]))
        #expect(phone.problem?.contains("relay") == true)
        #expect(phone.diagnostics.foundTheRelay == false)
    }

    @Test("a relay reached one way and not the other is nobody's business")
    func oneWayToTheRelayIsFine() async throws {
        let (phone, _, media, _) = try await inACall()
        media.found(
            CallDiagnostics(
                found: ["this network", CallDiagnostics.theRelay],
                relayTrouble: [
                    CallDiagnostics.RelayTrouble(
                        url: "turn:turn.example.com:443?transport=udp", code: 701, said: "timed out")
                ]))
        #expect(phone.problem == nil)
        #expect(phone.diagnostics.foundTheRelay)
    }

    @Test("a call that has been up for a few seconds with no sound in it says so")
    func silenceIsReported() async throws {
        let (phone, _, _, _) = try await inACall()
        let peer = SIPPeer(name: "Omar Khalil", number: "1024")
        let started = Date(timeIntervalSinceNow: -30)
        // Connected, "Encrypted" on the screen, and not a byte arriving: the
        // call the owner kept getting on a mobile network.
        phone.pretend(
            PhoneModel.Call(
                peer: peer, incoming: false, phase: .active, answeredAt: started,
                connection: MediaConnection(
                    route: .relayed, roundTripMs: 60, relayProtocol: "tls", audioBytesIn: 0)))
        #expect(phone.noSoundComingIn(at: Date()))
        // Sound arriving, and the screen says nothing.
        phone.pretend(
            PhoneModel.Call(
                peer: peer, incoming: false, phase: .active, answeredAt: started,
                connection: MediaConnection(
                    route: .relayed, roundTripMs: 60, relayProtocol: "tls", audioBytesIn: 4800)))
        #expect(!phone.noSoundComingIn(at: Date()))
        // And a call that has only just been answered is given its moment.
        phone.pretend(
            PhoneModel.Call(peer: peer, incoming: false, phase: .active, answeredAt: Date()))
        #expect(!phone.noSoundComingIn(at: Date()))
    }

    @Test("the details a person can copy out name where the sound stopped")
    func detailsInWords() {
        let call = PhoneModel.Call(
            peer: SIPPeer(name: "Omar Khalil", number: "1024"), incoming: false, phase: .active,
            answeredAt: Date(),
            connection: MediaConnection(
                route: .relayed, roundTripMs: 60, relayProtocol: "tls", audioBytesIn: 0,
                audioBytesOut: 9600))
        let words = CallDetailsView.words(
            call: call,
            diagnostics: CallDiagnostics(
                found: ["this network", CallDiagnostics.theRelay], ice: "connected",
                relayURLs: ["turns:turn.example.com:443?transport=tcp"]))
        #expect(words.contains("route: relayed (tls)"))
        #expect(words.contains("sound in: 0 bytes, out: 9600 bytes"))
        #expect(words.contains("turns:turn.example.com:443?transport=tcp"))
        #expect(CallDetailsView.size(0) == "nothing")
    }

    // MARK: - A phone with a call up

    private func inACall() async throws -> (PhoneModel, FakeTransport, FakeMedia, FakeSystemCalls) {
        let transport = FakeTransport()
        let media = FakeMedia()
        let system = FakeSystemCalls()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media }, calls: system)
        await phone.start()
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        phone.callNumber("1024", name: "Omar Khalil")
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        return (phone, transport, media, system)
    }

    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: ["turns:turn.example.com:443?transport=tcp"], username: "u", credential: "c",
                    expiresAt: Date(timeIntervalSinceNow: 3600))),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }
}
