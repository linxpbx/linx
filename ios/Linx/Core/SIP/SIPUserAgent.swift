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
    func setMuted(_ muted: Bool)
    /// The loudspeaker, or back to the earpiece.
    func setSpeaker(_ on: Bool)
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
