import Foundation
import Testing

@testable import Linx

// 1:1 video (docs/PHASE2.md §7, build step 7).
//
// The rule every one of these is really about: **a picture is added to a
// call that is already up, and taking it away leaves the call alone.** A
// Linx call rings as a phone call — that is what a lock screen, a car and a
// headset understand — and either side can put a camera into it afterwards
// with a re-INVITE. So what is checked here is the call *surviving*
// everything that can happen to the picture: the other side refusing it,
// both sides asking at once, Asterisk challenging it, and the network
// becoming too thin to carry it.
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
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600))),
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

    @Test("turning video on changes the call that is up, and never starts another")
    func videoGoesIntoTheCall() async throws {
        let (phone, transport, media, system) = try await inACall()
        let callID = try #require(phone.call?.id)
        let invites = transport.count("INVITE")

        phone.toggleVideo()
        #expect(await eventually { transport.count("INVITE") == invites + 1 })
        let reinvite = try #require(transport.last("INVITE"))
        // The same call: same Call-ID, same dialog, one higher in sequence.
        #expect(reinvite.callID == transport.last("ACK")?.callID)
        #expect(reinvite.body.contains("m=video"))
        #expect(media.video.mine)
        // Everyone expects a video call on the loudspeaker.
        #expect(phone.call?.speaker == true)

        var ok = SIPMessage.response(200, "OK", to: reinvite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.videoAnswer
        transport.asterisk(ok)
        #expect(await eventually { media.acceptedAnswer == FakeMedia.videoAnswer })
        // It is acknowledged with the re-INVITE's own sequence number, not
        // the invitation the call began with.
        let ack = try #require(transport.last("ACK"))
        #expect(ack.cseq?.number == reinvite.cseq?.number)
        #expect(await eventually { phone.call?.video == CallVideo(mine: true, theirs: true) })
        // The call never restarted: the system was told about the picture,
        // not about a new call.
        #expect(phone.call?.id == callID)
        #expect(system.video.last?.on == true)
        #expect(system.ended.isEmpty)
    }

    @Test("turning it off again leaves an ordinary phone call")
    func videoComesBackOut() async throws {
        let (phone, transport, media, system) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        let on = try #require(transport.last("INVITE"))
        var ok = SIPMessage.response(200, "OK", to: on).withTag("theirs")
        ok.body = FakeMedia.videoAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.video.mine == true })

        phone.toggleVideo()
        #expect(await eventually { transport.last("INVITE")?.body.contains("m=video") == false })
        let off = try #require(transport.last("INVITE"))
        var done = SIPMessage.response(200, "OK", to: off).withTag("theirs")
        done.body = FakeMedia.soundAnswer
        transport.asterisk(done)
        #expect(await eventually { phone.call?.video.on == false })
        #expect(phone.call?.phase == .active)
        #expect(system.video.last?.on == false)
    }

    @Test("the other side wouldn't have it: the call carries on, and says so once")
    func theyRefuseTheVideo() async throws {
        let (phone, transport, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        let asked = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(488, "Not Acceptable Here", to: asked).withTag("theirs"))

        #expect(await eventually { media.rolledBack })
        #expect(await eventually { phone.call?.video.on == false })
        #expect(phone.call?.phase == .active)
        #expect(phone.problem?.contains("sound is unaffected") == true)
        // A refused re-INVITE is still acknowledged (RFC 3261 §17.1.1.3).
        let ack = try #require(transport.last("ACK"))
        #expect(ack.cseq?.number == asked.cseq?.number)
    }

    @Test("both sides asked at once: the person is told to press it again")
    func glare() async throws {
        let (phone, transport, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        let asked = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(491, "Request Pending", to: asked).withTag("theirs"))
        #expect(await eventually { phone.problem?.contains("same moment") == true })
        #expect(phone.call?.phase == .active)
    }

    @Test("Asterisk challenges the re-INVITE: one go, with the credentials in hand")
    func challengedReinvite() async throws {
        let (phone, transport, media, _) = try await inACall()
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        let asked = try #require(transport.last("INVITE"))
        var challenge = SIPMessage.response(407, "Proxy Authentication Required", to: asked).withTag("theirs")
        challenge.add("Proxy-Authenticate", "Digest realm=\"asterisk\", nonce=\"n2\", qop=\"auth\"")
        let before = transport.count("INVITE")
        transport.asterisk(challenge)
        #expect(await eventually { transport.count("INVITE") == before + 1 })
        let again = try #require(transport.last("INVITE"))
        #expect(again.first("Authorization") != nil)
        #expect(again.body.contains("m=video"))
        // And a second challenge is the end of it, with the call untouched.
        var onceMore = SIPMessage.response(407, "Proxy Authentication Required", to: again).withTag("theirs")
        onceMore.add("Proxy-Authenticate", "Digest realm=\"asterisk\", nonce=\"n3\", qop=\"auth\"")
        transport.asterisk(onceMore)
        #expect(await eventually { phone.problem?.contains("sound is unaffected") == true })
        #expect(phone.call?.phase == .active)
    }

    @Test("they turn their camera on: this phone shows it and sends nothing of its own")
    func theyStartVideo() async throws {
        let (phone, transport, media, system) = try await inACall()
        var invite = SIPMessage.request("INVITE", "sip:d_Ab12Cd34@pbx.example.com")
        invite.add("Via", "SIP/2.0/WSS pbx.example.com;branch=z9hG4bK-reinvite")
        invite.add("From", "\"Omar Khalil\" <sip:1024@sip.example.com>;tag=theirs")
        invite.add("To", "<sip:d_Ab12Cd34@sip.example.com>;tag=\(try #require(transport.last("INVITE")).fromTag ?? "")")
        invite.add("Call-ID", try #require(phone.call?.id).uuidString)
        invite.set("Call-ID", try #require(transport.last("INVITE")?.callID))
        invite.add("CSeq", "7 INVITE")
        invite.add("Content-Type", "application/sdp")
        invite.body = FakeMedia.videoOffer
        transport.asterisk(invite)

        #expect(await eventually { phone.call?.video.theirs == true })
        #expect(phone.call?.video.mine == false)
        #expect(await eventually { transport.sent.last?.status == 200 })
        #expect(system.video.last?.on == true)
        #expect(media.answeredOffer == FakeMedia.videoOffer)
    }

    @Test("the camera is refused: the person is told, and nothing is sent")
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
        phone.toggleVideo()
        #expect(await eventually { media.video.mine })
        var ok = SIPMessage.response(200, "OK", to: try #require(transport.last("INVITE"))).withTag("theirs")
        ok.body = FakeMedia.videoAnswer
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.video.mine == true })

        // WebRTC says there is no room left for a picture.
        media.onVideoTooExpensive?()
        #expect(await eventually { phone.problem?.contains("too slow for video") == true })
        #expect(await eventually { transport.last("INVITE")?.body.contains("m=video") == false })
        #expect(phone.call?.phase == .active)
    }

    @Test("the video button in the Team list rings first and turns the camera on when they answer")
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
        // It rings as an ordinary call: no picture is offered until it is
        // answered, so the lock screen and a car see what they expect.
        let invite = try #require(transport.last("INVITE"))
        #expect(!invite.body.contains("m=video"))

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = FakeMedia.soundAnswer
        transport.asterisk(ok)
        #expect(await eventually { media.video.mine })
        #expect(await eventually { transport.last("INVITE")?.body.contains("m=video") == true })
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
}
