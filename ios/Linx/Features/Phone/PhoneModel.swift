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
        /// Whose camera is on. A call is sound until somebody presses the
        /// video button; then it is still the same call, with a picture in
        /// it (docs/PHASE2.md §7).
        var video = CallVideo()
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
    /// The pictures of the call that is up, for the screen to draw. It is
    /// the live media's own, so it is empty whenever there is no call.
    private(set) var tracks = VideoTracks()
    /// Whether this phone's own picture should be mirrored (a front camera).
    private(set) var mirrorsMyVideo = true
    /// Which picture has the big screen. The other person's by default — that
    /// is who you are talking to — and this phone's own when the person taps
    /// to swap them, which puts the other picture in the small tile (owner's
    /// ask, 2026-10-05). It lasts for the call, and only means anything while
    /// both cameras are on.
    private(set) var myPictureIsBig = false
    /// Where the sound of the call is coming out, read from the system
    /// rather than assumed. The call screen shows a plain Speaker switch
    /// while it is the phone's own earpiece or loudspeaker, and the
    /// system's picker once anything else is connected.
    private(set) var audioRoute: AudioRoute?
    /// Why the call sounds the way it does, for the Call details screen: the
    /// routes this phone found, what Linx's relay said, how far the connection
    /// got. It is the answer to "the call connected and I can't hear anything",
    /// which no other screen can give.
    private(set) var diagnostics = CallDiagnostics()
    /// A camera is going on or coming off the call that is up. On a mobile
    /// network that takes a moment — the picture needs a way through Linx's
    /// relay of its own — so the button says so instead of looking broken
    /// (owner, 2026-10-05: "it doesn't work immediately").
    private(set) var changingVideo = false
    /// They turned their camera on while this phone's was off, so the
    /// person is asked once whether to turn theirs on too (ADR-079, owner
    /// 2026-10-04). Saying no leaves a **one-way video call**, which is a
    /// perfectly ordinary call: they are seen, this phone is heard.
    private(set) var askAboutTheirVideo: SIPPeer?
    private(set) var recent: [Recent] = []
    /// The last thing that didn't work, for the screen to show once.
    private(set) var problem: String?
    /// What's on the keypad.
    var typed = ""
    /// The last number this phone rang, for the keypad's redial: pressing
    /// the call button with nothing typed fills it in, as a phone has
    /// always done. The sound test isn't a number anybody redials.
    private(set) var lastDialled = ""

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
    /// The video button started this call: the camera goes on as soon as
    /// the other side picks up.
    private var videoOnceAnswered = false
    /// Whether this call has already asked about their camera.
    private var askedAboutTheirVideo = false
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

    /// The call relay's credentials, and the job that keeps them fresh.
    ///
    /// They last an hour; a phone is signed in for days. A call made with
    /// expired ones gets no relay at all, which on a mobile network is the
    /// only way the sound can go — the call connects and nobody hears
    /// anything (owner, 2026-10-04, on 5G). The browser has refreshed them
    /// since Phase 1C (`web/src/phone/line.ts`); this is the app's side of
    /// the same thing.
    private var relay: PhoneLine.Turn?
    private var relayRefresh: Task<Void, Never>?

    private func keepTheRelayFresh(for line: Line) {
        relayRefresh?.cancel()
        relayRefresh = Task { [weak self] in
            while !Task.isCancelled {
                guard let until = self?.relay?.expiresAt else { return }
                // Five minutes before they run out, as the browser does,
                // and never less than half a minute from now.
                let wait = max(30, until.timeIntervalSinceNow - 5 * 60)
                try? await Task.sleep(for: .seconds(wait))
                guard !Task.isCancelled, let self, !self.stopped else { return }
                do {
                    let fresh = try await LinxClient(server: line.server)
                        .turnCredentials(token: line.token)
                    guard !Task.isCancelled else { return }
                    self.relay = fresh
                    // A call that is already up gets them too, so a long
                    // call can still mend a broken route.
                    self.liveMedia?.use(relay: fresh)
                } catch {
                    // The network, or a token that has moved on. Try again
                    // shortly rather than give up for the rest of the day.
                    try? await Task.sleep(for: .seconds(60))
                }
            }
        }
    }

    /// stop closes the line (the app went away, or the person signed out).
    func stop() {
        stopped = true
        reconnect?.cancel()
        reconnect = nil
        relayRefresh?.cancel()
        relayRefresh = nil
        ringer.stop()
        UIDevice.current.isProximityMonitoringEnabled = false
        holdTheScreen(false)
        agent?.stop()
        agent = nil
        if let call { calls.reportEnded(id: call.id, .failed) }
        call = nil
        videoOnceAnswered = false
        askAboutTheirVideo = nil
        askedAboutTheirVideo = false
        changingVideo = false
        saidAboutTheRelay = false
        myPictureIsBig = false
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
        relay = line.line.turn
        keepTheRelayFresh(for: line)
        let agent = SIPUserAgent(
            account: account, transport: makeTransport(account),
            media: { [makeMedia, weak self] in
                let media = makeMedia(self?.relay)
                media.onConnection = { [weak self] connection in
                    self?.call?.connection = connection
                }
                media.onRoute = { [weak self] route in self?.soundIsComingOut(route) }
                media.onRouteTrouble = { [weak self] words in self?.problem = words }
                media.onDiagnostics = { [weak self] what in self?.callDiagnostics(what) }
                self?.diagnostics = media.diagnostics
                media.onVideoChanged = { [weak self] video in self?.videoChanged(video) }
                media.onVideoTooExpensive = { [weak self] in self?.videoCostTooMuch() }
                media.onPictureGone = { [weak self] in
                    Task { await self?.agent?.dropVideo() }
                }
                self?.liveMedia = media
                self?.tracks = media.tracks
                return media
            })
        agent.onStatus = { [weak self] status in self?.lineChanged(status) }
        agent.onIncoming = { [weak self] peer in self?.incoming(peer) }
        agent.onProgress = { [weak self] in self?.ringingThere() }
        agent.onEstablished = { [weak self] in self?.answered() }
        agent.onEnded = { [weak self] why in self?.ended(why) }
        agent.onVideoRefused = { [weak self] why in self?.problem = why }
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

    /// `withVideo` is the video button in the Team list: the call still
    /// rings as an ordinary call — that is what a lock screen and a car
    /// understand — and this phone's camera goes on the moment it is
    /// answered (docs/PHASE2.md §7).
    func callNumber(_ number: String, name: String? = nil, withVideo: Bool = false) {
        guard case .ready = status, call == nil else { return }
        let target = number.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        problem = nil
        videoOnceAnswered = withVideo
        if target != Self.echoTest { lastDialled = target }
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

    // MARK: - The picture (docs/PHASE2.md §7)

    /// Turns this phone's camera on or off in the call that is up. The call
    /// itself carries straight on either way — that is why video is added to
    /// a call here and never rung as a different kind of call.
    func toggleVideo() {
        guard let call, call.phase == .active, !changingVideo else { return }
        let on = !call.video.mine
        problem = nil
        // Everyone expects a video call to come out of the loudspeaker; a
        // phone held to an ear with the camera on would show a ceiling.
        if on, !call.speaker { toggleSpeaker() }
        changingVideo = true
        Task { [weak self] in
            await self?.agent?.setVideo(on)
            guard let self else { return }
            self.changingVideo = false
            guard !on, let media = self.liveMedia else { return }
            // Whatever the other side made of it, the screen follows the
            // camera: if it is off, this is the call's own screen again.
            self.videoChanged(media.video)
        }
    }

    /// Whether a Linx call also goes into the iPhone's own Phone app
    /// (Settings → Your call history). It takes effect from the next call.
    func showCallsInThePhoneApp(_ on: Bool) { calls.showCallsInThePhoneApp(on) }

    /// Swaps the two pictures over: a tap on either one puts it on the big
    /// screen and sends the other to the small tile. With only one picture in
    /// the call there is nothing to swap, so nothing happens.
    func swapPictures() {
        guard let call, call.video.mine, call.video.theirs else { return }
        myPictureIsBig.toggle()
    }

    /// Whether tapping a picture does anything right now — both cameras on.
    var canSwapPictures: Bool {
        guard let call else { return false }
        return call.video.mine && call.video.theirs
    }

    /// The camera facing the person, or the one facing what they can see.
    func switchCamera() {
        agent?.switchCamera()
        mirrorsMyVideo = liveMedia?.mirrorsMyVideo ?? true
    }

    /// The person has answered the question above, one way or the other.
    func answeredAboutTheirVideo(turningMineOn: Bool) {
        askAboutTheirVideo = nil
        if turningMineOn { toggleVideo() }
    }

    private func videoChanged(_ video: CallVideo) {
        defer { theScreenDuringTheCall() }
        guard var current = call else { return }
        let was = current.video
        current.video = video
        call = current
        mirrorsMyVideo = liveMedia?.mirrorsMyVideo ?? true
        // A picture that has left the call takes the swap with it: the next
        // one starts the ordinary way round, with the other person on the big
        // screen.
        if !video.mine || !video.theirs { myPictureIsBig = false }
        // Asked once in a call, when their camera comes on while this phone's
        // is off. Never while this phone is already sending — there is nothing
        // to ask then — and never twice, because a picture that comes and goes
        // with a thin link would otherwise keep asking.
        if !was.theirs, video.theirs, !video.mine, !askedAboutTheirVideo {
            askedAboutTheirVideo = true
            askAboutTheirVideo = current.peer
        }
        if video.mine { askAboutTheirVideo = nil }
        // Their camera went off again while the question was still on the
        // screen: there is nothing left to answer.
        if !video.theirs { askAboutTheirVideo = nil }
        // The system shows a call with a picture in it as a video call.
        if was.on != video.on { calls.reportVideo(id: current.id, on: video.on) }
        if was.theirs != video.theirs, video.theirs, !current.speaker {
            // Their picture arrived while this phone was at an ear.
            toggleSpeaker()
        }
    }

    /// The link hasn't the room for a picture any more. The picture goes and
    /// the conversation stays, which is the right way round.
    private func videoCostTooMuch() {
        guard let call, call.video.mine else { return }
        problem = "The connection got too slow for video, so Linx turned your camera off. The call carries on."
        Task { [weak self] in await self?.agent?.setVideo(false) }
    }

    /// The loudspeaker is the app's own: the system has no such action, and
    /// its own route picker sits beside this button.
    func toggleSpeaker() {
        guard var current = call else { return }
        current.speaker.toggle()
        agent?.setSpeaker(current.speaker)
        call = current
    }

    /// Where the sound is actually coming out, read from the system: the
    /// button follows the sound, not the other way round, so a headset
    /// plugged in mid-call moves it.
    /// What the sound button says when it is the system's own route picker:
    /// the name of whatever the sound is coming out of. Nil means there is
    /// nowhere else for it to go, and the button is the plain Speaker switch.
    ///
    /// The picker appears as soon as there is somewhere else — AirPods
    /// connected in the middle of a call are a choice then and there, not on
    /// the next call (owner, 2026-10-05) — and it wears the name of whatever
    /// has the sound now, this phone included.
    var whereTheSoundGoes: String? {
        guard let audioRoute else { return nil }
        guard audioRoute.builtIn else { return audioRoute.name }
        guard audioRoute.others else { return nil }
        return audioRoute.speaker ? "Speaker" : UIDevice.current.model
    }

    private func soundIsComingOut(_ route: AudioRoute) {
        audioRoute = route
        theScreenDuringTheCall()
        guard call?.speaker != route.speaker else { return }
        call?.speaker = route.speaker
    }

    // MARK: - Why a call sounds the way it does

    private func callDiagnostics(_ what: CallDiagnostics) {
        diagnostics = what
        // Said only once the phone has finished looking for routes, and only
        // when it found no way through the relay at all. A relay that answers
        // over TLS and not over UDP is perfectly well — and the one that
        // fails usually fails first, which is how the owner got "couldn't
        // reach Linx's call relay" on a call they could hear (2026-10-05).
        if what.foundTheRelay {
            if saidAboutTheRelay {
                saidAboutTheRelay = false
                problem = nil
            }
            return
        }
        guard let words = what.relayWords, problem == nil else { return }
        problem = words
        saidAboutTheRelay = true
    }

    /// Whether the message on the screen is this call's relay warning, so it
    /// can be taken down again if a route turns up after all.
    private var saidAboutTheRelay = false

    /// Nothing is being heard. A call that has been up for a few seconds with
    /// no sound coming in is broken however well the rest of the screen looks,
    /// and the person is told so rather than left holding a silent phone
    /// (owner, repeatedly, on a mobile network: "I still don't hear anything").
    func noSoundComingIn(at now: Date) -> Bool {
        guard let call, call.phase == .active, let answeredAt = call.answeredAt else { return false }
        guard now.timeIntervalSince(answeredAt) >= Self.silenceIsWrong else { return false }
        return (call.connection?.audioBytesIn ?? 0) == 0
    }

    /// How long a call may be silent before the screen says so. Long enough
    /// for the first reading of the connection to have happened.
    private static let silenceIsWrong: TimeInterval = 7

    /// The two rules about the screen while a call is up: dark at an ear, and
    /// awake while there is a picture in the call.
    private func theScreenDuringTheCall() {
        watchForAnEar()
        keepTheScreenAwake()
    }

    /// The screen goes dark and stops taking taps while the phone is held
    /// to an ear, as every phone has done since phones had screens — and
    /// only then: not on the loudspeaker, not with a headset, and never
    /// during a video call, where the screen is the point (owner,
    /// 2026-10-04: "if I put the phone on my ear the screen is active and
    /// touch is enabled").
    private func watchForAnEar() {
        let atAnEar =
            call != nil && call?.phase == .active && call?.video.on != true
            && audioRoute?.speaker == false && audioRoute?.builtIn == true
        guard UIDevice.current.isProximityMonitoringEnabled != atAnEar else { return }
        UIDevice.current.isProximityMonitoringEnabled = atAnEar
    }

    /// A call with a picture in it **keeps the screen awake**: a video call
    /// that dims and locks itself halfway through is no video call at all, and
    /// the only thing that should turn the screen off during one is the power
    /// button (owner, 2026-10-05). A call with no picture leaves the screen to
    /// iOS exactly as before — holding a phone to an ear is what the rule
    /// above is for, and a call in a pocket has no business keeping a screen
    /// alight. It is let go of the moment the picture or the call ends, so a
    /// phone left on a table after a video call sleeps as it should.
    private func keepTheScreenAwake() {
        holdTheScreen(call?.phase == .active && call?.video.on == true)
    }

    private func holdTheScreen(_ awake: Bool) {
        guard screenIsHeldAwake != awake else { return }
        screenIsHeldAwake = awake
        UIApplication.shared.isIdleTimerDisabled = awake
    }

    /// Whether this call is holding the screen awake. It is the app's own
    /// decision, kept here rather than read back from `isIdleTimerDisabled`,
    /// because iOS only honours that flag while the app is in front — so the
    /// flag answers "is the screen being held *now*", which is not the same
    /// question and is not one a test can ask.
    private(set) var screenIsHeldAwake = false

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

    /// Whether the app shows its own call screen.
    ///
    /// An incoming call that is still ringing belongs to the **system**:
    /// CallKit puts it on the lock screen, or as a banner over whatever is
    /// on screen when the app is already open, and that banner is what
    /// answers it. The app's own screen on top of it is two rings for one
    /// call (the owner saw both at once on a real iPhone, 2026-10-04), so
    /// the app waits and shows the call once it has been answered. Where
    /// CallKit may not be used (ADR-078) there is no banner and the app's
    /// screen is the only ring there is.
    var showsCallScreen: Bool {
        guard let call else { return false }
        guard systemTakesCalls, call.incoming, call.phase == .ringing else { return true }
        return false
    }

    /// Whether the system takes this phone's calls — CallKit's lock screen,
    /// banner, car and headset. It follows the phone's own region
    /// (`CallStyle`, ADR-078); the tests and the screenshot run set it the
    /// other way to see the in-app ring that mainland China gets.
    var systemTakesCalls = CallStyle.usesCallKit

    private func ringInTheApp() {
        // With CallKit the ring is the system's, played with the person's
        // own ringtone whether the phone is locked, in a pocket or in a car.
        // Where CallKit may not be used, the app rings for itself (ADR-078).
        guard !systemTakesCalls else { return }
        ringer.start()
    }

    private func ringingThere() {
        guard let call else { return }
        self.call?.ringingThere = true
        calls.reportRingingThere(id: call.id)
    }

    private func answered() {
        ringer.stop()
        defer { theScreenDuringTheCall() }
        guard var current = call else { return }
        current.phase = .active
        current.answeredAt = Date()
        current.ringingThere = false
        call = current
        // An incoming call is already up as far as the system is concerned
        // (it asked the app to answer it); an outgoing one is connected the
        // moment the other side picks up.
        if !current.incoming { calls.reportAnswered(id: current.id) }
        if videoOnceAnswered {
            videoOnceAnswered = false
            toggleVideo()
        }
    }

    private func ended(_ why: SIPEnded) {
        ringer.stop()
        UIDevice.current.isProximityMonitoringEnabled = false
        holdTheScreen(false)
        guard let finished = call else { return }
        call = nil
        askAboutTheirVideo = nil
        askedAboutTheirVideo = false
        changingVideo = false
        saidAboutTheRelay = false
        myPictureIsBig = false
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
