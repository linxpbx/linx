import Foundation
@preconcurrency import WebRTC

// A call's picture (ADR-087, docs/VIDEO_CALLS.md §3): between two Linx
// devices it travels on a direct link between them, never through the phone
// system, which carries the sound only. The two find each other on the call
// link (GET /api/v1/call-link), a websocket the server answers only for this
// phone's own call and only with the device it is talking to; what they say
// to each other passes through it unread.
//
// The link is WebRTC's "perfect negotiation", the same as the browser's
// (web/src/phone/picture.ts): either side may turn its camera on or off at
// any time, and when both change something at once the polite side (the
// server says which) gives way. Turning the camera on is adding a track to a
// connection already there — a moment, and nothing else in the call notices:
// no re-INVITE, no question for the switch.
//
// It uses the call's own WebRTC factory, so there is one video engine and
// the picture's connection never touches the audio session CallKit owns.

/// The call link's websocket, as the picture needs it. The real one is
/// `CallLinkSocket`; the tests use one that answers from a script.
@MainActor protocol PictureLink: AnyObject {
    var onMessage: ((Data) -> Void)? { get set }
    var onClose: (() -> Void)? { get set }
    func start()
    func send(_ data: Data)
    func stop()
}

/// What goes over the link, both ways: a description or a route, exactly
/// as the browser sends them.
struct PictureSignal: Codable, Equatable, Sendable {
    struct Description: Codable, Equatable, Sendable {
        var type: String
        var sdp: String
    }
    struct Candidate: Codable, Equatable, Sendable {
        var candidate: String
        var sdpMid: String?
        var sdpMLineIndex: Int32?
    }
    var description: Description?
    var candidate: Candidate?
}

/// One message from the server.
struct CallLinkMessage: Decodable, Sendable {
    var type: String
    var video: Bool?
    var polite: Bool?
    var data: PictureSignal?
    /// "move": the call became a group call, and this is the way in.
    var room: String?
    var token: String?
    var url: String?
}

@MainActor final class DirectPicture: NSObject {
    private(set) var video = CallVideo()
    let tracks: VideoTracks
    var onVideoChanged: ((CallVideo) -> Void)?
    /// The link has too little room for a picture: the person's camera
    /// should go off (the call's sound comes first).
    var onTooExpensive: (() -> Void)?
    /// A step for the call's timeline (Call details).
    var onMark: ((String) -> Void)?
    /// The call has become a group call (ADR-088): the other side added
    /// someone, and this is how to join.
    var onMove: ((GroupJoin) -> Void)?

    private let link: any PictureLink
    private let configuration: RTCConfiguration
    private let factory: RTCPeerConnectionFactory
    private var connection: RTCPeerConnection?
    private var camera: Camera?
    private var polite = false
    private var makingOffer = false
    private var ignoreOffer = false
    private var wantCamera: Bool
    private var hd = false
    private var stopped = false
    private var watch: Task<Void, Never>?
    private var framesSeen = 0
    private var quietReadings = 0
    private var thinReadings = 0

    /// Room enough for the voice and a thin picture; below it for three
    /// readings the camera goes off (ADR-079's rule, unchanged).
    private static let tooThin = 150_000
    private static let thinReadingsAllowed = 3
    /// What the picture may use: 540p normally, 720p with **HD** on
    /// (ADR-081's top two steps). WebRTC's own congestion control does the
    /// adapting below that, end to end.
    private static let standard = VideoQuality.high
    private static let withHD = VideoQuality.hd

    init(
        link: any PictureLink, configuration: RTCConfiguration, factory: RTCPeerConnectionFactory,
        tracks: VideoTracks, cameraFirst: Bool
    ) {
        self.link = link
        self.configuration = configuration
        self.factory = factory
        self.tracks = tracks
        self.wantCamera = cameraFirst
        super.init()
        configuration.bundlePolicy = .maxBundle
        configuration.rtcpMuxPolicy = .require
        configuration.sdpSemantics = .unifiedPlan
        link.onMessage = { [weak self] data in Task { await self?.received(data) } }
        link.onClose = { [weak self] in self?.linkClosed() }
    }

    func start() {
        link.start()
    }

    var mirrored: Bool { camera?.mirrored ?? true }

    /// Turns this phone's camera on or off. Off always happens at once; on
    /// waits for the other side to be there.
    func setCamera(_ on: Bool) async throws {
        wantCamera = on
        guard on else {
            stopCamera()
            return
        }
        guard let connection, !video.mine else { return }
        let camera = self.camera ?? Camera(factory: factory, quality: hd ? Self.withHD : Self.standard)
        try await camera.start()
        guard !stopped, self.connection === connection, wantCamera else {
            camera.stop()
            return
        }
        self.camera = camera
        if let transceiver = connection.transceivers.first(where: { $0.mediaType == .video }) {
            transceiver.sender.track = camera.track
            transceiver.setDirection(.sendRecv, error: nil)
        } else {
            let setup = RTCRtpTransceiverInit()
            setup.direction = .sendRecv
            setup.streamIds = ["linx"]
            connection.addTransceiver(with: camera.track, init: setup)
        }
        tracks.local = camera.track
        capWhatThisPhoneSends()
        onMark?("Camera on")
        said(mine: true)
    }

    func switchCamera() { camera?.flip() }

    func useHD(_ on: Bool) {
        guard hd != on else { return }
        hd = on
        camera?.use(on ? Self.withHD : Self.standard)
        capWhatThisPhoneSends()
        onMark?(on ? "HD on" : "HD off")
    }

    func stop() {
        guard !stopped else { return }
        stopped = true
        watch?.cancel()
        watch = nil
        camera?.stop()
        camera = nil
        tracks.local = nil
        tracks.remote = nil
        connection?.close()
        connection = nil
        link.stop()
    }

    // MARK: - The link

    private func received(_ data: Data) async {
        guard !stopped, let message = try? JSONDecoder().decode(CallLinkMessage.self, from: data) else { return }
        switch message.type {
        case "peer":
            guard message.video == true else {
                onMark?("Their phone can't show a picture")
                said(available: false)
                return
            }
            polite = message.polite ?? false
            connect()
            onMark?("Picture link ready")
            said(available: true)
            if wantCamera { try? await setCamera(true) }
        case "peer-gone":
            // The other side's link went: it reconnects, or the call is
            // ending. A new "peer" starts afresh.
            closeConnection()
            said(mine: false, theirs: false, theirPicture: false)
        case "signal":
            if let signal = message.data { await signalled(signal) }
        case "move":
            if let room = message.room, let token = message.token, let url = message.url {
                onMove?(GroupJoin(room: room, token: token, url: url))
            }
        default:
            break
        }
    }

    private func linkClosed() {
        guard !stopped else { return }
        if video.available == nil { said(available: false) }
    }

    private func send(_ signal: PictureSignal) {
        struct Out: Encodable {
            var type = "signal"
            var data: PictureSignal
        }
        guard let data = try? JSONEncoder().encode(Out(data: signal)) else { return }
        link.send(data)
    }

    private func connect() {
        closeConnection()
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        connection = factory.peerConnection(with: configuration, constraints: constraints, delegate: self)
        startWatching()
    }

    private func closeConnection() {
        watch?.cancel()
        watch = nil
        camera?.stop()
        camera = nil
        tracks.local = nil
        tracks.remote = nil
        connection?.close()
        connection = nil
        makingOffer = false
        ignoreOffer = false
    }

    private func negotiate(_ connection: RTCPeerConnection) async {
        guard connection === self.connection else { return }
        makingOffer = true
        defer { makingOffer = false }
        let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            connection.setLocalDescriptionWithCompletionHandler { error in done.resume(returning: error == nil) }
        }
        guard ok, let local = connection.localDescription else { return }
        send(PictureSignal(description: .init(type: RTCSessionDescription.string(for: local.type), sdp: local.sdp)))
    }

    private func signalled(_ signal: PictureSignal) async {
        guard let connection else { return }
        if let description = signal.description {
            let type = RTCSessionDescription.type(for: description.type)
            let collision = type == .offer && (makingOffer || connection.signalingState != .stable)
            ignoreOffer = !polite && collision
            if ignoreOffer { return }
            let remote = RTCSessionDescription(type: type, sdp: description.sdp)
            let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                connection.setRemoteDescription(remote) { error in done.resume(returning: error == nil) }
            }
            guard ok, connection === self.connection else {
                connection.restartIce()
                return
            }
            if type == .offer {
                let answered = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                    connection.setLocalDescriptionWithCompletionHandler { error in
                        done.resume(returning: error == nil)
                    }
                }
                if answered, let local = connection.localDescription {
                    send(
                        PictureSignal(
                            description: .init(type: RTCSessionDescription.string(for: local.type), sdp: local.sdp)))
                }
            }
            capWhatThisPhoneSends()
            readTheirDirection()
        } else if let candidate = signal.candidate {
            let ice = RTCIceCandidate(
                sdp: candidate.candidate, sdpMLineIndex: candidate.sdpMLineIndex ?? 0, sdpMid: candidate.sdpMid)
            connection.add(ice) { _ in
                // A route for an offer this side ignored is refused, which
                // is expected; any other is out of step and the next change
                // puts it right.
            }
        }
    }

    // MARK: - The pictures

    /// Whether they say they are sending: their side of the agreed
    /// direction of the picture.
    private func readTheirDirection() {
        guard let connection else { return }
        var theirs = false
        for transceiver in connection.transceivers where transceiver.mediaType == .video {
            var current = RTCRtpTransceiverDirection.inactive
            if transceiver.currentDirection(&current), current == .sendRecv || current == .recvOnly {
                theirs = true
                tracks.remote = transceiver.receiver.track as? RTCVideoTrack
            }
        }
        if !theirs {
            framesSeen = 0
            quietReadings = 0
        }
        said(theirs: theirs, theirPicture: theirs && video.theirPicture)
    }

    private func stopCamera() {
        guard let connection else {
            camera?.stop()
            camera = nil
            tracks.local = nil
            said(mine: false)
            return
        }
        for transceiver in connection.transceivers
        where transceiver.mediaType == .video && transceiver.sender.track != nil {
            transceiver.sender.track = nil
            transceiver.setDirection(.recvOnly, error: nil)
        }
        camera?.stop()
        camera = nil
        tracks.local = nil
        thinReadings = 0
        if video.mine { onMark?("Camera off") }
        said(mine: false)
    }

    private func capWhatThisPhoneSends() {
        let most = (hd ? Self.withHD : Self.standard).bitrate
        for sender in connection?.senders ?? [] where sender.track?.kind == kRTCMediaStreamTrackKindVideo {
            let parameters = sender.parameters
            for encoding in parameters.encodings { encoding.maxBitrateBps = NSNumber(value: most) }
            parameters.degradationPreference = NSNumber(value: RTCDegradationPreference.balanced.rawValue)
            sender.parameters = parameters
        }
    }

    /// Every two seconds while the link is up: whether their frames are
    /// arriving, and whether there is room for this phone's.
    private func startWatching() {
        watch?.cancel()
        watch = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                guard let self, !Task.isCancelled, let connection = self.connection else { return }
                let report = await withCheckedContinuation { (done: CheckedContinuation<RTCStatisticsReport, Never>) in
                    connection.statistics { done.resume(returning: $0) }
                }
                self.read(report)
            }
        }
    }

    private func read(_ report: RTCStatisticsReport) {
        var frames = 0
        var room: Int?
        for statistic in report.statistics.values {
            if statistic.type == "inbound-rtp", statistic.values["kind"] as? String == "video" {
                frames += (statistic.values["framesDecoded"] as? NSNumber)?.intValue ?? 0
            }
            if statistic.type == "candidate-pair", statistic.values["nominated"] as? Bool == true,
                let available = (statistic.values["availableOutgoingBitrate"] as? NSNumber)?.intValue
            {
                room = available
            }
        }
        if video.theirs {
            if frames > framesSeen {
                quietReadings = 0
                said(theirPicture: true)
            } else {
                quietReadings += 1
                if quietReadings >= 2 { said(theirPicture: false) }
            }
        }
        framesSeen = frames
        if video.mine, let room, room < Self.tooThin {
            thinReadings += 1
            if thinReadings >= Self.thinReadingsAllowed {
                thinReadings = 0
                onTooExpensive?()
            }
        } else {
            thinReadings = 0
        }
    }

    private func said(
        available: Bool? = nil, mine: Bool? = nil, theirs: Bool? = nil, theirPicture: Bool? = nil
    ) {
        var now = video
        if let available { now.available = available }
        if let mine { now.mine = mine }
        if let theirs { now.theirs = theirs }
        if let theirPicture { now.theirPicture = theirPicture && now.theirs }
        guard now != video else { return }
        video = now
        onVideoChanged?(now)
    }
}

extension DirectPicture: RTCPeerConnectionDelegate {
    nonisolated func peerConnectionShouldNegotiate(_ connection: RTCPeerConnection) {
        Task { @MainActor [weak self] in await self?.negotiate(connection) }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        let signal = PictureSignal(
            candidate: .init(
                candidate: candidate.sdp, sdpMid: candidate.sdpMid, sdpMLineIndex: candidate.sdpMLineIndex))
        Task { @MainActor [weak self] in
            guard let self, connection === self.connection else { return }
            self.send(signal)
        }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        guard newState == .failed else { return }
        Task { @MainActor [weak self] in
            guard let self, connection === self.connection else { return }
            self.onMark?("Picture route lost; looking again")
            connection.restartIce()
        }
    }

    nonisolated func peerConnection(
        _ connection: RTCPeerConnection, didStartReceivingOn transceiver: RTCRtpTransceiver
    ) {
        Task { @MainActor [weak self] in
            guard let self, connection === self.connection else { return }
            self.readTheirDirection()
        }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {
        guard stateChanged == .stable else { return }
        Task { @MainActor [weak self] in
            guard let self, connection === self.connection else { return }
            self.readTheirDirection()
        }
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    nonisolated func peerConnection(_ connection: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
}

/// The call link's websocket: wss to this phone's Linx, with its device
/// token, exactly as the /sip relay and the Team list take it.
@MainActor final class CallLinkSocket: NSObject, PictureLink {
    var onMessage: ((Data) -> Void)?
    var onClose: (() -> Void)?

    private let url: URL
    private let token: String
    private var task: URLSessionWebSocketTask?
    private var session: URLSession?
    private var closed = false

    static let subprotocol = "linx.call.v1"

    /// The link for the call whose SIP Call-ID is `callID`, on the server
    /// whose /sip relay is `sip`.
    init?(sip: URL, token: String, callID: String) {
        guard var parts = URLComponents(url: sip, resolvingAgainstBaseURL: false) else { return nil }
        parts.path = "/api/v1/call-link"
        parts.queryItems = [URLQueryItem(name: "call", value: callID)]
        guard let url = parts.url else { return nil }
        self.url = url
        self.token = token
    }

    func start() {
        var request = URLRequest(url: url)
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.setValue(Self.subprotocol, forHTTPHeaderField: "Sec-WebSocket-Protocol")
        request.timeoutInterval = 20
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieAcceptPolicy = .never
        configuration.httpShouldSetCookies = false
        let session = URLSession(configuration: configuration, delegate: nil, delegateQueue: .main)
        self.session = session
        let task = session.webSocketTask(with: request)
        self.task = task
        task.resume()
        receive()
    }

    func send(_ data: Data) {
        task?.send(.string(String(decoding: data, as: UTF8.self))) { _ in }
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
                case .success(.string(let text)):
                    self.onMessage?(Data(text.utf8))
                    self.receive()
                case .success(.data(let data)):
                    self.onMessage?(data)
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
