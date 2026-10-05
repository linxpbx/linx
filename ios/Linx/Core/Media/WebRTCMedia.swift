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

    /// The words for a route through Linx's relay. On a mobile network it is
    /// the only way the sound can go.
    static let theRelay = "Linx's relay"

    /// Whether a route through the relay was found at all.
    var foundTheRelay: Bool { found.contains(Self.theRelay) }

    /// What to tell the person when the relay wouldn't have them. Only worth
    /// saying when no route through it was found: a phone that reaches the
    /// relay over TLS and not UDP is perfectly fine and says nothing.
    var relayWords: String? {
        guard !foundTheRelay, let first = relayTrouble.first else { return nil }
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

/// Whether the other side's picture is really arriving, from the frame count
/// alone — the only honest answer, because a side can stop sending without
/// saying so (the echo test does exactly that the moment this phone's camera
/// goes off) and can start again just as quietly.
///
/// A frozen last frame left on the screen for the rest of the call was the
/// first half of this (owner, 2026-10-04: "the big screen freezes"); a picture
/// switched off at the first reading and then unable ever to come back was the
/// second (owner, 2026-10-05: the echo test showed no picture at all, because
/// the first reading arrives before the first frame does).
struct PictureWatch {
    /// Readings with no new frame before the picture is called gone — two, so
    /// a lift, a lorry or a handover between masts doesn't take it away.
    static let quietReadings = 2

    private var seen = 0
    private var quiet = 0

    /// What the reading changes, or nil for "nothing has changed". `showing`
    /// is whether their picture is on the screen now.
    mutating func reading(framesIn: Int, showing: Bool) -> Bool? {
        defer { seen = framesIn }
        if framesIn > seen {
            quiet = 0
            return showing ? nil : true
        }
        guard showing else {
            quiet = 0
            return nil
        }
        quiet += 1
        guard quiet >= Self.quietReadings else { return nil }
        quiet = 0
        return false
    }

    /// Their picture came or went for a reason of its own (their SDP said so,
    /// or the call ended): the count starts again.
    mutating func forget() {
        quiet = 0
        seen = 0
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

    /// Whose camera is on, and the pictures themselves (docs/PHASE2.md §7).
    private(set) var video = CallVideo()
    let tracks = VideoTracks()
    var onVideoChanged: ((CallVideo) -> Void)?
    /// The link can't carry a picture any more: turn this phone's camera off
    /// and tell the other side, which is the user agent's job, not this
    /// one's.
    var onVideoTooExpensive: (() -> Void)?

    private var turn: PhoneLine.Turn?
    private var connection: RTCPeerConnection?
    private var audio: RTCAudioTrack?
    private var camera: Camera?
    private var videoSender: RTCRtpSender?
    /// How long the link has been too thin for a picture, in readings.
    private var tooThin = 0
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
    private static let gatherLimit: Duration = .seconds(10)
    private static let afterRelayCandidate: Duration = .milliseconds(300)
    private static let readRouteEvery: Duration = .seconds(5)
    /// Room enough for the voice, the overhead and a thin picture. Below it
    /// the picture is what gives way.
    private static let tooThinForVideo = 150_000
    private static let tooThinReadings = 3

    init(turn: PhoneLine.Turn?) {
        self.turn = turn
        super.init()
        noteTheRelay()
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
        let connection = try start()
        let local = try await withCheckedThrowingContinuation { (done: CheckedContinuation<String, Error>) in
            connection.offer(for: video.on ? Self.withVideo : Self.audioOnly) { description, error in
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
        return ours(full)
    }

    func answer(to offer: String) async throws -> String {
        let connection = try start()
        try await setRemote(RTCSessionDescription(type: .offer, sdp: offer))
        // They have added a picture to the call. This phone shows it and
        // answers "I'll watch, I'm not sending" — its own camera is never
        // switched on by somebody else, only by the button on this screen.
        let theirs = SDPTweaks.theySendVideo(offer)
        if theirs, camera == nil { watchOnly() }
        let local = try await withCheckedThrowingContinuation { (done: CheckedContinuation<String, Error>) in
            connection.answer(for: theirs || video.on ? Self.withVideo : Self.audioOnly) { description, error in
                guard let description else {
                    done.resume(throwing: error ?? MediaTrouble.noAnswer)
                    return
                }
                done.resume(returning: description.sdp)
            }
        }
        try await setLocal(RTCSessionDescription(type: .answer, sdp: SDPTweaks.preferOpusFecDtx(local)))
        await waitForCandidates()
        guard let full = connection.localDescription?.sdp else { throw MediaTrouble.noAnswer }
        capWhatThisPhoneSends()
        startWatching()
        theirVideo(theirs)
        return ours(full)
    }

    func accept(answer: String) async throws {
        try await setRemote(RTCSessionDescription(type: .answer, sdp: answer))
        capWhatThisPhoneSends()
        startWatching()
        // What they agreed to: a side that turns video down answers with a
        // video section of port 0, and this phone stops showing a window.
        theirVideo(SDPTweaks.theySendVideo(answer))
        if video.mine, !SDPTweaks.hasVideo(answer) { await stopCamera() }
    }

    /// The last thing done to every SDP this phone sends: the browser's own
    /// Opus settings, and the ceiling on the picture.
    private func ours(_ sdp: String) -> String {
        SDPTweaks.capVideo(SDPTweaks.preferOpusFecDtx(sdp))
    }

    // MARK: - The picture (docs/PHASE2.md §7)

    /// Switches this phone's camera on and hands back an offer for the
    /// re-INVITE that tells the other side. A call always starts as sound —
    /// that is what a lock screen, a car and a headset understand — and the
    /// picture is added to it afterwards, by either side, whenever someone
    /// presses the button.
    func startVideo() async throws -> String {
        guard connection != nil else { throw MediaTrouble.noOffer }
        guard !video.mine else { return try await offer() }
        let camera = self.camera ?? Camera(factory: Self.factory)
        try await camera.start()
        self.camera = camera
        if let sender = videoSender {
            // There is already a place for a picture in this call (they
            // started one): this phone's camera goes into it.
            sender.track = camera.track
            for transceiver in connection?.transceivers ?? []
            where transceiver.mediaType == .video {
                transceiver.setDirection(.sendRecv, error: nil)
            }
        } else {
            videoSender = connection?.add(camera.track, streamIds: ["linx"])
        }
        tracks.local = camera.track
        said(CallVideo(mine: true, theirs: video.theirs))
        let offer = try await offer()
        capWhatThisPhoneSends()
        return offer
    }

    /// Switches it off again, and hands back the offer that says so. The
    /// call carries on as sound, which is the point of doing it this way.
    func stopVideo() async throws -> String {
        await stopCamera()
        return try await offer()
    }

    /// The front camera or the back one.
    func switchCamera() { camera?.flip() }

    /// Newer relay credentials. The old ones last an hour and a phone is
    /// signed in for days, so these arrive before the old ones run out and
    /// are handed to the call that is already up as well as kept for the
    /// next one: the routes in use are left alone, and any gathered from
    /// now on use these.
    func use(relay: PhoneLine.Turn) {
        turn = relay
        connection?.setConfiguration(Self.configuration(relay: relay))
        noteTheRelay()
    }

    /// Nobody took the offer this phone just made (the other side wouldn't
    /// have video, or the switch refused it). `rollback` puts the call back
    /// exactly where it was (RFC 8829 §4.1.10) so the conversation carries
    /// on as though nothing had been asked.
    func rollbackOffer() async {
        try? await setLocal(RTCSessionDescription(type: .rollback, sdp: ""))
        if video.mine { await stopCamera() }
    }

    /// Whether this phone's own picture is the mirror image people expect of
    /// themselves (the front camera) or the plain one (the back).
    var mirrorsMyVideo: Bool { camera?.mirrored ?? true }

    private func stopCamera() async {
        camera?.stop()
        camera = nil
        tracks.local = nil
        // The place in the call stays, empty and receive-only: taking it
        // away altogether would renumber the call's streams, which Asterisk
        // and the other side would have to follow for no good reason.
        videoSender?.track = nil
        if video.theirs {
            watchOnly()
        } else {
            for transceiver in connection?.transceivers ?? [] where transceiver.mediaType == .video {
                transceiver.setDirection(.inactive, error: nil)
            }
        }
        said(CallVideo(mine: false, theirs: video.theirs))
    }

    /// "Show me yours, I'm not sending mine."
    private func watchOnly() {
        for transceiver in connection?.transceivers ?? [] where transceiver.mediaType == .video {
            transceiver.setDirection(.recvOnly, error: nil)
            videoSender = transceiver.sender
        }
    }

    private func theirVideo(_ on: Bool) {
        guard on != video.theirs else { return }
        // The track itself is kept either way. A picture that stopped has to
        // be able to come back — the same track starts carrying frames again
        // and WebRTC says nothing about it, because as far as it is concerned
        // nothing changed (owner, 2026-10-05: the echo test showed no picture
        // at all, the camera having been called off at the first reading,
        // before the first frame had had time to arrive). It is let go of when
        // the other side really takes it away, and when the call ends.
        frames.forget()
        said(CallVideo(mine: video.mine, theirs: on))
    }

    private func said(_ now: CallVideo) {
        guard now != video else { return }
        video = now
        tooThin = 0
        onVideoChanged?(now)
    }

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
        camera?.stop()
        camera = nil
        videoSender = nil
        tracks.local = nil
        tracks.remote = nil
        video = CallVideo()
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
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        try? session.overrideOutputAudioPort(.none)
        session.unlockForConfiguration()
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
    static func configuration(relay: PhoneLine.Turn?) -> RTCConfiguration {
        let configuration = RTCConfiguration()
        if let relay, !relay.urls.isEmpty {
            configuration.iceServers = [
                RTCIceServer(
                    urlStrings: relay.urls, username: relay.username, credential: relay.credential)
            ]
        }
        configuration.sdpSemantics = .unifiedPlan
        // Not "maxBundle": Asterisk's offers carry no BUNDLE group, and a
        // call is one stream of sound, so nothing is lost by it.
        configuration.rtcpMuxPolicy = .require
        configuration.continualGatheringPolicy = .gatherOnce
        return configuration
    }

    private func start() throws -> RTCPeerConnection {
        if let connection { return connection }
        Self.live = self
        Self.prepareAudioSession()
        watchTheRoute()
        let configuration = Self.configuration(relay: turn)
        guard
            let connection = Self.factory.peerConnection(
                with: configuration, constraints: Self.noConstraints, delegate: self)
        else { throw MediaTrouble.noOffer }
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
    }

    /// Their picture is on the screen while pictures are arriving — and back
    /// on it the moment they start arriving again.
    private func checkTheirPicture(_ connection: MediaConnection) {
        guard let arriving = frames.reading(framesIn: connection.framesIn, showing: video.theirs)
        else { return }
        guard !arriving || tracks.remote != nil else { return }
        theirVideo(arriving)
    }

    private var frames = PictureWatch()

    /// Sound comes first (docs/PHASE2.md §7). When the link has not got the
    /// room for a picture for three readings running — a quarter of a minute
    /// — this phone's camera goes off and the call carries on as a phone
    /// call, rather than letting the voice break up for a picture nobody can
    /// see properly anyway. One reading is not enough: a lift, a lorry or a
    /// handover between masts would otherwise end every video call.
    private func checkTheLink(_ connection: MediaConnection) {
        guard video.mine, let room = connection.outgoingBitrate else {
            tooThin = 0
            return
        }
        guard room < Self.tooThinForVideo else {
            tooThin = 0
            return
        }
        tooThin += 1
        guard tooThin >= Self.tooThinReadings else { return }
        tooThin = 0
        onVideoTooExpensive?()
    }

    /// Caps what this phone sends, whatever the other side's SDP allows:
    /// the voice at Opus's mono ceiling, and the picture well below what a
    /// phone would send if nobody asked (CLAUDE.md, the low-bandwidth rule).
    private func capWhatThisPhoneSends() {
        guard let connection else { return }
        for sender in connection.senders {
            let kind = sender.track?.kind
            guard kind == kRTCMediaStreamTrackKindAudio || kind == kRTCMediaStreamTrackKindVideo else { continue }
            let video = kind == kRTCMediaStreamTrackKindVideo
            let parameters = sender.parameters
            for encoding in parameters.encodings {
                encoding.maxBitrateBps = NSNumber(value: video ? SDPTweaks.videoMaxBitrate : SDPTweaks.opusMaxBitrate)
                if video { encoding.maxFramerate = NSNumber(value: Camera.frameRate) }
            }
            sender.parameters = parameters
        }
    }

    /// Reads how the sound is getting through, every few seconds — no more
    /// often, because a phone's battery pays for it.
    private func startWatching() {
        watch?.cancel()
        watch = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                if let connection = await self.readConnection() {
                    self.onConnection?(connection)
                    self.checkTheLink(connection)
                    self.checkTheirPicture(connection)
                }
                try? await Task.sleep(for: Self.readRouteEvery)
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
        var pairID: String?
        for (_, statistic) in report.statistics {
            switch statistic.type {
            case "transport":
                pairID = statistic.values["selectedCandidatePairId"] as? String ?? pairID
            case "outbound-rtp" where statistic.values["kind"] as? String == "audio":
                audioBytesOut += (statistic.values["bytesSent"] as? NSNumber)?.intValue ?? 0
            case "inbound-rtp" where statistic.values["kind"] as? String == "audio":
                audioBytesIn += (statistic.values["bytesReceived"] as? NSNumber)?.intValue ?? 0
            case "inbound-rtp" where statistic.values["kind"] as? String == "video":
                framesIn += (statistic.values["framesDecoded"] as? NSNumber)?.intValue ?? 0
            default: break
            }
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
        let relayed = local?.values["candidateType"] as? String == "relay"
        let rtt = (pair.values["currentRoundTripTime"] as? NSNumber)?.doubleValue
        let room = (pair.values["availableOutgoingBitrate"] as? NSNumber)?.intValue
        return MediaConnection(
            route: relayed ? .relayed : .direct,
            roundTripMs: rtt.map { Int(($0 * 1000).rounded()) },
            relayProtocol: relayed ? local?.values["relayProtocol"] as? String : nil,
            audioBytesIn: audioBytesIn, audioBytesOut: audioBytesOut, outgoingBitrate: room,
            framesIn: framesIn)
    }

    // MARK: - One engine for the whole app

    /// WebRTC's factory is expensive to make and cheap to keep, so the app
    /// makes one. The video codecs are the phone's own hardware ones first
    /// (H.264), which is the difference between a warm phone and a flat
    /// battery; nothing of them runs until a camera is switched on.
    private static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory(
            encoderFactory: RTCDefaultVideoEncoderFactory(), decoderFactory: RTCDefaultVideoDecoderFactory())
    }()

    private static let noConstraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)

    private static let audioOnly = RTCMediaConstraints(
        mandatoryConstraints: ["OfferToReceiveAudio": "true", "OfferToReceiveVideo": "false"],
        optionalConstraints: nil)

    /// Once there is a picture in the call, every offer and answer carries
    /// it: a re-offer that forgot to would take the call's video away.
    private static let withVideo = RTCMediaConstraints(
        mandatoryConstraints: ["OfferToReceiveAudio": "true", "OfferToReceiveVideo": "true"],
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
        let session = RTCAudioSession.sharedInstance()
        let system = CallStyle.usesCallKit
        session.useManualAudio = system
        if system { session.isAudioEnabled = false }
        session.lockForConfiguration()
        defer { session.unlockForConfiguration() }
        do {
            try session.setCategory(
                .playAndRecord, mode: .voiceChat,
                options: [.allowBluetoothHFP, .allowBluetoothA2DP, .duckOthers])
            if !system { try session.setActive(true) }
        } catch {
            // Nothing to do about it here: the call goes on without sound and
            // the person hears nothing, which the in-call screen shows.
        }
    }

    private static func releaseAudioSession() {
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
    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didAdd receiver: RTCRtpReceiver, streams: [RTCMediaStream]
    ) {
        guard let track = receiver.track as? RTCVideoTrack else { return }
        Task { @MainActor in
            self.tracks.remote = track
            self.theirVideo(true)
        }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove receiver: RTCRtpReceiver) {
        guard receiver.track is RTCVideoTrack else { return }
        Task { @MainActor in
            self.tracks.remote = nil
            self.theirVideo(false)
        }
    }

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
