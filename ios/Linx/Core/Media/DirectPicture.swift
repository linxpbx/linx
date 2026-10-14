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
    /// The pairing it belongs to (`CallLinkMessage.session`).
    var session: UInt64?
    /// The sender's picture goes through the relay over TCP or TLS
    /// (`DirectPicture.overTCP`), so the other side keeps its own small too.
    var slowRoute: Bool?
}

/// One message from the server.
struct CallLinkMessage: Decodable, Sendable {
    var type: String
    var video: Bool?
    var polite: Bool?
    /// "peer": this pairing of the two ends; what is sent is stamped with it.
    var session: UInt64?
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
    /// Who says the relay's TLS certificate is good: iOS, as for the call's
    /// sound (`RelayCertificates`). Without it a picture between two phones
    /// on mobile data had no route at all where only TLS reached the relay
    /// (2026-10-13), while the sound beside it was fine.
    private let verifier: RTCSSLCertificateVerifier?
    private var connection: RTCPeerConnection?
    private var camera: Camera?
    private var polite = false
    private var makingOffer = false
    private var ignoreOffer = false
    /// Their frames on earlier connections of this call's link.
    private var framesBefore = 0
    /// This pairing of the two ends, from the server's "peer" (`calllink`).
    private var session: UInt64?
    /// Their description is being used (`signalled`): no offer of this side's now.
    private var usingTheirs = false
    private var wantCamera: Bool
    private var hd = false
    private var stopped = false
    private var linkTries = 0
    private static let linkTriesAllowed = 15
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
        tracks: VideoTracks, cameraFirst: Bool, verifier: RTCSSLCertificateVerifier? = nil
    ) {
        self.link = link
        self.configuration = configuration
        self.factory = factory
        self.verifier = verifier
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
        // The connection is made now, while the link opens, so its route
        // through the relay (a TLS handshake and an allocation, seconds on
        // a slow link) is ready by the time the other side is.
        connect()
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
        let camera = self.camera ?? Camera(factory: factory, quality: quality)
        try await camera.start()
        guard !stopped, self.connection === connection, wantCamera else {
            camera.stop()
            return
        }
        self.camera = camera
        if polite, connection.remoteDescription == nil, !waitedForTheirOffer {
            // The polite side lets the other offer first, and its camera
            // goes in their offer's slot (`carryMyCamera`): offering at the
            // same moment costs a round of taking one back, seconds on a
            // slow link. If no offer comes (their camera is off), it offers
            // after all.
            tracks.local = camera.track
            onMark?("Camera on (waiting for their offer)")
            said(mine: true)
            Task { [weak self] in
                try? await Task.sleep(for: Self.waitForTheirOffer)
                guard let self, self.connection === connection, connection.remoteDescription == nil,
                    let camera = self.camera, !self.stopped
                else { return }
                self.waitedForTheirOffer = true
                self.addMyCamera(camera, to: connection)
            }
            return
        }
        addMyCamera(camera, to: connection)
        tracks.local = camera.track
        onMark?("Camera on")
        said(mine: true)
    }

    /// How long the polite side waits for the other side's offer before
    /// offering its own picture.
    static let waitForTheirOffer: Duration = .milliseconds(1500)
    private var waitedForTheirOffer = false

    private func addMyCamera(_ camera: Camera, to connection: RTCPeerConnection) {
        if let transceiver = connection.transceivers.first(where: { $0.mediaType == .video }) {
            transceiver.sender.track = camera.track
            transceiver.setDirection(.sendRecv, error: nil)
        } else {
            let setup = RTCRtpTransceiverInit()
            setup.direction = .sendRecv
            setup.streamIds = ["linx"]
            connection.addTransceiver(with: camera.track, init: setup)
        }
        capWhatThisPhoneSends()
    }

    func switchCamera() { camera?.flip() }

    func useHD(_ on: Bool) {
        guard hd != on else { return }
        hd = on
        camera?.use(quality)
        capWhatThisPhoneSends()
        onMark?(on ? "HD on" : "HD off")
    }

    func stop() {
        guard !stopped else { return }
        stopped = true
        onMark?("Their picture: \(framesBefore + framesMost) frames")
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
            session = message.session
            linkTries = 0
            // A connection nothing has been said on yet is kept (made at
            // `start`, its relay route already found); otherwise this is a
            // new pairing and it starts afresh.
            if connection == nil || connection?.localDescription != nil || connection?.remoteDescription != nil {
                connect()
            }
            onMark?("Picture link ready")
            said(available: true)
            if wantCamera { try? await setCamera(true) }
        case "peer-gone":
            // The other side's link went: it reconnects, or the call is
            // ending. A new "peer" starts afresh.
            closeConnection()
            said(mine: false, theirs: false, theirPicture: false)
        case "signal":
            // From an earlier pairing of the two ends: its connection is gone.
            if let theirs = message.data?.session, let session, theirs != session { return }
            if let signal = message.data { await signalled(signal) }
        case "move":
            if let room = message.room, let token = message.token, let url = message.url {
                onMove?(GroupJoin(room: room, token: token, url: url))
            }
        default:
            break
        }
    }

    /// The link closed or was refused. Linx refuses it until it has seen
    /// the answer, which on a phone just woken on mobile data can take
    /// seconds to arrive (2026-10-13: 7 s), so it is tried again while the
    /// call lasts, every two seconds for half a minute.
    private func linkClosed() {
        guard !stopped else { return }
        linkTries += 1
        guard linkTries <= Self.linkTriesAllowed else {
            onMark?("Picture link couldn't be opened")
            if video.available == nil { said(available: false) }
            return
        }
        if linkTries == 1 { onMark?("Picture link not ready yet; trying again") }
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard let self, !self.stopped else { return }
            self.link.start()
        }
    }

    private func send(_ signal: PictureSignal) {
        struct Out: Encodable {
            var type = "signal"
            var data: PictureSignal
        }
        var signal = signal
        signal.session = session
        guard let data = try? JSONEncoder().encode(Out(data: signal)) else { return }
        link.send(data)
    }

    private func connect() {
        closeConnection()
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        configuration.iceCandidatePoolSize = 1
        connection = factory.peerConnection(
            with: configuration, constraints: constraints, verifier: verifier, delegate: self)
        routeNow = nil
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
        framesBefore += framesMost
        framesMost = 0
        framesSeen = 0
        squeezed = nil
        roomyReadings = 0
        overTCP = false
        theirsOverTCP = false
        waitedForTheirOffer = false
        quietReadings = 0
        // Nothing is being sent or shown on a link that is gone: a new one
        // starts from nothing, and the camera goes on again if it is
        // wanted (it stayed off after the link came back, 2026-10-13).
        if video.mine || video.theirs || video.theirPicture {
            said(mine: false, theirs: false, theirPicture: false)
        }
    }

    private func negotiate(_ connection: RTCPeerConnection) async {
        // Not while their description is being used, and only from a
        // settled state: WebRTC asks again once it is back there if there is
        // still something to say. Offering in between is what refused their
        // offer after this side had given way.
        guard connection === self.connection, !usingTheirs, connection.signalingState == .stable else { return }
        makingOffer = true
        defer { makingOffer = false }
        let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            connection.setLocalDescriptionWithCompletionHandler { error in done.resume(returning: error == nil) }
        }
        guard ok, let local = connection.localDescription else {
            onMark?("Picture offer couldn't be made")
            return
        }
        onMark?("Picture \(RTCSessionDescription.string(for: local.type)) sent")
        send(PictureSignal(description: .init(type: RTCSessionDescription.string(for: local.type), sdp: local.sdp)))
    }

    private func signalled(_ signal: PictureSignal) async {
        guard let connection else { return }
        if let slow = signal.slowRoute, slow != theirsOverTCP {
            theirsOverTCP = slow
            onMark?(
                slow
                    ? "Their picture is over TCP: mine kept at \(Self.overTCPStep.height)p" : "Their picture is off TCP"
            )
            useQuality()
        }
        if let description = signal.description {
            usingTheirs = true
            defer { usingTheirs = false }
            let type = RTCSessionDescription.type(for: description.type)
            let collision = type == .offer && (makingOffer || connection.signalingState != .stable)
            ignoreOffer = !polite && collision
            onMark?("Picture \(description.type) arrived\(ignoreOffer ? " (ignored: both offered)" : "")")
            if ignoreOffer { return }
            if collision, connection.signalingState == .haveLocalOffer {
                // The polite side gives way: its own offer is taken back
                // before theirs is used. A browser does this by itself when
                // an offer arrives; WebRTC for iOS does not, and without it
                // their offer was refused and neither picture ever came
                // (both cameras go on together when a video call is
                // answered, so this was every app-to-app video call).
                let rolledBack = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                    connection.setLocalDescription(RTCSessionDescription(type: .rollback, sdp: "")) { error in
                        done.resume(returning: error == nil)
                    }
                }
                onMark?(rolledBack ? "Picture: gave way to their offer" : "Picture: couldn't take my offer back")
            }
            let remote = RTCSessionDescription(type: type, sdp: description.sdp)
            let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                connection.setRemoteDescription(remote) { error in done.resume(returning: error == nil) }
            }
            guard ok, connection === self.connection else {
                onMark?("Picture \(description.type) refused")
                connection.restartIce()
                return
            }
            if type == .offer { carryMyCamera(on: connection) }
            if type == .offer {
                let answered = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                    connection.setLocalDescriptionWithCompletionHandler { error in
                        done.resume(returning: error == nil)
                    }
                }
                if answered, let local = connection.localDescription {
                    onMark?("Picture answer sent")
                    send(
                        PictureSignal(
                            description: .init(type: RTCSessionDescription.string(for: local.type), sdp: local.sdp)))
                }
            }
            capWhatThisPhoneSends()
            readTheirDirection()
            offerIfStillNeeded(connection)
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

    /// Their offer has a slot for the picture; this phone's camera goes in
    /// it, so the answer sends as well as receives. Its own slot, made for
    /// an offer it then took back, would otherwise keep the camera and the
    /// other side would never be told about it: a browser reuses its slot by
    /// itself, WebRTC for iOS doesn't (one side's picture never came,
    /// 2026-10-13, on the phone bench).
    private func carryMyCamera(on connection: RTCPeerConnection) {
        guard let camera, wantCamera else { return }
        let video = connection.transceivers.filter { $0.mediaType == .video && !$0.isStopped }
        guard let theirs = video.first(where: { !$0.mid.isEmpty }), theirs.sender.track == nil else { return }
        for orphan in video where orphan !== theirs && orphan.mid.isEmpty {
            orphan.sender.track = nil
            orphan.stopInternal()
        }
        theirs.sender.track = camera.track
        theirs.setDirection(.sendRecv, error: nil)
    }

    /// WebRTC asks for a new offer while their description is being used,
    /// when this side may not make one (`negotiate`); it doesn't always ask
    /// again afterwards, so this side looks for itself once it may.
    private func offerIfStillNeeded(_ connection: RTCPeerConnection) {
        guard connection === self.connection, connection.signalingState == .stable else { return }
        let unsaid = connection.transceivers.contains {
            $0.mediaType == .video && !$0.isStopped && $0.sender.track != nil && $0.mid.isEmpty
        }
        guard unsaid else { return }
        Task { [weak self] in await self?.negotiate(connection) }
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

    /// What the picture may use now: 360p where either side's picture
    /// goes through the relay over TCP or TLS (`overTCP`), else the usual.
    private var quality: VideoQuality {
        guard let squeezed, squeezed.rawValue < unsqueezed.rawValue else { return unsqueezed }
        return squeezed
    }

    /// Over TCP — the relay on 443, which is the only way through where UDP
    /// is blocked (China) — a lost packet holds up everything behind it,
    /// and 540p over a link with 300 ms and 1% loss built up more than a
    /// second of delay until the camera switched itself off (the phone
    /// bench, 2026-10-13). Video apps send less on such a route; 360p at
    /// 350 kbit/s fits it.
    static let overTCPStep = VideoQuality.low
    private var overTCP = false
    private var theirsOverTCP = false

    /// The picture's route is known, or has changed: a TCP or TLS relay
    /// keeps it small, and the other side is told so it does the same. On a
    /// lossy link the first route to answer can be the relay over TLS even
    /// where UDP works, and WebRTC moves to the better one later: the size
    /// follows it back up (the phone bench, 2026-10-13).
    private func routeFound(_ route: String) {
        let tcp = route.hasPrefix("relay/tls") || route.hasPrefix("relay/tcp")
        guard tcp != overTCP else { return }
        overTCP = tcp
        onMark?(
            tcp
                ? "Picture over TCP: kept at \(Self.overTCPStep.height)p"
                : "Picture off TCP: back to \(unsqueezed.height)p")
        send(PictureSignal(slowRoute: tcp))
        useQuality()
    }

    /// The picture's route as last read.
    private var routeNow: String?

    private func useQuality() {
        camera?.use(quality)
        capWhatThisPhoneSends()
    }

    private func capWhatThisPhoneSends() {
        let most = quality.bitrate
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
        if let route = Self.route(in: report), route != routeNow {
            onMark?(routeNow == nil ? "Picture route: \(route)" : "Picture route now: \(route)")
            routeNow = route
            routeFound(route)
        }
        for statistic in report.statistics.values {
            if statistic.type == "outbound-rtp", statistic.values["kind"] as? String == "video",
                let height = (statistic.values["frameHeight"] as? NSNumber)?.intValue, height > 0
            {
                sending(height)
            }
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
        if framesSeen == 0, frames > 0 { onMark?("Their picture arrived") }
        framesSeen = frames
        framesMost = max(framesMost, frames)
        if video.mine, let room { follow(room: room) }
    }

    /// A thin link takes the picture down a step at a time — 360p, then
    /// 240p — and only a link too thin even for that switches the camera
    /// off, so the sound keeps what it needs. Switching off at the first
    /// thin stretch lost the picture on a slow TLS route that could carry a
    /// small one (the phone bench, China on a poor link, 2026-10-13). Room
    /// to spare for a while brings it back up a step.
    private func follow(room: Int) {
        if room < Self.tooThin {
            roomyReadings = 0
            thinReadings += 1
            guard thinReadings >= Self.thinReadingsAllowed else { return }
            thinReadings = 0
            let now = quality
            if let smaller = VideoQuality(rawValue: now.rawValue - 1), now != .thin {
                squeezed = smaller
                onMark?("Picture down to \(smaller.height)p: the link is thin")
                useQuality()
            } else if room < Self.tooThinEvenSmall {
                onTooExpensive?()
            }
            return
        }
        thinReadings = 0
        guard let squeezed, let bigger = VideoQuality(rawValue: squeezed.rawValue + 1),
            room > bigger.bitrate * 3 / 2
        else {
            roomyReadings = 0
            return
        }
        roomyReadings += 1
        guard roomyReadings >= Self.roomyReadingsWanted else { return }
        roomyReadings = 0
        self.squeezed = bigger.rawValue >= unsqueezed.rawValue ? nil : bigger
        onMark?("Picture back up to \(quality.height)p")
        useQuality()
    }

    /// The size this phone's picture is really going out at, which is what
    /// WebRTC makes of the step asked for and the link it finds: **HD** asks
    /// for 720p, and only this says whether it got there (owner, 2026-10-14:
    /// "clicking on HD doesn't change anything"). Marked when it settles on a
    /// new size, two readings running, so a size passed through on the way
    /// up isn't written down.
    private func sending(_ height: Int) {
        guard height != sendingHeight else {
            sendingCandidate = nil
            return
        }
        guard sendingCandidate == height else {
            sendingCandidate = height
            return
        }
        sendingCandidate = nil
        sendingHeight = height
        sizeMarks += 1
        if sizeMarks <= Self.mostSizeMarks { onMark?("Sending \(height)p") }
        onSending?(height)
    }

    private var sendingHeight = 0
    private var sendingCandidate: Int?
    private var sizeMarks = 0
    /// A call that wanders between sizes all its life fills its report with
    /// nothing else; the first few say what is needed.
    private static let mostSizeMarks = 12
    /// The size this phone's picture is going out at, for Call details.
    var onSending: ((Int) -> Void)?

    /// The step before any squeezing.
    private var unsqueezed: VideoQuality {
        if overTCP || theirsOverTCP { return Self.overTCPStep }
        return hd ? Self.withHD : Self.standard
    }

    /// The step a thin link has taken the picture down to, if any.
    private var squeezed: VideoQuality?
    private var roomyReadings = 0
    /// Below this even the smallest step goes off (bits a second).
    private static let tooThinEvenSmall = 30_000
    /// Readings (two seconds apart) with room to spare before a step up.
    private static let roomyReadingsWanted = 5
    /// The most of their frames decoded on this connection.
    private var framesMost = 0

    /// The picture's route in the call's own words ("relay/udp → relay/udp"),
    /// once it has one.
    private static func route(in report: RTCStatisticsReport) -> String? {
        let all = report.statistics
        guard
            let pair = all.values.first(where: {
                $0.type == "candidate-pair" && $0.values["nominated"] as? Bool == true
                    && $0.values["state"] as? String == "succeeded"
            }),
            let localID = pair.values["localCandidateId"] as? String,
            let remoteID = pair.values["remoteCandidateId"] as? String,
            let local = all[localID], let remote = all[remoteID]
        else { return nil }
        func words(_ candidate: RTCStatistics) -> String {
            let type = candidate.values["candidateType"] as? String ?? "?"
            let way = candidate.values["relayProtocol"] as? String ?? candidate.values["protocol"] as? String ?? "?"
            return "\(type)/\(way)"
        }
        return "\(words(local)) → \(words(remote))"
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
        if newState == .checking {
            Task { @MainActor [weak self] in
                guard let self, connection === self.connection else { return }
                self.onMark?("Picture looking for a route")
            }
        }
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

    /// Opens the link; again after it closed, as a new connection.
    func start() {
        task?.cancel(with: .goingAway, reason: nil)
        session?.invalidateAndCancel()
        closed = false
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
        guard let task else { return }
        task.receive { [weak self] result in
            Task { @MainActor in
                // A connection replaced by a newer one says nothing more.
                guard let self, !self.closed, task === self.task else { return }
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
