import AVFoundation
import Foundation
import Testing

@testable import Linx

// 1:1 video (ADR-087, docs/VIDEO_CALLS.md §3).
//
// The rule every one of these is really about: **the picture never goes
// through the phone system.** The call rings and carries its sound through
// Asterisk; the picture has its own direct link to the other device, opened
// once the call is answered, and a camera going on or off is that link's
// business alone — no re-INVITE, nothing for the switch to refuse or get
// wrong. So what is checked here is that SIP stays out of it, and that the
// call survives everything that can happen to the picture.
//
// Nothing here touches a camera, WebRTC or a network.

@MainActor
struct VideoTests {
    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600),
                    maxBitrateBps: 1_280_000)),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    /// A phone with a call up and talking, which is where every one of
    /// these starts.
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

    @Test("the picture link is told the call is up when it is answered")
    func theLinkOpensOnAnswer() async throws {
        let (_, _, media, _) = try await inACall()
        #expect(media.pictureLinks == 1)
        #expect(media.pictureCameraFirst == false)
    }

    @Test("a call that rings out opens its picture link while it rings")
    func theLinkOpensWhileRingingOut() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        phone.callNumber("1024", name: "Omar Khalil", withVideo: true)
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        #expect(media.pictureLinksWhileRinging == 0)
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(await eventually { media.pictureLinksWhileRinging == 1 })
        // A second ringing doesn't open another.
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(media.pictureLinks == 0)

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        #expect(media.pictureLinksWhileRinging == 1)
        #expect(media.pictureLinks == 1)
    }

    @Test("on a line that carries call links, the picture's messages go on the line")
    func theLinkGoesOnTheLine() async throws {
        let transport = FakeTransport()
        transport.carriesCallLinks = true
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        phone.callNumber("1024", name: "Omar Khalil", withVideo: true)
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        let callID = try #require(invite.callID)
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(await eventually { media.pictureLink != nil })
        let link = try #require(media.pictureLink as? LineLink)

        var heard: [String] = []
        var closed = 0
        link.onMessage = { heard.append(String(decoding: $0, as: UTF8.self)) }
        link.onClose = { closed += 1 }
        link.start()
        let open = try #require(transport.side.last)
        #expect(open == "{\"call\":\"\(callID)\",\"open\":true,\"early\":true}")

        // Linx says to wait, then pairs it; what the picture sends goes on the line.
        transport.onText?("{\"call\":\"\(callID)\",\"msg\":{\"type\":\"wait\"}}")
        #expect(heard.count == 1 && heard[0].contains("wait"))
        link.send(Data("{\"type\":\"signal\",\"data\":{\"candidate\":null}}".utf8))
        let sent = "{\"call\":\"\(callID)\",\"msg\":{\"type\":\"signal\",\"data\":{\"candidate\":null}}}"
        #expect(transport.side.last == sent)
        // Another call's message isn't this link's.
        transport.onText?("{\"call\":\"someone-else\",\"msg\":{\"type\":\"peer\"}}")
        #expect(heard.count == 1)
        // The line drops: the link is told, to try again on the next one.
        transport.drop()
        #expect(closed == 1)
    }

    @Test("a line Linx didn't say carries call links keeps the link's own websocket")
    func oldServersKeepTheirWebsocket() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        phone.callNumber("1024", name: "Omar Khalil")
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(await eventually { media.pictureLink != nil })
        #expect(media.pictureLink is CallLinkSocket)
        #expect(transport.side.isEmpty)
    }

    @Test("a call ringing here opens its picture link before it is answered")
    func theLinkOpensWhileRingingIn() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        var invite = CallTests.incomingInvite()
        invite.add("X-Linx-Video", "yes")
        transport.asterisk(invite)
        #expect(await eventually { transport.sent.contains { $0.status == 180 } })
        #expect(media.pictureLinksWhileRinging == 1)
        #expect(media.pictureLinks == 0)
    }

    @Test("turning video on never touches SIP, and never starts another call")
    func videoGoesOnTheLink() async throws {
        let (phone, transport, media, system) = try await inACall()
        let callID = try #require(phone.call?.id)
        let invites = transport.count("INVITE")

        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        #expect(media.cameraCalls == [true])
        #expect(transport.count("INVITE") == invites)
        // Everyone expects a video call on the loudspeaker.
        #expect(phone.call?.speaker == true)
        #expect(phone.call?.id == callID)
        #expect(system.video.last?.on == true)
        #expect(system.ended.isEmpty)
    }

    @Test("turning it off again leaves an ordinary phone call")
    func videoComesBackOut() async throws {
        let (phone, transport, media, system) = try await inACall()
        let invites = transport.count("INVITE")
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.mine == true })
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.on == false })
        #expect(media.cameraCalls == [true, false])
        #expect(transport.count("INVITE") == invites)
        #expect(phone.call?.phase == .active)
        #expect(system.video.last?.on == false)
    }

    @Test("a desk phone or an outside number at the other end: the video button does nothing")
    func noPictureThere() async throws {
        let (phone, _, media, _) = try await inACall()
        media.pretendNoPicture()
        #expect(await eventually { phone.call?.video.available == false })
        phone.toggleVideo()
        try? await Task.sleep(for: .milliseconds(50))
        #expect(media.cameraCalls.isEmpty)
        #expect(phone.call?.phase == .active)
    }

    @Test("they turn their camera on: this phone asks once, and a no leaves a one-way call")
    func askedAboutTheirVideo() async throws {
        let (phone, transport, media, _) = try await inACall()
        #expect(phone.askAboutTheirVideo == nil)

        // Their camera arrives.
        media.pretendTheirVideo(true)
        #expect(phone.askAboutTheirVideo?.number == "1024")

        // "Not now": the call carries on with their picture and none of
        // this phone's, and nothing is sent about it.
        let before = transport.count("INVITE")
        phone.answeredAboutTheirVideo(turningMineOn: false)
        #expect(phone.askAboutTheirVideo == nil)
        #expect(phone.call?.video.theirs == true)
        #expect(phone.call?.video.mine == false)
        #expect(media.cameraCalls.isEmpty)
        #expect(transport.count("INVITE") == before)
        #expect(phone.call?.phase == .active)
    }

    @Test("they turn their camera on and the person says yes: this phone's goes on too")
    func saidYesToTheirVideo() async throws {
        let (phone, _, media, _) = try await inACall()
        media.pretendTheirVideo(true)
        #expect(phone.askAboutTheirVideo != nil)

        phone.answeredAboutTheirVideo(turningMineOn: true)
        #expect(phone.askAboutTheirVideo == nil)
        #expect(await eventually { media.video.mine })
    }

    @Test("nobody is asked when this phone's camera is already on")
    func notAskedWhenAlreadySending() async throws {
        let (phone, _, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        media.pretendTheirVideo(true)
        #expect(await eventually { phone.call?.video.theirs == true })
        #expect(phone.askAboutTheirVideo == nil)
    }

    @Test("the camera is refused: the person is told")
    func cameraRefused() async throws {
        let (phone, transport, media, _) = try await inACall()
        media.cameraRefuses = .notAllowed
        let before = transport.count("INVITE")
        phone.toggleVideo()
        #expect(await eventually { phone.problem?.contains("Settings") == true })
        #expect(transport.count("INVITE") == before)
        #expect(phone.call?.video.on == false)
    }

    @Test("the link gets too thin: the picture goes and the conversation stays")
    func tooThinForAPicture() async throws {
        let (phone, transport, media, _) = try await inACall()
        let invites = transport.count("INVITE")
        phone.toggleVideo()
        #expect(await eventually { phone.call?.video.mine == true })

        // WebRTC says there is no room left for a picture.
        media.onVideoTooExpensive?()
        #expect(await eventually { phone.problem?.contains("too slow for video") == true })
        #expect(await eventually { media.video.mine == false })
        #expect(transport.count("INVITE") == invites)
        #expect(phone.call?.phase == .active)
    }

    @Test("the Video call button: a header says so, the INVITE carries sound only (ADR-087)")
    func videoCallFromTheTeamList() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))

        phone.callNumber("1024", name: "Omar Khalil", withVideo: true)
        #expect(await eventually { transport.last("INVITE") != nil })
        let invite = try #require(transport.last("INVITE"))
        #expect(invite.first("X-Linx-Video") == "yes")
        #expect(!invite.body.contains("m=video"))
        #expect(phone.call?.videoCall == true)
        #expect(phone.call?.speaker == true)
        // Nothing of the picture before the answer.
        #expect(media.pictureLinks == 0)

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        // The camera goes on through the picture link, as soon as the other
        // side is there; SIP hears nothing more about it.
        #expect(media.pictureLinks == 1)
        #expect(media.pictureCameraFirst == true)
        #expect(transport.count("INVITE") == 1)
        // A video call never asks "turn yours on too?": both chose video.
        media.pretendTheirVideo(true)
        #expect(phone.askAboutTheirVideo == nil)
    }

    @Test("an ordinary call doesn't say it's a video call")
    func voiceCallHasNoHeader() async throws {
        let (_, transport, _, _) = try await inACall()
        let invite = try #require(transport.sent.first { $0.method == "INVITE" })
        #expect(invite.first("X-Linx-Video") == nil)
    }

    @Test("the echo test has nobody to link a picture with")
    func echoTestHasNoLink() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: FakeSystemCalls())
        await phone.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: try #require(transport.last("REGISTER"))))
        phone.callNumber("*43")
        #expect(await eventually { transport.last("INVITE") != nil })
        var ok = SIPMessage.response(200, "OK", to: try #require(transport.last("INVITE"))).withTag("theirs")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        #expect(media.pictureLinks == 0)
    }

    @Test("hanging up puts the camera down with everything else")
    func hangingUpStopsTheCamera() async throws {
        let (phone, transport, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        phone.hangUp()
        #expect(phone.call == nil)
        #expect(media.stopped)
        #expect(transport.last("BYE") != nil)
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }

    @Test("a route's kind is read from its SDP line")
    func routeKind() {
        #expect(
            DirectPicture.kind(of: "candidate:1 1 udp 41885439 86.98.181.234 50000 typ relay raddr 0.0.0.0 rport 0")
                == "relay")
        #expect(DirectPicture.kind(of: "candidate:2 1 udp 2122260223 192.168.1.5 5000 typ host") == "host")
        #expect(DirectPicture.kind(of: "nonsense") == "?")
    }

    @Test("their picture is on its way until it first arrives, and stopped only after")
    func onItsWayThenStopped() {
        // The owner saw "Their picture has stopped" while it was still
        // loading (2026-10-15).
        var video = CallVideo(available: true, mine: true, theirs: true)
        #expect(video.waiting(.theirs) == "Their picture is on its way…")
        video.theirPictureSeen = true
        #expect(video.waiting(.theirs) == "Their picture has stopped. The call carries on.")
        video.theirs = false
        #expect(video.waiting(.theirs) == "Their camera is off.")
    }
}

/// Where a call puts itself on each kind of screen (`CallLayout`). The sizes
/// are the real ones, in points, of the devices Linx says it supports.
struct CallLayoutTests {
    @Test("a phone upright, and an iPhone Duo folded: one picture, buttons along the bottom")
    func tall() {
        #expect(CallLayout.shape(size: CGSize(width: 393, height: 852), horizontal: .compact) == .tall)
        // The Duo's outer screen is narrow whatever else it is.
        #expect(CallLayout.shape(size: CGSize(width: 360, height: 820), horizontal: .compact) == .tall)
    }

    @Test("a phone on its side: the buttons stand in a column out of the picture")
    func wide() {
        #expect(CallLayout.shape(size: CGSize(width: 852, height: 393), horizontal: .compact) == .wide)
        // A Max-sized iPhone on its side is "regular" but far too shallow
        // for two panels, so it is laid out as a phone on its side.
        #expect(CallLayout.shape(size: CGSize(width: 956, height: 440), horizontal: .regular) == .wide)
    }

    @Test("an iPad and an unfolded iPhone Duo: two panels, and nothing to press on the crease")
    func split() {
        // iPad, upright: the picture over the panel.
        #expect(
            CallLayout.shape(size: CGSize(width: 820, height: 1180), horizontal: .regular)
                == .split(sideBySide: false))
        // iPad, on its side.
        #expect(
            CallLayout.shape(size: CGSize(width: 1180, height: 820), horizontal: .regular)
                == .split(sideBySide: true))
        // The Duo opened out is nearly square: side by side, so every
        // button sits in one half and none of them lands on the fold.
        #expect(
            CallLayout.shape(size: CGSize(width: 1024, height: 1080), horizontal: .regular)
                == .split(sideBySide: true))
    }

    @Test("a window an iPad shrinks while the call is up stops being two panels")
    func aWindowThatChanges() {
        // Slide Over, or a narrow split view: compact again, and the layout
        // follows the window rather than the device it is on.
        #expect(CallLayout.shape(size: CGSize(width: 375, height: 1024), horizontal: .compact) == .tall)
        #expect(CallLayout.shape(size: CGSize(width: 639, height: 1024), horizontal: .regular) == .tall)
    }

    @Test("this phone's own picture is small, and smaller still on a phone")
    func selfView() {
        let phone = CallLayout.selfViewWidth(for: .tall, size: CGSize(width: 393, height: 852))
        let pad = CallLayout.selfViewWidth(for: .split(sideBySide: true), size: CGSize(width: 1180, height: 820))
        #expect(phone < 130)
        #expect(pad > phone)
    }

    @Test("a video call fills the screen on an iPad too, and splits only at a fold")
    func videoFillsTheScreen() {
        #expect(CallLayout.videoShape(size: CGSize(width: 1032, height: 1376), horizontal: .regular) == .tall)
        #expect(CallLayout.videoShape(size: CGSize(width: 1376, height: 1032), horizontal: .regular) == .wide)
        #expect(CallLayout.videoShape(size: CGSize(width: 393, height: 852), horizontal: .compact) == .tall)
        #expect(CallLayout.selfViewWidth(for: .tall, size: CGSize(width: 1032, height: 1376)) <= 200)
    }
    @Test("two apps meet on AV1; without it, AV1 is left out and H.264 leads")
    func codecOrder() {
        let listed = ["video/H264", "video/H264", "video/VP8", "video/rtx", "video/VP9", "video/AV1", "video/red"]
        let first = DirectPicture.codecOrder(listed, av1: true)
        #expect(listed[first[0]] == "video/AV1")
        #expect(first.count == listed.count)
        #expect(first.dropFirst().map { listed[$0] } == listed.filter { $0 != "video/AV1" })
        let without = DirectPicture.codecOrder(listed, av1: false)
        #expect(!without.map { listed[$0] }.contains("video/AV1"))
        #expect(listed[without[0]] == "video/H264")
        #expect(DirectPicture.codecName("video/H264") == "H.264")
        #expect(DirectPicture.codecName("video/AV1") == "AV1")
    }

    @Test("in AV1 a thin link carries a bigger picture than in H.264")
    func av1FitsMore() {
        #expect(DirectPicture.step(for: 250_000, under: .hd) == .low)
        #expect(DirectPicture.step(for: 250_000, under: .hd, av1: true) == .standard)
        #expect(VideoQuality.standard.bitrate(av1: true) == 400_000)
        #expect(VideoQuality.standard.bitrate(av1: false) == 600_000)
    }

    @Test("540p and 720p get H.264's full data in AV1, so HD isn't soft")
    func av1KeepsTheTopSharp() {
        #expect(VideoQuality.high.bitrate(av1: true) == 900_000)
        #expect(VideoQuality.hd.bitrate(av1: true) == 1_500_000)
    }

    @Test("a reading says what each way really carried, and how sharp")
    func readingRates() {
        var before = PictureReading()
        before.sentHeight = 720
        before.sent = .init(
            ["bytesSent": NSNumber(value: 0), "framesEncoded": NSNumber(value: 0), "qpSum": NSNumber(value: 0)],
            bytes: "bytesSent", frames: "framesEncoded")
        var now = before
        now.at = before.at.addingTimeInterval(10)
        now.sent = .init(
            [
                "bytesSent": NSNumber(value: 1_250_000), "framesEncoded": NSNumber(value: 300),
                "qpSum": NSNumber(value: 9_000),
            ],
            bytes: "bytesSent", frames: "framesEncoded")
        #expect(now.words(since: before) == "Picture: sending 720p 0 fps 1000 kbit/s qp 30, getting none")
        #expect(now.words == "Picture: sending 720p 0 fps, getting none")
    }

    @Test("Linx's own override before the sound has a route doesn't turn the loudspeaker off")
    func speakerWishSurvivesNoRoute() {
        #expect(WebRTCMedia.speakerWanted(was: true, outputs: []))
        #expect(!WebRTCMedia.speakerWanted(was: true, outputs: [.builtInReceiver]))
        #expect(WebRTCMedia.speakerWanted(was: false, outputs: [.builtInSpeaker]))
    }

    @Test("on mobile data the picture starts at 480p")
    func mobileStartsSmaller() {
        #expect(DirectPicture.mobileStart == .standard)
    }

    /// After a link made again mid-call, two slots can be open to their
    /// picture: the screen goes to the one whose frames are arriving
    /// (owner, build 52: the empty one was drawn once the camera went off).
    @Test("their picture is drawn from the slot its frames arrive in")
    func theSlotWithFrames() {
        #expect(DirectPicture.slotWithTheirPicture(now: ["1": 0, "2": 300], before: ["1": 0, "2": 240]) == "2")
        #expect(DirectPicture.slotWithTheirPicture(now: ["1": 90, "2": 300], before: ["1": 30, "2": 300]) == "1")
        // Nothing arriving anywhere: nothing to move to.
        #expect(DirectPicture.slotWithTheirPicture(now: ["1": 50, "2": 300], before: ["1": 50, "2": 300]) == nil)
        // A slot seen for the first time counts all its frames.
        #expect(DirectPicture.slotWithTheirPicture(now: ["3": 20], before: [:]) == "3")
        #expect(DirectPicture.slotWithTheirPicture(now: [:], before: [:]) == nil)
    }
}

// A picture that keeps stopping, and a link that blinks (owner in China,
// 2026-10-21). Both are about the same thing: what Linx does when the
// network between the two phones is poor rather than absent.
@MainActor
struct BadNetworkPictureTests {
    @Test("two readings of a stopping picture ask the other side for a smaller one")
    func stoppingAsksForLess() {
        var stops = PictureStops()
        let start = Date()
        // Nothing to compare the first reading with.
        let first = stops.saw(stoppedFor: 0, at: start)
        // One bad reading on its own is a jolt, not a broken picture.
        let second = stops.saw(stoppedFor: 0.4, at: start.addingTimeInterval(2))
        let third = stops.saw(stoppedFor: 0.8, at: start.addingTimeInterval(4))
        #expect(first == false)
        #expect(second == false)
        #expect(third == true)
    }

    @Test("a picture that is merely late is left alone")
    func aLittleLatenessIsNothing() {
        var stops = PictureStops()
        let start = Date()
        var asked = 0
        for reading in 0...10 {
            // A tenth of a second a reading: under what a person sees.
            let said = stops.saw(
                stoppedFor: Double(reading) * 0.1, at: start.addingTimeInterval(Double(reading) * 2))
            if said { asked += 1 }
        }
        #expect(asked == 0)
    }

    @Test("a picture isn't walked all the way down for one bad stretch")
    func oneStretchAsksOnce() {
        var stops = PictureStops()
        let start = Date()
        var asked = 0
        for reading in 0...10 {
            let said = stops.saw(
                stoppedFor: Double(reading) * 0.4, at: start.addingTimeInterval(Double(reading) * 2))
            if said { asked += 1 }
        }
        // Twenty seconds of a picture stopping: asked at the start and
        // every eight seconds after it, not at every reading.
        #expect(asked == 3)
    }

    @Test("a connection built again starts counting from nothing")
    func aNewConnectionStartsAgain() {
        var stops = PictureStops()
        let start = Date()
        _ = stops.saw(stoppedFor: 10, at: start)
        _ = stops.saw(stoppedFor: 10.5, at: start.addingTimeInterval(2))
        stops.forget()
        // WebRTC's count starts at nothing on a new connection: that must
        // not read as the picture coming back to life, nor as stopping.
        let afterwards = stops.saw(stoppedFor: 0, at: start.addingTimeInterval(4))
        let next = stops.saw(stoppedFor: 0.4, at: start.addingTimeInterval(6))
        #expect(afterwards == false)
        #expect(next == false)
    }

    @Test("the link's words say whether a pairing goes on or starts afresh")
    func theLinkSaysWhetherItGoesOn() throws {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        let resumed = try decoder.decode(
            CallLinkMessage.self,
            from: Data(#"{"type":"peer","video":true,"session":7,"resumed":true}"#.utf8))
        #expect(resumed.resumed == true)
        #expect(resumed.session == 7)
        // An older server says nothing about it, and that means a new one.
        let afresh = try decoder.decode(
            CallLinkMessage.self, from: Data(#"{"type":"peer","video":true,"session":8}"#.utf8))
        #expect(afresh.resumed == nil)

        // And the word that asks for a smaller picture survives the trip.
        let signal = try JSONEncoder().encode(PictureSignal(stopping: true))
        #expect(try JSONDecoder().decode(PictureSignal.self, from: signal).stopping == true)
    }
}
