import Foundation
@preconcurrency import WebRTC

// A group call (ADR-088, docs/VIDEO_CALLS.md §4): Add person moves a call
// between two Linx devices onto Linx's video server, LiveKit, and everyone's
// sound and picture go through it from then on.
//
// The app speaks LiveKit's signalling itself (`LiveKitWire`) on the WebRTC it
// already has, with the call's own factory: one video engine, one audio
// device, and the audio session stays CallKit's. Two connections, as LiveKit
// has them: the **publisher**, where this phone offers its microphone and
// camera, and the **subscriber**, where LiveKit offers everyone else's. Both
// reach the server through Linx's own relay, like the call itself.

/// The way into a group call (POST /api/v1/group-calls, or a "move" on the
/// call link).
struct GroupJoin: Decodable, Equatable, Sendable {
    let room: String
    let token: String
    /// The path on this phone's Linx server where LiveKit's signalling is.
    let url: String
}

/// One person in a group call, as the screen draws them.
struct GroupPerson: Identifiable, Equatable, Sendable {
    let id: String
    var name: String
    var me: Bool
    var video: Bool
    var sound: Bool
    var speaking: Bool
}

/// Everyone in the group call and their pictures, for the screen.
@MainActor @Observable final class GroupPeople {
    enum Phase: Equatable { case joining, joined, ended(String) }
    var phase = Phase.joining
    var people: [GroupPerson] = []
    /// Each person's picture, by their id: live WebRTC tracks, not copies.
    var pictures: [String: RTCVideoTrack] = [:]
    var camera = false
    var muted = false
}

/// The websocket to LiveKit, as the group call needs it. The real one is
/// `LiveKitSocket`; a test can answer from a script.
@MainActor protocol GroupSocket: AnyObject {
    var onMessage: ((Data) -> Void)? { get set }
    var onClose: (() -> Void)? { get set }
    func start()
    func send(_ data: Data)
    func stop()
}

@MainActor final class GroupCall: NSObject {
    let state = GroupPeople()
    /// The call is over for this phone: LiveKit sent it away, or lost it.
    var onEnded: ((String) -> Void)?
    var onMark: ((String) -> Void)?

    private let socket: any GroupSocket
    private let configuration: RTCConfiguration
    private let factory: RTCPeerConnectionFactory
    private var publisher: RTCPeerConnection?
    private var subscriber: RTCPeerConnection?
    private var mySID = ""
    private var others: [String: LiveKitParticipant] = [:]
    private var speaking: Set<String> = []
    private var microphone: RTCAudioTrack?
    private var camera: Camera?
    /// Tracks asked for and the server's id for each, once it says.
    private var published: [String: String] = [:]
    private var waitingToPublish: [String: RTCMediaStreamTrack] = [:]
    private var negotiating = false
    private var negotiateAgain = false
    private var early: [LiveKitTarget: [RTCIceCandidate]] = [:]
    private var ping: Task<Void, Never>?
    private var left = false

    static let microphoneID = "linx-group-mic"
    static let cameraID = "linx-group-camera"

    /// The websocket for `join`, on the Linx server at `server`.
    static func socket(server: URL, join: GroupJoin) -> (any GroupSocket)? {
        guard var parts = URLComponents(url: server, resolvingAgainstBaseURL: false) else { return nil }
        parts.scheme = parts.scheme == "http" ? "ws" : "wss"
        let base = join.url.hasSuffix("/") ? String(join.url.dropLast()) : join.url
        parts.path = base + "/rtc"
        parts.queryItems = [
            URLQueryItem(name: "access_token", value: join.token),
            URLQueryItem(name: "auto_subscribe", value: "1"),
            URLQueryItem(name: "sdk", value: "swift"),
            URLQueryItem(name: "version", value: "2.0.0"),
            URLQueryItem(name: "protocol", value: "12"),
            URLQueryItem(name: "adaptive_stream", value: "0"),
        ]
        guard let url = parts.url else { return nil }
        return LiveKitSocket(url: url)
    }

    init(
        socket: any GroupSocket, configuration: RTCConfiguration, factory: RTCPeerConnectionFactory,
        camera: Bool
    ) {
        self.socket = socket
        self.configuration = configuration
        self.factory = factory
        super.init()
        configuration.bundlePolicy = .maxBundle
        configuration.rtcpMuxPolicy = .require
        configuration.sdpSemantics = .unifiedPlan
        state.camera = camera
        socket.onMessage = { [weak self] data in Task { await self?.received(data) } }
        socket.onClose = { [weak self] in self?.lost() }
    }

    func start() {
        onMark?("Joining the group call")
        socket.start()
    }

    func setMuted(_ muted: Bool) {
        microphone?.isEnabled = !muted
        state.muted = muted
        if let sid = published[Self.microphoneID] { send(.mute(sid: sid, muted: muted)) }
    }

    func setCamera(_ on: Bool) async throws {
        if !on {
            camera?.stop()
            camera?.track.isEnabled = false
            state.camera = false
            if let sid = published[Self.cameraID] { send(.mute(sid: sid, muted: true)) }
            state.pictures[mySID] = nil
            refresh()
            return
        }
        let camera = self.camera ?? Camera(factory: factory, quality: .high)
        try await camera.start()
        camera.track.isEnabled = true
        self.camera = camera
        state.camera = true
        state.pictures[mySID] = camera.track
        if let sid = published[Self.cameraID] {
            send(.mute(sid: sid, muted: false))
        } else {
            publish(camera.track, id: Self.cameraID, type: .video, source: .camera)
        }
        refresh()
    }

    func switchCamera() { camera?.flip() }
    var mirrored: Bool { camera?.mirrored ?? true }

    func leave() {
        guard !left else { return }
        left = true
        send(.leave)
        close()
    }

    // MARK: - The server

    private func send(_ request: LiveKitRequest) {
        socket.send(request.data)
    }

    private func received(_ data: Data) async {
        guard !left, let response = try? LiveKitResponse(data) else { return }
        switch response {
        case .join(let me, let others, _, let interval):
            mySID = me.sid
            for p in others { self.others[p.sid] = p }
            connect()
            keepPinging(every: interval)
            state.phase = .joined
            onMark?("In the group call")
            refresh()
            publishMicrophone()
            if state.camera { try? await setCamera(true) }
        case .answer(let description):
            await setRemote(publisher, description, type: .answer)
            negotiated()
        case .offer(let description):
            await answer(description)
        case .trickle(let trickle):
            add(trickle)
        case .update(let participants):
            for p in participants where p.sid != mySID {
                if p.state == LiveKitParticipant.disconnected {
                    others[p.sid] = nil
                    state.pictures[p.sid] = nil
                } else {
                    others[p.sid] = p
                }
            }
            refresh()
        case .trackPublished(let cid, let track):
            published[cid] = track.sid
            if let local = waitingToPublish.removeValue(forKey: cid) { addToPublisher(local) }
        case .speakers(let sids):
            speaking = Set(sids)
            refresh()
        case .leave:
            end("The group call ended.")
        case .trackUnpublished, .refreshToken, .pong, .other:
            break
        }
    }

    private func lost() {
        guard !left else { return }
        end("The group call dropped.")
    }

    private func end(_ why: String) {
        guard !left else { return }
        left = true
        close()
        state.phase = .ended(why)
        onEnded?(why)
    }

    private func close() {
        ping?.cancel()
        ping = nil
        camera?.stop()
        camera = nil
        microphone = nil
        publisher?.close()
        subscriber?.close()
        publisher = nil
        subscriber = nil
        state.pictures = [:]
        socket.stop()
    }

    private func keepPinging(every seconds: Int) {
        ping?.cancel()
        let interval = max(5, seconds)
        ping = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(interval))
                guard !Task.isCancelled, let self else { return }
                self.send(.ping(timestampMs: Int64(Date().timeIntervalSince1970 * 1000)))
            }
        }
    }

    // MARK: - The two connections

    private func connect() {
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        publisher = factory.peerConnection(with: configuration, constraints: constraints, delegate: self)
        subscriber = factory.peerConnection(with: configuration, constraints: constraints, delegate: self)
    }

    private func publishMicrophone() {
        let source = factory.audioSource(with: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil))
        let track = factory.audioTrack(with: source, trackId: Self.microphoneID)
        track.isEnabled = !state.muted
        microphone = track
        publish(track, id: Self.microphoneID, type: .audio, source: .microphone)
    }

    private func publish(_ track: RTCMediaStreamTrack, id: String, type: LiveKitTrackType, source: LiveKitTrackSource) {
        waitingToPublish[id] = track
        let quality = VideoQuality.high
        send(
            .addTrack(
                cid: id, name: source == .camera ? "camera" : "microphone", type: type, source: source,
                width: type == .video ? quality.width : 0, height: type == .video ? quality.height : 0))
    }

    private func addToPublisher(_ track: RTCMediaStreamTrack) {
        guard let publisher else { return }
        let setup = RTCRtpTransceiverInit()
        setup.direction = .sendOnly
        setup.streamIds = [mySID]
        let transceiver = publisher.addTransceiver(with: track, init: setup)
        if track.kind == kRTCMediaStreamTrackKindVideo, let sender = transceiver?.sender {
            let parameters = sender.parameters
            for encoding in parameters.encodings {
                encoding.maxBitrateBps = NSNumber(value: VideoQuality.high.bitrate)
            }
            parameters.degradationPreference = NSNumber(value: RTCDegradationPreference.balanced.rawValue)
            sender.parameters = parameters
        }
        negotiatePublisher()
    }

    /// The publisher is always this phone's to offer: one change at a time,
    /// and a change asked for meanwhile goes right after.
    private func negotiatePublisher() {
        guard let publisher else { return }
        guard !negotiating else {
            negotiateAgain = true
            return
        }
        negotiating = true
        Task {
            let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
            let sdp = await withCheckedContinuation { (done: CheckedContinuation<String?, Never>) in
                publisher.offer(for: constraints) { description, _ in done.resume(returning: description?.sdp) }
            }
            guard let sdp, publisher === self.publisher else {
                self.negotiated()
                return
            }
            let offer = RTCSessionDescription(type: .offer, sdp: sdp)
            let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                publisher.setLocalDescription(offer) { error in done.resume(returning: error == nil) }
            }
            guard ok else {
                self.negotiated()
                return
            }
            self.send(.offer(LiveKitDescription(type: "offer", sdp: sdp)))
        }
    }

    private func negotiated() {
        negotiating = false
        if negotiateAgain {
            negotiateAgain = false
            negotiatePublisher()
        }
    }

    private func answer(_ offer: LiveKitDescription) async {
        guard let subscriber else { return }
        await setRemote(subscriber, offer, type: .offer)
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        let sdp = await withCheckedContinuation { (done: CheckedContinuation<String?, Never>) in
            subscriber.answer(for: constraints) { description, _ in done.resume(returning: description?.sdp) }
        }
        guard let sdp, subscriber === self.subscriber else { return }
        let answer = RTCSessionDescription(type: .answer, sdp: sdp)
        let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            subscriber.setLocalDescription(answer) { error in done.resume(returning: error == nil) }
        }
        guard ok else { return }
        send(.answer(LiveKitDescription(type: "answer", sdp: sdp, id: offer.id)))
    }

    private func setRemote(_ connection: RTCPeerConnection?, _ description: LiveKitDescription, type: RTCSdpType)
        async
    {
        guard let connection else { return }
        let remote = RTCSessionDescription(type: type, sdp: description.sdp)
        _ = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            connection.setRemoteDescription(remote) { error in done.resume(returning: error == nil) }
        }
        let target: LiveKitTarget = connection === publisher ? .publisher : .subscriber
        for candidate in early.removeValue(forKey: target) ?? [] { connection.add(candidate) { _ in } }
    }

    private func add(_ trickle: LiveKitTrickle) {
        struct Init: Decodable {
            var candidate: String
            var sdpMid: String?
            var sdpMLineIndex: Int32?
        }
        guard let parsed = try? JSONDecoder().decode(Init.self, from: Data(trickle.candidateInit.utf8)),
            !parsed.candidate.isEmpty
        else { return }
        let candidate = RTCIceCandidate(
            sdp: parsed.candidate, sdpMLineIndex: parsed.sdpMLineIndex ?? 0, sdpMid: parsed.sdpMid)
        let connection = trickle.target == .publisher ? publisher : subscriber
        guard let connection else { return }
        guard connection.remoteDescription != nil else {
            early[trickle.target, default: []].append(candidate)
            return
        }
        connection.add(candidate) { _ in }
    }

    private func routeFound(_ candidate: RTCIceCandidate, on connection: RTCPeerConnection) {
        struct Init: Encodable {
            var candidate: String
            var sdpMid: String?
            var sdpMLineIndex: Int32
        }
        guard
            let json = try? JSONEncoder().encode(
                Init(candidate: candidate.sdp, sdpMid: candidate.sdpMid, sdpMLineIndex: candidate.sdpMLineIndex))
        else { return }
        let target: LiveKitTarget = connection === publisher ? .publisher : .subscriber
        send(.trickle(LiveKitTrickle(candidateInit: String(decoding: json, as: UTF8.self), target: target)))
    }

    /// Someone's track arrived on the subscriber: LiveKit names the stream
    /// after the person ("PA_…" or "PA_…|TR_…").
    private func arrived(_ receiver: RTCRtpReceiver, streams: [String]) {
        guard let track = receiver.track as? RTCVideoTrack else { return }
        let owner =
            streams.first.map { String($0.split(separator: "|").first ?? "") }
            ?? others.first { $0.value.tracks.contains { $0.sid == track.trackId } }?.key
        guard let owner, !owner.isEmpty else { return }
        state.pictures[owner] = track
        refresh()
    }

    private func refresh() {
        var people = [
            GroupPerson(
                id: mySID, name: "You", me: true, video: state.camera, sound: !state.muted,
                speaking: speaking.contains(mySID))
        ]
        for p in others.values.sorted(by: { $0.name < $1.name }) {
            let camera = p.tracks.first { $0.source == .camera || $0.type == .video }
            let mic = p.tracks.first { $0.source == .microphone || $0.type == .audio }
            people.append(
                GroupPerson(
                    id: p.sid, name: p.name.isEmpty ? p.identity : p.name, me: false,
                    video: (camera.map { !$0.muted } ?? false) && state.pictures[p.sid] != nil,
                    sound: (mic.map { !$0.muted } ?? false), speaking: speaking.contains(p.sid)))
        }
        state.people = people
    }
}

extension GroupCall: RTCPeerConnectionDelegate {
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        Task { @MainActor [weak self] in self?.routeFound(candidate, on: connection) }
    }

    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didAdd receiver: RTCRtpReceiver, streams: [RTCMediaStream]
    ) {
        let ids = streams.map(\.streamId)
        Task { @MainActor [weak self] in
            guard let self, connection === self.subscriber else { return }
            self.arrived(receiver, streams: ids)
        }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        guard newState == .failed else { return }
        Task { @MainActor [weak self] in self?.onMark?("Group call route lost") }
    }

    nonisolated func peerConnectionShouldNegotiate(_ connection: RTCPeerConnection) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
}

/// LiveKit's websocket, through this phone's own Linx server (/livekit/).
@MainActor final class LiveKitSocket: GroupSocket {
    var onMessage: ((Data) -> Void)?
    var onClose: (() -> Void)?

    private let url: URL
    private var task: URLSessionWebSocketTask?
    private var session: URLSession?
    private var closed = false

    init(url: URL) { self.url = url }

    func start() {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        let session = URLSession(configuration: configuration, delegate: nil, delegateQueue: .main)
        self.session = session
        var request = URLRequest(url: url)
        request.timeoutInterval = 20
        let task = session.webSocketTask(with: request)
        // A room's whole description can be a few tens of kilobytes.
        task.maximumMessageSize = 1 << 20
        self.task = task
        task.resume()
        receive()
    }

    func send(_ data: Data) {
        task?.send(.data(data)) { _ in }
    }

    func stop() {
        closed = true
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
        session?.invalidateAndCancel()
        session = nil
    }

    private func receive() {
        task?.receive { [weak self] result in
            Task { @MainActor in
                guard let self, !self.closed else { return }
                switch result {
                case .success(.data(let data)):
                    self.onMessage?(data)
                    self.receive()
                case .success(.string(let text)):
                    self.onMessage?(Data(text.utf8))
                    self.receive()
                case .success:
                    self.receive()
                case .failure:
                    self.closed = true
                    self.onClose?()
                }
            }
        }
    }
}
