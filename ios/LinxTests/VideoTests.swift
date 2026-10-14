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
}
