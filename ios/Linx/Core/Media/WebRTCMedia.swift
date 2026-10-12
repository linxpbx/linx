import AVFoundation
import Foundation
@preconcurrency import WebRTC

// The sound of a call — and, when someone asks for it, the picture: Google's
// WebRTC, the same engine the browser uses
// (ADR-006), so Asterisk sees the phone and the tab as the same kind of
// endpoint — DTLS-SRTP, rtcp-mux, ICE, Opus. The media never goes near
// Linx's control plane: it is a direct route where there is one, and Linx's
// own TURN relay where there isn't (docs/WEB.md §6).

/// How a call's sound is getting through, for the in-call screen.
struct MediaConnection: Equatable, Sendable {
    enum Route: String, Sendable {
        case direct
        case relayed
    }

    var route: Route
    var roundTripMs: Int?
    /// For a relayed call: how this phone reaches the relay (udp, tcp, tls).
    var relayProtocol: String?
    /// Sound received so far, in bytes — "can I hear them?" in a number.
    var audioBytesIn: Int
    /// Sound sent so far, in bytes — "can they hear me?" in a number. The two
    /// together say which way round a one-way call is.
    var audioBytesOut = 0
    /// How much room WebRTC thinks this link has, in bits per second. It is
    /// what decides whether a picture can stay in the call.
    var outgoingBitrate: Int?
    /// Pictures received so far — "is their camera really still on?" in a
    /// number. A count that stops moving means it isn't.
    var framesIn = 0
    /// The route in use, end to end, in the words Call details shows:
    /// "relay/udp → host/udp 172.24.0.4".
    var path = ""
    /// What this phone offered as ways in ("relay/udp", "relay/tls"…), and
    /// every pairing tried with how it went: what Call details needs to say
    /// why one route won (2026-10-11, a mobile call that went over TLS with
    /// UDP open).
    var offered: [String] = []
    var pairs: [String] = []
    /// Reports from Linx about the sound it is receiving from this phone
    /// (RTCP, about one a second): a count that moves means this phone's
    /// route works, even while nothing has come back yet because the other
    /// end is still connecting.
    var reportsBack = 0
}

enum MediaTrouble: Error {
    case noOffer
    case noAnswer
}

/// Why a call sounds the way it does, in facts a person can read out to
/// somebody who can help (`CallDetailsView`). It is a diagnostic and nothing
/// more: nothing here changes what a call does.
///
/// It exists because a call that connects and carries no sound looks, from
/// the screen, exactly like a call that is working — and that is what the
/// owner's phone did on a mobile network while a phone on the same Wi-Fi was
/// fine (2026-10-04, 2026-10-05). The place the sound stops is now on the
/// screen instead of being guessed at.
struct CallDiagnostics: Equatable, Sendable {
    /// Linx's relay wouldn't give this phone a way through: its address, the
    /// code it answered with and its own words. 401 and 403 mean the
    /// credentials were refused; 701 means it couldn't be reached at all.
    struct RelayTrouble: Equatable, Sendable {
        var url: String
        var code: Int
        var said: String
    }

    /// The kinds of route this phone found for itself, in plain words.
    var found: [String] = []
    var relayTrouble: [RelayTrouble] = []
    /// How far the connection itself got.
    var ice = "not started"
    /// The relay this phone was given, and when its credentials run out. They
    /// last an hour and are refreshed; expired ones mean no relay at all.
    var relayURLs: [String] = []
    var relayExpiresAt: Date?
    /// The addresses the phone itself looked the relay up to (`RelayAddresses`):
    /// what WebRTC was given instead of the name. Empty: the name was left
    /// for WebRTC, which happens only when iOS couldn't look it up either.
    var relayAddresses: [String] = []
    /// How long each step of setting the call up took, from the moment the
    /// call began (the timeline on Call details).
    var steps: [Step] = []
    struct Step: Equatable, Sendable {
        var what: String
        var at: Date
    }
    /// The call was moved onto Linx's relay because no sound was getting
    /// through the route it started on.
    var movedToRelay = false
    /// What this phone offered as ways in, and every pairing tried
    /// (`MediaConnection.offered`, `.pairs`), as last read.
    var offered: [String] = []
    var pairs: [String] = []
    /// What the picture is set to now, in the words people use for it ("540p"),
    /// while this phone's camera is on (ADR-081).
    var picture: String?
    /// Whether the phone has finished looking for routes. Until it has, a
    /// relay address that failed means nothing: a server may offer two and
    /// one of them answer, and the one that fails usually fails first — which
    /// is how the owner got "couldn't reach Linx's call relay" on a call they
    /// could hear perfectly (2026-10-05).
    var settled = false

    /// The words for a route through Linx's relay. On a mobile network it is
    /// the only way the sound can go.
    static let theRelay = "Linx's relay"
    /// The timeline's step for the answer (the readings speed up from it).
    static let answered = "Answered"

    /// Whether a route through the relay was found at all.
    var foundTheRelay: Bool { found.contains(Self.theRelay) }

    /// What to tell the person when the relay wouldn't have them. Only worth
    /// saying when no route through it was found: a phone that reaches the
    /// relay over TLS and not UDP is perfectly fine and says nothing.
    var relayWords: String? {
        guard settled, !foundTheRelay, let first = relayTrouble.first else { return nil }
        switch first.code {
        case 401, 403, 438:
            return "Linx's call relay wouldn't accept this phone, so the call may have no sound."
        case 701:
            return "This phone couldn't reach Linx's call relay, so the call may have no sound."
        default:
            return "Linx's call relay answered \(first.code), so the call may have no sound."
        }
    }
}

@MainActor final class WebRTCMedia: NSObject, SIPCallMedia {
    /// What the phone found out about the route, every few seconds while a
    /// call is up.
    var onConnection: ((MediaConnection) -> Void)?
    /// The sound dropped, or came back.
    var onTrouble: ((Bool) -> Void)?
    /// Where the sound is coming out, read from the session itself every
    /// time it moves — so the button follows the sound rather than the
    /// other way round.
    var onRoute: ((AudioRoute) -> Void)?
    /// The sound wouldn't move where the person asked.
    var onRouteTrouble: ((String) -> Void)?
    /// Why the call sounds the way it does, each time there is more to say.
    var onDiagnostics: ((CallDiagnostics) -> Void)?

    /// Whose camera is on, and the pictures themselves. The picture travels
    /// on its own direct link between the two devices (`DirectPicture`,
    /// ADR-087); this call's own connection carries the sound only.
    var video: CallVideo { picture?.video ?? CallVideo() }
    let tracks = VideoTracks()
    var onVideoChanged: ((CallVideo) -> Void)?
    /// The link can't carry a picture any more: turn this phone's camera off.
    var onVideoTooExpensive: (() -> Void)?
    private var picture: DirectPicture?

    private var turn: PhoneLine.Turn?
    /// The relay's names, looked up by iOS (`RelayAddresses`) before the call
    /// starts.
    private var relayAddresses: [String: [String]] = [:]
    /// Only the relay, from the moment a call is moved onto it.
    private var relayOnly = false
    /// The offer that is out moves the call onto the relay.
    private var movingToRelay = false
    private var connection: RTCPeerConnection?
    private var audio: RTCAudioTrack?
    private var gathered: CheckedContinuation<Void, Never>?
    private var gatheringLimit: Task<Void, Never>?
    private var watch: Task<Void, Never>?
    /// Watching where the sound is coming out, while this call is up.
    private var routeWatch: NSObjectProtocol?
    private var stopped = false

    /// Asterisk doesn't take candidates one at a time, so the phone holds the
    /// offer until it has them; a relay that can't be reached would otherwise
    /// hold the call for a long time (the same rule as the browser's).
    /// How long the offer waits for routes before going without them.
    ///
    /// Three seconds was enough on Wi-Fi, where the phone has a route of
    /// its own in milliseconds. On a mobile network there is no such route:
    /// everything has to go through Linx's relay, and reaching it means a
    /// TLS handshake and an allocation over a link that has just woken its
    /// radio. An offer sent before that is an offer with nowhere for the
    /// sound to go — the call connects and nobody hears anything (owner, on
    /// 5G, 2026-10-04). The offer still leaves the moment a relay route is
    /// in hand, so nothing waits that doesn't have to.
    ///
    /// Ten seconds was the cap until 2026-10-08, and on a phone whose relay
    /// name wouldn't look up (Tailscale with an exit node) the offer sat out
    /// all ten before the call even left. The relay's address is looked up
    /// by iOS now and known before the call (`RelayAddresses`), so its route
    /// arrives in well under a second on 5G, and a call that started without
    /// it is moved onto the relay when no sound gets through
    /// (`moveToRelay`). Three seconds is the most anyone waits.
    private static let gatherLimit: Duration = .seconds(3)
    private static let afterRelayCandidate: Duration = .milliseconds(300)
    private static let readRouteEvery: Duration = .seconds(5)
    /// How long the readings come a second apart: from when the call's
    /// sound starts, and again from the answer (a call through a phone line
    /// often has sound — the ringing — before it is answered).
    private static let quickFor: TimeInterval = 8
    private var quickUntil = Date.distantPast

    init(turn: PhoneLine.Turn?, startedAt: Date = Date()) {
        self.turn = turn
        super.init()
        diagnostics.steps = [.init(what: "Call started", at: startedAt)]
        noteTheRelay()
    }

    /// When the person asked for the call: the timeline counts from there.
    func began(at: Date) {
        note { $0.steps = [.init(what: "Call started", at: at)] + $0.steps.dropFirst() }
    }

    /// One step of the call's setting up, for the timeline on Call details.
    func mark(_ what: String) {
        if what == CallDiagnostics.answered { quickUntil = Date().addingTimeInterval(Self.quickFor) }
        note { $0.steps.append(.init(what: what, at: Date())) }
    }

    /// Looks the relay up before the first offer or answer: by iOS, through
    /// the phone's VPN and DNS (`RelayAddresses`).
    private func lookUpTheRelay() async {
        guard connection == nil else { return }
        let hosts = RelayCertificates.hosts(in: turn?.urls ?? [])
        relayAddresses = await RelayAddresses.addresses(for: hosts)
        let found = relayAddresses.values.flatMap { $0 }
        note { $0.relayAddresses = found }
    }

    /// What the call knows about itself, for the Call details screen.
    private(set) var diagnostics = CallDiagnostics()

    private func note(_ change: (inout CallDiagnostics) -> Void) {
        var now = diagnostics
        change(&now)
        guard now != diagnostics else { return }
        diagnostics = now
        onDiagnostics?(now)
    }

    private func noteTheRelay() {
        note {
            $0.relayURLs = turn?.urls ?? []
            $0.relayExpiresAt = turn?.expiresAt
        }
    }

    // MARK: - SIPCallMedia

    func offer() async throws -> String {
        await lookUpTheRelay()
        let connection = try start()
        let local = try await withCheckedThrowingContinuation { (done: CheckedContinuation<String, Error>) in
            connection.offer(for: Self.audioOnly) { description, error in
                guard let description else {
                    done.resume(throwing: error ?? MediaTrouble.noOffer)
                    return
                }
                done.resume(returning: description.sdp)
            }
        }
        try await setLocal(RTCSessionDescription(type: .offer, sdp: SDPTweaks.preferOpusFecDtx(local)))
        await waitForCandidates()
        guard let full = connection.localDescription?.sdp else { throw MediaTrouble.noOffer }
        // An offer with no routes would switch Asterisk's ICE off for good;
        // not sending it leaves the call as it was (the caller rolls back).
        guard SDPTweaks.everyStreamHasRoutes(full) else {
            mark("Found no routes to offer, so nothing was changed")
            throw MediaTrouble.noOffer
        }
        mark(diagnostics.foundTheRelay ? "Routes ready" : "Routes ready (no relay yet)")
        return ours(full)
    }

    /// Moves a call that is up onto Linx's relay: the routes are looked for
    /// again (an ICE restart) with only the relay allowed, and the offer that
    /// tells the other side comes back for a re-INVITE. The conversation is
    /// untouched while it happens — the old route stays in use until the new
    /// one answers — and Asterisk takes a restart in a re-INVITE as it is
    /// (res_rtp_asterisk restarts its ICE session when the candidates or
    /// credentials change).
    ///
    /// For a call whose sound isn't getting through the route it started on:
    /// the owner's phone on Tailscale chose a direct route through the
    /// Synology that carried connectivity checks but not the encrypted sound,
    /// and nothing moved it (2026-10-08). The relay is the route that works
    /// from anywhere.
    ///
    /// A second move gives every route another go instead (an ordinary ICE
    /// restart): a move onto the relay that brought no sound must not be
    /// where the call is left — at home, the relay may not reach the server
    /// at all (2026-10-08).
    func moveToRelay() async throws -> String {
        guard let connection else { throw MediaTrouble.noOffer }
        relayOnly = !diagnostics.movedToRelay
        movingToRelay = true
        mark(relayOnly ? "Moving to the relay" : "Trying every route again")
        note { $0.movedToRelay = true }
        connection.setConfiguration(configuration())
        connection.restartIce()
        return try await offer()
    }

    func answer(to offer: String) async throws -> String {
        if diagnostics.steps.isEmpty { note { $0.steps = [.init(what: "Call arrived", at: Date())] } }
        await lookUpTheRelay()
        let connection = try start()
        try await setRemote(RTCSessionDescription(type: .offer, sdp: offer))
        let local = try await withCheckedThrowingContinuation { (done: CheckedContinuation<String, Error>) in
            connection.answer(for: Self.audioOnly) { description, error in
                guard let description else {
                    done.resume(throwing: error ?? MediaTrouble.noAnswer)
                    return
                }
                done.resume(returning: description.sdp)
            }
        }
        // Asterisk starts the encryption once its own side of the route is
        // confirmed (`SDPTweaks.letThemStartTheEncryption`).
        let answer = SDPTweaks.letThemStartTheEncryption(SDPTweaks.preferOpusFecDtx(local), offer: offer)
        try await setLocal(RTCSessionDescription(type: .answer, sdp: answer))
        await waitForCandidates()
        // Whether Linx started its routes afresh, which is when this phone
        // may switch route: worth seeing next to the "Route now" lines.
        mark(theyRestarted ? "Answer ready (Linx restarted its routes)" : "Answer ready")
        guard let full = connection.localDescription?.sdp else { throw MediaTrouble.noAnswer }
        capWhatThisPhoneSends()
        startWatching()
        return ours(full)
    }

    func accept(answer: String) async throws {
        try await setRemote(RTCSessionDescription(type: .answer, sdp: answer))
        if movingToRelay {
            movingToRelay = false
            mark(relayOnly ? "On the relay" : "Every route allowed again")
        }
        capWhatThisPhoneSends()
        startWatching()
    }

    /// The last thing done to every SDP this phone sends: the browser's own
    /// Opus settings.
    private func ours(_ sdp: String) -> String {
        SDPTweaks.preferOpusFecDtx(sdp)
    }

    // MARK: - The picture (ADR-087)

    /// Opens the call's picture link once the call is up: the two devices
    /// find each other on it and the picture goes straight between them.
    /// `cameraFirst` is a video call — this phone's camera goes on as soon
    /// as the other side is there.
    func startPicture(link: any PictureLink, cameraFirst: Bool) {
        guard picture == nil, !stopped else { return }
        let picture = DirectPicture(
            link: link, configuration: configuration(), factory: Self.factory, tracks: tracks,
            cameraFirst: cameraFirst)
        picture.onMark = { [weak self] what in self?.mark(what) }
        picture.onTooExpensive = { [weak self] in self?.onVideoTooExpensive?() }
        picture.onVideoChanged = { [weak self] video in
            guard let self else { return }
            self.note { $0.picture = video.mine ? (self.hd ? "720p" : "540p") : nil }
            self.onVideoChanged?(video)
        }
        self.picture = picture
        picture.start()
    }

    /// This phone's camera, on or off. The call's sound is never touched.
    func setCamera(_ on: Bool) async throws {
        try await picture?.setCamera(on)
    }

    /// The front camera or the back one.
    func switchCamera() { picture?.switchCamera() }

    /// The **HD** button: 720p instead of 540p for this phone's picture.
    func useHD(_ on: Bool) {
        hd = on
        picture?.useHD(on)
    }

    private var hd = false

    /// Newer relay credentials. The old ones last an hour and a phone is
    /// signed in for days, so these arrive before the old ones run out and
    /// are handed to the call that is already up as well as kept for the
    /// next one: the routes in use are left alone, and any gathered from
    /// now on use these.
    func use(relay: PhoneLine.Turn) {
        turn = relay
        RelayAddresses.warm(RelayCertificates.hosts(in: relay.urls))
        connection?.setConfiguration(configuration())
        noteTheRelay()
    }

    /// Nobody took the offer this phone just made (a move onto the relay
    /// the switch refused). `rollback` puts the call back exactly where it
    /// was (RFC 8829 §4.1.10) so the conversation carries on.
    func rollbackOffer() async {
        try? await setLocal(RTCSessionDescription(type: .rollback, sdp: ""))
        movingToRelay = false
    }

    /// Whether this phone's own picture is the mirror image people expect of
    /// themselves (the front camera) or the plain one (the back).
    var mirrorsMyVideo: Bool { picture?.mirrored ?? true }

    func setMuted(_ muted: Bool) {
        audio?.isEnabled = !muted
    }

    func sendTone(_ digit: Character) {
        let sender = connection?.senders.first { $0.track?.kind == kRTCMediaStreamTrackKindAudio }
        // The tones go the way Asterisk expects them from a WebRTC phone
        // (RFC 4733 in the media, not SIP INFO).
        sender?.dtmfSender?.insertDtmf(String(digit), duration: 0.1, interToneGap: 0.07)
    }

    func stop() {
        guard !stopped else { return }
        stopped = true
        watch?.cancel()
        watch = nil
        if let routeWatch {
            NotificationCenter.default.removeObserver(routeWatch)
            self.routeWatch = nil
        }
        if Self.live === self { Self.live = nil }
        gatheringLimit?.cancel()
        gatheringLimit = nil
        gathered?.resume()
        gathered = nil
        picture?.stop()
        picture = nil
        tracks.local = nil
        tracks.remote = nil
        audio = nil
        connection?.close()
        connection = nil
        Self.releaseAudioSession()
    }

    /// The system has given the app the microphone and the speaker, or
    /// taken them back (CallKit's `didActivate`/`didDeactivate`). WebRTC is
    /// in manual-audio mode whenever CallKit is in charge, so this is the
    /// only thing that ever starts or stops a call's sound.
    func systemAudio(_ on: Bool) {
        mark(on ? "iOS gave the call its sound" : "iOS took the sound back")
        Self.handOver(on)
        // Nothing else happens here, and nothing may. Setting the category
        // on the session CallKit has just handed over stops the sound dead,
        // both ways — which is what a build that did it turned out to do on
        // a real iPhone (2026-10-04). The hand-over, and only the
        // hand-over.
        if on { Self.tellTheRoute() }
    }

    /// WebRTC counts the hand-over, so it has to be balanced exactly once
    /// each way — and the call's media is often gone by the time the system
    /// takes the session back, which is why this is the class's and not one
    /// call's.
    private static var handedOver = false

    /// The hand-over when no call's media is there to take it (it arrived
    /// before the media was made, or after it was gone): the count still
    /// has to follow the system's, or the next call starts silent.
    static func systemAudioWithNoCall(_ on: Bool) {
        handOver(on)
    }

    private static func handOver(_ on: Bool) {
        guard on != handedOver else { return }
        let session = RTCAudioSession.sharedInstance()
        if on {
            session.audioSessionDidActivate(AVAudioSession.sharedInstance())
        } else {
            session.audioSessionDidDeactivate(AVAudioSession.sharedInstance())
        }
        session.isAudioEnabled = on
        handedOver = on
    }

    /// Puts the call on the loudspeaker, or back on the earpiece.
    ///
    /// Only the output is moved, and nothing else about the session is
    /// touched: the category and the mode were set before the call started
    /// and must stay exactly as they are while CallKit has the session,
    /// because changing them mid-call silences it (2026-10-04).
    ///
    /// Whether it worked is read back from the route itself rather than
    /// assumed, and that is what the button and the words beside it show.
    func setSpeaker(_ on: Bool) {
        Self.wantsSpeaker = on
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        do {
            try session.overrideOutputAudioPort(on ? .speaker : .none)
        } catch {
            // Say so rather than leave the button lit with the sound still
            // in the earpiece.
            onRouteTrouble?("This phone wouldn't move the sound. The call carries on.")
        }
        session.unlockForConfiguration()
        Self.tellTheRoute()
    }

    /// Where the sound is actually coming out, and whether there is anywhere
    /// else it could.
    nonisolated static func route(
        of route: AVAudioSessionRouteDescription, others: Bool = false
    ) -> AudioRoute {
        guard let output = route.outputs.first else {
            return AudioRoute(name: "No sound", speaker: false, builtIn: true, others: others)
        }
        switch output.portType {
        case .builtInSpeaker:
            return AudioRoute(name: "Speaker", speaker: true, builtIn: true, others: others)
        case .builtInReceiver:
            return AudioRoute(name: "Speaker", speaker: false, builtIn: true, others: others)
        case .carAudio:
            return AudioRoute(name: "Car", speaker: false, builtIn: false, others: others)
        default:
            return AudioRoute(name: output.portName, speaker: false, builtIn: false, others: others)
        }
    }

    /// Whether the sound could come out of something other than this phone:
    /// AirPods, a wired headset, a car. The *current* route is not the
    /// question — a pair of AirPods connected in the middle of a call is
    /// available long before anything moves to it, and the person has to be
    /// able to choose it (owner, 2026-10-05: "the speaker icon didn't change
    /// and didn't include the airpod option unless I make a new call").
    private static var somewhereElseToSend: Bool {
        let session = AVAudioSession.sharedInstance()
        if session.currentRoute.outputs.contains(where: { !builtIn($0.portType) }) { return true }
        // A headset's *input* is how iOS says it is there at all, which is
        // what the Phone app's own button goes by.
        return (session.availableInputs ?? []).contains { $0.portType != .builtInMic }
    }

    private nonisolated static func builtIn(_ port: AVAudioSession.Port) -> Bool {
        port == .builtInSpeaker || port == .builtInReceiver
    }

    /// Something was just connected. An output the person asked for by hand —
    /// the loudspeaker — otherwise sits in the way of it: iOS leaves the
    /// override where it was put, and the AirPods never get the call. Clearing
    /// it hands the choice back to the system, which does what every phone
    /// does and moves the sound to what was just connected.
    private static func newDeviceArrived() {
        guard onTheSpeaker, somewhereElseToSend else { return }
        wantsSpeaker = false
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        try? session.overrideOutputAudioPort(.none)
        session.unlockForConfiguration()
    }

    /// The person put the call on the loudspeaker, and nothing they did has
    /// taken it off: a headset arriving clears it (`newDeviceArrived`), and
    /// so does the call ending.
    private static var wantsSpeaker = false

    /// The loudspeaker was switched on or off by hand, wherever the button
    /// was: the app's own, or the system's call screen on the lock screen,
    /// which the app never hears about except as a route change. Only the
    /// app's button used to count, so a call answered on the lock screen
    /// with the speaker on fell back to the earpiece the next time the
    /// sound was set up again (owner, 2026-10-11).
    private static func speakerChosen() {
        wantsSpeaker = onTheSpeaker
    }

    /// The loudspeaker was switched off by the audio session being set up
    /// again underneath the call, not by the person: put it back. The owner
    /// turned the speaker on while a call rang and it went off by itself the
    /// moment the call was answered (2026-10-08) — which is when WebRTC
    /// starts the sound and, if its settings differ from the app's, sets the
    /// session's category again, and a category change drops the override.
    /// The settings now agree (`prepareAudioSession`), so this is the
    /// belt to those braces. Only those two reasons: a headset, the system's
    /// own picker or the person are never overruled.
    private static func keepTheSpeaker(after reason: AVAudioSession.RouteChangeReason?) {
        guard wantsSpeaker, reason == .categoryChange || reason == .routeConfigurationChange, !onTheSpeaker,
            AVAudioSession.sharedInstance().currentRoute.outputs.contains(where: { $0.portType == .builtInReceiver })
        else { return }
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        try? session.overrideOutputAudioPort(.speaker)
        session.unlockForConfiguration()
        live?.mark("Speaker put back")
    }

    /// Where the sound is coming out, in words for the call's timeline.
    private static var outputWords: String {
        guard let output = AVAudioSession.sharedInstance().currentRoute.outputs.first else { return "nowhere" }
        switch output.portType {
        case .builtInSpeaker: return "loudspeaker"
        case .builtInReceiver: return "earpiece"
        default: return output.portName
        }
    }

    /// Why the sound moved, in words for the call's timeline.
    private static func words(for reason: AVAudioSession.RouteChangeReason?) -> String {
        switch reason {
        case .newDeviceAvailable: return "something connected"
        case .oldDeviceUnavailable: return "something disconnected"
        case .categoryChange: return "sound set up again"
        case .override: return "switched"
        case .wakeFromSleep: return "woke"
        case .noSuitableRouteForCategory: return "nowhere to send it"
        case .routeConfigurationChange: return "route set up again"
        default: return "the system"
        }
    }

    /// Whether the sound is coming out of the loudspeaker right now, which
    /// is what the button shows — not what was asked for.
    private static var onTheSpeaker: Bool {
        AVAudioSession.sharedInstance().currentRoute.outputs.contains { $0.portType == .builtInSpeaker }
    }

    /// Watches where the sound goes and says so — and only says so. The
    /// route is never put back by force: a headset, a car or the system's
    /// own picker is the person's choice, and fighting it is how a call
    /// ends up silent.
    private func watchTheRoute() {
        guard routeWatch == nil else { return }
        routeWatch = NotificationCenter.default.addObserver(
            forName: AVAudioSession.routeChangeNotification, object: nil, queue: .main
        ) { note in
            let reason = (note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt)
                .flatMap(AVAudioSession.RouteChangeReason.init(rawValue:))
            MainActor.assumeIsolated {
                if reason == .newDeviceAvailable { Self.newDeviceArrived() }
                if reason == .override { Self.speakerChosen() }
                Self.keepTheSpeaker(after: reason)
                Self.live?.mark("Sound to \(Self.outputWords) (\(Self.words(for: reason)))")
                Self.tellTheRoute()
            }
        }
    }

    /// The one place the app finds out where the sound is: read from the
    /// session, never guessed.
    private static func tellTheRoute() {
        live?.onRoute?(
            route(of: AVAudioSession.sharedInstance().currentRoute, others: somewhereElseToSend))
    }

    /// The call whose sound is up, so a route change can be told to it.
    /// One call at a time, as everywhere else here.
    private static weak var live: WebRTCMedia?

    // MARK: - The peer connection

    /// A direct route first, then Linx's relay over UDP, then over TLS on
    /// 443 — which is the one that works on a network that blocks
    /// everything else (docs/WEB.md §6).
    static func configuration(relay: PhoneLine.Turn?, addresses: [String: [String]] = [:], relayOnly: Bool = false)
        -> RTCConfiguration
    {
        let configuration = RTCConfiguration()
        if let relay, !relay.urls.isEmpty {
            // By address where iOS looked the name up, each server still
            // carrying its name for TLS (SNI and the certificate check).
            configuration.iceServers = RelayAddresses.servers(for: relay.urls, addresses: addresses).map {
                RTCIceServer(
                    urlStrings: $0.urls, username: relay.username, credential: relay.credential,
                    tlsCertPolicy: .secure, hostname: $0.hostname)
            }
        }
        if relayOnly { configuration.iceTransportPolicy = .relay }
        configuration.sdpSemantics = .unifiedPlan
        // Not "maxBundle": Asterisk's offers carry no BUNDLE group, and a
        // call is one stream of sound, so nothing is lost by it.
        configuration.rtcpMuxPolicy = .require
        configuration.continualGatheringPolicy = .gatherOnce
        // Only the routes Asterisk can use, so the offer and the answer stay
        // small: a phone listed 24 of them (11 over TCP, which Asterisk's ICE
        // never uses, and 11 of its own addresses, most of them IPv6 ones on
        // every network it has), and the 4.4 KB answer took 7 s to cross a
        // slow mobile uplink while the caller heard it ring on (2026-10-11).
        // The relay still has its TCP and TLS ways in; they are relay
        // candidates, which this doesn't touch.
        // One set of routes gathered as soon as the connection is made,
        // rather than when the call's offer or answer is: a connection made
        // ahead of the call (`reserveTheRelay`) has its relay ready by then.
        configuration.iceCandidatePoolSize = 1
        configuration.tcpCandidatePolicy = .disabled
        configuration.disableLinkLocalNetworks = true
        configuration.maxIPv6Networks = 1
        return configuration
    }

    /// This call's configuration: the relay by the addresses iOS found, and
    /// only the relay once the call has been moved onto it.
    private func configuration() -> RTCConfiguration {
        Self.configuration(relay: turn, addresses: relayAddresses, relayOnly: relayOnly)
    }

    // MARK: - The relay, reserved ahead of the call

    /// A connection made before any call, with its routes (the relay above
    /// all) already gathered. Reserving a relay on a mobile network took
    /// about five seconds, and it used to start only when the call arrived,
    /// so the person answered into silence (2026-10-11). The phone keeps one
    /// ready while the app is in front and from the moment a push wakes it,
    /// and the next call takes it over. It goes when the app goes to the
    /// background, so nothing is held for a phone nobody is using.
    private struct Reservation {
        let key: String
        let connection: RTCPeerConnection
        let verifier: RelayCertificates
        let delegate: NobodyListening
        let made: Date
    }

    private static var reservation: Reservation?
    /// Relay credentials are good for an hour and the line refreshes them;
    /// a reservation older than this is made again.
    private static let reservationLasts: TimeInterval = 20 * 60

    nonisolated static func reservationKey(_ turn: PhoneLine.Turn?, _ addresses: [String: [String]]) -> String {
        guard let turn else { return "" }
        let found = addresses.keys.sorted().map { "\($0)=\(addresses[$0]!.sorted().joined(separator: ","))" }
        return ([turn.username] + turn.urls + found).joined(separator: "|")
    }

    /// Reserves a relay for the next call, unless one for the same relay
    /// and credentials is already waiting.
    static func reserveTheRelay(_ turn: PhoneLine.Turn?) async {
        guard let turn, !turn.urls.isEmpty else { return }
        let addresses = await RelayAddresses.addresses(for: RelayCertificates.hosts(in: turn.urls))
        let key = reservationKey(turn, addresses)
        if let held = reservation, held.key == key, Date().timeIntervalSince(held.made) < reservationLasts {
            return
        }
        releaseTheRelay()
        let verifier = RelayCertificates(relay: turn)
        let delegate = NobodyListening()
        guard
            let connection = factory.peerConnection(
                with: configuration(relay: turn, addresses: addresses), constraints: noConstraints,
                certificateVerifier: verifier, delegate: delegate)
        else { return }
        reservation = Reservation(
            key: key, connection: connection, verifier: verifier, delegate: delegate, made: Date())
    }

    /// Lets the reserved relay go.
    static func releaseTheRelay() {
        reservation?.connection.close()
        reservation = nil
    }

    private static func takeTheReservation(_ key: String) -> RTCPeerConnection? {
        guard let held = reservation, held.key == key, Date().timeIntervalSince(held.made) < reservationLasts
        else { return nil }
        reservation = nil
        return held.connection
    }

    private func start() throws -> RTCPeerConnection {
        if let connection { return connection }
        Self.live = self
        Self.prepareAudioSession()
        watchTheRoute()
        let connection: RTCPeerConnection
        if let ready = Self.takeTheReservation(Self.reservationKey(turn, relayAddresses)) {
            // Made ahead of the call, its relay already reserved: the call
            // takes it over and its routes are ready at once.
            ready.delegate = self
            connection = ready
            mark("Relay reserved ahead")
        } else {
            // Who says the relay's TLS certificate is good: iOS, not the list
            // frozen inside WebRTC (`RelayCertificates`). Without this, a relay
            // certificate newer than the library is refused and a call that
            // needs the relay has nowhere for its sound to go.
            guard
                let made = Self.factory.peerConnection(
                    with: configuration(), constraints: Self.noConstraints,
                    certificateVerifier: RelayCertificates(relay: turn), delegate: self)
            else { throw MediaTrouble.noOffer }
            connection = made
        }
        let source = Self.factory.audioSource(with: Self.microphone)
        let track = Self.factory.audioTrack(with: source, trackId: "linx-audio")
        connection.add(track, streamIds: ["linx"])
        self.audio = track
        self.connection = connection
        return connection
    }

    private func setLocal(_ description: RTCSessionDescription) async throws {
        guard let connection else { throw MediaTrouble.noOffer }
        try await withCheckedThrowingContinuation { (done: CheckedContinuation<Void, Error>) in
            connection.setLocalDescription(description) { error in
                if let error {
                    done.resume(throwing: error)
                } else {
                    done.resume()
                }
            }
        }
    }

    private func setRemote(_ description: RTCSessionDescription) async throws {
        guard let connection else { throw MediaTrouble.noAnswer }
        let user = SDPTweaks.iceUser(description.sdp)
        theyRestarted = theirICEUser != nil && user != nil && user != theirICEUser
        theirICEUser = user ?? theirICEUser
        try await withCheckedThrowingContinuation { (done: CheckedContinuation<Void, Error>) in
            connection.setRemoteDescription(description) { error in
                if let error {
                    done.resume(throwing: error)
                } else {
                    done.resume()
                }
            }
        }
    }

    /// Waits for this phone's addresses, but never for long.
    private func waitForCandidates() async {
        // Right after an ICE restart the state can still read "complete"
        // from the last gathering, before the new one has begun: an offer
        // sent then has no routes in it at all, which Asterisk takes as "no
        // ICE" for the rest of the call (SDPTweaks.everyStreamHasRoutes).
        // So "complete" counts only once the routes are in the description.
        let giveUp = Date().addingTimeInterval(TimeInterval(Self.gatherLimit.components.seconds))
        while connection?.iceGatheringState == .complete, !stopped,
            !SDPTweaks.everyStreamHasRoutes(connection?.localDescription?.sdp ?? ""), Date() < giveUp
        {
            try? await Task.sleep(for: .milliseconds(50))
        }
        guard connection?.iceGatheringState != .complete else { return }
        gatheringLimit = Task { [weak self] in
            try? await Task.sleep(for: Self.gatherLimit)
            guard !Task.isCancelled else { return }
            self?.doneGathering()
        }
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            if connection?.iceGatheringState == .complete || stopped {
                done.resume()
            } else {
                gathered = done
            }
        }
    }

    private func doneGathering() {
        gatheringLimit?.cancel()
        gatheringLimit = nil
        gathered?.resume()
        gathered = nil
        note { $0.settled = true }
    }

    /// Caps what this phone sends, whatever the other side's SDP allows:
    /// the voice at Opus's mono ceiling (CLAUDE.md, the low-bandwidth rule).
    private func capWhatThisPhoneSends() {
        guard let connection else { return }
        for sender in connection.senders where sender.track?.kind == kRTCMediaStreamTrackKindAudio {
            let parameters = sender.parameters
            for encoding in parameters.encodings {
                encoding.maxBitrateBps = NSNumber(value: SDPTweaks.opusMaxBitrate)
            }
            sender.parameters = parameters
        }
    }

    private var lastPath: String?
    /// Linx's ICE username from its last description, and whether the last
    /// one changed it.
    private var theirICEUser: String?
    private var theyRestarted = false
    private var soundHasArrived = false

    /// The route and the first sound, on the timeline: when the call
    /// changed route, and when anything was first heard from the other
    /// end, so a slow start says whether it was the route or the sound.
    private func noteTheRoute(_ connection: MediaConnection) {
        if connection.path != lastPath {
            lastPath = connection.path
            mark("Route now \(connection.path)")
        }
        if !soundHasArrived, connection.audioBytesIn > 0 {
            soundHasArrived = true
            mark("First sound arrived")
        }
        note {
            $0.offered = connection.offered
            $0.pairs = connection.pairs
        }
    }

    /// Reads how the sound is getting through, every few seconds — no more
    /// often, because a phone's battery pays for it.
    private func startWatching() {
        watch?.cancel()
        quickUntil = Date().addingTimeInterval(Self.quickFor)
        watch = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                if let connection = await self.readConnection() {
                    self.noteTheRoute(connection)
                    self.onConnection?(connection)
                }
                // Every second for the first few seconds, so a call whose sound
                // isn't getting through is noticed and mended in two or three
                // (PhoneModel.mendIfSilent); then the usual pace.
                let pause: Duration = Date() < self.quickUntil ? .seconds(1) : Self.readRouteEvery
                try? await Task.sleep(for: pause)
            }
        }
    }

    func readConnection() async -> MediaConnection? {
        guard let connection else { return nil }
        let report = await withCheckedContinuation { (done: CheckedContinuation<RTCStatisticsReport, Never>) in
            connection.statistics { done.resume(returning: $0) }
        }
        var audioBytesIn = 0
        var audioBytesOut = 0
        var framesIn = 0
        var reportsBack = 0
        var pairID: String?
        // The sound's own route. A call whose picture travels separately has
        // two, and taking whichever came first made Call details seem to
        // switch route every few seconds (2026-10-12).
        var soundTransport: String?
        for (_, statistic) in report.statistics {
            switch statistic.type {
            case "transport":
                pairID = pairID ?? statistic.values["selectedCandidatePairId"] as? String
            case "outbound-rtp" where statistic.values["kind"] as? String == "audio":
                soundTransport = statistic.values["transportId"] as? String ?? soundTransport
                audioBytesOut += (statistic.values["bytesSent"] as? NSNumber)?.intValue ?? 0
            case "inbound-rtp" where statistic.values["kind"] as? String == "audio":
                audioBytesIn += (statistic.values["bytesReceived"] as? NSNumber)?.intValue ?? 0
            case "inbound-rtp" where statistic.values["kind"] as? String == "video":
                framesIn += (statistic.values["framesDecoded"] as? NSNumber)?.intValue ?? 0
            case "remote-inbound-rtp" where statistic.values["kind"] as? String == "audio":
                reportsBack += (statistic.values["roundTripTimeMeasurements"] as? NSNumber)?.intValue ?? 0
            default: break
            }
        }
        if let soundTransport,
            let chosen = report.statistics[soundTransport]?.values["selectedCandidatePairId"] as? String
        {
            pairID = chosen
        }
        if pairID == nil {
            pairID =
                report.statistics.first { _, statistic in
                    statistic.type == "candidate-pair" && statistic.values["nominated"] as? Bool == true
                        && statistic.values["state"] as? String == "succeeded"
                }?.key
        }
        guard let pairID, let pair = report.statistics[pairID] else { return nil }
        let local = (pair.values["localCandidateId"] as? String).flatMap { report.statistics[$0] }
        let remote = (pair.values["remoteCandidateId"] as? String).flatMap { report.statistics[$0] }
        let relayed = local?.values["candidateType"] as? String == "relay"
        let rtt = (pair.values["currentRoundTripTime"] as? NSNumber)?.doubleValue
        let room = (pair.values["availableOutgoingBitrate"] as? NSNumber)?.intValue
        return MediaConnection(
            route: relayed ? .relayed : .direct,
            roundTripMs: rtt.map { Int(($0 * 1000).rounded()) },
            relayProtocol: relayed ? local?.values["relayProtocol"] as? String : nil,
            audioBytesIn: audioBytesIn, audioBytesOut: audioBytesOut, outgoingBitrate: room,
            framesIn: framesIn,
            path: "\(Self.words(for: local?.values)) → \(Self.words(for: remote?.values, address: true))",
            offered: Self.offered(in: report), pairs: Self.pairs(in: report, chosen: pairID), reportsBack: reportsBack)
    }

    /// One end of a route in a few words: its kind, how it travels, and for
    /// the far end where it is. This phone's own addresses are left out —
    /// they're what a person would least want pasted into a message.
    nonisolated static func words(for candidate: [String: NSObject]?, address: Bool = false) -> String {
        guard let candidate else { return "?" }
        let kind = candidate["candidateType"] as? String ?? "?"
        let travels = (kind == "relay" ? candidate["relayProtocol"] : candidate["protocol"]) as? String ?? "?"
        let at = address ? (candidate["address"] as? String ?? candidate["ip"] as? String ?? "") : ""
        return "\(kind)/\(travels)\(at.isEmpty ? "" : " \(at)")"
    }

    private static func offered(in report: RTCStatisticsReport) -> [String] {
        let kinds = report.statistics.values.filter { $0.type == "local-candidate" }.map { words(for: $0.values) }
        return Array(Set(kinds)).sorted()
    }

    /// Every pairing tried, the chosen one first, at most eight.
    private static func pairs(in report: RTCStatisticsReport, chosen: String) -> [String] {
        let all = report.statistics.filter { $0.value.type == "candidate-pair" }
        let ordered = all.sorted { a, b in
            if (a.key == chosen) != (b.key == chosen) { return a.key == chosen }
            return a.key < b.key
        }
        return ordered.prefix(8).map { id, pair in
            let local = (pair.values["localCandidateId"] as? String).flatMap { report.statistics[$0] }
            let remote = (pair.values["remoteCandidateId"] as? String).flatMap { report.statistics[$0] }
            var line = "\(words(for: local?.values)) → \(words(for: remote?.values, address: true)): "
            line += pair.values["state"] as? String ?? "?"
            if id == chosen { line += ", in use" }
            if let rtt = (pair.values["currentRoundTripTime"] as? NSNumber)?.doubleValue {
                line += ", \(Int((rtt * 1000).rounded())) ms"
            }
            return line
        }
    }

    // MARK: - One engine for the whole app

    /// WebRTC's factory is expensive to make and cheap to keep, so the app
    /// makes one. The video codecs are the phone's own hardware ones first
    /// (H.264), which is the difference between a warm phone and a flat
    /// battery; nothing of them runs until a camera is switched on.
    static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory(
            encoderFactory: RTCDefaultVideoEncoderFactory(), decoderFactory: RTCDefaultVideoDecoderFactory())
    }()

    private static let noConstraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)

    private static let audioOnly = RTCMediaConstraints(
        mandatoryConstraints: ["OfferToReceiveAudio": "true", "OfferToReceiveVideo": "false"],
        optionalConstraints: nil)

    /// What WebRTC does to the microphone's sound before it goes out.
    private static let microphone = RTCMediaConstraints(
        mandatoryConstraints: nil,
        optionalConstraints: [
            "googEchoCancellation": "true", "googAutoGainControl": "true",
            "googNoiseSuppression": "true",
        ])

    /// A phone call's audio session: the earpiece by default, the sound of
    /// other apps ducked, and Bluetooth headsets allowed.
    ///
    /// Who turns it on depends on who owns the call. With CallKit the system
    /// does, in `didActivate`, and WebRTC is put in **manual audio** mode so
    /// nothing of the call's sound starts a moment earlier — which is what
    /// prevents the dropouts reviewers notice (docs/PHASE2.md §14, "Audio
    /// path"). Where CallKit may not be used (ADR-078) the app turns it on
    /// itself, as it did before step 6.
    private static func prepareAudioSession() {
        // WebRTC sets the session up again itself when the call's sound
        // starts (at the answer, for a line that sends nothing before it).
        // Told the same settings as these, it finds nothing to change; left
        // with its own, it set the category again with different options,
        // and a category change takes the call off the loudspeaker
        // (2026-10-08).
        let shared = RTCAudioSessionConfiguration.webRTC()
        shared.category = AVAudioSession.Category.playAndRecord.rawValue
        shared.mode = AVAudioSession.Mode.voiceChat.rawValue
        shared.categoryOptions = callOptions
        RTCAudioSessionConfiguration.setWebRTC(shared)
        let session = RTCAudioSession.sharedInstance()
        let system = CallStyle.usesCallKit
        session.useManualAudio = system
        // Off until the system hands the sound over — unless it already
        // has. Switching it off then left it off for the whole call, since
        // the hand-over that turns it on had already happened: a call with
        // no sound either way (2026-10-12).
        if system { session.isAudioEnabled = handedOver }
        session.lockForConfiguration()
        defer { session.unlockForConfiguration() }
        do {
            try session.setCategory(.playAndRecord, mode: .voiceChat, options: callOptions)
            if !system { try session.setActive(true) }
        } catch {
            // Nothing to do about it here: the call goes on without sound and
            // the person hears nothing, which the in-call screen shows.
        }
    }

    private static let callOptions: AVAudioSession.CategoryOptions = [
        .allowBluetoothHFP, .allowBluetoothA2DP, .duckOthers,
    ]

    private static func releaseAudioSession() {
        wantsSpeaker = false
        let session = RTCAudioSession.sharedInstance()
        if CallStyle.usesCallKit {
            // The system owns the session, and deactivating it from here is
            // what makes the *next* call silent. Only the hand-over is
            // closed, so WebRTC's count is even again.
            handOver(false)
            return
        }
        session.lockForConfiguration()
        defer { session.unlockForConfiguration() }
        try? session.setActive(false)
    }
}

// MARK: - What WebRTC says back

extension WebRTCMedia: RTCPeerConnectionDelegate {
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange state: RTCIceGatheringState) {
        guard state == .complete else { return }
        Task { @MainActor in self.doneGathering() }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        let kind = Self.kind(of: candidate.sdp)
        // A candidate through Linx's relay means every route worth trying is
        // in hand: send the offer a moment later rather than waiting out the
        // whole gathering (the browser does the same).
        let relay = candidate.sdp.contains(" typ relay")
        Task { @MainActor in
            if let kind, !self.diagnostics.found.contains(kind), kind == CallDiagnostics.theRelay {
                self.mark("Relay ready")
            }
            if let kind { self.note { if !$0.found.contains(kind) { $0.found.append(kind) } } }
            guard relay else { return }
            try? await Task.sleep(for: Self.afterRelayCandidate)
            self.doneGathering()
        }
    }

    /// A route this phone found for itself, in words rather than in ICE's.
    private nonisolated static func kind(of candidate: String) -> String? {
        guard let typ = candidate.range(of: " typ ") else { return nil }
        switch candidate[typ.upperBound...].prefix(while: { !$0.isWhitespace }) {
        case "host": return "this network"
        case "srflx", "prflx": return "the internet"
        case "relay": return CallDiagnostics.theRelay
        default: return nil
        }
    }

    /// Linx's relay wouldn't give this phone a way through. It is the one
    /// thing that silences a call on a mobile network, where there is no other
    /// route, and the reason is worth keeping word for word: 401 is credentials
    /// it wouldn't take, 701 a relay it couldn't reach at all.
    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didFailToGatherIceCandidate event: RTCIceCandidateErrorEvent
    ) {
        let trouble = CallDiagnostics.RelayTrouble(
            url: event.url, code: Int(event.errorCode), said: event.errorText)
        Task { @MainActor in
            self.note {
                guard !$0.relayTrouble.contains(trouble) else { return }
                $0.relayTrouble.append(trouble)
            }
        }
    }

    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didChange state: RTCIceConnectionState
    ) {
        let words = Self.words(for: state)
        let trouble: Bool?
        switch state {
        case .connected, .completed: trouble = false
        case .disconnected, .failed: trouble = true
        default: trouble = nil
        }
        Task { @MainActor in
            self.note { $0.ice = words }
            guard let trouble else { return }
            self.onTrouble?(trouble)
        }
    }

    private nonisolated static func words(for state: RTCIceConnectionState) -> String {
        switch state {
        case .new: return "not started"
        case .checking: return "finding a way through"
        case .connected: return "connected"
        case .completed: return "connected"
        case .failed: return "no way through"
        case .disconnected: return "dropped"
        case .closed: return "closed"
        @unknown default: return "unknown"
        }
    }

    /// The other side's picture has arrived (unified plan tells the app
    /// about each stream as it starts). The track itself is what the screen
    /// draws; nothing is copied.
    nonisolated func peerConnectionShouldNegotiate(_ connection: RTCPeerConnection) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange state: RTCSignalingState) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]
    ) {}
    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didOpen dataChannel: RTCDataChannel
    ) {}
}

/// The delegate of a connection made ahead of the call, before any call
/// wants to hear from it (`WebRTCMedia.reserveTheRelay`). The call that
/// takes the connection over becomes its delegate.
final class NobodyListening: NSObject, RTCPeerConnectionDelegate {
    func peerConnection(_ connection: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ connection: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ connection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ connection: RTCPeerConnection) {}
    func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {}
    func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    func peerConnection(_ connection: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {}
    func peerConnection(_ connection: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ connection: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
}
