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

    // MARK: - The steps of picture quality (ADR-081)

    @Test("every step is smaller than the one above it, and names itself")
    func theSteps() {
        let steps = VideoQuality.allCases
        #expect(steps == steps.sorted())
        for (lower, higher) in zip(steps, steps.dropFirst()) {
            #expect(lower.bitrate < higher.bitrate)
            #expect(lower.width <= higher.width)
            #expect(lower.height < higher.height)
        }
        #expect(VideoQuality.hd.words == "720p")
        #expect(VideoQuality.standard.words == "480p")
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

    @Test("the relay's issuers are fetched over its own TLS port, ahead of the call")
    func relayIssuersComeFromTheRelay() {
        // Only turns: URLs speak TLS; the port is the one Linx gave, 443 when
        // none is given, and one fetch per relay.
        let found = RelayCertificates.tlsEndpoints(in: [
            "turn:turn.example.com:443?transport=udp",
            "turns:turn.example.com:443?transport=tcp",
            "turns:turn.example.com:5349?transport=tcp",
            "turns:relay.example.net?transport=tcp",
            "turns:[2001:db8::1]:8443?transport=tcp",
            "stun:stun.example.com:3478",
        ])
        #expect(found.map(\.host) == ["turn.example.com", "relay.example.net", "2001:db8::1"])
        #expect(found.map(\.port) == [443, 443, 8443])
    }

    // MARK: - Their picture coming and going

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
        let (phone, _, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(phone.changingVideo)
        // A second press while the first is still in the air changes nothing.
        phone.toggleVideo()
        #expect(await eventually { !phone.changingVideo })
        #expect(media.cameraCalls == [true])
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

    @Test("a crash iOS reports becomes a record with the crashed thread's frames")
    func crashInWords() throws {
        // The shape MetricKit's call stack tree has: the crashed thread
        // marked, each frame a binary and an offset, the next frame inside.
        let tree: [String: Any] = [
            "callStacks": [
                ["threadAttributed": false, "callStackRootFrames": [["binaryName": "UIKitCore"]]],
                [
                    "threadAttributed": true,
                    "callStackRootFrames": [
                        [
                            "binaryName": "libdispatch.dylib", "offsetIntoBinaryTextSegment": 0x37d28,
                            "subFrames": [
                                [
                                    "binaryName": "Linx", "offsetIntoBinaryTextSegment": 0x16a88,
                                    "binaryUUID": "BAE0B802-A78C-3706-B26F-1113A00BDC3F",
                                ]
                            ],
                        ]
                    ],
                ],
            ]
        ]
        let json = try JSONSerialization.data(withJSONObject: tree)
        let report = CrashReports.report(
            at: Date(timeIntervalSince1970: 1_791_514_909), app: "1.0.0 (16)", os: "iPhone OS 27.0.1", exception: 6,
            code: 1, signal: 5, termination: nil, stack: json)
        #expect(report.kind == .crash && report.peer.isEmpty)
        #expect(report.text.contains("exception: EXC_BREAKPOINT (6), code 1"))
        #expect(report.text.contains("signal: SIGTRAP (5)"))
        #expect(report.text.contains("crashed thread:\n  libdispatch.dylib +0x37d28\n  Linx +0x16a88 (BAE0B802)"))
        // The same crash handed over twice is the same record.
        let again = CrashReports.report(
            at: Date(timeIntervalSince1970: 1_791_514_909), app: "1.0.0 (16)", os: "iPhone OS 27.0.1", exception: 6,
            code: 1, signal: 5, termination: nil, stack: json)
        #expect(report.callID == again.callID)
    }

    @Test("an answer lets Asterisk start the encryption, so nothing is thrown away")
    func asteriskStartsTheEncryption() {
        // Asterisk dropped a DTLS handshake that came before its side of the
        // route was confirmed, and WebRTC waited seconds to try again.
        let offer = "v=0\r\na=setup:actpass\r\n"
        #expect(SDPTweaks.letThemStartTheEncryption("a=setup:active\r\n", offer: offer) == "a=setup:passive\r\n")
        // An offer that has already chosen is answered as it must be.
        #expect(
            SDPTweaks.letThemStartTheEncryption("a=setup:active\r\n", offer: "a=setup:passive\r\n")
                == "a=setup:active\r\n")
    }

    @Test("a relay reserved ahead is taken only by a call with the same relay and credentials")
    func reservationsMatchTheirRelay() {
        let turn = PhoneLine.Turn(
            urls: ["turn:turn.example.com:443?transport=udp"], username: "u1", credential: "c",
            expiresAt: Date(timeIntervalSinceNow: 3600), maxBitrateBps: 1_280_000)
        let same = WebRTCMedia.reservationKey(turn, ["turn.example.com": ["203.0.113.5"]])
        #expect(same == WebRTCMedia.reservationKey(turn, ["turn.example.com": ["203.0.113.5"]]))
        let renewed = PhoneLine.Turn(
            urls: turn.urls, username: "u2", credential: "c", expiresAt: turn.expiresAt, maxBitrateBps: 1_280_000)
        #expect(same != WebRTCMedia.reservationKey(renewed, ["turn.example.com": ["203.0.113.5"]]))
        #expect(same != WebRTCMedia.reservationKey(turn, ["turn.example.com": ["203.0.113.9"]]))
        #expect(WebRTCMedia.reservationKey(nil, [:]).isEmpty)
    }

    @Test("a call offers only the routes Asterisk can use, so its answer stays small")
    func onlyUsefulRoutes() {
        // A 4.4 KB answer listing 24 routes took 7 s up a slow mobile link
        // (2026-10-11): TCP ones Asterisk never uses go, and so do link-local
        // and all but one IPv6 network. The relay keeps its TCP/TLS ways in.
        let configuration = WebRTCMedia.configuration(relay: nil)
        #expect(configuration.tcpCandidatePolicy == .disabled)
        #expect(configuration.disableLinkLocalNetworks)
        #expect(configuration.maxIPv6Networks == 1)
        #expect(configuration.iceTransportPolicy == .all)
    }

    @Test("the details say which routes were offered and tried, and which one won")
    func routesInWords() {
        // A mobile call went over TLS with UDP open, and the details couldn't
        // say why (2026-10-11): now they list every pairing.
        let relay: [String: NSObject] = [
            "candidateType": "relay" as NSString,
            "relayProtocol": "udp" as NSString,
            "address": "86.98.181.234" as NSString,
        ]
        let asterisk: [String: NSObject] = [
            "candidateType": "host" as NSString,
            "protocol": "udp" as NSString,
            "address": "172.24.0.4" as NSString,
        ]
        #expect(WebRTCMedia.words(for: relay) == "relay/udp", "this phone's own address is never shown")
        #expect(WebRTCMedia.words(for: asterisk, address: true) == "host/udp 172.24.0.4")
        let call = PhoneModel.Call(
            peer: SIPPeer(name: "Omar Khalil", number: "1024"), incoming: true, phase: .active, answeredAt: Date())
        var diagnostics = CallDiagnostics()
        diagnostics.offered = ["relay/tls", "relay/udp"]
        diagnostics.pairs = ["relay/udp → host/udp 172.24.0.4: succeeded, in use, 40 ms"]
        let words = CallDetailsView.words(call: call, diagnostics: diagnostics)
        #expect(words.contains("this phone offered: relay/tls, relay/udp"))
        #expect(words.contains("routes tried:\n  relay/udp → host/udp 172.24.0.4: succeeded, in use, 40 ms"))
    }

    @Test("the call's timer counts from Linx's moment of the answer, the same on every device")
    func oneTimerForEveryone() async throws {
        let (phone, _, media, system) = try await inACall()
        // The lock screen's timer waits a moment for Linx's word.
        #expect(system.answered.isEmpty)
        let linx = Date(timeIntervalSinceNow: -1.4)
        media.onAnsweredAt?(linx)
        #expect(phone.call?.answeredAt == linx)
        #expect(system.answeredAt == [linx], "the lock screen counts from Linx's moment too")
        // Another call's moment, far from this one's, is ignored.
        media.onAnsweredAt?(Date(timeIntervalSinceNow: -600))
        #expect(phone.call?.answeredAt == linx)
        #expect(system.answered.count == 1)
    }

    @Test("a call Linx gives no moment for still starts the lock screen's timer")
    func noMomentFromLinx() async throws {
        let (phone, _, _, system) = try await inACall()
        // A slow machine (CI) may run the two-second wait late: up to six.
        let giveUp = Date().addingTimeInterval(6)
        while system.answered.isEmpty, Date() < giveUp { try await Task.sleep(for: .milliseconds(100)) }
        #expect(system.answered.count == 1)
        #expect(system.answeredAt.first == phone.call?.answeredAt)
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
