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

    // MARK: - The screen during a call

    @Test("a video call keeps the screen awake, and lets it go again")
    func theScreenStaysAwake() async throws {
        let (phone, _, _, _) = try await inACall()
        // A call with no picture in it leaves the screen to iOS: a phone at an
        // ear or in a pocket has no business keeping a screen alight.
        #expect(!phone.screenIsHeldAwake)
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.mine == true })
        // With a picture, the screen stays on until the power button says
        // otherwise (owner, 2026-10-05).
        #expect(phone.screenIsHeldAwake)
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.on == false })
        #expect(!phone.screenIsHeldAwake)
        // And the call ending always lets it go, however it ended. (One press
        // at a time: the button ignores a second while the first is still in
        // the air, which is the point of `changingVideo`.)
        #expect(await eventually { !phone.changingVideo })
        phone.toggleVideo()
        #expect(await eventually { phone.screenIsHeldAwake })
        phone.stop()
        #expect(!phone.screenIsHeldAwake)
    }

    // MARK: - Which picture has the big screen (owner, 2026-10-05)

    @Test("a tap swaps the two pictures over, and only when there are two")
    func swappingThePictures() async throws {
        let (phone, _, media, _) = try await inACall()
        // One camera on: the only picture there is has the big screen, and
        // there is nothing to swap.
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.mine == true })
        #expect(!phone.canSwapPictures)
        phone.swapPictures()
        #expect(!phone.myPictureIsBig)
        // Both cameras on: the other person has the big screen to begin with,
        // and a tap puts this phone's own picture there instead.
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.canSwapPictures })
        phone.swapPictures()
        #expect(phone.myPictureIsBig)
        phone.swapPictures()
        #expect(!phone.myPictureIsBig)
        phone.swapPictures()
        // Their picture leaving the call takes the swap with it, so the next
        // video call starts the ordinary way round.
        #expect(phone.myPictureIsBig)
        media.pretendTheirVideo(false)
        #expect(await eventually { phone.myPictureIsBig == false })
        phone.stop()
    }

    // MARK: - A picture that follows the link (ADR-081)

    @Test("the picture never asks for more than the route allows")
    func theCeiling() {
        // Straight to the other side: as good as the link can carry.
        #expect(VideoQuality.ceiling(route: .direct, relay: nil) == .hd)
        #expect(VideoQuality.ceiling(route: .direct, relay: 1_280_000) == .hd)
        // Through Linx's relay: never more than the relay will carry for one
        // call, because asking for more doesn't slow the picture down, it
        // freezes it (owner, 2026-10-05).
        #expect(VideoQuality.ceiling(route: .relayed, relay: 1_280_000) == .high)
        #expect(VideoQuality.ceiling(route: .relayed, relay: 512_000) == .low)
        #expect(VideoQuality.ceiling(route: .relayed, relay: 200_000) == .thin)
        // A server too old to say stays where every Linx relay has always been.
        #expect(VideoQuality.ceiling(route: .relayed, relay: nil) == .standard)
    }

    @Test("a picture just switched on isn't made worse while the link is still being measured")
    func theLadderSettles() {
        var ladder = VideoLadder()
        // The room on a link is measured from what is flowing on it, so for the
        // first readings after a camera comes on the measurement is still
        // catching up and reads low. Acting on it would make a good picture
        // worse the moment it appeared (owner, 2026-10-05).
        for _ in 0..<VideoLadder.settlingReadings {
            #expect(ladder.reading(spare: 50_000, ceiling: .hd) == nil)
        }
        #expect(ladder.quality == .standard)
        // After that it is believed — on the second tight reading, not the
        // first.
        #expect(ladder.reading(spare: 50_000, ceiling: .hd) == nil)
        #expect(ladder.reading(spare: 50_000, ceiling: .hd) == .low)
    }

    @Test("the picture comes down soon and goes up slowly")
    func theLadder() {
        var ladder = VideoLadder()
        #expect(ladder.quality == .standard)
        // A link with room to spare: nothing happens for a few readings, then
        // it steps up once — not twice, and not every reading.
        for _ in 0..<(VideoLadder.steadyReadings - 1) {
            #expect(ladder.reading(spare: 4_000_000, ceiling: .hd) == nil)
        }
        #expect(ladder.reading(spare: 4_000_000, ceiling: .hd) == .high)
        #expect(ladder.reading(spare: 4_000_000, ceiling: .hd) == nil)
        // The link narrows: down after two readings that say so, each step
        // judged on its own readings.
        #expect(ladder.reading(spare: 300_000, ceiling: .hd) == nil)
        #expect(ladder.reading(spare: 300_000, ceiling: .hd) == .standard)
        #expect(ladder.reading(spare: 300_000, ceiling: .hd) == nil)
        #expect(ladder.reading(spare: 300_000, ceiling: .hd) == .low)
        // The bottom step is the bottom: the camera going off altogether is
        // the other rule's job, not this one's.
        #expect(ladder.reading(spare: 10_000, ceiling: .hd) == nil)
        #expect(ladder.reading(spare: 10_000, ceiling: .hd) == .thin)
        #expect(ladder.reading(spare: 10_000, ceiling: .hd) == nil)
        #expect(ladder.reading(spare: 10_000, ceiling: .hd) == nil)
        #expect(ladder.quality == .thin)
        // A ceiling that drops — the call turned out to be relayed — takes the
        // picture with it at once.
        var relayed = VideoLadder(start: .hd)
        #expect(relayed.reading(spare: 4_000_000, ceiling: .high) == .high)
        #expect(relayed.reading(spare: 4_000_000, ceiling: .high) == nil)
    }

    @Test("every step is smaller than the one above it, and names itself")
    func theSteps() {
        let steps = VideoQuality.allCases
        #expect(steps == steps.sorted())
        for (lower, higher) in zip(steps, steps.dropFirst()) {
            #expect(lower.bitrate < higher.bitrate)
            #expect(lower.width <= higher.width)
            #expect(lower.height < higher.height)
            #expect(lower.needs > lower.bitrate)
        }
        #expect(VideoQuality.hd.words == "720p")
        #expect(VideoQuality.standard.words == "480p")
        // What a phone sends is never above the ceiling written into the SDP.
        #expect(SDPTweaks.videoMaxBitrate == VideoQuality.hd.bitrate)
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
            expiresAt: Date(timeIntervalSinceNow: 3600), maxBitrateBps: 1_280_000)
        #expect(!RelayCertificates(relay: relay).verify(Data()))
        #expect(!RelayCertificates(relay: relay).verify(Data(repeating: 0x30, count: 64)))
    }

    // MARK: - Their picture coming and going

    @Test("a picture that stops arriving comes back when it starts again")
    func theirPictureComesBack() {
        var watch = PictureWatch()
        // It is announced before any frame has arrived, which is the ordinary
        // way round: nothing is said until frames stop coming.
        #expect(watch.reading(framesIn: 0, showing: true, announced: true) == nil)
        #expect(watch.reading(framesIn: 0, showing: true, announced: true) == false)
        // The echo test's picture starts the moment this phone's camera does.
        #expect(watch.reading(framesIn: 12, showing: false, announced: true) == true)
        #expect(watch.reading(framesIn: 30, showing: true, announced: true) == nil)
        // It stops. One quiet reading is a hiccup; two mean it has gone.
        #expect(watch.reading(framesIn: 30, showing: true, announced: true) == nil)
        #expect(watch.reading(framesIn: 30, showing: true, announced: true) == false)
        // And it comes back, which is the half that was missing.
        #expect(watch.reading(framesIn: 31, showing: false, announced: true) == true)
    }

    @Test("frames left over from a picture that has been turned off don't put it back")
    func theirPictureStaysOffWhenNobodyIsSending() {
        var watch = PictureWatch()
        // Stop video: their SDP says they are not sending, and the last few
        // frames still arrive. The screen must stay as it is — the owner's
        // phone went back to the voice call and then straight to the video
        // screen with nobody's camera on (2026-10-05).
        #expect(watch.reading(framesIn: 40, showing: false, announced: false) == nil)
        #expect(watch.reading(framesIn: 44, showing: false, announced: false) == nil)
        // Once they say they are sending again, it comes back.
        #expect(watch.reading(framesIn: 48, showing: false, announced: true) == true)
    }

    @Test("a picture nobody is sending is taken out of the call")
    func thePictureLeavesTheCall() async throws {
        let (phone, transport, media, _) = try await inACall()
        // They turn their camera on and this phone leaves its own off: a
        // one-way video call, which is perfectly ordinary.
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.call?.video.theirs == true })
        phone.answeredAboutTheirVideo(turningMineOn: false)
        let invites = transport.count("INVITE")
        // Then their picture stops arriving. A video screen with nothing in it
        // is not what the call is, so the picture is taken out of the call and
        // both ends go back to an ordinary call (owner, 2026-10-05).
        media.pretendTheirPictureStopped()
        #expect(await eventually { media.dropped })
        #expect(await eventually { transport.count("INVITE") == invites + 1 })
        #expect(phone.call?.video.on == false)
        #expect(phone.call?.phase == .active)
    }

    @Test("the question about their camera waits for an answer")
    func theQuestionWaits() async throws {
        let (phone, _, media, _) = try await inACall()
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.askAboutTheirVideo != nil })
        // Everything else that happens when a picture arrives — the sound
        // moving to the loudspeaker, the connection being read, the screen
        // changing — leaves the question alone. Only an answer takes it away
        // (owner, 2026-10-05: it "comes and disappears in less than a second").
        media.soundComesOut(of: AudioRoute(name: "Speaker", speaker: true, builtIn: true))
        media.found(CallDiagnostics(found: [CallDiagnostics.theRelay], settled: true))
        #expect(phone.askAboutTheirVideo != nil)
        phone.answeredAboutTheirVideo(turningMineOn: false)
        #expect(phone.askAboutTheirVideo == nil)
        // A one-way video call carries on exactly as it was.
        #expect(phone.call?.video.theirs == true)
        #expect(phone.call?.video.mine == false)
        #expect(phone.call?.phase == .active)
        // And their camera going off again takes the question with it: there
        // is nothing left to answer.
        media.pretendTheirVideo(true)
        media.pretendTheirVideo(false)
        #expect(phone.askAboutTheirVideo == nil)
    }

    @Test("a picture announced but never arriving leaves the call too")
    func thePictureThatNeverCame() async throws {
        let (phone, transport, media, _) = try await inACall()
        // The other side says they are sending and nothing ever arrives: on
        // the iPad the owner turned video on and off again and was left on a
        // split video screen with neither picture in it (2026-10-05). The
        // frame watch only notices a picture that *stops*, so one that never
        // starts is counted instead.
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.call?.video.theirs == true })
        phone.answeredAboutTheirVideo(turningMineOn: false)
        let invites = transport.count("INVITE")
        media.pretendNothingIsArriving(readings: PictureWatch.quietReadings)
        #expect(await eventually { media.dropped })
        #expect(await eventually { transport.count("INVITE") == invites + 1 })
        #expect(phone.call?.video.on == false)
        #expect(phone.call?.phase == .active)
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
        let refused = [
            CallDiagnostics.RelayTrouble(url: "turns:turn.example.com:443", code: 401, said: "Unauthorized")
        ]
        // While the phone is still looking for routes, nothing is said: one
        // address of two failing is ordinary and the one that fails usually
        // fails first.
        media.found(CallDiagnostics(found: ["this network"], relayTrouble: refused))
        #expect(phone.problem == nil)
        media.found(CallDiagnostics(found: ["this network"], relayTrouble: refused, settled: true))
        #expect(phone.problem?.contains("relay") == true)
        #expect(phone.diagnostics.foundTheRelay == false)
        // And if a route through the relay turns up after all, the message
        // comes off the screen.
        media.found(
            CallDiagnostics(
                found: ["this network", CallDiagnostics.theRelay], relayTrouble: refused, settled: true))
        #expect(phone.problem == nil)
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
                ], settled: true))
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
                    expiresAt: Date(timeIntervalSinceNow: 3600), maxBitrateBps: 1_280_000)),
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
