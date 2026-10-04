import Foundation
import Testing

@testable import Linx

// Ringing a sleeping phone, from the app's side (docs/PHASE2.md §12, step 6).
//
// The rule these tests are really about is Apple's: a VoIP push must report
// a call to the system *at once*, every time, or iOS kills the app and stops
// sending it pushes (§14 item 1). So the first thing each of these checks is
// the order things happen in.
//
// Nothing here touches CallKit, PushKit, a network or a microphone: the
// system is a stand-in that records what it was told and acts as the real one
// does when the person presses a button.

/// The system, without one. It keeps what it was told, and passes on what the
/// person asked for exactly as CallKit's provider delegate would.
@MainActor final class FakeSystemCalls: SystemCalls {
    var onRequest: ((SystemCallRequest) -> Void)?
    var onAudio: ((Bool) -> Void)?
    var onReset: (() -> Void)?

    private(set) var reported: [(id: UUID, peer: SIPPeer)] = []
    private(set) var renamed: [(id: UUID, peer: SIPPeer)] = []
    private(set) var ringingThere: [UUID] = []
    /// Every time the system was told a picture went into a call, or came
    /// out of it.
    private(set) var video: [(id: UUID, on: Bool)] = []
    /// Whether the app last asked for its calls to show in the iPhone's own
    /// Phone app (Settings → Your call history).
    private(set) var inPhoneApp: Bool?
    private(set) var answered: [UUID] = []
    private(set) var ended: [(id: UUID, ending: SystemCallEnding)] = []
    private(set) var asked: [SystemCallRequest] = []

    /// Whether the system takes a call it is told about. It refuses one from
    /// a blocked number, or in a Focus the caller isn't allowed through.
    var takesCalls = true
    /// Whether the system will start a call the person asked for. It won't
    /// while an emergency call is up — and then, as CallKit does, it asks
    /// the app to end the call that never began.
    var startsCalls = true

    func reportIncoming(id: UUID, from: SIPPeer) async -> Bool {
        reported.append((id, from))
        return takesCalls
    }

    func rename(id: UUID, to peer: SIPPeer) { renamed.append((id, peer)) }
    func reportVideo(id: UUID, on: Bool) { video.append((id, on)) }
    func showCallsInThePhoneApp(_ on: Bool) { inPhoneApp = on }
    func reportRingingThere(id: UUID) { ringingThere.append(id) }
    func reportAnswered(id: UUID) { answered.append(id) }
    func reportEnded(id: UUID, _ ending: SystemCallEnding) { ended.append((id, ending)) }

    func ask(_ request: SystemCallRequest) {
        asked.append(request)
        if case .start(let id, _) = request, !startsCalls {
            onRequest?(.end(id))
            return
        }
        onRequest?(request)
    }

    /// The system restarted and forgot everything.
    func forgetEverything() { onReset?() }
}

@MainActor
struct RingingTests {
    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@pbx.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600))),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    /// A phone with its line signed in, and a stand-in system beside it.
    private func phone(
        calls: FakeSystemCalls, inFront: Bool = true
    ) async -> (PhoneModel, FakeTransport, FakeMedia) {
        let transport = FakeTransport()
        let media = FakeMedia()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in media },
            calls: calls, inFront: { inFront })
        await phone.start()
        let first = transport.last("REGISTER")!
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        return (phone, transport, media)
    }

    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }

    private static func invite(from number: String, name: String, callID: String) -> SIPMessage {
        var invite = SIPMessage.request("INVITE", "sip:d_Ab12Cd34@abc.invalid;transport=ws")
        invite.add("Via", "SIP/2.0/WSS 1.2.3.4:8089;branch=z9hG4bK\(callID)")
        invite.add("From", "\"\(name)\" <sip:\(number)@pbx.example.com>;tag=theirs")
        invite.add("To", "<sip:d_Ab12Cd34@pbx.example.com>")
        invite.add("Call-ID", callID)
        invite.add("CSeq", "1 INVITE")
        invite.add("Contact", "<sip:\(number)@1.2.3.4:8089;transport=ws>")
        invite.add("Content-Type", "application/sdp")
        invite.body = "v=0\r\ntheir-offer\r\n"
        return invite
    }

    @Test("a push reports the call to the system before the line is even open")
    func aPushRingsFirst() async throws {
        let calls = FakeSystemCalls()
        // No line at all yet: the phone was asleep, and this is the push
        // arriving. Nothing about the network may come first.
        let phone = PhoneModel(line: { nil }, calls: calls, inFront: { true })
        #expect(await phone.woken(from: "+971500000001"))
        #expect(calls.reported.count == 1)
        #expect(calls.reported[0].peer.number == "+971500000001")
        #expect(phone.busy)
        // Nothing is claimed about the call yet: it hasn't arrived.
        #expect(phone.call == nil)
        #expect(calls.ended.isEmpty)
    }

    @Test("the call that follows the push keeps the id the system was given")
    func theCallArrivesAfterwards() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        _ = await phone.woken(from: "0500000001")
        let reported = try #require(calls.reported.first?.id)

        transport.asterisk(Self.invite(from: "+971500000001", name: "Omar Nasser", callID: "call-1"))
        #expect(phone.call?.id == reported)
        #expect(phone.call?.phase == .ringing)
        // One call, reported once: the system must not be told twice.
        #expect(calls.reported.count == 1)
        // What it does learn is the caller's name, which only the
        // invitation knows — the push carried the number alone.
        #expect(calls.renamed.count == 1)
        #expect(calls.renamed[0].peer.name == "Omar Nasser")
        #expect(!phone.busy == false)

        // Answered from the lock screen: the system asks, the app answers.
        calls.ask(.answer(reported))
        #expect(await eventually { transport.sent.contains { $0.status == 200 } })
        #expect(phone.call?.phase == .active)
    }

    @Test("a call that comes in while the app is open is reported once")
    func aCallWhileTheAppIsOpen() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-2"))
        #expect(phone.call?.phase == .ringing)
        #expect(await eventually { calls.reported.count == 1 })
        #expect(calls.reported[0].id == phone.call?.id)
        #expect(calls.reported[0].peer.name == "Omar Nasser")
    }

    @Test("while the system is ringing a call, the app doesn't ring it too")
    func onlyOneRingingScreen() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-both"))
        #expect(phone.call?.phase == .ringing)
        // CallKit is ringing it — on the lock screen, or as a banner over
        // the app — so the app shows nothing of its own until it has been
        // answered. Both at once is what the owner saw on a real iPhone
        // (2026-10-04).
        phone.systemTakesCalls = true
        #expect(!phone.showsCallScreen)
        // Reporting it to the system is a hop of its own, as it is for the
        // call that comes in while the app is open, above.
        #expect(await eventually { calls.reported.count == 1 })
        let reported = try #require(calls.reported.first?.id)
        calls.ask(.answer(reported))
        #expect(await eventually { phone.call?.phase == .active })
        #expect(phone.showsCallScreen)
    }

    @Test("where the system may not ring a call, the app's own screen does")
    func theAppRingsItWhereCallKitMayNotBeUsed() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        phone.systemTakesCalls = false
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-cn"))
        #expect(phone.call?.phase == .ringing)
        #expect(phone.showsCallScreen)
    }

    @Test("a call this phone makes is on screen from the start")
    func anOutgoingCallIsOnScreen() async throws {
        let calls = FakeSystemCalls()
        let (phone, _, _) = await phone(calls: calls)
        phone.callNumber("1031")
        #expect(await eventually { phone.call != nil })
        #expect(phone.showsCallScreen)
    }

    @Test("a call the system won't take is turned down, not left ringing")
    func theSystemRefusesACall() async throws {
        let calls = FakeSystemCalls()
        calls.takesCalls = false
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-3"))
        #expect(await eventually { phone.call == nil })
        #expect(transport.sent.contains { $0.status == 486 })
    }

    @Test("a push that no call follows stops ringing by itself")
    func aPushThatComesToNothing() async throws {
        let calls = FakeSystemCalls()
        let (phone, _, _) = await phone(calls: calls, inFront: false)
        _ = await phone.woken(from: "1031")
        let reported = try #require(calls.reported.first?.id)
        // The caller gave up while the phone was waking. Rather than wait
        // out the twenty seconds, the person's own Hang up is the same path.
        calls.ask(.end(reported))
        #expect(!phone.busy)
        // Nobody is looking at the app, so the line closes again: a
        // registration left behind would make Linx think this phone is
        // awake and stop pushing to it.
        #expect(phone.status == .starting)
    }

    @Test("a second call while this one is up is reported and ended at once")
    func twoCallsAtOnce() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-4"))
        calls.ask(.answer(try #require(phone.call?.id)))
        #expect(await eventually { phone.call?.phase == .active })

        // This phone has one line. The push still has to be reported — iOS
        // is watching — and then ended, which is what the server does too:
        // the caller gets busy, and voicemail after it.
        #expect(await phone.woken(from: "1042") == false)
        #expect(calls.reported.count == 2)
        let second = calls.reported[1].id
        #expect(calls.ended.contains { $0.id == second && $0.ending == .missed })
        #expect(phone.call?.peer.number == "1031")
    }

    @Test("the caller giving up takes the call off the system too")
    func theCallerGivesUp() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-5"))
        let id = try #require(phone.call?.id)
        var cancel = SIPMessage.request("CANCEL", "sip:d_Ab12Cd34@abc.invalid")
        cancel.add("Via", "SIP/2.0/WSS 1.2.3.4;branch=z9hG4bKcall-5")
        cancel.add("From", "\"Omar Nasser\" <sip:1031@pbx.example.com>;tag=theirs")
        cancel.add("To", "<sip:d_Ab12Cd34@pbx.example.com>")
        cancel.add("Call-ID", "call-5")
        cancel.add("CSeq", "1 CANCEL")
        transport.asterisk(cancel)
        #expect(phone.call == nil)
        #expect(calls.ended.contains { $0.id == id && $0.ending == .missed })
        #expect(phone.recent.first?.kind == .missed)
    }

    @Test("every button goes through the system, and only then does the app act")
    func theSystemOwnsTheButtons() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, media) = await phone(calls: calls)

        // Out: the system is told from the start, so the call shows in
        // Recents and a car can hang it up.
        phone.typed = "1024"
        phone.dial()
        let placed = try #require(calls.asked.first)
        guard case .start(let id, let peer) = placed else {
            Issue.record("the app placed a call without telling the system: \(placed)")
            return
        }
        #expect(peer.number == "1024")
        #expect(phone.call?.id == id)
        #expect(await eventually { transport.last("INVITE") != nil })

        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(calls.ringingThere == [id])

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = "v=0\r\ntheir-answer\r\n"
        transport.asterisk(ok)
        #expect(await eventually { phone.call?.phase == .active })
        #expect(calls.answered == [id])

        // Mute from the app's own button, and from the system's: one path.
        phone.toggleMute()
        #expect(calls.asked.contains(.mute(id, true)))
        #expect(phone.call?.muted == true)
        #expect(media.muted)
        calls.ask(.mute(id, false))
        #expect(phone.call?.muted == false)
        #expect(!media.muted)

        // A keypad tone goes the same way (a car's keypad, or the app's).
        phone.sendTone("5")
        #expect(media.tones == "5")

        // The sound only ever starts when the system hands it over.
        #expect(media.systemAudioOn == nil)
        calls.onAudio?(true)
        #expect(media.systemAudioOn == true)

        phone.hangUp()
        #expect(calls.asked.contains(.end(id)))
        #expect(phone.call == nil)
        #expect(calls.ended.contains { $0.id == id && $0.ending == .hungUp })
    }

    @Test("a call the system wouldn't start says so rather than sitting there")
    func theSystemRefusesToStartACall() async throws {
        let calls = FakeSystemCalls()
        calls.startsCalls = false
        let (phone, transport, _) = await phone(calls: calls)
        phone.typed = "1024"
        phone.dial()
        // Nothing of it ever reached the line, and the person is told
        // rather than left looking at a keypad that did nothing.
        #expect(phone.call == nil)
        #expect(phone.problem != nil)
        #expect(transport.last("INVITE") == nil)
    }

    @Test("the system restarting ends the call rather than leaving it open")
    func theSystemForgetsEverything() async throws {
        let calls = FakeSystemCalls()
        let (phone, transport, _) = await phone(calls: calls)
        transport.asterisk(Self.invite(from: "1031", name: "Omar Nasser", callID: "call-6"))
        calls.ask(.answer(try #require(phone.call?.id)))
        #expect(await eventually { phone.call?.phase == .active })

        calls.forgetEverything()
        #expect(await eventually { transport.last("BYE") != nil })
    }

    @Test("a push's number and the invitation's are the same caller")
    func matchingTheCaller() {
        #expect(PhoneModel.sameNumber("+971500000001", "00971500000001"))
        #expect(PhoneModel.sameNumber("050 000 0001", "+971500000001"))
        #expect(PhoneModel.sameNumber("1031", "1031"))
        #expect(!PhoneModel.sameNumber("1031", "1042"))
        // A withheld number matches nothing, and must not match everything.
        #expect(!PhoneModel.sameNumber("", ""))
    }

    @Test("a withheld number still says who is calling")
    func aWithheldNumber() {
        #expect(PhoneModel.caller("").name == "Number withheld")
        #expect(PhoneModel.caller("  ").number.isEmpty)
        #expect(PhoneModel.caller("1031").name == "1031")
    }

    // MARK: - What a push carries, and where it goes

    @Test("a push is read for the call and the caller, and nothing else")
    func whatAPushSays() {
        let said = PushService.call(in: ["linx": ["call": "1759500000.7", "from": "+971500000001", "at": 1]])
        #expect(said?.call == "1759500000.7")
        #expect(said?.from == "+971500000001")
        // A push with no call in it is not a Linx call and is left alone.
        #expect(PushService.call(in: ["aps": [:]]) == nil)
        #expect(PushService.call(in: ["linx": ["from": "1031"]]) == nil)
        // A withheld number is a call all the same.
        #expect(PushService.call(in: ["linx": ["call": "c1"]])?.from == "")
        // The "a call is ringing" notification, which the app never shows as
        // a banner while it is open (ADR-078).
        #expect(PushService.isRingingCall(["linx": ["kind": "call"]]))
        #expect(!PushService.isRingingCall(["linx": ["kind": "voicemail"]]))
    }

    @Test("a token is sent to Linx as lower-case hex")
    func tokensAreHex() {
        #expect(PushService.hex(Data([0x00, 0xab, 0xff])) == "00abff")
        #expect(PushService.hex(nil).isEmpty)
        // Nothing is sent to Linx until Apple has given the app a token.
        #expect(!PushTokens().worthSending)
        #expect(PushTokens(voip: "aabb").worthSending)
        #expect(PushTokens(alert: "ccdd", callAlerts: true).worthSending)
    }

    @Test("which Apple this build's tokens belong to is read from its profile")
    func whichApple() {
        let development = "<key>aps-environment</key>\n\t<string>development</string>"
        #expect(PushEnvironment.value(of: "aps-environment", in: development) == "development")
        let store = "<key>aps-environment</key><string>production</string>"
        #expect(PushEnvironment.value(of: "aps-environment", in: store) == "production")
        #expect(PushEnvironment.value(of: "aps-environment", in: "<key>other</key>") == nil)
    }

    @Test("where CallKit may not be used, the app rings for itself")
    func chinaRingsInTheApp() {
        #expect(CallStyle.noCallKit.contains("CN"))
        // Everywhere else the call belongs to the system.
        #expect(!CallStyle.noCallKit.contains("AE"))
        #expect(!CallStyle.noCallKit.contains("GB"))
        // The note is said once, and only where it is true.
        if CallStyle.usesCallKit {
            #expect(CallStyle.inAppRingingNote == nil)
        } else {
            #expect(CallStyle.inAppRingingNote != nil)
        }
    }
}
