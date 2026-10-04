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
    /// How much room WebRTC thinks this link has, in bits per second. It is
    /// what decides whether a picture can stay in the call.
    var outgoingBitrate: Int?
}

enum MediaTrouble: Error {
    case noOffer
    case noAnswer
}

@MainActor final class WebRTCMedia: NSObject, SIPCallMedia {
    /// What the phone found out about the route, every few seconds while a
    /// call is up.
    var onConnection: ((MediaConnection) -> Void)?
    /// The sound dropped, or came back.
    var onTrouble: ((Bool) -> Void)?
    /// Where the sound is coming out, in words, and whether that is the
    /// loudspeaker. Read from the session itself every time it moves, so
    /// the button and the label follow the sound rather than the other way
    /// round.
    var onRoute: ((String, Bool) -> Void)?
    /// The sound wouldn't move where the person asked.
    var onRouteTrouble: ((String) -> Void)?

    /// Whose camera is on, and the pictures themselves (docs/PHASE2.md §7).
    private(set) var video = CallVideo()
    let tracks = VideoTracks()
    var onVideoChanged: ((CallVideo) -> Void)?
    /// The link can't carry a picture any more: turn this phone's camera off
    /// and tell the other side, which is the user agent's job, not this
    /// one's.
    var onVideoTooExpensive: (() -> Void)?

    private let turn: PhoneLine.Turn?
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
    private static let gatherLimit: Duration = .seconds(3)
    private static let afterRelayCandidate: Duration = .milliseconds(300)
    private static let readRouteEvery: Duration = .seconds(5)
    /// Room enough for the voice, the overhead and a thin picture. Below it
    /// the picture is what gives way.
    private static let tooThinForVideo = 150_000
    private static let tooThinReadings = 3

    init(turn: PhoneLine.Turn?) {
        self.turn = turn
        super.init()
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
        if !on { tracks.remote = nil }
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

    /// Where the sound is actually coming out, in the person's words.
    nonisolated static func routeName(_ route: AVAudioSessionRouteDescription) -> String {
        guard let output = route.outputs.first else { return "No sound" }
        switch output.portType {
        case .builtInSpeaker: return "Speaker"
        case .builtInReceiver: return "Earpiece"
        case .headphones, .headsetMic: return "Headphones"
        case .bluetoothA2DP, .bluetoothHFP, .bluetoothLE: return output.portName
        case .carAudio: return "Car"
        case .airPlay: return "AirPlay"
        default: return output.portName
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
        ) { _ in
            MainActor.assumeIsolated { Self.tellTheRoute() }
        }
    }

    /// The one place the app finds out where the sound is: read from the
    /// session, never guessed.
    private static func tellTheRoute() {
        let route = AVAudioSession.sharedInstance().currentRoute
        live?.onRoute?(routeName(route), onTheSpeaker)
    }

    /// The call whose sound is up, so a route change can be told to it.
    /// One call at a time, as everywhere else here.
    private static weak var live: WebRTCMedia?

    // MARK: - The peer connection

    private func start() throws -> RTCPeerConnection {
        if let connection { return connection }
        Self.live = self
        Self.prepareAudioSession()
        watchTheRoute()
        let configuration = RTCConfiguration()
        if let turn, !turn.urls.isEmpty {
            // A direct route first, then Linx's relay over UDP, then over TLS
            // on 443 — which is the one that works on a network that blocks
            // everything else (docs/WEB.md §6).
            configuration.iceServers = [
                RTCIceServer(urlStrings: turn.urls, username: turn.username, credential: turn.credential)
            ]
        }
        configuration.sdpSemantics = .unifiedPlan
        // Not "maxBundle": Asterisk's offers carry no BUNDLE group, and a
        // call is one stream of sound, so nothing is lost by it.
        configuration.rtcpMuxPolicy = .require
        configuration.continualGatheringPolicy = .gatherOnce
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
        var pairID: String?
        for (_, statistic) in report.statistics {
            switch statistic.type {
            case "transport":
                pairID = statistic.values["selectedCandidatePairId"] as? String ?? pairID
            case "inbound-rtp" where statistic.values["kind"] as? String == "audio":
                audioBytesIn += (statistic.values["bytesReceived"] as? NSNumber)?.intValue ?? 0
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
            audioBytesIn: audioBytesIn, outgoingBitrate: room)
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
        // A candidate through Linx's relay means every route worth trying is
        // in hand: send the offer a moment later rather than waiting out the
        // whole gathering (the browser does the same).
        guard candidate.sdp.contains(" typ relay") else { return }
        Task { @MainActor in
            try? await Task.sleep(for: Self.afterRelayCandidate)
            self.doneGathering()
        }
    }

    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didChange state: RTCIceConnectionState
    ) {
        let trouble: Bool?
        switch state {
        case .connected, .completed: trouble = false
        case .disconnected, .failed: trouble = true
        default: trouble = nil
        }
        guard let trouble else { return }
        Task { @MainActor in self.onTrouble?(trouble) }
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
