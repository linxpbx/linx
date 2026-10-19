import Foundation

// One call provider for the whole app, with several accounts behind it
// (docs/MULTI_ACCOUNT.md §6).
//
// iOS wants **one** `CXProvider` per app. Until there could be more than one
// account, each phone line made its own, which was the same thing. Now the
// app makes one and every account's line works through it, and each call is
// handed back to the account whose call it is, by the id iOS already gives
// every call.
//
// Each line is a `SystemCalls` of its own, so nothing in `PhoneModel`
// changed: it still asks the system and is told by the system, and never
// learns that another account exists.

@MainActor final class SystemCallsRouter {
    /// The one real system underneath: CallKit, or the in-app ringing where
    /// CallKit may not be used (`CallStyle`).
    private let system: any SystemCalls
    /// Which line each call belongs to, by the id the system knows it by.
    /// Weakly, like every reference here: a line is owned by its account's
    /// `PhoneModel`, and a call left behind by an account that has gone must
    /// not keep it alive.
    private var owners: [UUID: WeakLine] = [:]
    /// Every line, so a system reset reaches all of them.
    private var lines: [WeakLine] = []
    /// The call the microphone and the speaker belong to. The system says
    /// "the sound is yours now" without naming a call, so the app has to
    /// know which one it meant: it is the one most recently answered or
    /// placed, which is the only call that can be using them.
    private var soundIsFor: UUID?

    private struct WeakLine {
        weak var line: SystemCallsLine?
    }

    init(system: (any SystemCalls)? = nil) {
        self.system = system ?? CallStyle.systemCalls()
        self.system.onRequest = { [weak self] request in self?.deliver(request) }
        self.system.onAudio = { [weak self] on in self?.deliverAudio(on) }
        self.system.onReset = { [weak self] in self?.deliverReset() }
    }

    /// A line of its own for one account, which is what its `PhoneModel`
    /// talks to.
    func line() -> SystemCallsLine {
        let line = SystemCallsLine(router: self)
        lines.append(WeakLine(line: line))
        lines.removeAll { $0.line == nil }
        return line
    }

    /// Whether a call of this app's also goes into the iPhone's own Phone
    /// app. It is the app's setting, not an account's.
    func showCallsInThePhoneApp(_ on: Bool) { system.showCallsInThePhoneApp(on) }

    // MARK: - What a line asks of the system

    fileprivate func reportIncoming(_ id: UUID, from: SIPPeer, video: Bool, for line: SystemCallsLine) async -> Bool {
        // Written down before the system is told, because the system can
        // answer the call before `reportNewIncomingCall` has even returned.
        owners[id] = WeakLine(line: line)
        let taken = await system.reportIncoming(id: id, from: line.named(from), video: video)
        if !taken { forget(id) }
        return taken
    }

    fileprivate func ask(_ request: SystemCallRequest, for line: SystemCallsLine) {
        switch request {
        case .start(let id, let peer):
            owners[id] = WeakLine(line: line)
            soundIsFor = id
            system.ask(.start(id, line.named(peer)))
        case .answer(let id):
            soundIsFor = id
            system.ask(request)
        default:
            system.ask(request)
        }
    }

    fileprivate func rename(_ id: UUID, to peer: SIPPeer, for line: SystemCallsLine) {
        system.rename(id: id, to: line.named(peer))
    }

    fileprivate func reportVideo(_ id: UUID, on: Bool) { system.reportVideo(id: id, on: on) }
    fileprivate func reportRingingThere(_ id: UUID) { system.reportRingingThere(id: id) }

    fileprivate func reportAnswered(_ id: UUID, at: Date) {
        soundIsFor = id
        system.reportAnswered(id: id, at: at)
    }

    fileprivate func reportEnded(_ id: UUID, _ ending: SystemCallEnding) {
        system.reportEnded(id: id, ending)
        forget(id)
    }

    private func forget(_ id: UUID) {
        owners[id] = nil
        if soundIsFor == id { soundIsFor = nil }
    }

    // MARK: - What the system tells the app

    private func deliver(_ request: SystemCallRequest) {
        guard let line = owners[request.id]?.line else {
            // A call no account owns: one this app has already forgotten, or
            // one left over from a provider that was replaced. Nothing to do
            // but let it go — the system is told the call ended, so it
            // doesn't sit on the lock screen.
            if case .end = request { return }
            system.reportEnded(id: request.id, .failed)
            return
        }
        if case .answer(let id) = request { soundIsFor = id }
        line.onRequest?(request)
    }

    private func deliverAudio(_ on: Bool) {
        // The call it belongs to, or — if that call has just gone — the one
        // line that still has a call, which is the only one it could be for.
        if let id = soundIsFor, let line = owners[id]?.line {
            line.onAudio?(on)
            return
        }
        let busy = owners.values.compactMap(\.line)
        if Set(busy.map(ObjectIdentifier.init)).count == 1, let line = busy.first {
            line.onAudio?(on)
            return
        }
        // No call of any account's: the app still has to put WebRTC's sound
        // back where it found it, and every line does that the same way.
        lines.first?.line?.onAudio?(on)
    }

    private func deliverReset() {
        owners.removeAll()
        soundIsFor = nil
        for held in lines { held.line?.onReset?() }
        lines.removeAll { $0.line == nil }
    }
}

/// One account's view of the system's calls. It is a `SystemCalls` like any
/// other, so a `PhoneModel` cannot tell the difference between this and
/// owning the provider outright.
@MainActor final class SystemCallsLine: SystemCalls {
    var onRequest: ((SystemCallRequest) -> Void)?
    var onAudio: ((Bool) -> Void)?
    var onReset: (() -> Void)?

    /// What this account is called on the lock screen, in a car and in the
    /// Phone app's Recents, where there is one line of text and it has to
    /// carry both the caller and the account (§6). Nil with one account,
    /// which is every phone until somebody adds a second.
    var accountLabel: String?

    /// Held, not borrowed: the router keeps every reference to a line weak,
    /// so there is no circle here, and a line handed to a `PhoneModel`
    /// must not be left pointing at a provider that has gone.
    private let router: SystemCallsRouter

    fileprivate init(router: SystemCallsRouter) {
        self.router = router
    }

    /// The caller with this account's name beside them — "Ahmed Khan —
    /// Work" — because the system gives a call one line of text and the app
    /// name under it is always Linx's own.
    fileprivate func named(_ peer: SIPPeer) -> SIPPeer {
        guard let accountLabel, !accountLabel.isEmpty else { return peer }
        let who = peer.name.isEmpty ? peer.number : peer.name
        guard !who.isEmpty else { return SIPPeer(name: accountLabel, number: peer.number) }
        return SIPPeer(name: "\(who) — \(accountLabel)", number: peer.number)
    }

    func reportIncoming(id: UUID, from: SIPPeer, video: Bool) async -> Bool {
        await router.reportIncoming(id, from: from, video: video, for: self)
    }
    func rename(id: UUID, to peer: SIPPeer) { router.rename(id, to: peer, for: self) }
    func reportVideo(id: UUID, on: Bool) { router.reportVideo(id, on: on) }
    func reportRingingThere(id: UUID) { router.reportRingingThere(id) }
    func reportAnswered(id: UUID, at: Date) { router.reportAnswered(id, at: at) }
    func reportEnded(id: UUID, _ ending: SystemCallEnding) { router.reportEnded(id, ending) }
    func showCallsInThePhoneApp(_ on: Bool) { router.showCallsInThePhoneApp(on) }
    func ask(_ request: SystemCallRequest) { router.ask(request, for: self) }
}
