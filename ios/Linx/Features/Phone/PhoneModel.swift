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
        /// The **HD** button on the video screen, off at the start of every call.
        var hd = false
        /// Rung as a video call (owner, 2026-10-12): the ringing screens say
        /// so, and answering it turns this phone's camera on.
        var videoCall = false
        /// Their phone is ringing (Asterisk said so).
        var ringingThere = false
        /// Whose camera is on. A call is sound until somebody presses the
        /// video button; then it is still the same call, with a picture in
        /// it (docs/PHASE2.md §7).
        var video = CallVideo()
        var connection: MediaConnection?
        /// Mending a call whose sound stopped getting through: how many times
        /// it has been moved onto the relay, when last, and when sound last
        /// arrived (`mendIfSilent`).
        var relayMoves = 0
        var lastRelayMove: Date?
        var soundLastArrived: Date?
        var bytesInLastSeen = 0
        /// Linx's reports on the sound it receives from this phone: the
        /// count last read, and when it last moved.
        var reportsLastSeen = 0
        var linxLastHeardUs: Date?
        /// When a route for the sound was first found: the silence clock
        /// starts here, not at the answer (`mendIfSilent`).
        var routeFoundAt: Date?
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
    /// The small window a video call goes on in while another app is in
    /// front (owner, 2026-10-18).
    let smallWindow = CallPictureInPicture()
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
    /// Whether the in-call keypad and the Call details sheet are open.
    /// They live here rather than in the call screen because the screen
    /// itself is rebuilt whenever the call moves — a phone being opened
    /// out, an iPad turned, a call going from covering the screen to
    /// standing beside the app — and a keypad that closes itself halfway
    /// through someone typing a PIN into a menu is its own small disaster
    /// (owner's point about the fold, 2026-10-05).
    var callKeypadIsOpen = false
    /// The Add person sheet is open (ADR-088).
    var addingPerson = false
    var callDetailsAreOpen = false
    private(set) var recent: [Recent] = []
    /// The last thing that didn't work, for the screen to show once.
    private(set) var problem: String?
    /// Which of this phone's accounts this line is, for the call screen to
    /// name under the caller. Nil with one account, which is every phone
    /// until somebody adds a second (docs/MULTI_ACCOUNT.md §7).
    var accountLabel: String?
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

    /// How long a woken phone waits once its line is signed in. Linx rings a
    /// phone the moment it signs in, so an invitation that hasn't come by
    /// now is for a call that has already ended: the caller gave up while
    /// Apple's push was late. It used to ring on for twenty seconds after
    /// the caller had gone (2026-10-11).
    ///
    /// Ten seconds, not four: on the owner's 5G a just-woken line carried
    /// the invitation nine seconds after it signed in (2026-10-15), and the
    /// call they had already answered was taken off the screen and rang
    /// again. A call the person has answered isn't cut short by this at all.
    static let waitOnceSignedIn: Duration = .seconds(10)

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
        var video = false
        /// Answered on the lock screen before the call itself arrived: it is
        /// answered the moment it does (2026-10-12, a woken iPhone on 5G
        /// rang for 11 s before the invitation came, and the person's
        /// Answer was dropped).
        var answered = false
        var giveUp: Task<Void, Never>?
    }

    private let lineForThisPhone: @MainActor () async -> Line?
    /// Sends what this phone saw of a call to Linx when it ends.
    private let sendReport: @MainActor (Line, CallReport) -> Void
    /// Keeps a relay reserved for the next call while someone could be about
    /// to call or be called: the app is in front, or a push has woken it for
    /// a call that is on its way (`WebRTCMedia.reserveTheRelay`). Not while
    /// a call is up, which is already using one.
    private func reserveTheRelayIfWanted() {
        guard call == nil, inFront() || !waking.isEmpty, let relay else { return }
        Task { await WebRTCMedia.reserveTheRelay(relay) }
    }

    /// The line in use, which is where a call's record goes.
    private var lineInUse: Line?
    /// The call's SIP messages so far, by their first line, for its record.
    private var sipTrace: [(at: Date, what: String)] = []
    private static let mostTraced = 200
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
    /// What happened between a push and its call arriving, for the call's
    /// timeline: a woken iPhone took 11 s to ring and its first half-minute
    /// was slow (2026-10-15), and the report started only at the invitation.
    private var wakeSteps: [(what: String, at: Date)] = []

    private func noteWake(_ what: String) {
        guard !waking.isEmpty, wakeSteps.count < Self.mostWakeSteps else { return }
        wakeSteps.append((what, Date()))
    }
    private static let mostWakeSteps = 24
    /// Who an outgoing call is to, by the id the system was given: the
    /// system only keeps a number, and the app keeps the name as well.
    private var intended: [UUID: SIPPeer] = [:]
    /// When the person asked for each of those calls: the call's timeline
    /// starts there, before CallKit and the line have had their turn.
    private var tappedAt: [UUID: Date] = [:]
    /// The video button started this call: the camera goes on as soon as
    /// the other side picks up.
    /// Calls asked for with the **Video call** button, until they start.
    private var videoCalls: Set<UUID> = []
    /// Whether this call has already asked about their camera.
    private var askedAboutTheirVideo = false
    /// The sound of the call that is up, for handing the microphone and the
    /// speaker over when the system says so.
    private weak var liveMedia: (any SIPCallMedia)?

    /// The group call this call became (ADR-088): everyone in it and their
    /// pictures, for the screen.
    private(set) var group: GroupPeople?
    /// The media that holds the group call. Held here, because the phone
    /// call underneath it may end — the other person who was in it left —
    /// while this phone carries on in the group.
    private var groupMedia: (any SIPCallMedia)?
    /// The person ended the call: the phone call ending is the end of all
    /// of it, group call included.
    private var leavingGroup = false

    init(
        line: @escaping @MainActor () async -> Line?,
        transport: @escaping @MainActor (SIPUserAgent.Account) -> any SIPTransport = {
            WebSocketTransport(url: $0.websocket, token: $0.token)
        },
        media: @escaping @MainActor (PhoneLine.Turn?) -> any SIPCallMedia = { WebRTCMedia(turn: $0) },
        calls: (any SystemCalls)? = nil,
        inFront: @escaping @MainActor () -> Bool = { UIApplication.shared.applicationState == .active },
        report: @escaping @MainActor (Line, CallReport) -> Void = PhoneModel.sendToLinx
    ) {
        self.sendReport = report
        self.lineForThisPhone = line
        self.makeTransport = transport
        self.makeMedia = media
        self.calls = calls ?? CallStyle.systemCalls()
        self.inFront = inFront
        self.calls.onRequest = { [weak self] request in self?.systemAsked(request) }
        self.calls.onAudio = { [weak self] on in
            guard let media = self?.liveMedia else {
                WebRTCMedia.systemAudioWithNoCall(on)
                return
            }
            media.systemAudio(on)
        }
        self.calls.onReset = { [weak self] in self?.systemForgotEverything() }
        smallWindow.onMark = { [weak self] what in self?.liveMedia?.mark(what) }
    }

    // MARK: - The line

    /// start signs the phone line in. It is called once the phone is signed
    /// in to Linx, again by itself whenever the connection drops, and again
    /// when a push wakes the app for a call.
    ///
    /// Only one line at a time: a push and the app coming to the front both
    /// start it, and a woken iPhone answered on its own screen did both in
    /// the same moment — two lines, two sets of relay credentials, the
    /// relay reserved twice, on a mobile link that was struggling already
    /// (the owner's call, 2026-10-16). A start while one is under way waits
    /// for it, and a line that is up is left as it is.
    func start() async {
        if let opening {
            await opening.value
            return
        }
        if !stopped, agent != nil, status == .ready { return }
        let task = Task { await openTheLine() }
        opening = task
        await task.value
        opening = nil
    }

    private var opening: Task<Void, Never>?

    private func openTheLine() async {
        stopped = false
        noteWake("Asking Linx for this phone's line")
        let line = await lineForThisPhone()
        noteWake(line == nil ? "No line from Linx" : "Line given")
        guard let line else {
            // A hotel's, an airport's or a plane's Wi-Fi that wants signing
            // in to first: said so, rather than "trying again" for ever.
            let portal = await CaptivePortal.suspected()
            if portal { noteWake("This Wi-Fi wants signing in to") }
            status = .unavailable(
                portal ? CaptivePortal.words : "Linx couldn't give this phone a line. It will try again.")
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
                    RelayAddresses.warm(RelayCertificates.hosts(in: fresh.urls))
                    RelayCertificates.warm(fresh.urls)
                    self.reserveTheRelayIfWanted()
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
        WebRTCMedia.releaseTheRelay()
        reconnect?.cancel()
        reconnect = nil
        relayRefresh?.cancel()
        relayRefresh = nil
        ringer.stop()
        UIDevice.current.isProximityMonitoringEnabled = false
        holdTheScreen(false)
        groupMedia?.stop()
        groupMedia = nil
        group = nil
        groupRoom = nil
        agent?.stop()
        agent = nil
        if let call { calls.reportEnded(id: call.id, .failed) }
        call = nil
        videoCalls.removeAll()
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
        // A call held while the line came back keeps its agent: it is given
        // the new connection, and the call goes on (`SIPUserAgent.reattach`).
        if let agent, agent.holdingACall, let account = Self.account(for: line) {
            lineInUse = line
            relay = line.line.turn
            keepTheRelayFresh(for: line)
            status = .starting
            agent.reattach(account: account, transport: makeTransport(account))
            return
        }
        agent?.stop()
        lineInUse = line
        guard let account = Self.account(for: line) else {
            status = .unavailable("Linx gave this phone a line it couldn't read. Set the phone up again.")
            return
        }
        relay = line.line.turn
        Self.sendWhatWasKept(line)
        // Look the relay up now, through iOS, so the first call finds its
        // address already known (`RelayAddresses`).
        RelayAddresses.warm(RelayCertificates.hosts(in: line.line.turn.urls))
        RelayCertificates.warm(line.line.turn.urls)
        reserveTheRelayIfWanted()
        keepTheRelayFresh(for: line)
        let agent = SIPUserAgent(
            account: account, transport: makeTransport(account),
            media: { [makeMedia, weak self] in
                let media = makeMedia(self?.relay)
                media.onConnection = { [weak self] connection in
                    self?.call?.connection = connection
                    self?.mendIfSilent()
                }
                media.onRoute = { [weak self] route in self?.soundIsComingOut(route) }
                media.onRouteTrouble = { [weak self] words in self?.problem = words }
                media.onDiagnostics = { [weak self] what in self?.callDiagnostics(what) }
                self?.diagnostics = media.diagnostics
                media.onVideoChanged = { [weak self] video in self?.videoChanged(video) }
                media.onVideoTooExpensive = { [weak self] in self?.videoCostTooMuch() }
                media.onAnsweredAt = { [weak self] at in self?.answeredAtLinx(at) }
                media.onGroupMove = { [weak self] join in self?.joinGroup(join) }
                media.onGroupEnded = { [weak self] why in self?.groupEnded(why) }
                self?.liveMedia = media
                self?.tracks = media.tracks
                return media
            })
        agent.onStatus = { [weak self] status in self?.lineChanged(status) }
        agent.onIncoming = { [weak self] peer, video in self?.incoming(peer, video: video) }
        agent.onProgress = { [weak self] in self?.ringingThere() }
        agent.onEstablished = { [weak self] in self?.answered() }
        agent.onEnded = { [weak self] why in self?.ended(why) }
        agent.onVideoRefused = { [weak self] why in self?.problem = why }
        agent.onTrace = { [weak self] words in
            guard let self else { return }
            // A call starts with its INVITE: whatever came before it (the
            // last call's goodbye answered late) belongs to no call.
            if words.hasSuffix(" INVITE"), !self.sipTrace.contains(where: { $0.what.hasSuffix(" INVITE") }) {
                self.sipTrace = []
            }
            guard self.sipTrace.count < Self.mostTraced else { return }
            self.sipTrace.append((Date(), words))
        }
        self.agent = agent
        status = .starting
        agent.start()
    }

    private func lineChanged(_ status: SIPStatus) {
        switch status {
        case .connecting:
            noteWake("Line connecting")
            self.status = .starting
        case .ready:
            noteWake("Line signed in")
            attempts = 0
            self.status = .ready
            problem = nil
            waitLessForWhatWasWoken()
            reserveTheRelayIfWanted()
            callForTheBench()
        case .reconnecting:
            noteWake("Line lost, connecting again")
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
        // A call is waiting for the line: try again every second while it
        // is held (`SIPUserAgent.holdTheCallFor`).
        let wait = agent?.holdingACall == true ? 1 : min(30, Int(pow(2.0, Double(min(attempts, 5)))))
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
    func woken(from: String, video: Bool = false) async -> Bool {
        let id = UUID()
        let peer = Self.caller(from)
        guard await calls.reportIncoming(id: id, from: peer, video: video) else { return false }
        // One line, one call: a second caller hears busy and the server
        // sends them to voicemail, as it does for the browser. The push
        // still had to be reported, so the call is ended right away.
        if call != nil {
            calls.reportEnded(id: id, .missed)
            return false
        }
        var woken = Waking(id: id, from: from, video: video)
        woken.giveUp = Task { [weak self] in
            try? await Task.sleep(for: Self.waitForTheInvitation)
            guard !Task.isCancelled else { return }
            self?.nothingArrived(id)
        }
        waking.append(woken)
        if waking.count == 1 { wakeSteps = [] }
        noteWake("Woken by the push")
        return true
    }

    /// A VoIP push arrived that isn't a call this app can ring (a payload a
    /// later Linx sent, or a damaged one). iOS requires **every** VoIP push
    /// to report a call — an app that doesn't is killed, and after a few it
    /// stops receiving them at all (docs/PHASE2.md §14 item 1) — so one is
    /// reported and ended in the same breath. Nothing rings, nothing is
    /// left on the screen, and the rule is kept.
    func wokenByNothing() async {
        let id = UUID()
        guard await calls.reportIncoming(id: id, from: SIPPeer(name: "", number: ""), video: false) else { return }
        calls.reportEnded(id: id, .failed)
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

    /// The line is signed in, so a call still on its way would be here in a
    /// moment: every phone being woken waits only a few seconds more.
    private func waitLessForWhatWasWoken() {
        for index in waking.indices where !waking[index].answered {
            let id = waking[index].id
            waking[index].giveUp?.cancel()
            waking[index].giveUp = Task { [weak self] in
                try? await Task.sleep(for: Self.waitOnceSignedIn)
                guard !Task.isCancelled else { return }
                self?.nothingArrived(id)
            }
        }
    }

    private func forgetWhatWasWoken(_ ending: SystemCallEnding) {
        for woken in waking {
            woken.giveUp?.cancel()
            calls.reportEnded(id: woken.id, ending)
        }
        waking.removeAll()
    }

    /// The app went to the background (locked, or another app in front):
    /// the line signs out before it closes, in time iOS is asked for, so
    /// Linx knows at once to push the next call instead of sending it down a
    /// line that is gone. Closed without a goodbye, the line stayed on
    /// Asterisk's books for up to its 30 s check, and a call made in that
    /// moment went to it and was never rung (the owner's iPhone, 11:50 on
    /// 2026-10-16).
    func leaveForTheBackground() {
        guard !busy else { return }
        // The relay held ahead for the next call goes at once: iOS may stop
        // the app before the sign-out below finishes, and a reservation
        // left behind holds two of the person's relay places for ten
        // minutes (the phone bench ran out of them, 2026-10-16).
        WebRTCMedia.releaseTheRelay()
        guard let agent else {
            stop()
            return
        }
        var task = UIBackgroundTaskIdentifier.invalid
        task = UIApplication.shared.beginBackgroundTask(withName: "Signing the line out") {
            UIApplication.shared.endBackgroundTask(task)
        }
        Task { [weak self] in
            await agent.signOff()
            defer { UIApplication.shared.endBackgroundTask(task) }
            guard let self, self.agent === agent else { return }
            if self.busy || self.inFront() {
                agent.signBackOn()
            } else {
                self.stop()
            }
        }
    }

    /// The line is signed out before it closes, so a call Asterisk sends in
    /// that moment still arrives and is rung instead of lost (2026-10-12).
    private func closeIfNobodyIsLooking() {
        guard !busy, !inFront() else { return }
        guard let agent else {
            stop()
            return
        }
        Task { [weak self] in
            await agent.signOff()
            guard let self, self.agent === agent else { return }
            if self.busy || self.inFront() {
                agent.signBackOn()
            } else {
                self.stop()
            }
        }
    }

    // MARK: - What the person asks for

    /// dial rings whatever is on the keypad, as a voice call or a video call.
    func dial(withVideo: Bool = false) {
        let number = typed
        typed = ""
        callNumber(number, withVideo: withVideo)
    }

    /// `withVideo` is the **Video call** button: the camera is on from the
    /// start and the call rings as a video call. A phone that can't show a
    /// picture answers it as an ordinary call (owner, 2026-10-12).
    func callNumber(_ number: String, name: String? = nil, withVideo: Bool = false) {
        guard case .ready = status, call == nil else { return }
        let target = number.trimmingCharacters(in: .whitespaces)
        guard !target.isEmpty else { return }
        problem = nil
        if target != Self.echoTest { lastDialled = target }
        let peer = SIPPeer(name: name ?? (target == Self.echoTest ? "Test sound" : target), number: target)
        let id = UUID()
        if withVideo { videoCalls.insert(id) }
        intended[id] = peer
        tappedAt[id] = Date()
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

    // MARK: - Group calls (ADR-088)

    /// Whether Add person can do anything now: a call between two Linx
    /// devices that is up, or a group call.
    var canAddPeople: Bool {
        guard let call, call.phase == .active else { return false }
        return group != nil || call.video.available == true
    }

    /// Add person: rings someone into this call, which becomes a group call
    /// on Linx's video server if it isn't one already. Words for the screen
    /// if it couldn't, or nil.
    func addPerson(_ number: String) async -> String? {
        guard let line = lineInUse else { return "This phone isn't signed in to Linx." }
        let client = LinxClient(server: line.server)
        do {
            if let room = groupRoom {
                try await client.addToGroupCall(room: room, extension: number, token: line.token)
                return nil
            }
            guard let callID = agent?.callID else { return "There's no call to add them to." }
            let join = try await client.startGroupCall(call: callID, extension: number, token: line.token)
            joinGroup(join)
            return nil
        } catch let error as LinxError {
            return error.words
        } catch {
            return "Linx couldn't add them. Try again."
        }
    }

    /// The people Add person can choose from.
    func teamToAdd() async -> [TeamMember] {
        guard let line = lineInUse else { return [] }
        let list = try? await LinxClient(server: line.server).team(token: line.token)
        return (list?.items ?? []).filter { $0.extensionNumber != line.line.extensionNumber }
    }

    /// The group call's room, once this call is one.
    private var groupRoom: String?

    /// `camera`: whether this phone's camera goes on in the group call — on
    /// if it was on in the call, or for an invitation from a video call.
    private func joinGroup(_ join: GroupJoin, camera: Bool? = nil) {
        guard group == nil, let media = liveMedia, let line = lineInUse else { return }
        guard let people = media.joinGroup(join, server: line.server, camera: camera ?? media.video.mine) else {
            problem = "Linx couldn't join the group call."
            return
        }
        groupMedia = media
        groupRoom = join.room
        group = people
        askAboutTheirVideo = nil
        askedAboutTheirVideo = true
        if let call, !call.speaker { toggleSpeaker() }
        theScreenDuringTheCall()
    }

    private func joinInvitation() async {
        guard let line = lineInUse, let callID = agent?.callID else { return }
        do {
            let join = try await LinxClient(server: line.server).joinGroupCall(call: callID, token: line.token)
            joinGroup(join, camera: call?.videoCall ?? false)
        } catch {
            problem = "Linx couldn't join the group call."
        }
    }

    /// The video server let this phone go (the room closed, or the link to
    /// it broke): the call is over.
    private func groupEnded(_ why: String) {
        guard groupMedia != nil, let call else { return }
        problem = why
        calls.ask(.end(call.id))
    }

    // MARK: - The picture (ADR-087)

    /// Turns this phone's camera on or off in the call that is up. The
    /// picture has its own direct link to the other device, so the call's
    /// sound carries straight on either way. Nothing happens on a call to a
    /// phone that can't show a picture (a desk phone, an outside number).
    func toggleVideo() {
        guard let call, call.phase == .active, !changingVideo, call.video.available != false else { return }
        let on = !call.video.mine
        problem = nil
        // Everyone expects a video call to come out of the loudspeaker; a
        // phone held to an ear with the camera on would show a ceiling.
        if on, !call.speaker { toggleSpeaker() }
        changingVideo = true
        if let groupMedia {
            Task { [weak self] in
                do {
                    try await groupMedia.setCamera(on)
                } catch let trouble as CameraTrouble {
                    self?.problem = trouble.words
                } catch {
                    self?.problem = "This phone couldn't start its camera. Try again in a moment."
                }
                self?.changingVideo = false
            }
            return
        }
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

    /// The **HD** button: lets this phone's picture reach 720p, or holds it at
    /// 540p to spend less data (owner, 2026-10-12).
    func toggleHD() {
        guard var current = call else { return }
        current.hd.toggle()
        call = current
        agent?.useHD(current.hd)
    }

    /// The camera facing the person, or the one facing what they can see.
    func switchCamera() {
        if let groupMedia {
            groupMedia.switchCamera()
            mirrorsMyVideo = groupMedia.mirrorsMyVideo
            return
        }
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
            if Bench.mineOnToo {
                Task { [weak self] in
                    try? await Task.sleep(for: .seconds(1))
                    self?.answeredAboutTheirVideo(turningMineOn: true)
                }
            }
        }
        if video.mine { askAboutTheirVideo = nil }
        // Their camera went off again while the question was still on the
        // screen: there is nothing left to answer.
        if !video.theirs { askAboutTheirVideo = nil }
        // The system shows a call with a picture in it as a video call.
        if was.on != video.on { calls.reportVideo(id: current.id, on: video.on) }
        // What the screen shows, in the call's record: what the picture
        // link says can differ from what is drawn (owner, build 52).
        if was.on != video.on || was.theirs != video.theirs || was.theirPicture != video.theirPicture {
            liveMedia?.mark(
                !video.on
                    ? "Screen: the voice call"
                    : "Screen: video, theirs \(video.theirs ? (video.theirPicture ? "shown" : "waiting") : "off")")
        }
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
    /// for the first reading of the connection to have happened, and for a
    /// move onto the relay (`mendIfSilent`) to have had its chance.
    private static let silenceIsWrong: TimeInterval = 7

    /// A call that is up but whose sound isn't getting through is moved onto
    /// Linx's relay, without hanging up: no sound at all four seconds after a
    /// route was found, or sound that stopped for three seconds (the phone
    /// changed networks, a VPN came on or went off). At most twice a call
    /// and ten seconds apart, so a network that is simply bad can't make the
    /// call hop back and forth. The owner's iPhone on Tailscale chose a
    /// direct route through the Synology that never carried the sound, and
    /// sat on it (2026-10-08): the relay is the route that works from
    /// anywhere, so that is where a stuck call goes.
    ///
    /// The clock starts when a route is found, not at the answer. Build 15
    /// counted two seconds from the answer, and on the owner's home Wi-Fi a
    /// call to a line that answers at once had no sound *yet* — the routes
    /// are only tried from the answer, and the encryption is agreed after
    /// that — so a working call was moved, onto a relay that couldn't reach
    /// the server from there, and was silent for good (2026-10-08, both
    /// calls in Asterisk's and coturn's logs). And a call that has never had
    /// sound is only moved off a *direct* route: one already on the relay
    /// gains nothing from a relay-only restart. A second move gives every
    /// route another go (`WebRTCMedia.moveToRelay`), so a move that made
    /// things worse can't be the last word.
    func mendIfSilent(at now: Date = Date()) {
        guard var current = call, current.phase == .active, current.answeredAt != nil,
            let connection = current.connection
        else { return }
        if current.routeFoundAt == nil {
            current.routeFoundAt = now
            call = current
        }
        // Any change is sound arriving. A count that went *down* is a new
        // stream from Linx (a picture added or taken away gives the sound a
        // fresh count), not silence: build 21 read it as three seconds of
        // nothing and moved both ends of a video call onto the relay at
        // once, which broke the call (2026-10-12).
        if connection.reportsBack != current.reportsLastSeen {
            current.reportsLastSeen = connection.reportsBack
            current.linxLastHeardUs = now
            call = current
        }
        if connection.audioBytesIn != current.bytesInLastSeen {
            current.bytesInLastSeen = connection.audioBytesIn
            current.soundLastArrived = now
            call = current
            return
        }
        guard
            Self.shouldMoveToRelay(
                now: now, routeFoundAt: current.routeFoundAt, soundLastArrived: current.soundLastArrived,
                relayed: connection.route == .relayed, moves: current.relayMoves, lastMove: current.lastRelayMove,
                linxHeardUs: current.linxLastHeardUs)
        else { return }
        current.relayMoves += 1
        current.lastRelayMove = now
        call = current
        Task { [weak self] in await self?.agent?.moveToRelay() }
    }

    nonisolated static let silentAfterRoute: TimeInterval = 4
    nonisolated static let silentMidCall: TimeInterval = 3
    nonisolated static let mostRelayMoves = 2
    nonisolated static let betweenRelayMoves: TimeInterval = 10
    /// Linx reports on what it hears about once a second; a report this
    /// recent means the route works both ways.
    nonisolated static let linxHearsUsWithin: TimeInterval = 3

    /// The rule `mendIfSilent` follows, on its own so it can be tested.
    ///
    /// A route Linx says it is hearing us over is never left: the silence is
    /// the other end, still connecting. The iPad at home moved off a perfect
    /// direct route because the iPhone on 5G hadn't been heard yet, and the
    /// move made both ends negotiate again (2026-10-12).
    nonisolated static func shouldMoveToRelay(
        now: Date, routeFoundAt: Date?, soundLastArrived: Date?, relayed: Bool, moves: Int, lastMove: Date?,
        linxHeardUs: Date? = nil
    ) -> Bool {
        guard moves < mostRelayMoves else { return false }
        if let linxHeardUs, now.timeIntervalSince(linxHeardUs) < linxHearsUsWithin { return false }
        if let lastMove, now.timeIntervalSince(lastMove) < betweenRelayMoves { return false }
        if let soundLastArrived {
            // Already on the relay, a pause is not the route's fault, and a
            // relay-only restart gains nothing: it threw away the owner's
            // working relay route over UDP 13 s into a call and landed on
            // TCP, and the picture stopped for 40 s both ways until that
            // route broke (2026-10-15, build 38; the call of 07:11 the same).
            // A relay route that has really gone is noticed by the route
            // itself ("Sound route dropped") and looked for again then.
            guard !relayed || moves > 0 else { return false }
            return now.timeIntervalSince(soundLastArrived) >= silentMidCall
        }
        guard let routeFoundAt, !(relayed && moves == 0) else { return false }
        return now.timeIntervalSince(routeFoundAt) >= silentAfterRoute
    }

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
            if let index = waking.firstIndex(where: { $0.id == id }) {
                // Answered before the call itself is here: it gets the whole
                // wait again from now. The owner's woken iPhone was handed
                // its invitation twenty seconds after the push (2026-10-15).
                waking[index].answered = true
                noteWake("Answered before the call arrived")
                waking[index].giveUp?.cancel()
                waking[index].giveUp = Task { [weak self] in
                    try? await Task.sleep(for: Self.waitForTheInvitation)
                    guard !Task.isCancelled else { return }
                    self?.nothingArrived(id)
                }
                return
            }
            guard call?.id == id, call?.phase == .ringing else { return }
            ringer.stop()
            Task { [weak self] in await self?.agent?.answer() }
        case .end(let id):
            end(id)
        case .mute(let id, let muted):
            guard var current = call, current.id == id else { return }
            current.muted = muted
            if let groupMedia {
                groupMedia.setMuted(muted)
            } else {
                agent?.setMuted(muted)
            }
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
        let video = videoCalls.remove(id) != nil
        call = Call(id: id, peer: peer, incoming: false, phase: .calling, speaker: video, videoCall: video)
        if video { calls.reportVideo(id: id, on: true) }
        let startedAt = tappedAt.removeValue(forKey: id) ?? Date()
        Task { [weak self] in
            await self?.agent?.call(peer.number, name: peer.name, startedAt: startedAt, video: video)
        }
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
        if let groupMedia {
            // Leaving the group call: it goes, and the phone call with it
            // if it's still there.
            leavingGroup = true
            groupMedia.stop()
            self.groupMedia = nil
            group = nil
            if agent?.callID != nil {
                agent?.hangUp()
            } else {
                ended(.hungUp)
            }
            return
        }
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

    // MARK: - One timer for everyone on the call

    /// Linx says when the call was answered: the timer counts from there,
    /// so it reads the same on every phone and browser on the call (owner,
    /// 2026-10-17). Each used to count from its own answer, which reached
    /// them a second or more apart. A moment far from this phone's own is
    /// a different call's, and ignored.
    func answeredAtLinx(_ at: Date) {
        guard var current = call else { return }
        // It can come a moment before this phone hears the answer itself.
        guard current.phase == .active, let mine = current.answeredAt else {
            answerMomentFromLinx = at
            return
        }
        guard abs(at.timeIntervalSince(mine)) < Self.answerMomentsAgree else { return }
        current.answeredAt = at
        call = current
        if let waiting = connectedForTheSystem {
            waiting.cancel()
            connectedForTheSystem = nil
            calls.reportAnswered(id: current.id, at: at)
        }
    }

    /// An outgoing call's "connected", waiting for Linx's moment.
    private var connectedForTheSystem: Task<Void, Never>?

    /// How long an outgoing call waits for Linx's moment before the system
    /// is told with this phone's own: a call with no call link (an outside
    /// number) never gets one.
    static let waitForLinxMoment: Duration = .seconds(2)

    /// Linx's moment, kept until this phone's own answer.
    private var answerMomentFromLinx: Date?

    static let answerMomentsAgree: TimeInterval = 30

    // MARK: - The test bench (`Bench`)

    private var benchCalled = false

    /// The bench's one call, once the line is first up.
    private func callForTheBench() {
        guard let number = Bench.call, !benchCalled else { return }
        benchCalled = true
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(Bench.callAfter))
            self?.callNumber(number, withVideo: Bench.video)
        }
    }

    private func hangUpForTheBench() {
        if Bench.hd, let id = call?.id {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(5))
                guard let self, self.call?.id == id, self.call?.hd == false else { return }
                self.toggleHD()
            }
        }
        for (at, on) in [(Bench.cameraOnAt, true), (Bench.cameraOffAt, false)] where at > 0 {
            guard let id = call?.id else { break }
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(at))
                guard let self, self.call?.id == id, self.call?.video.mine != on else { return }
                self.toggleVideo()
            }
        }
        if Bench.swapAt > 0, let id = call?.id {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(Bench.swapAt))
                guard let self, self.call?.id == id else { return }
                self.swapPictures()
                self.liveMedia?.mark(self.myPictureIsBig ? "Pictures swapped: mine big" : "Pictures not swapped")
            }
        }
        let seconds = Bench.hangUpAfter
        guard seconds > 0, let id = call?.id else { return }
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(seconds))
            guard let self, self.call?.id == id else { return }
            self.hangUp()
        }
    }

    // MARK: - What the line tells the app

    private func incoming(_ peer: SIPPeer, video: Bool) {
        // The call the phone was woken for: the system has been ringing
        // since the push arrived, so it keeps the same id and only learns
        // the caller's name now.
        if let index = matching(peer) {
            let woken = waking.remove(at: index)
            woken.giveUp?.cancel()
            if let media = liveMedia, let first = wakeSteps.first {
                media.began(at: first.at)
                for step in wakeSteps { media.mark(step.what, at: step.at) }
                media.mark("The call arrived")
            }
            wakeSteps = []
            call = Call(id: woken.id, peer: peer, incoming: true, phase: .ringing, videoCall: video)
            calls.rename(id: woken.id, to: peer)
            // The push may have come from a Linx that doesn't say video, or
            // been wrong: the invitation is the last word.
            if video != woken.video { calls.reportVideo(id: woken.id, on: video) }
            if woken.answered {
                Task { [weak self] in await self?.agent?.answer() }
                return
            }
            ringInTheApp()
            return
        }
        // The app was already here, so nothing has been reported yet.
        let id = UUID()
        call = Call(id: id, peer: peer, incoming: true, phase: .ringing, videoCall: video)
        ringInTheApp()
        if Bench.answer {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(Bench.ringFor))
                self?.answer()
            }
        }
        Task { [weak self] in
            guard let self else { return }
            guard await self.calls.reportIncoming(id: id, from: peer, video: video) else {
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
        hangUpForTheBench()
        defer { theScreenDuringTheCall() }
        guard var current = call else { return }
        current.phase = .active
        let now = Date()
        if let linx = answerMomentFromLinx, abs(linx.timeIntervalSince(now)) < Self.answerMomentsAgree {
            current.answeredAt = linx
        } else {
            current.answeredAt = now
        }
        answerMomentFromLinx = nil
        current.ringingThere = false
        call = current
        // Connected, both ways: an outgoing call the moment the other side
        // picks up, an incoming one once this phone's answer has gone out —
        // which is when the system's own call timer starts (`CallKitCalls`).
        // That timer is the lock screen's, so it is given Linx's moment too
        // (owner, 2026-10-17: one timer for everyone). An incoming answer is
        // never held for it, because the sound waits on that answer; an
        // outgoing call's sound is already on, so it waits a moment for
        // Linx's word, which follows the answer over the call link.
        if current.incoming || current.answeredAt != now {
            calls.reportAnswered(id: current.id, at: current.answeredAt ?? now)
        } else {
            let id = current.id
            connectedForTheSystem = Task { [weak self] in
                try? await Task.sleep(for: Self.waitForLinxMoment)
                guard !Task.isCancelled, let self, self.call?.id == id else { return }
                self.connectedForTheSystem = nil
                self.calls.reportAnswered(id: id, at: self.call?.answeredAt ?? now)
            }
        }
        // A video call: this phone's camera goes on as soon as the picture
        // link finds the other side (the user agent asked for it), so there
        // is nothing to ask about their camera, and the sound comes out of
        // the loudspeaker as everyone expects.
        if current.videoCall {
            askedAboutTheirVideo = true
            if !current.speaker { toggleSpeaker() }
        }
        // An invitation into a group call: the server says which, and this
        // phone goes straight there.
        if agent?.callIsInvitation == true { Task { [weak self] in await self?.joinInvitation() } }
    }

    private func ended(_ why: SIPEnded) {
        answerMomentFromLinx = nil
        connectedForTheSystem?.cancel()
        connectedForTheSystem = nil
        // In a group call the phone call underneath may end — the person it
        // was with left — and this phone carries on in the group.
        if groupMedia != nil, !leavingGroup, call != nil { return }
        leavingGroup = false
        groupMedia = nil
        group = nil
        groupRoom = nil
        ringer.stop()
        UIDevice.current.isProximityMonitoringEnabled = false
        holdTheScreen(false)
        smallWindow.end()
        guard let finished = call else { return }
        call = nil
        askAboutTheirVideo = nil
        askedAboutTheirVideo = false
        changingVideo = false
        saidAboutTheRelay = false
        myPictureIsBig = false
        callKeypadIsOpen = false
        callDetailsAreOpen = false
        addingPerson = false
        calls.reportEnded(id: finished.id, Self.ending(why))
        if case .failed(let said) = why { problem = said }
        sendTheRecord(of: finished, why: why)
        // The call took the reserved relay with it: reserve the next one.
        reserveTheRelayIfWanted()
        defer { closeIfNobodyIsLooking() }
        guard finished.peer.number != Self.echoTest else { return }
        let kind: Recent.Kind =
            finished.incoming ? (finished.answeredAt == nil ? .missed : .incoming) : .outgoing
        recent = Array(([Recent(peer: finished.peer, kind: kind, at: Date())] + recent).prefix(20))
    }

    /// What this phone saw of the call, to Linx: the Call details words and
    /// the SIP messages by their first line. A failure to send is let go —
    /// the record is a help, never a reason for anything else to wait.
    private func sendTheRecord(of finished: Call, why: SIPEnded) {
        let trace = sipTrace
        sipTrace = []
        guard let line = lineInUse, finished.peer.number != Self.echoTest else { return }
        // One clock for the steps and the SIP messages: a woken call's starts
        // at the push, before its invitation.
        let started = diagnostics.steps.first?.at ?? trace.first?.at ?? Date()
        sendReport(line, Self.record(of: finished, diagnostics: diagnostics, trace: trace, why: why, started: started))
    }

    static func record(
        of call: Call, diagnostics: CallDiagnostics, trace: [(at: Date, what: String)], why: SIPEnded, started: Date
    ) -> CallReport {
        var lines = [CallDetailsView.words(call: call, diagnostics: diagnostics)]
        if !trace.isEmpty {
            lines.append("sip:")
            lines += trace.map { "  \(CallDetailsView.after($0.at, started)) \($0.what)" }
        }
        switch why {
        case .hungUp: lines.append("ended: hung up")
        case .missed: lines.append("ended: not answered")
        case .failed(let said): lines.append("ended: failed — \(said)")
        }
        let text = String(lines.joined(separator: "\n").prefix(16_000))
        return CallReport(
            callID: call.id.uuidString, incoming: call.incoming, peer: String(call.peer.number.prefix(100)),
            startedAt: started, endedAt: Date(), text: text)
    }

    /// The real sending: a background task of its own, so a call that ends
    /// as the app goes to the background still gets its record out.
    static func sendToLinx(_ line: Line, _ report: CallReport) {
        let task = UIApplication.shared.beginBackgroundTask(withName: "Call record")
        Task {
            do {
                try await LinxClient(server: line.server).sendCallReport(report, token: line.token)
            } catch {
                UnsentReports.keep(report)
            }
            UIApplication.shared.endBackgroundTask(task)
        }
    }

    /// The reports that couldn't be sent before, sent now the line is open
    /// (`UnsentReports`).
    static func sendWhatWasKept(_ line: Line) {
        let kept = UnsentReports.takeAll()
        guard !kept.isEmpty else { return }
        Task {
            for report in kept.reversed() {
                do {
                    try await LinxClient(server: line.server).sendCallReport(report, token: line.token)
                } catch {
                    UnsentReports.keep(report)
                }
            }
        }
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
        func pretend(_ diagnostics: CallDiagnostics) { self.diagnostics = diagnostics }

        func pretend(_ status: Status) { self.status = status }

        func pretendLastDialled(_ number: String) { lastDialled = number }
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
