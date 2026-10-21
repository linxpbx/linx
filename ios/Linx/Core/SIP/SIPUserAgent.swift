import Foundation

// The app's SIP user agent: signing the phone line in, making a call, taking
// a call, and ending one (ADR-006, docs/PHASE2.md §7). It speaks only the
// requests Linx's relay allows — REGISTER, INVITE, ACK, BYE, CANCEL — and
// answers the ones Asterisk sends it. Everything else a telephone switch can
// say is Asterisk's business, not a phone's.
//
// Nothing here knows about microphones: the sound is behind SIPCallMedia, so
// the whole flow can be tested without WebRTC, a simulator or a network.

/// Who is on the other end of a call.
struct SIPPeer: Equatable, Sendable {
    var name: String
    var number: String
}

/// Why a call ended, in words the screens show as they are.
enum SIPEnded: Equatable, Sendable {
    /// Someone hung up: a finished call, not a problem.
    case hungUp
    /// The call never started, and why in plain words.
    case failed(String)
    /// An incoming call the person never picked up (the caller gave up).
    case missed
}

/// Where the phone line stands.
enum SIPStatus: Equatable, Sendable {
    case connecting
    case ready
    /// The connection dropped; the app is getting it back.
    case reconnecting
    /// It can't sign in, and why.
    case unavailable(String)
}

/// Where a call's sound is coming out. `builtIn` is the phone's own
/// earpiece or loudspeaker, which is a plain switch between two; anything
/// else — a headset, a car — is one of several, and the system's own picker
/// chooses between them, exactly as the Phone app does.
struct AudioRoute: Equatable, Sendable {
    let name: String
    let speaker: Bool
    let builtIn: Bool
    /// Whether there is anywhere *else* the sound could come out — AirPods, a
    /// headset, a car — whether or not it has moved there yet. It is what
    /// turns the plain Speaker switch into the system's own picker, so a pair
    /// of AirPods connected in the middle of a call can be chosen there and
    /// then instead of on the next call (owner, 2026-10-05).
    var others = false
}

/// The sound of a call, from the user agent's side. WebRTCMedia is the real
/// one; the tests use a stand-in.
extension SIPCallMedia {
    func pictureAnswerWithTheCall() async -> Data? { nil }
    func mark(_ what: String, at: Date) { mark(what) }
}

@MainActor protocol SIPCallMedia: AnyObject {
    /// How the sound is getting through, read every few seconds while a call
    /// is up: direct, or through Linx's relay.
    var onConnection: ((MediaConnection) -> Void)? { get set }
    /// An SDP offer with this phone's candidates already in it: Asterisk
    /// doesn't take them one at a time (docs/WEB.md §6).
    func offer() async throws -> String
    /// An answer to the other side's offer, candidates in it too.
    func answer(to offer: String) async throws -> String
    /// The other side's answer to the offer this phone sent.
    func accept(answer: String) async throws
    /// Newer credentials for Linx's call relay, while a call is up: the
    /// old ones last an hour and a call can outlive them.
    func use(relay: PhoneLine.Turn)
    func setMuted(_ muted: Bool)
    /// The loudspeaker, or back to the earpiece.
    func setSpeaker(_ on: Bool)
    /// Where the sound is actually coming out, read from the system each
    /// time it moves, never assumed.
    var onRoute: ((AudioRoute) -> Void)? { get set }
    /// The sound wouldn't move where the person asked it to.
    var onRouteTrouble: ((String) -> Void)? { get set }
    /// Why the call sounds the way it does — the routes this phone found, what
    /// Linx's relay said, how far the connection got. A diagnostic for the
    /// person (`CallDetailsView`), never a decision.
    var diagnostics: CallDiagnostics { get }
    var onDiagnostics: ((CallDiagnostics) -> Void)? { get set }
    /// Whose camera is on in this call, and the two pictures themselves.
    /// The picture travels on a direct link between the two devices
    /// (ADR-087), never through Asterisk, which carries the sound only.
    var video: CallVideo { get }
    var tracks: VideoTracks { get }
    var onVideoChanged: ((CallVideo) -> Void)? { get set }
    /// The link hasn't the room for a picture any more.
    var onVideoTooExpensive: (() -> Void)? { get set }
    /// The server's moment of the answer, from the call link: the call's
    /// timer counts from it, on every device on the call alike.
    var onAnsweredAt: ((Date) -> Void)? { get set }
    /// Opens the picture link while the call rings (`ringing`), so the
    /// connection is made before the answer, and is told again at the
    /// answer; `cameraFirst` turns this phone's camera on as soon as the
    /// other side is there (a video call).
    func startPicture(link: any PictureLink, cameraFirst: Bool, ringing: Bool)
    /// The answer to a picture offer held while the call rang, made now to
    /// go in the same message as the call's 200 OK; nil if there is none.
    func pictureAnswerWithTheCall() async -> Data?
    /// This phone's camera, on or off. Nothing about the call's sound
    /// changes, and nothing goes through the phone system.
    func setCamera(_ on: Bool) async throws
    func switchCamera()
    /// The other side made this a group call (ADR-088).
    var onGroupMove: ((GroupJoin) -> Void)? { get set }
    /// The group call ended for this phone, and why in words.
    var onGroupEnded: ((String) -> Void)? { get set }
    /// Who is in the group call, or what they are sending, has changed.
    var onGroupChanged: (() -> Void)? { get set }
    /// Moves the call onto the video server: its sound and pictures go
    /// there, and the phone call's own sound goes quiet. Nil if it can't.
    func joinGroup(_ join: GroupJoin, server: URL, camera: Bool) -> GroupPeople?
    /// In a group call: the phone call may end without the call ending.
    var inGroup: Bool { get }
    /// The **HD** button: whether this phone's picture may go above 540p.
    func useHD(_ on: Bool)
    /// Moves a call that is up onto Linx's relay (an ICE restart that allows
    /// only the relay) and answers with the offer that tells the other side.
    func moveToRelay() async throws -> String
    /// The sound's route has gone — a network change cut it — and stayed
    /// gone: the user agent looks for a new one (`restartRoute`).
    var onRouteLost: (() -> Void)? { get set }
    /// Looks for routes afresh (an ICE restart, every route allowed, the
    /// relay's credentials renewed) and answers with the offer that tells
    /// the other side.
    func restartRoute() async throws -> String
    /// One step of setting the call up, for the timeline on Call details.
    func mark(_ what: String)
    /// When the person asked for the call: the timeline counts from there.
    func began(at: Date)
    /// A step that happened before the call's media was made (a woken
    /// phone's push, its line signing in), put in its place on the timeline.
    func mark(_ what: String, at: Date)
    /// Nobody accepted the last offer: put the call back exactly as it was,
    /// so the sound carries on untouched.
    func rollbackOffer() async
    /// Whether this phone's own picture is shown mirrored (a front camera).
    var mirrorsMyVideo: Bool { get }
    /// The system has handed over the microphone and the speaker, or taken
    /// them back. With CallKit the app never starts a call's sound itself:
    /// it waits to be given them, which is also what stops the sound
    /// cutting out on an answered call (docs/PHASE2.md §14, "Audio path").
    func systemAudio(_ on: Bool)
    func sendTone(_ digit: Character)
    /// The call is over: everything the phone had open for it closes.
    func stop()
}

@MainActor final class SIPUserAgent {
    /// This phone's SIP login, from POST /api/v1/me/phone-line. The password
    /// is in memory only and is new every time the app starts.
    struct Account: Sendable {
        var username: String
        var password: String
        var domain: String
        var displayName: String
        var websocket: URL
        /// The device token, which is how the relay knows this phone
        /// (docs/PHASE2.md §12, "Step 4a, as built").
        var token: String
    }

    var onStatus: ((SIPStatus) -> Void)?
    /// A call coming in, and whether it is a video call (X-Linx-Video on
    /// their invitation, ADR-087).
    var onIncoming: ((SIPPeer, Bool) -> Void)?
    /// Their phone is ringing (180 or 183).
    var onProgress: (() -> Void)?
    /// Sound arrived before the answer (a 183 with a body): the ringing
    /// tone the provider sends, or "the number you have dialled…". The
    /// phone stops making a ringing tone of its own.
    var onEarlySound: (() -> Void)?
    var onEstablished: (() -> Void)?
    var onEnded: ((SIPEnded) -> Void)?
    /// A picture couldn't be added, and why in plain words.
    var onVideoRefused: ((String) -> Void)?
    /// Every SIP message of a call, by its first line ("→ 200 OK (INVITE)"),
    /// for the record the phone sends Linx after the call. Signing in and
    /// keep-alives are left out; nothing but the first line ever is.
    var onTrace: ((String) -> Void)?

    /// Who this phone is on the line. A new connection after a dropped one
    /// brings a new password (`reattach`), never a new username.
    private var account: Account
    private var transport: any SIPTransport
    private let makeMedia: @MainActor () -> any SIPCallMedia
    /// The call link for a call's SIP Call-ID (`CallLinkSocket`).
    private let customLink: (@MainActor (String) -> (any PictureLink)?)?
    /// A host that is this phone and nowhere else: .invalid is reserved for
    /// exactly this (RFC 2606), and the websocket, not the address, is what
    /// Asterisk answers on.
    private let localHost = SIPRandom.token(10) + ".invalid"

    private var registration = Registration()
    /// Signing the line out (`signOff`): the REGISTER that says so, and a
    /// second go at it after a challenge, both carry expires=0.
    private var leaving = false
    private var signingOff: CheckedContinuation<Void, Never>?
    private var call: Call?
    private var registerTimer: Task<Void, Never>?
    /// Invitations this phone gave up on, kept only long enough to answer
    /// Asterisk's last word about them (RFC 3261 says every final answer to
    /// an INVITE is acknowledged, even a refusal).
    private var abandoned: [String: SIPMessage] = [:]

    /// How long the registration lasts; the app signs in again halfway
    /// through, which is also what keeps the websocket alive.
    static let registerSeconds = 300
    /// A request with no final answer by then has failed (RFC 3261 Timer B).
    static let answerWithin: Duration = .seconds(32)

    init(
        account: Account, transport: any SIPTransport,
        media: @escaping @MainActor () -> any SIPCallMedia,
        link: (@MainActor (String) -> (any PictureLink)?)? = nil,
        holdFor: Duration = SIPUserAgent.holdTheCallFor
    ) {
        self.holdFor = holdFor
        self.account = account
        self.transport = transport
        self.makeMedia = media
        self.customLink = link
        listen(to: transport)
    }

    private func listen(to transport: any SIPTransport) {
        transport.onOpen = { [weak self] in
            self?.lineIsOpen = true
            self?.register()
        }
        transport.onText = { [weak self] text in self?.read(text) }
        transport.onClose = { [weak self] why in self?.closed(why) }
    }

    /// The call link for a call. On a line that carries call links it
    /// travels on the line (`LineLink`), which has been open since the app
    /// signed in; otherwise it is a websocket of its own, with this line's
    /// current token.
    private func makeLink(_ callID: String) -> (any PictureLink)? {
        if let customLink { return customLink(callID) }
        if transport.carriesCallLinks { return LineLink(callID: callID, line: self) }
        return CallLinkSocket(sip: account.websocket, token: account.token, callID: callID)
    }

    // MARK: - Call links on the line (ADR-087)

    /// The line is open now (its websocket, whether or not signed in yet).
    private var lineIsOpen = false
    /// The call links on this line, by SIP Call-ID.
    private var lineLinks: [String: LineLink] = [:]

    /// Opens `link` on the line, or tells it the line isn't there to open
    /// on: it tries again, as a call link's websocket does.
    func open(_ link: LineLink) {
        guard lineIsOpen, transport.carriesCallLinks else {
            Task { @MainActor in link.lineClosed() }
            return
        }
        lineLinks[link.callID] = link
        transport.send(Bench.noEarly ? LineLink.frame(link.callID, "open", true) : LineLink.opening(link.callID))
    }

    /// What `link` sends the other device.
    func send(_ data: Data, on link: LineLink) {
        guard lineIsOpen, lineLinks[link.callID] === link else { return }
        transport.send(LineLink.frame(link.callID, msg: data))
    }

    func close(_ link: LineLink) {
        guard lineLinks[link.callID] === link else { return }
        lineLinks[link.callID] = nil
        if lineIsOpen { transport.send(LineLink.frame(link.callID, "close", true)) }
    }

    /// A side message from Linx: one of this line's call links.
    private func readLink(_ text: String) {
        guard let (callID, message) = LineLink.parse(text), let link = lineLinks[callID] else { return }
        switch message {
        case .message(let data): link.received(data)
        case .closed:
            lineLinks[callID] = nil
            link.lineClosed()
        }
    }

    /// The line went: every link on it with it. Each tries again, and is
    /// opened on the line that comes back.
    private func lineGone() {
        lineIsOpen = false
        let links = lineLinks.values
        lineLinks = [:]
        for link in links { link.lineClosed() }
    }

    // MARK: - A dropped connection (a phone between networks)

    /// How long a call is held while its line to Linx comes back. The sound
    /// and the picture don't travel on that line, so they go on meanwhile;
    /// what can't happen until it is back is hanging up or changing the call.
    static let holdTheCallFor: Duration = .seconds(30)
    private let holdFor: Duration

    /// Whether a call is being held for the line to come back, so the phone
    /// gives this agent its new connection (`reattach`) instead of starting
    /// a new one, which would end the call.
    var holdingACall: Bool { call?.lineLost == true }

    /// The line is back on a new connection: the same phone, a fresh
    /// password and token. Once it has signed in again, the call is told
    /// where this phone is now (`refreshTheCall`).
    func reattach(account: Account, transport: any SIPTransport) {
        lineGone()
        self.transport.onOpen = nil
        self.transport.onText = nil
        self.transport.onClose = nil
        self.transport.stop()
        self.account = account
        self.transport = transport
        registration.credentials = nil
        registration.attempts = 0
        listen(to: transport)
        start()
    }

    /// An UPDATE in the call (RFC 3311), with no change to the sound: it
    /// refreshes the call's target, so Asterisk sends what it has for this
    /// call down the new connection.
    private func refreshTheCall() {
        guard let current = call, current.lineLost else { return }
        var message = inDialog("UPDATE", of: current)
        message.add("Contact", contact)
        call?.refreshing = message.cseq?.number
        send(message)
    }

    // MARK: - Signing in

    func start() {
        onStatus?(.connecting)
        transport.start()
    }

    /// stop ends the call, signs the line out and closes the connection.
    /// signOff tells Asterisk this phone is going and waits for it to agree
    /// (two seconds at most) before the connection closes. Closing first
    /// lost a call Asterisk had sent in that same moment: it went down a
    /// connection that was shutting and rang nothing for thirty seconds
    /// (2026-10-12). A call that arrives while this waits is taken as usual.
    func signOff() async {
        registerTimer?.cancel()
        registerTimer = nil
        leaving = true
        register(unregister: true)
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            signingOff = done
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(2))
                self?.signedOff()
            }
        }
    }

    /// The line stays after all: a call arrived while it was signing off.
    func signBackOn() {
        leaving = false
        register()
    }

    private func signedOff() {
        signingOff?.resume()
        signingOff = nil
    }

    func stop() {
        registerTimer?.cancel()
        registerTimer = nil
        if let call {
            if call.established {
                send(inDialog("BYE", of: call))
            } else if call.incoming, let invite = call.invite {
                send(.response(486, "Busy Here", to: invite).withTag(call.localTag))
            }
            end(call, why: .hungUp, tell: false)
        }
        transport.stop()
    }

    private func register(unregister: Bool = false) {
        registration.seq += 1
        var message = SIPMessage.request("REGISTER", "sip:" + account.domain)
        message.add("Via", via(branch: SIPRandom.branch()))
        message.add("Max-Forwards", "70")
        message.add("From", "\(selfAddress);tag=\(registration.tag)")
        message.add("To", selfAddress)
        message.add("Call-ID", registration.callID)
        message.add("CSeq", "\(registration.seq) REGISTER")
        message.add("Contact", "\(contact);expires=\(unregister ? 0 : Self.registerSeconds)")
        message.add("Expires", unregister ? "0" : String(Self.registerSeconds))
        addCommonHeaders(&message)
        if registration.credentials != nil {
            message.add(
                "Authorization",
                answer(&registration.credentials, method: "REGISTER", uri: "sip:" + account.domain))
        }
        send(message)
    }

    private func registered(_ response: SIPMessage) {
        registration.attempts = 0
        onStatus?(.ready)
        refreshTheCall()
        // Sign in again halfway through, as a desk phone does: it keeps the
        // line alive and the websocket with it.
        let seconds = Int(response.first("Expires") ?? "") ?? Self.registerSeconds
        registerTimer?.cancel()
        registerTimer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(max(30, seconds / 2)))
            guard !Task.isCancelled else { return }
            self?.register()
        }
    }

    // MARK: - Making and taking a call

    /// call rings someone: the number as typed, or *43 to hear yourself back.
    func call(_ number: String, name: String? = nil, startedAt: Date = Date(), video: Bool = false) async {
        guard call == nil else { return }
        let target = number.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        let peer = SIPPeer(name: name ?? target, number: target)
        var outgoing = Call(
            id: SIPRandom.token(16) + "@" + localHost, localTag: SIPRandom.token(10),
            peer: peer, incoming: false, media: makeMedia())
        outgoing.media.began(at: startedAt)
        let outgoingID = outgoing.id
        outgoing.media.onRouteLost = { [weak self] in self?.routeLost(outgoingID) }
        outgoing.remoteURI = "sip:\(target)@\(account.domain)"
        outgoing.remoteTarget = outgoing.remoteURI
        // A video call says so in a header (ADR-087): the INVITE carries the
        // sound only, and the cameras go on over the picture link once it's
        // answered. A phone that can't show a picture just rings.
        outgoing.videoCall = video
        if video { outgoing.media.setSpeaker(true) }
        call = outgoing

        let offer: String
        do {
            offer = try await outgoing.media.offer()
        } catch {
            end(outgoing, why: .failed("This phone couldn't start the call's sound. Try again."))
            return
        }
        // The person may have hung up while the sound was being set up.
        guard call?.id == outgoing.id else { return }
        outgoing.offer = offer
        call = outgoing
        sendInvite()
    }

    private func sendInvite() {
        guard var outgoing = call, let offer = outgoing.offer else { return }
        outgoing.seq += 1
        outgoing.inviteBranch = SIPRandom.branch()
        var message = SIPMessage.request("INVITE", outgoing.remoteURI)
        message.add("Via", via(branch: outgoing.inviteBranch))
        message.add("Max-Forwards", "70")
        message.add("From", "\(selfAddress);tag=\(outgoing.localTag)")
        message.add("To", "<\(outgoing.remoteURI)>")
        message.add("Call-ID", outgoing.id)
        message.add("CSeq", "\(outgoing.seq) INVITE")
        message.add("Contact", contact)
        if outgoing.videoCall { message.add("X-Linx-Video", "yes") }
        addCommonHeaders(&message)
        if outgoing.credentials != nil {
            message.add("Authorization", answer(&outgoing.credentials, method: "INVITE", uri: outgoing.remoteURI))
        }
        message.add("Content-Type", "application/sdp")
        message.body = offer
        outgoing.invite = message
        if outgoing.attempts == 0 { outgoing.media.mark("Call sent") }
        call = outgoing
        send(message)
        outgoing.timeout?.cancel()
        call?.timeout = Task { [weak self] in
            try? await Task.sleep(for: Self.answerWithin)
            guard !Task.isCancelled, let self, let waiting = self.call, !waiting.established else { return }
            self.end(waiting, why: .failed("There was no answer."))
        }
    }

    /// answer picks up the call that is ringing.
    func answer() async {
        guard var incoming = call, incoming.incoming, let invite = incoming.invite, !incoming.established
        else { return }
        let answer: String
        do {
            // Made while it rang; a call answered before that finished
            // waits only for what's left of it.
            if let preparing = incoming.preparing {
                answer = try await preparing.value
            } else {
                answer = try await incoming.media.answer(to: invite.body)
            }
        } catch {
            send(.response(500, "Server Internal Error", to: invite).withTag(incoming.localTag))
            end(incoming, why: .failed("This phone couldn't start the call's sound."))
            return
        }
        guard call?.id == incoming.id else { return }
        var ok = SIPMessage.response(200, "OK", to: invite).withTag(incoming.localTag)
        ok.add("Contact", contact)
        addCommonHeaders(&ok)
        ok.add("Content-Type", "application/sdp")
        ok.body = answer
        // On a line that carries call links, the picture's answer goes in
        // the same message: sent after the 200 OK, a phone's network held it
        // until the 200 OK was acknowledged — seconds on a just-opened slow
        // line (DirectPicture.answerWithTheCall).
        if transport.carriesSIPInLinks, lineIsOpen, lineLinks[incoming.id] != nil,
            let picture = await incoming.media.pictureAnswerWithTheCall(), call?.id == incoming.id
        {
            if let words = Self.traceWords(ok) { onTrace?("→ \(words)") }
            transport.send(LineLink.frame(incoming.id, msg: picture, sip: ok.text))
        } else {
            guard call?.id == incoming.id else { return }
            send(ok)
        }
        incoming.established = true
        call = incoming
        startPicture(of: incoming)
        onEstablished?()
    }

    /// decline sends the caller to voicemail, as a busy phone does.
    func decline() {
        guard let incoming = call, incoming.incoming, let invite = incoming.invite, !incoming.established
        else { return }
        send(.response(486, "Busy Here", to: invite).withTag(incoming.localTag))
        end(incoming, why: .hungUp)
    }

    /// hangUp ends a call, whichever way it was going and whatever it was
    /// doing: ringing out, ringing in, or talking.
    func hangUp() {
        guard let current = call else { return }
        if current.established {
            send(inDialog("BYE", of: current))
            end(current, why: .hungUp)
            return
        }
        if current.incoming {
            decline()
            return
        }
        guard let invite = current.invite else {
            end(current, why: .hungUp)
            return
        }
        // Nothing has come back yet: cancel the invitation (RFC 3261 §9.1),
        // and the 487 that follows finishes it.
        var cancel = SIPMessage.request("CANCEL", current.remoteURI)
        cancel.add("Via", via(branch: current.inviteBranch))
        cancel.add("Max-Forwards", "70")
        for name in ["From", "To", "Call-ID"] {
            if let value = invite.first(name) { cancel.add(name, value) }
        }
        cancel.add("CSeq", "\(current.seq) CANCEL")
        addCommonHeaders(&cancel)
        send(cancel)
        abandoned[current.id] = invite
        end(current, why: .hungUp)
    }

    // MARK: - The picture (ADR-087)

    /// The picture link opens while the call rings and Linx holds it until
    /// the answer: a new connection on a poor mobile link took 9 s and more
    /// (the owner's call, 2026-10-14), and made after the answer that was 9 s
    /// more of a video call with no picture. At the answer it is told the
    /// call is up, and on a video call this phone's camera goes on as soon
    /// as the other side is there. The echo test has nobody on the other end
    /// to link with.
    private func startPicture(of current: Call, ringing: Bool = false) {
        guard current.peer.number != "*43", let link = makeLink(current.id) else { return }
        current.media.startPicture(link: link, cameraFirst: current.videoCall, ringing: ringing)
    }

    /// Turns this phone's camera on or off in a call that is up. The call
    /// itself is untouched: the picture has its own link to the other
    /// device, so nothing goes through the phone system and the sound never
    /// notices.
    func setVideo(_ on: Bool) async {
        guard let current = call, current.established else { return }
        do {
            try await current.media.setCamera(on)
        } catch let trouble as CameraTrouble {
            onVideoRefused?(trouble.words)
        } catch {
            onVideoRefused?("This phone couldn't start its camera. Try again in a moment.")
        }
    }

    /// Moves the call onto Linx's relay: the sound isn't getting through the
    /// route it is on (PhoneModel decides when, and how often). The
    /// re-INVITE carries an ICE restart that allows only the relay; the
    /// conversation stays where it is until the new route answers.
    func moveToRelay() async {
        guard let current = call, current.established, !current.changing, !current.reinviting else { return }
        call?.changing = true
        call?.movedToRelay = true
        defer { call?.changing = false }
        guard let offer = try? await current.media.moveToRelay(), call?.id == current.id,
            !(call?.reinviting ?? false)
        else { return }
        call?.reinviteForRelay = true
        sendReinvite(offer, of: current)
    }

    /// The SIP Call-ID of the call that is up, which is how the server finds
    /// it (the call link, group calls).
    var callID: String? { call?.id }
    /// Whether the call that is up was an invitation into a group call.
    var callIsInvitation: Bool { call?.invitation ?? false }

    /// Hands the camera over to the one on the other side of the phone.
    func switchCamera() { call?.media.switchCamera() }

    /// The **HD** button, for the picture this phone sends.
    func useHD(_ on: Bool) { call?.media.useHD(on) }

    private func sendReinvite(_ offer: String, of current: Call) {
        guard call?.id == current.id else { return }
        var message = inDialog("INVITE", of: current)
        message.add("Contact", contact)
        if current.credentials != nil {
            var credentials: Credentials? = current.credentials
            message.add("Authorization", answer(&credentials, method: "INVITE", uri: current.remoteTarget))
            call?.credentials = credentials
        }
        message.add("Content-Type", "application/sdp")
        message.body = offer
        call?.reinviting = true
        call?.reinviteOffer = offer
        call?.reinviteRequest = message
        call?.reinviteTimeout?.cancel()
        let id = current.id
        let number = message.cseq?.number
        call?.reinviteTimeout = Task { [weak self] in
            // Unanswered for as long as SIP waits on a reliable connection
            // (Timer B, RFC 3261 §17.1.1.2). A connection that dropped is
            // handled when the line comes back (`refreshed`); on one that is
            // still up the answer is only late, and giving up after 8 s sent
            // a second change while the first was still on its way, which
            // Asterisk refused with a 500 (2026-10-17).
            try? await Task.sleep(for: .seconds(32))
            guard !Task.isCancelled, let self, self.call?.id == id, self.call?.reinviting == true,
                self.call?.reinviteRequest?.cseq?.number == number
            else { return }
            self.forgetTheLostChange()
            self.restartIfWanted()
        }
        send(message)
    }

    /// A change of this phone's that will never be answered: the call goes
    /// back to how it was, and a route that is still wanted is asked again.
    private func forgetTheLostChange() {
        guard let current = call else { return }
        call?.reinviting = false
        call?.reinviteOffer = nil
        call?.reinviteRequest = nil
        call?.reinviteForRelay = false
        call?.reinviteTimeout?.cancel()
        call?.restartWanted = true
        Task { await current.media.rollbackOffer() }
    }

    /// The ACK for a re-INVITE that was refused or challenged. It belongs to
    /// that request, not to the invitation the call began with, so it
    /// carries that request's own branch and sequence number (RFC 3261
    /// §17.1.1.3).
    private func ackReinvite(_ response: SIPMessage, of current: Call) {
        guard let request = current.reinviteRequest else { return }
        var ack = SIPMessage.request("ACK", request.requestURI ?? current.remoteTarget)
        ack.add("Via", request.first("Via") ?? via(branch: SIPRandom.branch()))
        ack.add("Max-Forwards", "70")
        ack.add("From", request.first("From") ?? "")
        ack.add("To", response.first("To") ?? request.first("To") ?? "")
        ack.add("Call-ID", current.id)
        ack.add("CSeq", "\(request.cseq?.number ?? current.seq) ACK")
        send(ack)
    }

    func setMuted(_ muted: Bool) { call?.media.setMuted(muted) }

    func setSpeaker(_ on: Bool) { call?.media.setSpeaker(on) }

    func sendTone(_ digit: Character) { call?.media.sendTone(digit) }

    // MARK: - What comes back

    private func read(_ text: String) {
        if text.first == "{" {
            readLink(text)
            return
        }
        guard let message = SIPMessage(text: text) else { return }
        if let words = Self.traceWords(message) { onTrace?("← \(words)") }
        if message.status != nil {
            response(message)
        } else {
            request(message)
        }
    }

    private func response(_ message: SIPMessage) {
        guard let status = message.status, let cseq = message.cseq else { return }
        if cseq.method == "REGISTER" {
            registerResponse(message, status: status)
            return
        }
        if cseq.method == "UPDATE" {
            refreshed(message, status: status, number: cseq.number)
            return
        }
        guard cseq.method == "INVITE" else { return }
        if let callID = message.callID, let invite = abandoned[callID] {
            // The person hung up before this came back. Acknowledge it, and
            // hang up properly if it turns out they were answered.
            lastWord(message, status: status, invite: invite, callID: callID)
            return
        }
        guard var current = call, message.callID == current.id else { return }
        if current.reinviting, cseq.number == current.reinviteRequest?.cseq?.number {
            reinviteResponse(message, status: status, of: current)
            return
        }
        if current.established {
            // A late answer to a change this phone already gave up on: once
            // the call is up, no answer to an INVITE can end it.
            strayAnswer(message, status: status, of: current)
            return
        }
        switch status {
        case 100: return
        case 180, 183:
            if !current.ringingMarked {
                current.ringingMarked = true
                call = current
                current.media.mark("Ringing")
                startPicture(of: current, ringing: true)
            }
            onProgress?()
            // A 183 that carries sound is Asterisk playing something before
            // the call is answered (a ringing tone, or "the number you have
            // dialled…"): the phone plays it, as the browser does.
            guard status == 183, !message.body.isEmpty, !current.earlyMedia else { return }
            current.earlyMedia = true
            current.remoteTag = message.toTag
            call = current
            Task { [weak self] in
                guard let self else { return }
                do {
                    try await current.media.accept(answer: message.body)
                    self.onEarlySound?()
                } catch {
                    // No early sound; the call itself is unaffected.
                    self.call?.earlyMedia = false
                }
            }
        case 401, 407:
            // Asterisk asks the phone to prove the password it was given a
            // moment ago. One go: a second challenge means it is wrong.
            ack(message, of: current)
            guard let challenge = challenge(in: message), current.attempts < 2 else {
                end(current, why: .failed("This phone's line couldn't sign in to make that call."))
                return
            }
            current.attempts += 1
            current.credentials = Credentials(challenge: challenge)
            current.remoteTag = nil
            call = current
            sendInvite()
        case 200..<300:
            current.remoteTag = message.toTag
            current.remoteTarget = SIPMessage.uri(in: message.first("Contact") ?? current.remoteURI)
            current.routeSet = message.all("Record-Route").reversed()
            current.established = true
            current.timeout?.cancel()
            call = current
            current.media.mark(CallDiagnostics.answered)
            send(ack2xx(for: message, of: current))
            Task { [weak self] in
                guard let self else { return }
                do {
                    // The sound was already agreed if Asterisk sent it early.
                    if !current.earlyMedia {
                        try await current.media.accept(answer: message.body)
                    }
                    guard self.call?.id == current.id else { return }
                    self.startPicture(of: current)
                    self.onEstablished?()
                } catch {
                    self.send(self.inDialog("BYE", of: current))
                    self.end(current, why: .failed("The call connected but its sound didn't. Try again."))
                }
            }
        default:
            ack(message, of: current)
            end(current, why: .failed(Self.words(for: status, message.reason)))
        }
    }

    /// Asterisk's final word on an invitation the person had already given
    /// up on: acknowledged, and hung up if it was answered after all.
    private func lastWord(_ message: SIPMessage, status: Int, invite: SIPMessage, callID: String) {
        guard status >= 200 else { return }
        abandoned[callID] = nil
        guard (200..<300).contains(status) else {
            var ack = SIPMessage.request("ACK", invite.requestURI ?? "")
            ack.add("Via", invite.first("Via") ?? via(branch: SIPRandom.branch()))
            ack.add("Max-Forwards", "70")
            ack.add("From", invite.first("From") ?? "")
            ack.add("To", message.first("To") ?? invite.first("To") ?? "")
            ack.add("Call-ID", callID)
            ack.add("CSeq", "\(invite.cseq?.number ?? 1) ACK")
            send(ack)
            return
        }
        let target = SIPMessage.uri(in: message.first("Contact") ?? invite.requestURI ?? "")
        let seq = invite.cseq?.number ?? 1
        var ack = SIPMessage.request("ACK", target)
        ack.add("Via", via(branch: SIPRandom.branch()))
        ack.add("Max-Forwards", "70")
        ack.add("From", invite.first("From") ?? "")
        ack.add("To", message.first("To") ?? "")
        ack.add("Call-ID", callID)
        ack.add("CSeq", "\(seq) ACK")
        send(ack)
        var bye = SIPMessage.request("BYE", target)
        bye.add("Via", via(branch: SIPRandom.branch()))
        bye.add("Max-Forwards", "70")
        for route in message.all("Record-Route").reversed() { bye.add("Route", route) }
        bye.add("From", invite.first("From") ?? "")
        bye.add("To", message.first("To") ?? "")
        bye.add("Call-ID", callID)
        bye.add("CSeq", "\(seq + 1) BYE")
        addCommonHeaders(&bye)
        send(bye)
    }

    /// What came back from this phone's own re-INVITE (a move onto the
    /// relay). The conversation itself is untouched whichever way it goes.
    private func reinviteResponse(_ message: SIPMessage, status: Int, of current: Call) {
        switch status {
        case 100..<200:
            return
        case 200..<300:
            call?.reinviteForRelay = false
            call?.reinviting = false
            call?.reinviteOffer = nil
            call?.reinviteRequest = nil
            call?.reinviteAttempts = 0
            call?.reinviteTimeout?.cancel()
            defer { restartIfWanted() }
            var ack = SIPMessage.request("ACK", current.remoteTarget)
            ack.add("Via", via(branch: SIPRandom.branch()))
            ack.add("Max-Forwards", "70")
            for route in current.routeSet { ack.add("Route", route) }
            ack.add("From", message.first("From") ?? "\(selfAddress);tag=\(current.localTag)")
            ack.add("To", message.first("To") ?? "<\(current.remoteURI)>")
            ack.add("Call-ID", current.id)
            ack.add("CSeq", "\(message.cseq?.number ?? current.seq) ACK")
            send(ack)
            Task {
                do {
                    try await current.media.accept(answer: message.body)
                } catch {
                    await current.media.rollbackOffer()
                }
            }
        case 401, 407:
            // Asterisk asks this phone to prove itself again, in the middle
            // of a call. One go: the password hasn't changed since it signed
            // in a moment ago.
            ackReinvite(message, of: current)
            guard let challenge = challenge(in: message), current.reinviteAttempts < 1,
                let offer = current.reinviteOffer
            else {
                giveUpOnChange(current)
                return
            }
            call?.reinviteAttempts += 1
            call?.credentials = Credentials(challenge: challenge)
            call?.reinviting = false
            guard let again = call else { return }
            sendReinvite(offer, of: again)
        default:
            ackReinvite(message, of: current)
            // 491 is both sides asking at once (RFC 3261 §14.1); the next
            // move onto the relay, if it is still needed, tries again.
            giveUpOnChange(current)
        }
    }

    /// Puts the call back exactly as it was before the offer nobody
    /// accepted. The only change this phone asks for is a move onto the
    /// relay, and one that didn't happen is no news to the person.
    private func giveUpOnChange(_ current: Call) {
        call?.reinviteForRelay = false
        call?.reinviting = false
        call?.reinviteOffer = nil
        call?.reinviteRequest = nil
        call?.reinviteAttempts = 0
        Task { await current.media.rollbackOffer() }
    }

    private func registerResponse(_ message: SIPMessage, status: Int) {
        switch status {
        case 100..<200: return
        case 200..<300:
            if leaving {
                signedOff()
                return
            }
            registered(message)
        case 401, 407:
            // Two goes at a challenge: a nonce Asterisk has forgotten is
            // normal and gets a second one; a wrong password isn't, and the
            // relay closes the line after three failures anyway.
            guard let challenge = challenge(in: message), registration.attempts < 2 else {
                onStatus?(
                    .unavailable(
                        "This phone's line couldn't sign in. Set the phone up again if it keeps happening."))
                return
            }
            registration.attempts += 1
            registration.credentials = Credentials(challenge: challenge)
            register(unregister: leaving)
        default:
            if leaving {
                signedOff()
                return
            }
            onStatus?(.unavailable(Self.words(for: status, message.reason)))
        }
    }

    private func request(_ message: SIPMessage) {
        switch message.method {
        case "INVITE":
            if let current = call, current.id == message.callID, current.established {
                guard !current.reinviting else {
                    // This phone has a change of its own outstanding: both
                    // sides asked at once, and the rule is that the caller
                    // of the two tries again (RFC 3261 §14.2).
                    send(.response(491, "Request Pending", to: message).withTag(current.localTag))
                    return
                }
                reinvite(message, of: current)
            } else {
                invited(message)
            }
        case "ACK":
            return
        case "BYE":
            send(.response(200, "OK", to: message))
            if let current = call, current.id == message.callID {
                end(current, why: .hungUp)
            }
        case "CANCEL":
            send(.response(200, "OK", to: message))
            guard let current = call, current.id == message.callID, let invite = current.invite,
                !current.established
            else { return }
            send(.response(487, "Request Terminated", to: invite).withTag(current.localTag))
            end(current, why: .missed)
        case "OPTIONS":
            var ok = SIPMessage.response(200, "OK", to: message)
            addCommonHeaders(&ok)
            send(ok)
        case "NOTIFY", "MESSAGE", "INFO", "UPDATE":
            send(.response(200, "OK", to: message))
        default:
            send(.response(405, "Method Not Allowed", to: message))
        }
    }

    /// The sound's route went and didn't come back by itself: a fresh look
    /// for routes, told to Asterisk with a re-INVITE — as soon as the line
    /// to Linx is there to carry it, and once whatever change is out has
    /// been answered.
    private func routeLost(_ id: String) {
        guard let current = call, current.id == id, current.established else { return }
        if current.lineLost || current.reinviting || current.changing {
            call?.restartWanted = true
            return
        }
        call?.restartWanted = false
        call?.changing = true
        Task { [weak self] in
            let offer = try? await current.media.restartRoute()
            guard let self, self.call?.id == id else { return }
            self.call?.changing = false
            guard let offer else {
                // Nothing to offer yet: the network that would carry it isn't
                // back. Look again shortly, for as long as the call lasts.
                self.call?.restartWanted = true
                try? await Task.sleep(for: .seconds(2))
                self.restartIfWanted()
                return
            }
            guard !(self.call?.reinviting ?? false) else {
                self.call?.restartWanted = true
                return
            }
            self.sendReinvite(offer, of: current)
        }
    }

    /// A restart that had to wait goes now.
    private func restartIfWanted() {
        guard let current = call, current.restartWanted, !current.lineLost, !current.reinviting else { return }
        routeLost(current.id)
    }

    /// Asterisk's answer to `refreshTheCall`.
    private func refreshed(_ message: SIPMessage, status: Int, number: Int) {
        guard let current = call, message.callID == current.id, current.refreshing == number, status >= 200
        else { return }
        call?.refreshing = nil
        if status < 300 {
            let away = current.lineLostSince.map { String(format: "%.1f", Date().timeIntervalSince($0)) } ?? "?"
            current.media.mark("Line to Linx back after \(away) s; the call goes on")
            call?.lineLost = false
            call?.lineLostSince = nil
            call?.giveUp?.cancel()
            call?.giveUp = nil
            // A re-INVITE sent down the old connection was lost with it.
            if current.reinviting { forgetTheLostChange() }
            restartIfWanted()
        } else if status == 481 || status == 408 {
            // Asterisk no longer has the call.
            end(current, why: .failed("The call dropped: this phone lost touch with Linx."))
        } else {
            current.media.mark("Linx answered the line's return with \(status)")
        }
    }

    private func invited(_ invite: SIPMessage) {
        send(.response(100, "Trying", to: invite))
        // One call at a time in this slice: a second caller is told the phone
        // is busy, and Linx sends them to voicemail.
        guard call == nil else {
            send(.response(486, "Busy Here", to: invite))
            return
        }
        guard let from = invite.first("From") else { return }
        let number = SIPMessage.user(of: from)
        let peer = SIPPeer(name: SIPMessage.displayName(of: from) ?? number, number: number)
        var incoming = Call(
            id: invite.callID ?? SIPRandom.token(16), localTag: SIPRandom.token(10), peer: peer,
            incoming: true, media: makeMedia())
        incoming.invite = invite
        let incomingID = incoming.id
        incoming.media.onRouteLost = { [weak self] in self?.routeLost(incomingID) }
        incoming.remoteTag = invite.fromTag
        incoming.remoteURI = SIPMessage.uri(in: from)
        incoming.remoteTarget = SIPMessage.uri(in: invite.first("Contact") ?? from)
        incoming.routeSet = invite.all("Record-Route")
        incoming.videoCall = invite.first("X-Linx-Video") == "yes"
        incoming.invitation = invite.first("X-Linx-Room") != nil
        let media = incoming.media
        incoming.preparing = Task { try await media.answer(to: invite.body) }
        call = incoming
        var ringing = SIPMessage.response(180, "Ringing", to: invite).withTag(incoming.localTag)
        ringing.add("Contact", contact)
        send(ringing)
        startPicture(of: incoming, ringing: true)
        onIncoming?(peer, incoming.videoCall)
    }

    /// Asterisk sometimes asks to change a call that is already up (a
    /// transfer, or the sound taking a new route). The phone answers with
    /// what its side can do now.
    private func reinvite(_ invite: SIPMessage, of current: Call) {
        Task { [weak self] in
            guard let self else { return }
            do {
                let answer = try await current.media.answer(to: invite.body)
                guard self.call?.id == current.id else { return }
                var ok = SIPMessage.response(200, "OK", to: invite).withTag(current.localTag)
                ok.add("Contact", self.contact)
                self.addCommonHeaders(&ok)
                ok.add("Content-Type", "application/sdp")
                ok.body = answer
                self.send(ok)
            } catch {
                // Why, in the call record: a refused change leaves the two
                // ends disagreeing about the call (2026-10-12).
                current.media.mark("Refused Linx's change (\(SDPTweaks.iceWords(invite.body))): \(error)")
                self.send(.response(488, "Not Acceptable Here", to: invite).withTag(current.localTag))
            }
        }
    }

    private func closed(_ why: String?) {
        lineGone()
        registerTimer?.cancel()
        registerTimer = nil
        if let current = call {
            if current.established, !leaving, !(why ?? "").hasPrefix(WebSocketTransport.closedByLinx) {
                // The sound and the picture have their own routes: keep the
                // call while the line comes back, as any phone does when it
                // moves between networks.
                current.media.mark("Line to Linx lost (\(why ?? "closed")); holding the call")
                call?.lineLost = true
                call?.lineLostSince = Date()
                call?.giveUp?.cancel()
                call?.giveUp = Task { [weak self] in
                    try? await Task.sleep(for: self?.holdFor ?? Self.holdTheCallFor)
                    guard !Task.isCancelled, let self, let held = self.call, held.id == current.id, held.lineLost
                    else { return }
                    self.end(held, why: .failed("The call dropped: this phone lost touch with Linx."))
                }
            } else {
                end(current, why: .failed("The call dropped: this phone lost touch with Linx."))
            }
        }
        onStatus?(.reconnecting)
    }

    // MARK: - Writing it down

    private var selfAddress: String {
        "\"\(Self.quotable(account.displayName))\" <sip:\(account.username)@\(account.domain)>"
    }

    /// The address Asterisk sends calls back to. The host is this phone's own
    /// made-up name: the websocket is the route, as RFC 7118 intends.
    private var contact: String { "<sip:\(account.username)@\(localHost);transport=ws>" }

    private func via(branch: String) -> String { "SIP/2.0/WSS \(localHost);branch=\(branch);rport" }

    private func addCommonHeaders(_ message: inout SIPMessage) {
        message.set("Allow", "INVITE, ACK, CANCEL, BYE, OPTIONS, INFO, NOTIFY, UPDATE, MESSAGE")
        message.set("User-Agent", "Linx iOS")
    }

    /// An in-dialog request (BYE, in this slice): to the other side's contact,
    /// through the same route the call came by.
    private func inDialog(_ method: String, of current: Call) -> SIPMessage {
        var message = SIPMessage.request(method, current.remoteTarget)
        message.add("Via", via(branch: SIPRandom.branch()))
        message.add("Max-Forwards", "70")
        for route in current.routeSet { message.add("Route", route) }
        message.add("From", "\(selfAddress);tag=\(current.localTag)")
        message.add("To", "<\(current.remoteURI)>" + (current.remoteTag.map { ";tag=\($0)" } ?? ""))
        message.add("Call-ID", current.id)
        message.add("CSeq", "\(current.seq + 1) \(method)")
        addCommonHeaders(&message)
        call?.seq += 1
        return message
    }

    /// Acknowledges an answer to a re-INVITE this phone has stopped waiting
    /// for, from that answer's own headers. Built from the first invitation
    /// instead, it carried the caller's From on a call this phone was rung
    /// for, the `/sip` relay took it for another phone's line and closed it,
    /// and the call dropped (2026-10-17).
    private func strayAnswer(_ message: SIPMessage, status: Int, of current: Call) {
        guard status >= 200, let number = message.cseq?.number else { return }
        // The call's own answer again is the commonest: Asterisk sends it
        // until this phone's ACK reaches it, which on a slow link is after the
        // second copy left. It is acknowledged again and nothing else —
        // taking it for a new answer used the sound's answer twice, failed,
        // and hung up a call that was fine (the phone bench, 300 ms and 2%
        // loss, 2026-10-13).
        if number != current.invite?.cseq?.number {
            current.media.mark("A late answer to an earlier change (\(status)), acknowledged")
        }
        var ack = SIPMessage.request("ACK", current.remoteTarget)
        if (200..<300).contains(status) {
            ack.add("Via", via(branch: SIPRandom.branch()))
            ack.add("Max-Forwards", "70")
            for route in current.routeSet { ack.add("Route", route) }
        } else {
            // A refusal's ACK belongs to the refused request's transaction.
            ack.add("Via", message.first("Via") ?? via(branch: SIPRandom.branch()))
            ack.add("Max-Forwards", "70")
        }
        ack.add("From", message.first("From") ?? "\(selfAddress);tag=\(current.localTag)")
        ack.add("To", message.first("To") ?? "<\(current.remoteURI)>")
        ack.add("Call-ID", current.id)
        ack.add("CSeq", "\(number) ACK")
        send(ack)
    }

    /// The ACK that finishes an answered call (its own transaction, RFC 3261
    /// §17.1.1.3).
    private func ack2xx(for response: SIPMessage, of current: Call) -> SIPMessage {
        var ack = SIPMessage.request("ACK", current.remoteTarget)
        ack.add("Via", via(branch: SIPRandom.branch()))
        ack.add("Max-Forwards", "70")
        for route in current.routeSet { ack.add("Route", route) }
        ack.add("From", response.first("From") ?? "\(selfAddress);tag=\(current.localTag)")
        ack.add("To", response.first("To") ?? "<\(current.remoteURI)>")
        ack.add("Call-ID", current.id)
        ack.add("CSeq", "\(current.seq) ACK")
        return ack
    }

    /// The ACK that answers a refusal, which goes the way the INVITE went.
    private func ack(_ response: SIPMessage, of current: Call) {
        guard let invite = current.invite else { return }
        var ack = SIPMessage.request("ACK", invite.requestURI ?? current.remoteURI)
        ack.add("Via", via(branch: current.inviteBranch))
        ack.add("Max-Forwards", "70")
        ack.add("From", invite.first("From") ?? "")
        ack.add("To", response.first("To") ?? invite.first("To") ?? "")
        ack.add("Call-ID", current.id)
        ack.add("CSeq", "\(current.seq) ACK")
        send(ack)
    }

    private func challenge(in message: SIPMessage) -> SIPChallenge? {
        if let header = message.first("WWW-Authenticate") { return SIPChallenge.parse(header, proxy: false) }
        if let header = message.first("Proxy-Authenticate") { return SIPChallenge.parse(header, proxy: true) }
        return nil
    }

    /// Answers a challenge, counting this use of its nonce (RFC 2617's nc).
    private func answer(_ credentials: inout Credentials?, method: String, uri: String) -> String {
        guard var held = credentials else { return "" }
        held.count += 1
        credentials = held
        return SIPDigest.authorization(
            challenge: held.challenge, username: account.username, password: account.password,
            method: method, uri: uri, nonceCount: held.count)
    }

    private func send(_ message: SIPMessage) {
        if let words = Self.traceWords(message) { onTrace?("→ \(words)") }
        transport.send(message.text)
    }

    /// A message's first line in a few words, or nil for signing in and
    /// keep-alives, which say nothing about a call.
    nonisolated static func traceWords(_ message: SIPMessage) -> String? {
        let method = message.cseq?.method
        guard method != "REGISTER", method != "OPTIONS" else { return nil }
        if let request = message.method { return request }
        guard let status = message.status else { return nil }
        let reason = message.reason.map { " \($0)" } ?? ""
        return "\(status)\(reason)\(method.map { " (\($0))" } ?? "")"
    }

    private func end(_ current: Call, why: SIPEnded, tell: Bool = true) {
        guard call?.id == current.id else { return }
        call?.timeout?.cancel()
        call?.giveUp?.cancel()
        call?.reinviteTimeout?.cancel()
        call?.preparing?.cancel()
        // In a group call the phone call was only what rang: its sound is
        // the video server's now, and the phone model ends it.
        if !current.media.inGroup { current.media.stop() }
        call = nil
        if tell { onEnded?(why) }
    }

    /// What a refusal means, in words a person reads once and understands.
    static func words(for status: Int, _ reason: String?) -> String {
        switch status {
        case 403: return "Linx wouldn't put that call through."
        case 404, 484: return "There's no such number."
        case 408: return "There was no answer."
        case 410: return "That number isn't in use any more."
        case 480: return "They're not available."
        case 486, 600: return "They're on another call."
        case 487: return "The call ended."
        case 488: return "This phone and Linx couldn't agree on how to carry the sound."
        case 503: return "Linx couldn't connect that call. Try again in a moment."
        case 603: return "They declined the call."
        default: return "The call didn't go through (\(status) \(reason ?? ""))."
        }
    }

    private static func quotable(_ name: String) -> String {
        name.filter { $0 != "\"" && $0 != "\\" && !$0.isNewline }
    }

    // MARK: - What the agent is keeping track of

    /// A challenge in hand, and how many times its nonce has been used.
    private struct Credentials {
        var challenge: SIPChallenge
        var count = 0
    }

    private struct Registration {
        var callID = SIPRandom.token(16)
        var tag = SIPRandom.token(10)
        var seq = 0
        var attempts = 0
        var credentials: Credentials?
    }

    /// One call. There is at most one at a time in this slice; call waiting
    /// and transfers come with the rest of the app.
    private struct Call {
        let id: String
        let localTag: String
        var peer: SIPPeer
        var incoming: Bool
        var media: any SIPCallMedia
        var seq = 0
        var remoteURI = ""
        var remoteTarget = ""
        var remoteTag: String?
        var routeSet: [String] = []
        var inviteBranch = ""
        var invite: SIPMessage?
        /// The answer, being made while the phone rings: looking the relay
        /// up and gathering this phone's routes takes seconds, and doing it
        /// after the person pressed Answer left them in silence and the
        /// caller hearing it ring on (2026-10-11).
        var preparing: Task<String, Error>?
        var offer: String?
        var credentials: Credentials?
        var attempts = 0
        var established = false
        var earlyMedia = false
        /// A re-INVITE of this phone's is out and hasn't been answered yet
        /// (a move onto the relay).
        var reinviting = false
        var reinviteOffer: String?
        var reinviteRequest: SIPMessage?
        var reinviteAttempts = 0
        /// Busy changing the call: one change at a time.
        var changing = false
        /// The re-INVITE that is out moves the call onto the relay.
        var reinviteForRelay = false
        /// Rung as a video call (X-Linx-Video, ADR-087).
        var videoCall = false
        /// Rung to invite this phone into a group call (X-Linx-Room, ADR-088).
        var invitation = false
        /// The call has been moved onto the relay once already.
        var movedToRelay = false
        /// The line to Linx dropped during the call, and the call is held
        /// for it to come back (`closed`, `reattach`).
        var lineLost = false
        var lineLostSince: Date?
        var giveUp: Task<Void, Never>?
        /// The sequence number of the UPDATE telling Asterisk where the
        /// phone is now.
        var refreshing: Int?
        /// The sound's route needs looking for again once nothing is in
        /// the way (the line back, the change that is out answered).
        var restartWanted = false
        var reinviteTimeout: Task<Void, Never>?
        var ringingMarked = false
        var timeout: Task<Void, Never>?
    }
}

extension SIPMessage {
    /// A response with our own tag on its To header, which is what makes the
    /// phone one end of a dialog (RFC 3261 §12.1.1).
    func withTag(_ tag: String) -> SIPMessage {
        var message = self
        if let to = message.first("To"), SIPMessage.parameter("tag", in: to) == nil {
            message.set("To", "\(to);tag=\(tag)")
        }
        return message
    }
}
