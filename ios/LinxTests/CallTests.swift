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
    /// Linx said the line carries call links; what went to them.
    var carriesCallLinks = false
    var carriesSIPInLinks = false
    private(set) var side: [String] = []
    private(set) var started = false

    func start() {
        started = true
        onOpen?()
    }

    func send(_ text: String) {
        if text.first == "{" {
            side.append(text)
            return
        }
        guard let message = SIPMessage(text: text) else {
            keepAlives += 1
            return
        }
        sent.append(message)
    }

    func stop() { started = false }

    /// The connection drops under the phone (it moved between networks).
    func drop() { onClose?("The network connection was lost.") }

    /// Asterisk says something.
    func asterisk(_ message: SIPMessage) { onText?(message.text) }

    /// The last request of this kind the phone sent.
    func last(_ method: String) -> SIPMessage? { sent.last { $0.method == method } }

    func count(_ method: String) -> Int { sent.filter { $0.method == method }.count }
}

/// The sound of a call, without any.
@MainActor final class FakeMedia: SIPCallMedia {
    var onConnection: ((MediaConnection) -> Void)?
    var onRoute: ((AudioRoute) -> Void)?
    var onRouteTrouble: ((String) -> Void)?
    var diagnostics = CallDiagnostics()
    var onDiagnostics: ((CallDiagnostics) -> Void)?
    private(set) var acceptedAnswer: String?
    private(set) var answeredOffer: String?
    private(set) var stopped = false
    private(set) var muted = false
    private(set) var speaker = false
    private(set) var tones = ""
    /// Whether the system has handed over the microphone and the speaker.
    private(set) var systemAudioOn: Bool?

    // The picture (ADR-087). The camera and the link are make-believe: what
    // the tests check is that SIP never carries them.
    var video = CallVideo()
    var onVideoChanged: ((CallVideo) -> Void)?
    var onVideoTooExpensive: (() -> Void)?
    var onAnsweredAt: ((Date) -> Void)?
    let tracks = VideoTracks()
    var mirrorsMyVideo = true
    private(set) var rolledBack = false
    private(set) var cameraSwitched = 0
    /// Set to make the camera refuse, as one does when the person said no.
    var cameraRefuses: CameraTrouble?
    /// How often the picture link was opened, and whether as a video call.
    /// Told the call is up (the link opens, or learns it at the answer).
    private(set) var pictureLinks = 0
    private(set) var pictureLinksWhileRinging = 0
    /// The first link it was given.
    private(set) var pictureLink: (any PictureLink)?
    private(set) var pictureCameraFirst: Bool?
    /// Every on and off asked of the camera.
    private(set) var cameraCalls: [Bool] = []

    func offer() async throws -> String { Self.soundOffer }

    func answer(to offer: String) async throws -> String {
        answeredOffer = offer
        return Self.soundAnswer
    }

    /// The answer to a picture offer held while ringing, if there is one.
    var pictureAnswer: Data?

    func pictureAnswerWithTheCall() async -> Data? { pictureAnswer }

    func startPicture(link: any PictureLink, cameraFirst: Bool, ringing: Bool) {
        if pictureLink == nil { pictureLink = link }
        if ringing {
            pictureLinksWhileRinging += 1
        } else {
            pictureLinks += 1
        }
        pictureCameraFirst = cameraFirst
    }

    func setCamera(_ on: Bool) async throws {
        if on, let cameraRefuses { throw cameraRefuses }
        cameraCalls.append(on)
        var now = video
        now.mine = on
        setVideo(now)
    }

    /// The other side can't show a picture (a desk phone), as the call link
    /// would say it.
    func pretendNoPicture() {
        var now = video
        now.available = false
        setVideo(now)
    }

    func switchCamera() { cameraSwitched += 1 }
    var onGroupMove: ((GroupJoin) -> Void)?
    var onGroupEnded: ((String) -> Void)?
    var onGroupChanged: (() -> Void)?
    /// The group calls this call was moved into.
    private(set) var groupsJoined: [GroupJoin] = []
    /// The room as the screen sees it, kept so a test can change who is in
    /// it and what they are sending.
    private(set) var groupPeople: GroupPeople?
    var inGroup: Bool { !groupsJoined.isEmpty && !stopped }
    /// Whether each group call was joined with the camera on.
    private(set) var groupCameras: [Bool] = []
    func joinGroup(_ join: GroupJoin, server: URL, camera: Bool) -> GroupPeople? {
        groupsJoined.append(join)
        groupCameras.append(camera)
        let people = GroupPeople()
        groupPeople = people
        return people
    }

    /// Who is in the group call now, as LiveKit would have said it.
    func pretendGroup(_ people: [GroupPerson]) {
        groupPeople?.people = people
        groupPeople?.camera = people.first { $0.me }?.video ?? false
        onGroupChanged?()
    }
    var hd: [Bool] = []
    func useHD(_ on: Bool) { hd.append(on) }

    private(set) var relayMoves = 0
    private(set) var steps: [String] = []
    static let relayOffer = "v=0\r\no=- 3 3 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\na=ice-ufrag:relay\r\n"
    func moveToRelay() async throws -> String {
        relayMoves += 1
        return Self.relayOffer
    }
    var onRouteLost: (() -> Void)?
    private(set) var routeRestarts = 0
    func restartRoute() async throws -> String {
        routeRestarts += 1
        return Self.relayOffer
    }
    func mark(_ what: String) { steps.append(what) }
    func began(at: Date) { steps.insert("began", at: 0) }

    func rollbackOffer() async { rolledBack = true }

    private func setVideo(_ now: CallVideo) {
        guard now != video else { return }
        video = now
        onVideoChanged?(now)
    }

    static let soundOffer = "v=0\r\na=linx-offer\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=sendrecv\r\n"
    static let soundAnswer = "v=0\r\na=linx-answer\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=sendrecv\r\n"

    private(set) var answersAccepted = 0
    func accept(answer: String) async throws {
        acceptedAnswer = answer
        answersAccepted += 1
    }
    /// The sound's route went, as WebRTC would say it.
    func loseTheRoute() { onRouteLost?() }
    func setMuted(_ muted: Bool) { self.muted = muted }
    func setSpeaker(_ on: Bool) {
        speaker = on
        onRoute?(AudioRoute(name: "Speaker", speaker: on, builtIn: true))
    }
    private(set) var relayGiven: PhoneLine.Turn?
    func use(relay: PhoneLine.Turn) { relayGiven = relay }

    /// What a call found out about itself, as WebRTCMedia would say it.
    func found(_ what: CallDiagnostics) {
        diagnostics = what
        onDiagnostics?(what)
    }

    /// Where the sound is, as the system would say it.
    func soundComesOut(of route: AudioRoute) { onRoute?(route) }

    /// Their camera came on, or went off, as the picture link would say it.
    func pretendTheirVideo(_ on: Bool) {
        var now = video
        now.available = true
        now.theirs = on
        now.theirPicture = on
        setVideo(now)
    }
    func sendTone(_ digit: Character) { tones.append(digit) }
    func systemAudio(_ on: Bool) { systemAudioOn = on }
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
            expiresAt: Date(timeIntervalSinceNow: 3600), maxBitrateBps: 1_280_000))

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
        agent.onIncoming = { peer, _ in box.incoming = peer }
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

    @Test("on a line whose call links take SIP, the picture's answer goes in the 200 OK's own message")
    func answerCarriesThePicture() async throws {
        let (agent, transport, media, box) = await signedIn()
        transport.carriesCallLinks = true
        transport.carriesSIPInLinks = true
        var invite = Self.incomingInvite()
        invite.add("X-Linx-Video", "yes")
        transport.asterisk(invite)
        let link = try #require(media.pictureLink as? LineLink)
        link.start()
        media.pictureAnswer = Data(#"{"type":"signal","data":{"session":7}}"#.utf8)

        await agent.answer()
        #expect(!transport.sent.contains { $0.status == 200 })
        let both = try #require(transport.side.last)
        let parts = try #require(
            try JSONSerialization.jsonObject(with: Data(both.utf8)) as? [String: Any])
        #expect(parts["call"] as? String == "call-in-1")
        #expect((parts["msg"] as? [String: Any])?["type"] as? String == "signal")
        let sip = try #require(parts["sip"] as? String)
        let ok = try #require(SIPMessage(text: sip))
        #expect(ok.status == 200)
        #expect(ok.body.contains("linx-answer"))
        #expect(box.established)
        agent.stop()
    }

    @Test("a line whose call links don't take SIP sends the 200 OK on its own")
    func answerAloneOnOlderServers() async throws {
        let (agent, transport, media, _) = await signedIn()
        transport.carriesCallLinks = true
        transport.asterisk(Self.incomingInvite())
        let link = try #require(media.pictureLink as? LineLink)
        link.start()
        media.pictureAnswer = Data(#"{"type":"signal","data":{}}"#.utf8)
        await agent.answer()
        #expect(transport.sent.contains { $0.status == 200 })
        #expect(!transport.side.contains { $0.contains("\"sip\"") })
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

    @Test("losing the connection holds the call, and ends it if the line doesn't come back in time")
    func dropped() async throws {
        let transport = FakeTransport()
        let media = FakeMedia()
        let box = Box()
        let agent = SIPUserAgent(
            account: Self.account, transport: transport, media: { media }, holdFor: .milliseconds(100))
        agent.onStatus = { box.status = $0 }
        agent.onEnded = { box.ended = $0 }
        agent.start()
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        transport.asterisk(Self.incomingInvite())
        await agent.answer()
        transport.onClose?("the network went away")
        #expect(box.status == .reconnecting)
        #expect(!media.stopped)
        #expect(box.ended == nil)
        #expect(await eventually { box.ended != nil })
        #expect(media.stopped)
        if case .failed(let said) = box.ended {
            #expect(said.contains("lost touch"))
        } else {
            Issue.record("the call should have ended")
        }
        agent.stop()
    }

    @Test("losing the connection before the call is answered ends it")
    func droppedWhileRinging() async throws {
        let (agent, transport, media, box) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        transport.onClose?("the network went away")
        #expect(media.stopped)
        if case .failed = box.ended {} else { Issue.record("the call should have ended") }
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

// A phone between networks (2026-10-13, found on the phone bench): the
// answer repeated on a slow link, the line to Linx dropping in the middle of
// a call, and the sound's route lost with it.
extension CallTests {
    /// A call out, answered and up.
    private func callUp() async throws -> (SIPUserAgent, FakeTransport, FakeMedia, Box, SIPMessage) {
        let (agent, transport, media, box) = await signedIn()
        await agent.call("1024")
        let invite = try #require(transport.last("INVITE"))
        var ok = SIPMessage.response(200, "OK", to: invite).withTag("theirs")
        ok.add("Contact", "<sip:1024@1.2.3.4:8089;transport=ws>")
        ok.body = "v=0\r\ntheir-answer\r\n"
        transport.asterisk(ok)
        #expect(await eventually { box.established })
        return (agent, transport, media, box, ok)
    }

    @Test("the same answer arriving twice is acknowledged twice and changes nothing")
    func repeatedAnswer() async throws {
        let (agent, transport, media, box, ok) = try await callUp()
        transport.asterisk(ok)
        #expect(await eventually { transport.count("ACK") == 2 })
        try? await Task.sleep(for: .milliseconds(50))
        #expect(media.answersAccepted == 1)
        #expect(transport.count("BYE") == 0)
        #expect(box.ended == nil)
        agent.stop()
    }

    @Test("a dropped line holds the call, and the new one tells Asterisk where the phone is")
    func lineComesBack() async throws {
        let (agent, transport, media, box, _) = try await callUp()
        transport.drop()
        #expect(box.ended == nil)
        #expect(agent.holdingACall)
        #expect(box.status == .reconnecting)
        #expect(!media.stopped)

        let fresh = FakeTransport()
        agent.reattach(account: Self.account, transport: fresh)
        let register = try #require(fresh.last("REGISTER"))
        fresh.asterisk(SIPMessage.response(200, "OK", to: register))
        let update = try #require(fresh.last("UPDATE"))
        #expect(update.callID == transport.last("INVITE")?.callID)
        #expect(update.requestURI == "sip:1024@1.2.3.4:8089;transport=ws")
        #expect(update.first("Contact")?.contains("transport=ws") == true)
        #expect(update.body.isEmpty)
        fresh.asterisk(SIPMessage.response(200, "OK", to: update))
        #expect(!agent.holdingACall)

        agent.hangUp()
        #expect(fresh.last("BYE") != nil)
        #expect(box.ended == .hungUp)
        agent.stop()
    }

    @Test("a line Linx closed on purpose ends the call at once: a stopped phone isn't held")
    func lineClosedByLinx() async throws {
        let (agent, transport, media, box, _) = try await callUp()
        transport.onClose?(WebSocketTransport.closedByLinx + "signed out")
        #expect(!agent.holdingACall)
        if case .failed = box.ended {} else { Issue.record("the call should have ended") }
        #expect(media.stopped)
        agent.stop()
    }

    @Test("a call Asterisk no longer has ends when the line comes back")
    func lineComesBackTooLate() async throws {
        let (agent, transport, _, box, _) = try await callUp()
        transport.drop()
        let fresh = FakeTransport()
        agent.reattach(account: Self.account, transport: fresh)
        fresh.asterisk(SIPMessage.response(200, "OK", to: try #require(fresh.last("REGISTER"))))
        fresh.asterisk(
            SIPMessage.response(481, "Call/Transaction Does Not Exist", to: try #require(fresh.last("UPDATE"))))
        if case .failed = box.ended {
        } else {
            Issue.record("the call should have ended, not \(String(describing: box.ended))")
        }
        agent.stop()
    }

    @Test("a lost sound route is looked for again once the line is back")
    func routeRestartsAfterTheLine() async throws {
        let (agent, transport, media, _, _) = try await callUp()
        transport.drop()
        media.loseTheRoute()
        try? await Task.sleep(for: .milliseconds(50))
        #expect(media.routeRestarts == 0)
        #expect(transport.count("INVITE") == 1)

        let fresh = FakeTransport()
        agent.reattach(account: Self.account, transport: fresh)
        fresh.asterisk(SIPMessage.response(200, "OK", to: try #require(fresh.last("REGISTER"))))
        fresh.asterisk(SIPMessage.response(200, "OK", to: try #require(fresh.last("UPDATE"))))
        #expect(await eventually { media.routeRestarts == 1 })
        #expect(await eventually { fresh.last("INVITE") != nil })
        #expect(fresh.last("INVITE")?.body == FakeMedia.relayOffer)
        agent.stop()
    }

    @Test("a late refusal of an earlier change neither ends the call nor speaks for the caller")
    func lateRefusalAfterAMove() async throws {
        // 2026-10-17 on Etisalat: a call this phone was rung for moved its
        // route, its change was answered, and a late 500 for it came after. Acknowledged from the first invitation, the ACK carried the
        // caller's From, the relay closed the line and the call dropped.
        let (agent, transport, _, box) = await signedIn()
        transport.asterisk(Self.incomingInvite())
        await agent.answer()
        await agent.moveToRelay()
        let change = try #require(transport.last("INVITE"))
        #expect(change.body == FakeMedia.relayOffer)
        transport.asterisk(SIPMessage.response(200, "OK", to: change).withTag("theirs"))
        transport.asterisk(
            SIPMessage.response(500, "Another INVITE transaction in progress", to: change).withTag("theirs"))
        #expect(box.ended == nil)
        let ack = try #require(transport.last("ACK"))
        #expect(SIPMessage.user(of: ack.first("From") ?? "") == "d_Ab12Cd34")
        #expect(ack.cseq?.number == change.cseq?.number)
        for request in transport.sent where request.method != nil {
            #expect(SIPMessage.user(of: request.first("From") ?? "") == "d_Ab12Cd34")
        }
        agent.stop()
    }

    @Test("a lost sound route on a line that is up is looked for again at once")
    func routeRestarts() async throws {
        let (agent, transport, media, _, _) = try await callUp()
        media.loseTheRoute()
        #expect(await eventually { media.routeRestarts == 1 })
        #expect(await eventually { transport.count("INVITE") == 2 })
        agent.stop()
    }
}
