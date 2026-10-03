import Foundation
import Testing

@testable import Linx

// Making and taking a call, end to end through the app's own SIP, against a
// stand-in Asterisk inside the test process (docs/PHASE2.md §12, step 4b).
// Nothing here touches a network, a microphone or WebRTC.

/// A websocket that isn't one: it keeps what the phone sent and lets the test
/// answer as Asterisk would.
@MainActor final class FakeTransport: SIPTransport {
    var onOpen: (() -> Void)?
    var onText: ((String) -> Void)?
    var onClose: ((String?) -> Void)?

    private(set) var sent: [SIPMessage] = []
    private(set) var keepAlives = 0
    private(set) var started = false

    func start() {
        started = true
        onOpen?()
    }

    func send(_ text: String) {
        guard let message = SIPMessage(text: text) else {
            keepAlives += 1
            return
        }
        sent.append(message)
    }

    func stop() { started = false }

    /// Asterisk says something.
    func asterisk(_ message: SIPMessage) { onText?(message.text) }

    /// The last request of this kind the phone sent.
    func last(_ method: String) -> SIPMessage? { sent.last { $0.method == method } }

    func count(_ method: String) -> Int { sent.filter { $0.method == method }.count }
}

/// The sound of a call, without any.
@MainActor final class FakeMedia: SIPCallMedia {
    var onConnection: ((MediaConnection) -> Void)?
    private(set) var acceptedAnswer: String?
    private(set) var answeredOffer: String?
    private(set) var stopped = false
    private(set) var muted = false
    private(set) var speaker = false
    private(set) var tones = ""

    func offer() async throws -> String { "v=0\r\nlinx-offer\r\n" }

    func answer(to offer: String) async throws -> String {
        answeredOffer = offer
        return "v=0\r\nlinx-answer\r\n"
    }

    func accept(answer: String) async throws { acceptedAnswer = answer }
    func setMuted(_ muted: Bool) { self.muted = muted }
    func setSpeaker(_ on: Bool) { speaker = on }
    func sendTone(_ digit: Character) { tones.append(digit) }
    func stop() { stopped = true }
}

@MainActor
struct CallTests {
    private static let line = PhoneLine(
        deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
        password: "a-password-in-memory-only", sipURI: "sip:d_Ab12Cd34@pbx.example.com",
        websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
        turn: PhoneLine.Turn(
            urls: ["turns:pbx.example.com:443?transport=tcp"], username: "u", credential: "c",
            expiresAt: Date(timeIntervalSinceNow: 3600)))

    private static var account: SIPUserAgent.Account {
        SIPUserAgent.Account(
            username: line.sipUsername, password: line.password, domain: "pbx.example.com",
            displayName: line.displayName, websocket: URL(string: "wss://pbx.example.com/sip")!,
            token: "a-device-token")
    }

    /// Waits for something the app does in a Task of its own.
    private func eventually(_ check: @MainActor () -> Bool) async -> Bool {
        for _ in 0..<200 {
            if check() { return true }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return check()
    }

    private func signedIn() async -> (SIPUserAgent, FakeTransport, FakeMedia, Box) {
        let transport = FakeTransport()
        let media = FakeMedia()
        let box = Box()
        let agent = SIPUserAgent(account: Self.account, transport: transport, media: { media })
        agent.onStatus = { box.status = $0 }
        agent.onIncoming = { box.incoming = $0 }
        agent.onProgress = { box.ringingThere = true }
        agent.onEstablished = { box.established = true }
        agent.onEnded = { box.ended = $0 }
        agent.start()
        // Asterisk always challenges the first REGISTER.
        let first = transport.last("REGISTER")!
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add(
            "WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\", algorithm=MD5")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        return (agent, transport, media, box)
    }

    @MainActor final class Box {
        var status: SIPStatus?
        var incoming: SIPPeer?
        var ringingThere = false
        var established = false
        var ended: SIPEnded?
    }

    @Test("the phone line signs in, answering Asterisk's challenge once")
    func registers() async throws {
        let (agent, transport, _, box) = await signedIn()
        #expect(box.status == .ready)
        #expect(transport.count("REGISTER") == 2)
        let signedIn = try #require(transport.last("REGISTER"))
        let authorization = try #require(signedIn.first("Authorization"))
        #expect(authorization.contains("username=\"d_Ab12Cd34\""))
        #expect(authorization.contains("realm=\"asterisk\""))
        #expect(!authorization.contains(Self.line.password))
        // The relay only forwards a REGISTER whose From and To are this
        // line's own username (internal/siprelay).
        #expect(SIPMessage.user(of: signedIn.first("From") ?? "") == "d_Ab12Cd34")
        #expect(SIPMessage.user(of: signedIn.first("To") ?? "") == "d_Ab12Cd34")
        #expect(signedIn.first("Contact")?.contains("transport=ws") == true)
        #expect(signedIn.first("Expires") == "300")
        agent.stop()
    }

    @Test("a call out: invite, ringing, answered, hung up")
    func callsOut() async throws {
        let (agent, transport, media, box) = await signedIn()
        await agent.call("1024", name: "Sara Haddad")

        let invite = try #require(transport.last("INVITE"))
        #expect(invite.requestURI == "sip:1024@pbx.example.com")
        #expect(invite.body.contains("linx-offer"))
        #expect(invite.first("Content-Type") == "application/sdp")
        #expect(SIPMessage.user(of: invite.first("From") ?? "") == "d_Ab12Cd34")

        transport.asterisk(SIPMessage.response(100, "Trying", to: invite))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        #expect(box.ringingThere)

        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.add("Record-Route", "<sip:1.2.3.4:8089;transport=ws;lr>")
        ok.body = "v=0\r\ntheir-answer\r\n"
        transport.asterisk(ok)

        #expect(await eventually { box.established })
        #expect(media.acceptedAnswer?.contains("their-answer") == true)
        let ack = try #require(transport.last("ACK"))
        #expect(ack.requestURI == "sip:1024@1.2.3.4:8089;transport=ws")
        #expect(ack.cseq?.number == invite.cseq?.number)
        #expect(ack.first("To")?.contains("tag=theirs") == true)

        agent.hangUp()
        let bye = try #require(transport.last("BYE"))
        #expect(bye.requestURI == "sip:1024@1.2.3.4:8089;transport=ws")
        #expect(bye.first("Route") == "<sip:1.2.3.4:8089;transport=ws;lr>")
        #expect(bye.first("To")?.contains("tag=theirs") == true)
        #expect(media.stopped)
        #expect(box.ended == .hungUp)
        agent.stop()
    }

    @Test("a call Asterisk challenges is tried once more with the password")
    func challengedCall() async throws {
        let (agent, transport, _, box) = await signedIn()
        await agent.call("1024")
        let invite = try #require(transport.last("INVITE"))
        var challenge = SIPMessage.response(407, "Proxy Authentication Required", to: invite)
        challenge.add("Proxy-Authenticate", "Digest realm=\"asterisk\", nonce=\"n2\", qop=\"auth\"")
        transport.asterisk(challenge)

        #expect(transport.count("ACK") == 1)  // a refusal is acknowledged
        #expect(transport.count("INVITE") == 2)
        let again = try #require(transport.last("INVITE"))
        #expect(again.first("Authorization")?.contains("nonce=\"n2\"") == true)
        #expect(again.cseq?.number == (invite.cseq?.number ?? 0) + 1)
        #expect(box.ended == nil)
        agent.stop()
    }

    @Test("a busy number ends the call with words a person understands")
    func busy() async throws {
        let (agent, transport, media, box) = await signedIn()
        await agent.call("1024")
        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(486, "Busy Here", to: invite).withTag("theirs"))
        #expect(transport.count("ACK") == 1)
        #expect(box.ended == .failed("They're on another call."))
        #expect(media.stopped)
        agent.stop()
    }

    @Test("hanging up before they answer cancels the invitation and still acknowledges it")
    func cancels() async throws {
        let (agent, transport, _, box) = await signedIn()
        await agent.call("1024")
        let invite = try #require(transport.last("INVITE"))
        transport.asterisk(SIPMessage.response(180, "Ringing", to: invite).withTag("theirs"))
        agent.hangUp()

        let cancel = try #require(transport.last("CANCEL"))
        #expect(cancel.cseq?.method == "CANCEL")
        #expect(cancel.cseq?.number == invite.cseq?.number)
        #expect(cancel.first("Call-ID") == invite.first("Call-ID"))
        #expect(box.ended == .hungUp)

        // Asterisk's final word still arrives, and is acknowledged.
        transport.asterisk(SIPMessage.response(487, "Request Terminated", to: invite).withTag("theirs"))
        #expect(transport.count("ACK") == 1)
        agent.stop()
    }

    @Test("a call in: ringing, answered with an answer, then they hang up")
    func callsIn() async throws {
        let (agent, transport, media, box) = await signedIn()
        transport.asterisk(Self.incomingInvite())

        #expect(box.incoming?.number == "1031")
        #expect(box.incoming?.name == "Omar Nasser")
        #expect(transport.sent.contains { $0.status == 100 })
        let ringing = try #require(transport.sent.last { $0.status == 180 })
        #expect(ringing.toTag != nil)
        #expect(ringing.first("Contact")?.contains("d_Ab12Cd34") == true)

        await agent.answer()
        let ok = try #require(transport.sent.last { $0.status == 200 })
        #expect(ok.body.contains("linx-answer"))
        #expect(media.answeredOffer?.contains("their-offer") == true)
        #expect(box.established)

        var bye = SIPMessage.request("BYE", "sip:d_Ab12Cd34@abc.invalid")
        bye.add("Via", "SIP/2.0/WSS 1.2.3.4;branch=z9hG4bK9")
        bye.add("From", "<sip:1031@pbx.example.com>;tag=theirs")
        bye.add("To", "<sip:d_Ab12Cd34@pbx.example.com>;tag=mine")
        bye.add("Call-ID", "call-in-1")
        bye.add("CSeq", "2 BYE")
        transport.asterisk(bye)
        #expect(box.ended == .hungUp)
        #expect(media.stopped)
        #expect(transport.sent.last?.status == 200)
        agent.stop()
    }

    @Test("a caller who gives up leaves a missed call, not a failure")
    func missed() async throws {
        let (agent, transport, _, box) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        var cancel = SIPMessage.request("CANCEL", "sip:d_Ab12Cd34@abc.invalid")
        cancel.add("Via", "SIP/2.0/WSS 1.2.3.4;branch=z9hG4bK8")
        cancel.add("From", "<sip:1031@pbx.example.com>;tag=theirs")
        cancel.add("To", "<sip:d_Ab12Cd34@pbx.example.com>")
        cancel.add("Call-ID", "call-in-1")
        cancel.add("CSeq", "1 CANCEL")
        transport.asterisk(cancel)
        #expect(box.ended == .missed)
        #expect(transport.sent.contains { $0.status == 487 })
        agent.stop()
    }

    @Test("a second caller is told the phone is busy")
    func oneCallAtATime() async throws {
        let (agent, transport, _, _) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        await agent.answer()
        var second = Self.incomingInvite()
        second.set("Call-ID", "call-in-2")
        transport.asterisk(second)
        #expect(transport.sent.last?.status == 486)
        agent.stop()
    }

    @Test("the phone only ever sends requests Linx's relay allows")
    func onlyAllowedRequests() async throws {
        let (agent, transport, _, _) = await signedIn()
        await agent.call("1024")
        let invite = try #require(transport.last("INVITE"))
        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = "v=0\r\ntheir-answer\r\n"
        transport.asterisk(ok)
        _ = await eventually { transport.last("ACK") != nil }
        agent.hangUp()
        transport.asterisk(Self.incomingInvite())
        agent.decline()

        // internal/siprelay's allowedMethods, and nothing else; every request
        // is From this line's own username, which the relay checks too.
        let allowed: Set<String> = [
            "REGISTER", "INVITE", "ACK", "BYE", "CANCEL", "OPTIONS", "INFO", "UPDATE", "PRACK",
            "MESSAGE", "NOTIFY", "SUBSCRIBE", "REFER",
        ]
        for request in transport.sent where request.method != nil {
            #expect(allowed.contains(request.method ?? ""))
            #expect(SIPMessage.user(of: request.first("From") ?? "") == "d_Ab12Cd34")
        }
        agent.stop()
    }

    @Test("mute, the loudspeaker and keypad tones reach the call's sound")
    func duringACall() async throws {
        let (agent, transport, media, _) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        await agent.answer()
        agent.setMuted(true)
        agent.setSpeaker(true)
        agent.sendTone("5")
        #expect(media.muted)
        #expect(media.speaker)
        #expect(media.tones == "5")
        agent.stop()
    }

    @Test("losing the connection ends the call and says the line is coming back")
    func dropped() async throws {
        let (agent, transport, media, box) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        await agent.answer()
        transport.onClose?("the network went away")
        #expect(box.status == .reconnecting)
        #expect(media.stopped)
        if case .failed(let said) = box.ended {
            #expect(said.contains("lost touch"))
        } else {
            Issue.record("the call should have ended")
        }
        agent.stop()
    }

    static func incomingInvite() -> SIPMessage {
        var invite = SIPMessage.request("INVITE", "sip:d_Ab12Cd34@abc.invalid;transport=ws")
        invite.add("Via", "SIP/2.0/WSS 1.2.3.4:8089;branch=z9hG4bK7")
        invite.add("From", "\"Omar Nasser\" <sip:1031@pbx.example.com>;tag=theirs")
        invite.add("To", "<sip:d_Ab12Cd34@pbx.example.com>")
        invite.add("Call-ID", "call-in-1")
        invite.add("CSeq", "1 INVITE")
        invite.add("Contact", "<sip:1031@1.2.3.4:8089;transport=ws>")
        invite.add("Content-Type", "application/sdp")
        invite.body = "v=0\r\ntheir-offer\r\n"
        return invite
    }
}
