import Foundation
import SwiftUI

// The phone, as the screens see it: whether the line is up, what the one call
// is doing, and the last few calls (docs/PHASE2.md §12, step 4b). The SIP of
// it is in Core/SIP and the sound in Core/Media; this is the part that
// decides what a person sees and what the buttons do.

@MainActor @Observable final class PhoneModel {
    enum Status: Equatable {
        case starting
        case ready
        /// The connection dropped and the app is getting it back.
        case reconnecting
        /// No calls from this phone, and why in plain words.
        case unavailable(String)
    }

    struct Call: Equatable {
        enum Phase: Equatable {
            /// Someone is calling this phone.
            case ringing
            /// This phone is calling someone.
            case calling
            case active
        }

        var peer: SIPPeer
        var incoming: Bool
        var phase: Phase
        var answeredAt: Date?
        var muted = false
        var speaker = false
        /// Their phone is ringing (Asterisk said so).
        var ringingThere = false
        var connection: MediaConnection?
    }

    struct Recent: Identifiable, Equatable {
        enum Kind: String { case outgoing, incoming, missed }

        let id = UUID()
        var peer: SIPPeer
        var kind: Kind
        var at: Date
    }

    private(set) var status: Status = .starting
    private(set) var call: Call?
    private(set) var recent: [Recent] = []
    /// The last thing that didn't work, for the screen to show once.
    private(set) var problem: String?
    /// What's on the keypad.
    var typed = ""

    /// Calls to *43 hear themselves back (the echo test, docs/PBX.md).
    static let echoTest = "*43"

    /// Everything the app needs to open a line: the SIP login, which Linx
    /// server it is, and the device token the relay knows this phone by.
    struct Line: Sendable {
        var line: PhoneLine
        var server: URL
        var token: String
    }

    private let lineForThisPhone: @MainActor () async -> Line?
    private let makeTransport: @MainActor (SIPUserAgent.Account) -> any SIPTransport
    private let makeMedia: @MainActor (PhoneLine.Turn?) -> any SIPCallMedia
    private let ringer = Ringer()

    private var agent: SIPUserAgent?
    private var reconnect: Task<Void, Never>?
    private var attempts = 0
    private var stopped = false

    init(
        line: @escaping @MainActor () async -> Line?,
        transport: @escaping @MainActor (SIPUserAgent.Account) -> any SIPTransport = {
            WebSocketTransport(url: $0.websocket, token: $0.token)
        },
        media: @escaping @MainActor (PhoneLine.Turn?) -> any SIPCallMedia = { WebRTCMedia(turn: $0) }
    ) {
        self.lineForThisPhone = line
        self.makeTransport = transport
        self.makeMedia = media
    }

    // MARK: - The line

    /// start signs the phone line in. It is called once the phone is signed
    /// in to Linx, and again by itself whenever the connection drops.
    func start() async {
        stopped = false
        guard let line = await lineForThisPhone() else {
            status = .unavailable("Linx couldn't give this phone a line. It will try again.")
            retryLater()
            return
        }
        connect(to: line)
    }

    /// stop closes the line (the app went away, or the person signed out).
    func stop() {
        stopped = true
        reconnect?.cancel()
        reconnect = nil
        ringer.stop()
        agent?.stop()
        agent = nil
        call = nil
        status = .starting
    }

    private func connect(to line: Line) {
        agent?.stop()
        guard let account = Self.account(for: line) else {
            status = .unavailable("Linx gave this phone a line it couldn't read. Set the phone up again.")
            return
        }
        let turn = line.line.turn
        let agent = SIPUserAgent(
            account: account, transport: makeTransport(account),
            media: { [makeMedia] in
                let media = makeMedia(turn)
                media.onConnection = { [weak self] connection in
                    self?.call?.connection = connection
                }
                return media
            })
        agent.onStatus = { [weak self] status in self?.lineChanged(status) }
        agent.onIncoming = { [weak self] peer in self?.incoming(peer) }
        agent.onProgress = { [weak self] in self?.call?.ringingThere = true }
        agent.onEstablished = { [weak self] in self?.answered() }
        agent.onEnded = { [weak self] why in self?.ended(why) }
        self.agent = agent
        status = .starting
        agent.start()
    }

    private func lineChanged(_ status: SIPStatus) {
        switch status {
        case .connecting:
            self.status = .starting
        case .ready:
            attempts = 0
            self.status = .ready
            problem = nil
        case .reconnecting:
            self.status = .reconnecting
            retryLater()
        case .unavailable(let why):
            // Usually the line's password has moved on (the app asked for a
            // new one): ask for the line again, which is a new password and a
            // fresh token both.
            self.status = .unavailable(why)
            retryLater()
        }
    }

    /// Tries again, waiting longer each time up to half a minute, so a phone
    /// on a train doesn't hammer the network or the battery.
    private func retryLater() {
        guard !stopped, reconnect == nil else { return }
        attempts += 1
        let wait = min(30, Int(pow(2.0, Double(min(attempts, 5)))))
        reconnect = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self, !self.stopped else { return }
            self.reconnect = nil
            await self.start()
        }
    }

    // MARK: - Calls

    /// dial rings whatever is on the keypad.
    func dial() {
        let number = typed
        typed = ""
        callNumber(number)
    }

    func callNumber(_ number: String, name: String? = nil) {
        guard case .ready = status, call == nil else { return }
        let target = number.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        problem = nil
        let peer = SIPPeer(name: name ?? (target == Self.echoTest ? "Test sound" : target), number: target)
        call = Call(peer: peer, incoming: false, phase: .calling)
        Task { [weak self] in await self?.agent?.call(target, name: peer.name) }
    }

    func answer() {
        guard call?.phase == .ringing else { return }
        ringer.stop()
        Task { [weak self] in await self?.agent?.answer() }
    }

    func decline() {
        ringer.stop()
        agent?.decline()
    }

    func hangUp() {
        ringer.stop()
        agent?.hangUp()
    }

    func toggleMute() {
        guard var current = call, current.phase == .active else { return }
        current.muted.toggle()
        agent?.setMuted(current.muted)
        call = current
    }

    func toggleSpeaker() {
        guard var current = call else { return }
        current.speaker.toggle()
        agent?.setSpeaker(current.speaker)
        call = current
    }

    /// A keypad press during a call (a menu, an extension, a conference PIN).
    func sendTone(_ digit: Character) {
        guard call?.phase == .active else { return }
        agent?.sendTone(digit)
    }

    private func incoming(_ peer: SIPPeer) {
        call = Call(peer: peer, incoming: true, phase: .ringing)
        ringer.start()
    }

    private func answered() {
        ringer.stop()
        guard var current = call else { return }
        current.phase = .active
        current.answeredAt = Date()
        current.ringingThere = false
        call = current
    }

    private func ended(_ why: SIPEnded) {
        ringer.stop()
        guard let finished = call else { return }
        call = nil
        if case .failed(let said) = why { problem = said }
        guard finished.peer.number != Self.echoTest else { return }
        let kind: Recent.Kind =
            finished.incoming ? (finished.answeredAt == nil ? .missed : .incoming) : .outgoing
        recent = Array(([Recent(peer: finished.peer, kind: kind, at: Date())] + recent).prefix(20))
    }

    #if DEBUG
        /// Made-up contents for the screenshot harness and the tests
        /// (`ios/tools/screens.sh`). A release build has none of this, and it
        /// never touches a network, a key or a real call.
        func pretend(_ call: Call) { self.call = call }

        func pretend(_ status: Status) { self.status = status }
    #endif

    /// What the app needs out of the phone line Linx handed it: the SIP
    /// account, and the websocket it signs in over.
    static func account(for line: Line) -> SIPUserAgent.Account? {
        let sip = line.line
        let uri = sip.sipURI.hasPrefix("sip:") ? String(sip.sipURI.dropFirst(4)) : sip.sipURI
        guard let at = uri.lastIndex(of: "@") else { return nil }
        let domain = String(uri[uri.index(after: at)...])
        guard !domain.isEmpty, let path = URL(string: sip.websocketPath, relativeTo: line.server),
            var parts = URLComponents(url: path, resolvingAgainstBaseURL: true)
        else { return nil }
        // Always wss: the app never speaks SIP in the clear, and never falls
        // back to anything that does.
        parts.scheme = "wss"
        guard let websocket = parts.url, parts.host?.isEmpty == false else { return nil }
        return SIPUserAgent.Account(
            username: sip.sipUsername, password: sip.password, domain: domain,
            displayName: sip.displayName, websocket: websocket, token: line.token)
    }
}
