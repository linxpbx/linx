import Foundation
import SwiftUI
import UIKit

// The phone, as the screens see it: whether the line is up, what the one call
// is doing, and the last few calls (docs/PHASE2.md §12, steps 4b and 6). The
// SIP of it is in Core/SIP, the sound in Core/Media and the system's side of
// a call in Core/Call; this is the part that decides what a person sees and
// what the buttons do.
//
// Every button goes the same way round (`SystemCalls`): the app *asks* the
// system, the system *tells* the app, and only then does the app do it. So
// Answer on the lock screen, Answer in a car and Answer on this phone's own
// screen are one path, and the two screens can never disagree about what the
// call is doing.

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

        /// What the system knows this call by (CallKit's call id). It is
        /// made when the call starts — on a push, before anything else —
        /// and the same id is used until the call is over.
        var id = UUID()
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

    /// How long a woken phone waits for Asterisk's invitation before it
    /// gives up and takes the call off the screen. The server holds the
    /// call about six seconds and then rings whatever phones are there, so
    /// the invitation is normally in well under ten; this is the outside
    /// edge, for a phone on a slow mobile network.
    static let waitForTheInvitation: Duration = .seconds(20)

    /// Everything the app needs to open a line: the SIP login, which Linx
    /// server it is, and the device token the relay knows this phone by.
    struct Line: Sendable {
        var line: PhoneLine
        var server: URL
        var token: String
    }

    /// A phone woken by a push, with the system already ringing, waiting for
    /// the call itself to arrive over SIP.
    private struct Waking {
        let id: UUID
        let from: String
        var giveUp: Task<Void, Never>?
    }

    private let lineForThisPhone: @MainActor () async -> Line?
    private let makeTransport: @MainActor (SIPUserAgent.Account) -> any SIPTransport
    private let makeMedia: @MainActor (PhoneLine.Turn?) -> any SIPCallMedia
    private let calls: any SystemCalls
    /// Whether the app is on the display. A line that came up for a push
    /// and came to nothing is closed again, because a registration left
    /// behind would make Linx think this phone is awake.
    private let inFront: @MainActor () -> Bool
    private let ringer = Ringer()

    private var agent: SIPUserAgent?
    private var reconnect: Task<Void, Never>?
    private var attempts = 0
    private var stopped = false
    private var waking: [Waking] = []
    /// Who an outgoing call is to, by the id the system was given: the
    /// system only keeps a number, and the app keeps the name as well.
    private var intended: [UUID: SIPPeer] = [:]
    /// The sound of the call that is up, for handing the microphone and the
    /// speaker over when the system says so.
    private weak var liveMedia: (any SIPCallMedia)?

    init(
        line: @escaping @MainActor () async -> Line?,
        transport: @escaping @MainActor (SIPUserAgent.Account) -> any SIPTransport = {
            WebSocketTransport(url: $0.websocket, token: $0.token)
        },
        media: @escaping @MainActor (PhoneLine.Turn?) -> any SIPCallMedia = { WebRTCMedia(turn: $0) },
        calls: (any SystemCalls)? = nil,
        inFront: @escaping @MainActor () -> Bool = { UIApplication.shared.applicationState == .active }
    ) {
        self.lineForThisPhone = line
        self.makeTransport = transport
        self.makeMedia = media
        self.calls = calls ?? CallStyle.systemCalls()
        self.inFront = inFront
        self.calls.onRequest = { [weak self] request in self?.systemAsked(request) }
        self.calls.onAudio = { [weak self] on in self?.liveMedia?.systemAudio(on) }
        self.calls.onReset = { [weak self] in self?.systemForgotEverything() }
    }

    // MARK: - The line

    /// start signs the phone line in. It is called once the phone is signed
    /// in to Linx, again by itself whenever the connection drops, and again
    /// when a push wakes the app for a call.
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
        if let call { calls.reportEnded(id: call.id, .failed) }
        call = nil
        forgetWhatWasWoken(.missed)
        status = .starting
    }

    /// busy is "don't close this line": a call is up, or a push woke the app
    /// and the call itself is still on its way.
    var busy: Bool { call != nil || !waking.isEmpty }

    private func connect(to line: Line) {
        agent?.stop()
        guard let account = Self.account(for: line) else {
            status = .unavailable("Linx gave this phone a line it couldn't read. Set the phone up again.")
            return
        }
        let turn = line.line.turn
        let agent = SIPUserAgent(
            account: account, transport: makeTransport(account),
            media: { [makeMedia, weak self] in
                let media = makeMedia(turn)
                media.onConnection = { [weak self] connection in
                    self?.call?.connection = connection
                }
                self?.liveMedia = media
                return media
            })
        agent.onStatus = { [weak self] status in self?.lineChanged(status) }
        agent.onIncoming = { [weak self] peer in self?.incoming(peer) }
        agent.onProgress = { [weak self] in self?.ringingThere() }
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

    // MARK: - Being woken for a call

    /// woken is what a VoIP push does, and the order of it is the whole
    /// point: the system is told there is a call **first**, before a token,
    /// a network call or anything else that can be slow or fail. iOS kills
    /// an app that takes a VoIP push without reporting a call, and stops
    /// sending pushes to one that keeps doing it (docs/PHASE2.md §14 item 1).
    ///
    /// It answers whether the phone is now ringing, which is when the app
    /// goes on to open its line and wait for the call itself.
    func woken(from: String) async -> Bool {
        let id = UUID()
        let peer = Self.caller(from)
        guard await calls.reportIncoming(id: id, from: peer) else { return false }
        // One line, one call: a second caller hears busy and the server
        // sends them to voicemail, as it does for the browser. The push
        // still had to be reported, so the call is ended right away.
        if call != nil {
            calls.reportEnded(id: id, .missed)
            return false
        }
        var woken = Waking(id: id, from: from)
        woken.giveUp = Task { [weak self] in
            try? await Task.sleep(for: Self.waitForTheInvitation)
            guard !Task.isCancelled else { return }
            self?.nothingArrived(id)
        }
        waking.append(woken)
        return true
    }

    /// The call never came: the caller gave up while the phone was waking,
    /// or it was answered somewhere else. The phone stops ringing, and the
    /// line closes again unless someone is looking at the app.
    private func nothingArrived(_ id: UUID) {
        guard let index = waking.firstIndex(where: { $0.id == id }) else { return }
        waking.remove(at: index)
        calls.reportEnded(id: id, .missed)
        closeIfNobodyIsLooking()
    }

    private func forgetWhatWasWoken(_ ending: SystemCallEnding) {
        for woken in waking {
            woken.giveUp?.cancel()
            calls.reportEnded(id: woken.id, ending)
        }
        waking.removeAll()
    }

    private func closeIfNobodyIsLooking() {
        guard !busy, !inFront() else { return }
        stop()
    }

    // MARK: - What the person asks for

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
        let id = UUID()
        intended[id] = peer
        calls.ask(.start(id, peer))
    }

    func answer() {
        guard let call, call.incoming, call.phase == .ringing else { return }
        calls.ask(.answer(call.id))
    }

    func decline() {
        guard let call else { return }
        calls.ask(.end(call.id))
    }

    func hangUp() {
        guard let call else { return }
        calls.ask(.end(call.id))
    }

    func toggleMute() {
        guard let call, call.phase == .active else { return }
        calls.ask(.mute(call.id, !call.muted))
    }

    /// A keypad press during a call (a menu, an extension, a conference PIN).
    func sendTone(_ digit: Character) {
        guard let call, call.phase == .active else { return }
        calls.ask(.tone(call.id, digit))
    }

    /// The loudspeaker is the app's own: the system has no such action, and
    /// its own route picker sits beside this button.
    func toggleSpeaker() {
        guard var current = call else { return }
        current.speaker.toggle()
        agent?.setSpeaker(current.speaker)
        call = current
    }

    // MARK: - What the system tells the app to do

    private func systemAsked(_ request: SystemCallRequest) {
        switch request {
        case .start(let id, let peer):
            place(id: id, to: intended[id] ?? peer)
        case .answer(let id):
            guard call?.id == id, call?.phase == .ringing else { return }
            ringer.stop()
            Task { [weak self] in await self?.agent?.answer() }
        case .end(let id):
            end(id)
        case .mute(let id, let muted):
            guard var current = call, current.id == id else { return }
            current.muted = muted
            agent?.setMuted(muted)
            call = current
        case .tone(let id, let digit):
            guard call?.id == id, call?.phase == .active else { return }
            agent?.sendTone(digit)
        }
    }

    private func place(id: UUID, to peer: SIPPeer) {
        intended.removeValue(forKey: id)
        guard case .ready = status, call == nil else {
            calls.reportEnded(id: id, .failed)
            return
        }
        call = Call(id: id, peer: peer, incoming: false, phase: .calling)
        Task { [weak self] in await self?.agent?.call(peer.number, name: peer.name) }
    }

    private func end(_ id: UUID) {
        if intended.removeValue(forKey: id) != nil {
            // The system wouldn't start the call, so it never began.
            problem = "This phone couldn't start that call. Try again in a moment."
            return
        }
        if let index = waking.firstIndex(where: { $0.id == id }) {
            // The person turned down a call the phone was still waking for.
            waking[index].giveUp?.cancel()
            waking.remove(at: index)
            closeIfNobodyIsLooking()
            return
        }
        guard let call, call.id == id else { return }
        ringer.stop()
        if call.incoming, call.phase == .ringing {
            agent?.decline()
        } else {
            agent?.hangUp()
        }
    }

    /// The system restarted and has forgotten everything it knew. Whatever
    /// this phone had is over with it.
    private func systemForgotEverything() {
        ringer.stop()
        waking.forEach { $0.giveUp?.cancel() }
        waking.removeAll()
        if call != nil { agent?.hangUp() }
    }

    // MARK: - What the line tells the app

    private func incoming(_ peer: SIPPeer) {
        // The call the phone was woken for: the system has been ringing
        // since the push arrived, so it keeps the same id and only learns
        // the caller's name now.
        if let index = matching(peer) {
            let woken = waking.remove(at: index)
            woken.giveUp?.cancel()
            call = Call(id: woken.id, peer: peer, incoming: true, phase: .ringing)
            calls.rename(id: woken.id, to: peer)
            ringInTheApp()
            return
        }
        // The app was already here, so nothing has been reported yet.
        let id = UUID()
        call = Call(id: id, peer: peer, incoming: true, phase: .ringing)
        ringInTheApp()
        Task { [weak self] in
            guard let self else { return }
            guard await self.calls.reportIncoming(id: id, from: peer) else {
                // The system wouldn't have it: a blocked number, or a Focus
                // this caller isn't allowed through. Linx is told the phone
                // is busy and rings whatever else the person has.
                self.ringer.stop()
                self.agent?.decline()
                self.call = nil
                return
            }
        }
    }

    /// Which woken call this invitation is: the caller's number if it says,
    /// otherwise the one that has been waiting longest. Only one call can be
    /// on this line at a time, so there is never much to choose between.
    private func matching(_ peer: SIPPeer) -> Int? {
        guard !waking.isEmpty else { return nil }
        if let exact = waking.firstIndex(where: { Self.sameNumber($0.from, peer.number) }) { return exact }
        return waking.startIndex
    }

    private func ringInTheApp() {
        // With CallKit the ring is the system's, played with the person's
        // own ringtone whether the phone is locked, in a pocket or in a car.
        // Where CallKit may not be used, the app rings for itself (ADR-078).
        guard !CallStyle.usesCallKit else { return }
        ringer.start()
    }

    private func ringingThere() {
        guard let call else { return }
        self.call?.ringingThere = true
        calls.reportRingingThere(id: call.id)
    }

    private func answered() {
        ringer.stop()
        guard var current = call else { return }
        current.phase = .active
        current.answeredAt = Date()
        current.ringingThere = false
        call = current
        // An incoming call is already up as far as the system is concerned
        // (it asked the app to answer it); an outgoing one is connected the
        // moment the other side picks up.
        if !current.incoming { calls.reportAnswered(id: current.id) }
    }

    private func ended(_ why: SIPEnded) {
        ringer.stop()
        guard let finished = call else { return }
        call = nil
        calls.reportEnded(id: finished.id, Self.ending(why))
        if case .failed(let said) = why { problem = said }
        defer { closeIfNobodyIsLooking() }
        guard finished.peer.number != Self.echoTest else { return }
        let kind: Recent.Kind =
            finished.incoming ? (finished.answeredAt == nil ? .missed : .incoming) : .outgoing
        recent = Array(([Recent(peer: finished.peer, kind: kind, at: Date())] + recent).prefix(20))
    }

    private static func ending(_ why: SIPEnded) -> SystemCallEnding {
        switch why {
        case .hungUp: return .hungUp
        case .missed: return .missed
        case .failed: return .failed
        }
    }

    /// Who is calling, from the little a push carries: the number, or that
    /// there wasn't one. The name arrives with the call itself.
    static func caller(_ from: String) -> SIPPeer {
        let number = from.trimmingCharacters(in: .whitespaces)
        return SIPPeer(name: number.isEmpty ? "Number withheld" : number, number: number)
    }

    /// Whether a push's number and an invitation's are the same caller. The
    /// two come by different roads (Apple, then Asterisk) and one of them
    /// may be written with a + or spaces, so only the digits are compared.
    static func sameNumber(_ one: String, _ other: String) -> Bool {
        let digits = { (text: String) in String(text.filter(\.isNumber).suffix(9)) }
        let a = digits(one), b = digits(other)
        return !a.isEmpty && a == b
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
