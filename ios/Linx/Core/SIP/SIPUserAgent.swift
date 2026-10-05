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
    /// Whose camera is on in this call, and the two pictures themselves
    /// (docs/PHASE2.md §7). A call is sound until somebody asks for more.
    var video: CallVideo { get }
    var tracks: VideoTracks { get }
    var onVideoChanged: ((CallVideo) -> Void)? { get set }
    /// The link hasn't the room for a picture any more.
    var onVideoTooExpensive: (() -> Void)? { get set }
    /// Switches this phone's camera on or off and answers with the offer
    /// that tells the other side.
    func startVideo() async throws -> String
    func stopVideo() async throws -> String
    func switchCamera()
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
    var onIncoming: ((SIPPeer) -> Void)?
    /// Their phone is ringing (180 or 183).
    var onProgress: (() -> Void)?
    var onEstablished: (() -> Void)?
    var onEnded: ((SIPEnded) -> Void)?
    /// A picture couldn't be added, and why in plain words.
    var onVideoRefused: ((String) -> Void)?

    private let account: Account
    private let transport: any SIPTransport
    private let makeMedia: @MainActor () -> any SIPCallMedia
    /// A host that is this phone and nowhere else: .invalid is reserved for
    /// exactly this (RFC 2606), and the websocket, not the address, is what
    /// Asterisk answers on.
    private let localHost = SIPRandom.token(10) + ".invalid"

    private var registration = Registration()
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
        media: @escaping @MainActor () -> any SIPCallMedia
    ) {
        self.account = account
        self.transport = transport
        self.makeMedia = media
        transport.onOpen = { [weak self] in self?.register() }
        transport.onText = { [weak self] text in self?.read(text) }
        transport.onClose = { [weak self] why in self?.closed(why) }
    }

    // MARK: - Signing in

    func start() {
        onStatus?(.connecting)
        transport.start()
    }

    /// stop ends the call, signs the line out and closes the connection.
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
    func call(_ number: String, name: String? = nil) async {
        guard call == nil else { return }
        let target = number.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        let peer = SIPPeer(name: name ?? target, number: target)
        var outgoing = Call(
            id: SIPRandom.token(16) + "@" + localHost, localTag: SIPRandom.token(10),
            peer: peer, incoming: false, media: makeMedia())
        outgoing.remoteURI = "sip:\(target)@\(account.domain)"
        outgoing.remoteTarget = outgoing.remoteURI
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
        addCommonHeaders(&message)
        if outgoing.credentials != nil {
            message.add("Authorization", answer(&outgoing.credentials, method: "INVITE", uri: outgoing.remoteURI))
        }
        message.add("Content-Type", "application/sdp")
        message.body = offer
        outgoing.invite = message
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
            answer = try await incoming.media.answer(to: invite.body)
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
        send(ok)
        incoming.established = true
        call = incoming
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

    // MARK: - The picture (docs/PHASE2.md §7)

    /// Turns this phone's camera on or off in a call that is already up, and
    /// tells the other side with a re-INVITE. The call itself never stops:
    /// video comes and goes inside it, which is why a bad network can drop
    /// the picture and leave the conversation alone.
    func setVideo(_ on: Bool) async {
        guard let current = call, current.established else { return }
        guard on else {
            await stopTheCamera(of: current)
            return
        }
        guard !current.changing, !current.media.video.mine else { return }
        call?.changing = true
        defer { call?.changing = false }
        do {
            let offer = try await current.media.startVideo()
            guard call?.id == current.id else { return }
            sendReinvite(offer, of: current)
        } catch let trouble as CameraTrouble {
            onVideoRefused?(trouble.words)
        } catch {
            onVideoRefused?("This phone couldn't start its camera. Try again in a moment.")
        }
    }

    /// Stop video always happens. The camera is the person's, not the other
    /// side's and not the switch's: it goes off here and the screen goes
    /// back to the call, whatever is still in the air — a change not yet
    /// answered, or a far end that never answers at all. Only the message
    /// that tells the other side has to wait its turn, and a call carrying
    /// a camera that is off costs nothing but a line of SDP (the owner's
    /// phone sat on the video screen with no way back, 2026-10-04).
    private func stopTheCamera(of current: Call) async {
        call?.changing = true
        defer { call?.changing = false }
        let offer = try? await current.media.stopVideo()
        guard call?.id == current.id else { return }
        // One change at a time (RFC 3261 §14.1): if the last one hasn't
        // been answered, the camera is off here and the other side finds
        // out from the next offer either side makes.
        guard let offer, !(call?.reinviting ?? false) else { return }
        sendReinvite(offer, of: current)
    }

    /// Hands the camera over to the one on the other side of the phone.
    func switchCamera() { call?.media.switchCamera() }

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
        send(message)
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
        guard let message = SIPMessage(text: text) else { return }
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
        guard cseq.method == "INVITE" else { return }
        if let callID = message.callID, let invite = abandoned[callID] {
            // The person hung up before this came back. Acknowledge it, and
            // hang up properly if it turns out they were answered.
            lastWord(message, status: status, invite: invite, callID: callID)
            return
        }
        guard var current = call, message.callID == current.id else { return }
        if current.reinviting {
            reinviteResponse(message, status: status, of: current)
            return
        }
        switch status {
        case 100: return
        case 180, 183:
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
            send(ack2xx(for: message, of: current))
            Task { [weak self] in
                guard let self else { return }
                do {
                    // The sound was already agreed if Asterisk sent it early.
                    if !current.earlyMedia {
                        try await current.media.accept(answer: message.body)
                    }
                    guard self.call?.id == current.id else { return }
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

    /// What came back from this phone's own re-INVITE: the picture going
    /// into a call that is up, or coming out of it. The conversation itself
    /// is untouched whichever way it goes — that is the whole reason video
    /// is added to a call rather than being a different kind of call.
    private func reinviteResponse(_ message: SIPMessage, status: Int, of current: Call) {
        switch status {
        case 100..<200:
            return
        case 200..<300:
            call?.reinviting = false
            call?.reinviteOffer = nil
            call?.reinviteRequest = nil
            call?.reinviteAttempts = 0
            var ack = SIPMessage.request("ACK", current.remoteTarget)
            ack.add("Via", via(branch: SIPRandom.branch()))
            ack.add("Max-Forwards", "70")
            for route in current.routeSet { ack.add("Route", route) }
            ack.add("From", message.first("From") ?? "\(selfAddress);tag=\(current.localTag)")
            ack.add("To", message.first("To") ?? "<\(current.remoteURI)>")
            ack.add("Call-ID", current.id)
            ack.add("CSeq", "\(message.cseq?.number ?? current.seq) ACK")
            send(ack)
            Task { [weak self] in
                guard let self else { return }
                do {
                    try await current.media.accept(answer: message.body)
                } catch {
                    await current.media.rollbackOffer()
                    self.onVideoRefused?("The call couldn't take a picture just now. The sound is unaffected.")
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
                giveUpOnVideo(current, why: "This phone couldn't turn the video on. The sound is unaffected.")
                return
            }
            call?.reinviteAttempts += 1
            call?.credentials = Credentials(challenge: challenge)
            call?.reinviting = false
            guard let again = call else { return }
            sendReinvite(offer, of: again)
        default:
            ackReinvite(message, of: current)
            // 491 is both sides asking at once (RFC 3261 §14.1): rare with
            // one switch in the middle, and the person simply presses the
            // button again.
            giveUpOnVideo(
                current,
                why: status == 491
                    ? "Both phones changed the call at the same moment. Try the video button again."
                    : "The other phone wouldn't take video. The sound is unaffected.")
        }
    }

    /// Puts the call back exactly as it was before the offer nobody
    /// accepted, and says so once.
    private func giveUpOnVideo(_ current: Call, why: String) {
        call?.reinviting = false
        call?.reinviteOffer = nil
        call?.reinviteRequest = nil
        call?.reinviteAttempts = 0
        Task { [weak self] in
            await current.media.rollbackOffer()
            self?.onVideoRefused?(why)
        }
    }

    private func registerResponse(_ message: SIPMessage, status: Int) {
        switch status {
        case 100..<200: return
        case 200..<300:
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
            register()
        default:
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
        incoming.remoteTag = invite.fromTag
        incoming.remoteURI = SIPMessage.uri(in: from)
        incoming.remoteTarget = SIPMessage.uri(in: invite.first("Contact") ?? from)
        incoming.routeSet = invite.all("Record-Route")
        call = incoming
        var ringing = SIPMessage.response(180, "Ringing", to: invite).withTag(incoming.localTag)
        ringing.add("Contact", contact)
        send(ringing)
        onIncoming?(peer)
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
                self.send(.response(488, "Not Acceptable Here", to: invite).withTag(current.localTag))
            }
        }
    }

    private func closed(_ why: String?) {
        registerTimer?.cancel()
        registerTimer = nil
        if let current = call {
            end(current, why: .failed("The call dropped: this phone lost touch with Linx."))
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

    private func send(_ message: SIPMessage) { transport.send(message.text) }

    private func end(_ current: Call, why: SIPEnded, tell: Bool = true) {
        guard call?.id == current.id else { return }
        call?.timeout?.cancel()
        current.media.stop()
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
        var offer: String?
        var credentials: Credentials?
        var attempts = 0
        var established = false
        var earlyMedia = false
        /// A re-INVITE of this phone's is out and hasn't been answered yet
        /// (adding or taking away the picture).
        var reinviting = false
        var reinviteOffer: String?
        var reinviteRequest: SIPMessage?
        var reinviteAttempts = 0
        /// Busy asking the camera for something: one at a time.
        var changing = false
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
