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
    /// About the call's sound on this link (ADR-092, `DirectSound`).
    var sound: SoundWord?

    init(
        description: Description? = nil, candidate: Candidate? = nil, session: UInt64? = nil,
        slowRoute: Bool? = nil, sound: SoundWord? = nil
    ) {
        self.description = description
        self.candidate = candidate
        self.session = session
        self.slowRoute = slowRoute
        self.sound = sound
    }

    /// A word about the sound this version doesn't know (a later one's) is
    /// left out, not the whole message.
    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        description = try c.decodeIfPresent(Description.self, forKey: .description)
        candidate = try c.decodeIfPresent(Candidate.self, forKey: .candidate)
        session = try c.decodeIfPresent(UInt64.self, forKey: .session)
        slowRoute = try c.decodeIfPresent(Bool.self, forKey: .slowRoute)
        sound = try? c.decodeIfPresent(SoundWord.self, forKey: .sound)
    }
}

/// One message from the server.
struct CallLinkMessage: Decodable, Sendable {
    var type: String
    var video: Bool?
    var polite: Bool?
    /// "peer": this pairing of the two ends; what is sent is stamped with it.
    /// "early": the exchange while the call rings, likewise.
    var session: UInt64?
    /// "peer": the pairing goes on from the exchange while the call rang.
    var early: Bool?
    /// "early": this phone made the call, and offers now.
    var offer: Bool?
    var data: PictureSignal?
    /// "peer": how long ago Asterisk answered the call, the moment every
    /// device on it counts its timer from (`answered_ms_ago`).
    var answeredMsAgo: Int64?
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
    /// This phone now plays the other side's sound from this route
    /// (ADR-092): the call's own sound, through Asterisk, is quieted while
    /// it is `.direct`.
    var onHearing: ((SoundRoute) -> Void)?
    /// The other side now plays this phone's sound from this route: what
    /// goes through Asterisk can be quiet while it is `.direct`.
    var onTheyHear: ((SoundRoute) -> Void)?

    private let link: any PictureLink
    /// This phone's microphone on the direct link (ADR-092): a track of its
    /// own from the call's microphone, so each route can be quieted apart.
    /// Silent until the other side says it can take it (`SoundWord.can`).
    private let sound: RTCAudioTrack?
    private var soundRoute = DirectSound()
    private var soundWatch: Task<Void, Never>?
    private var theyCanTakeSound = false
    private var muted = false
    /// The other side's sound on the direct link: off until it is the route
    /// this phone plays, so a word is never heard twice.
    private var theirSound: RTCAudioTrack?
    /// How their sound has arrived on this link, as last read.
    private var soundQuality: SoundQuality?
    /// The last message being handled: the next waits for it.
    private var inbox: Task<Void, Never>?
    private var configuration: RTCConfiguration
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
    /// AV1 goes first in this phone's picture while it keeps up with it;
    /// once it can't, it is left out for the rest of the call (`codecSeen`).
    private var av1Allowed = true
    /// The picture is going out in AV1, at the last reading.
    private var sendingAV1 = false
    private var codecMarked = false
    /// Readings in a row with the encoder held back by the processor.
    private var busyReadings = 0
    private static let busyReadingsAllowed = 3
    private var stopped = false
    private var linkTries = 0
    /// The call has been answered; until then a link Linx refuses (a server
    /// older than holding one while ringing) is tried again without counting.
    private var callUp = true
    private var saidWaiting = false
    private static let linkTriesAllowed = 15
    private var watch: Task<Void, Never>?
    /// The exchange while the call rings (`calllink`'s early.go), if this
    /// connection is having one.
    private var earlySession: UInt64?
    /// Their offer came while this phone rang: it is answered when the
    /// person answers, not before, so nothing of this phone's goes to the
    /// caller until then.
    private var heldOffer = false
    /// Their routes that came before their description, added after it.
    private var earlyRoutes: [RTCIceCandidate] = []
    private var framesSeen = 0
    private var quietReadings = 0
    private var thinReadings = 0
    /// The first route each way has been written in the call's report.
    private var ourRouteMarked = false
    private var theirRouteMarked = false

    /// Room enough for the voice and a thin picture; below it for three
    /// readings the picture goes to its smallest step, and below
    /// `tooThinEvenSmall` there the camera goes off (ADR-079's rule).
    private static let tooThin = 150_000
    private static let thinReadingsAllowed = 3
    /// What the picture may use: 540p normally, 720p with **HD** on
    /// (ADR-081's top two steps). WebRTC's own congestion control does the
    /// adapting below that, end to end.
    private static let standard = VideoQuality.high
    private static let withHD = VideoQuality.hd
    /// On mobile data the picture starts at 480p and grows as the link
    /// shows room: an iPhone just woken on 5G could send 300 kbit/s, then
    /// 30, and a 540p start choked its upload — sound included — for half
    /// a minute (the owner's call, 2026-10-15). **HD** skips the wait.
    nonisolated static let mobileStart = VideoQuality.standard
    private let onMobileData: Bool
    /// The picture is still at `mobileStart`, where it began.
    private var startedSmall: Bool

    init(
        link: any PictureLink, configuration: RTCConfiguration, factory: RTCPeerConnectionFactory,
        tracks: VideoTracks, cameraFirst: Bool, verifier: RTCSSLCertificateVerifier? = nil,
        onMobileData: Bool = false, sound: RTCAudioTrack? = nil
    ) {
        self.link = link
        self.sound = sound
        sound?.isEnabled = false
        self.onMobileData = onMobileData
        squeezed = onMobileData ? Self.mobileStart : nil
        startedSmall = onMobileData
        self.configuration = configuration
        self.factory = factory
        self.verifier = verifier
        self.tracks = tracks
        self.wantCamera = cameraFirst
        super.init()
        configuration.bundlePolicy = .maxBundle
        configuration.rtcpMuxPolicy = .require
        configuration.sdpSemantics = .unifiedPlan
        // One message at a time, in the order they came: handled side by
        // side, an offer could arrive while the answer before it was still
        // being applied, be taken for both sides offering at once and
        // dropped, and the two would wait for each other for ever (the
        // browser suite, 2026-10-18, once the sound joined the link).
        link.onMessage = { [weak self] data in
            guard let self else { return }
            let before = self.inbox
            self.inbox = Task { [weak self] in
                await before?.value
                await self?.received(data)
            }
        }
        link.onClose = { [weak self] in self?.linkClosed() }
    }

    func start(ringing: Bool = false) {
        callUp = !ringing
        // The connection is made now, while the link opens, so its route
        // through the relay (a TLS handshake and an allocation, seconds on
        // a slow link) is ready by the time the other side is.
        connect()
        link.start()
    }

    /// The call was answered. A link opened while it rang is already open
    /// (Linx holds it until now), or still being tried; from here its tries
    /// are counted.
    func callIsUp() {
        guard !callUp else { return }
        callUp = true
        linkTries = 0
        if heldOffer { Task { await answerHeldOffer() } }
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
        if on, startedSmall {
            startedSmall = false
            squeezed = nil
        }
        camera?.use(quality)
        capWhatThisPhoneSends()
        onMark?(on ? "HD on" : "HD off")
    }

    func stop() {
        guard !stopped else { return }
        stopped = true
        onMark?("Their picture: \(framesBefore + framesMost) frames")
        if let freezes = theirFreezes {
            onMark?("Their picture stopped \(freezes.count) times, \(String(format: "%.1f", freezes.seconds)) s in all")
        }
        watch?.cancel()
        watch = nil
        if let soundQuality, soundQuality.packets > 0 { onMark?("Sound on the direct link: \(soundQuality.words)") }
        soundGone("the call's link closed")
        camera?.stop()
        camera = nil
        tracks.local = nil
        tracks.remote = nil
        connection?.close()
        connection = nil
        link.stop()
    }

    /// Mutes this phone's microphone on the direct link too.
    func setMuted(_ on: Bool) {
        muted = on
        sound?.isEnabled = theyCanTakeSound && !muted
    }

    // MARK: - The link

    private func received(_ data: Data) async {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        guard !stopped, let message = try? decoder.decode(CallLinkMessage.self, from: data) else { return }
        switch message.type {
        case "peer":
            if let ago = message.answeredMsAgo, ago >= 0 {
                onAnswered?(Date().addingTimeInterval(-Double(ago) / 1000))
            }
            guard message.video == true else {
                onMark?("Their phone can't show a picture")
                // Its camera went on as the call rang, for a Linx device
                // that might have answered.
                if video.mine { stopCamera() }
                said(available: false)
                return
            }
            polite = message.polite ?? false
            linkTries = 0
            let goesOn = message.early == true && earlySession != nil && message.session == earlySession
            session = message.session
            // A connection nothing has been said on yet is kept (made at
            // `start`, its relay route already found), and so is one set up
            // while the call rang if the pairing goes on from it; otherwise
            // this is a new pairing and it starts afresh.
            if !goesOn,
                connection == nil || connection?.localDescription != nil || connection?.remoteDescription != nil
            {
                connect()
            }
            earlySession = nil
            onMark?(goesOn ? "Picture link ready (set up while ringing)" : "Picture link ready")
            said(available: true)
            if sound != nil {
                send(PictureSignal(sound: .can))
                // A voice call paired after the answer (with a browser) has
                // no offer yet: the side that leads makes one, with its
                // microphone in it, and the other's goes in its slot.
                if !polite, let connection { addMySound(to: connection) }
            }
            // Beside the link's messages, not in their line: a camera takes
            // a moment to start, and the other side's offer mustn't wait.
            if wantCamera, !heldOffer { Task { [weak self] in try? await self?.setCamera(true) } }
        case "peer-gone":
            // The other side's link went: it reconnects, or the call is
            // ending. A new "peer" starts afresh.
            closeConnection()
            said(mine: false, theirs: false, theirPicture: false)
        case "signal":
            // From an earlier pairing of the two ends: its connection is gone.
            if let theirs = message.data?.session, let session, theirs != session { return }
            if let signal = message.data { await signalled(signal) }
        case "early":
            await ringingExchange(session: message.session, offer: message.offer == true)
        case "wait":
            // Open while the call rings; Linx sends "peer" at the answer.
            if !saidWaiting {
                saidWaiting = true
                onMark?("Picture link open, waiting for the answer")
            }
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
        guard callUp else {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(3))
                guard let self, !self.stopped else { return }
                self.link.start()
            }
            return
        }
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

    /// The call rings and Linx has opened its exchange: the caller offers
    /// now — its camera in the offer on a video call — and the phone being
    /// rung takes that offer and holds it (`heldOffer`).
    private func ringingExchange(session new: UInt64?, offer: Bool) async {
        guard let new, !callUp else { return }
        if earlySession != nil, earlySession != new { connect() }
        earlySession = new
        session = new
        guard offer, let connection else {
            onMark?("Picture link open while ringing")
            return
        }
        onMark?("Picture offered while ringing")
        if wantCamera {
            try? await setCamera(true)
        }
        // No camera of this phone's (or it couldn't start): the offer asks
        // for theirs alone.
        if self.connection === connection, !connection.transceivers.contains(where: { $0.mediaType == .video }) {
            let setup = RTCRtpTransceiverInit()
            setup.direction = .recvOnly
            connection.addTransceiver(of: .video, init: setup)
        }
    }

    /// The person answered a call whose offer this phone was holding: the
    /// camera goes in its slot and the answer goes at once.
    private func answerHeldOffer() async {
        if let answer = await makeHeldAnswer() { send(answer) }
    }

    /// The held offer's answer made now, for the phone line to send in the
    /// same message as the call's 200 OK (`SIPUserAgent.answer`): sent after
    /// it, the phone's network held it until the 200 OK was acknowledged,
    /// three seconds on the bench's slow, just-opened line. Nil when no
    /// offer is held.
    func answerWithTheCall() async -> Data? {
        guard let answer = await makeHeldAnswer() else { return nil }
        return encoded(answer)
    }

    /// The camera goes in the held offer's slot and the answer is made; the
    /// camera starts behind it, so the answer isn't held up by it.
    private func makeHeldAnswer() async -> PictureSignal? {
        guard heldOffer, let connection, connection.signalingState == .haveRemoteOffer else { return nil }
        heldOffer = false
        var starting: Camera?
        if wantCamera, camera == nil {
            let camera = Camera(factory: factory, quality: quality)
            self.camera = camera
            carryMyCamera(on: connection)
            tracks.local = camera.track
            onMark?("Camera on")
            said(mine: true)
            starting = camera
        }
        preferCodecs(connection)
        let answered = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
            connection.setLocalDescriptionWithCompletionHandler { error in done.resume(returning: error == nil) }
        }
        guard connection === self.connection else { return nil }
        if let starting {
            Task { [weak self] in
                do {
                    try await starting.start()
                } catch {
                    guard let self, self.camera === starting else { return }
                    self.stopCamera()
                }
            }
        }
        capWhatThisPhoneSends()
        readTheirDirection()
        guard answered, let local = connection.localDescription else {
            onMark?("Picture answer couldn't be made")
            return nil
        }
        onMark?("Picture answer sent")
        return PictureSignal(description: .init(type: RTCSessionDescription.string(for: local.type), sdp: local.sdp))
    }

    private func send(_ signal: PictureSignal) {
        guard let data = encoded(signal) else { return }
        link.send(data)
    }

    /// What goes on the link for `signal`, stamped with this pairing.
    private func encoded(_ signal: PictureSignal) -> Data? {
        struct Out: Encodable {
            var type = "signal"
            var data: PictureSignal
        }
        var signal = signal
        signal.session = session
        return try? JSONEncoder().encode(Out(data: signal))
    }

    private func connect() {
        closeConnection()
        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        configuration.iceCandidatePoolSize = 1
        connection = factory.peerConnection(
            with: configuration, constraints: constraints, verifier: verifier, delegate: self)
        routeNow = nil
        startWatching()
        startListening()
    }

    private func closeConnection() {
        watch?.cancel()
        watch = nil
        soundGone("the picture's link was made again")
        camera?.stop()
        camera = nil
        tracks.local = nil
        tracks.remote = nil
        connection?.close()
        connection = nil
        makingOffer = false
        ignoreOffer = false
        heldOffer = false
        earlyRoutes = []
        framesBefore += framesMost
        framesMost = 0
        framesSeen = 0
        lastTold = nil
        squeezed = onMobileData && !hd ? Self.mobileStart : nil
        startedSmall = squeezed != nil
        sendingAV1 = false
        codecMarked = false
        busyReadings = 0
        roomyReadings = 0
        tightReadings = 0
        steadyReadings = 0
        steadyWanted = Self.steadyFirst
        tryingFor = 0
        delayFloor = nil
        delayedReadings = 0
        ourRouteMarked = false
        theirRouteMarked = false
        overTCP = false
        theirsOverTCP = false
        waitedForTheirOffer = false
        quietReadings = 0
        if let first = firstFrame { first.track.remove(first.watcher) }
        firstFrame = nil
        theirPictureMarked = false
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
        addMySound(to: connection)
        preferCodecs(connection)
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
        if let word = signal.sound { soundSaid(word) }
        guard let connection else { return }
        if let slow = signal.slowRoute, slow != theirsOverTCP {
            theirsOverTCP = slow
            startOverTCPSmall()
            onMark?(
                slow
                    ? "Their picture is over TCP: mine starts at \(quality.height)p" : "Their picture is off TCP"
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
            // Opus at the direct link's quality (40 kbit/s, full band, FEC,
            // DTX): this phone's encoder takes them from their description.
            let remote = RTCSessionDescription(
                type: type,
                sdp: SDPTweaks.preferOpusFecDtx(
                    SDPTweaks.startPictureAt(description.sdp), bitrate: SDPTweaks.directOpusMaxBitrate))
            let ok = await withCheckedContinuation { (done: CheckedContinuation<Bool, Never>) in
                connection.setRemoteDescription(remote) { error in done.resume(returning: error == nil) }
            }
            guard ok, connection === self.connection else {
                onMark?("Picture \(description.type) refused")
                connection.restartIce()
                return
            }
            for route in earlyRoutes { connection.add(route) { _ in } }
            earlyRoutes = []
            if type == .offer { carryMySound(on: connection) }
            findTheirSound(on: connection)
            if type == .offer, earlySession != nil {
                heldOffer = true
                guard callUp else {
                    onMark?("Picture offer held until the call is answered")
                    return
                }
                // Answered before the caller's offer got here (a slow link):
                // answered at once, with the camera in it, as a held one is.
                // Answered without it, the camera came after in a second
                // round that crossed the other side's (the bench's poor
                // link, 2026-10-14: 4–7 s to the picture).
                await answerHeldOffer()
                return
            }
            if type == .offer { carryMyCamera(on: connection) }
            if type == .offer {
                preferCodecs(connection)
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
            if !theirRouteMarked {
                theirRouteMarked = true
                onMark?("Picture: their first route arrived (\(Self.kind(of: candidate.candidate)))")
            }
            let ice = RTCIceCandidate(
                sdp: candidate.candidate, sdpMLineIndex: candidate.sdpMLineIndex ?? 0, sdpMid: candidate.sdpMid)
            // A route can overtake the description it belongs to while that
            // is being set: it waits for it.
            guard connection.remoteDescription != nil else {
                if earlyRoutes.count < 64 { earlyRoutes.append(ice) }
                return
            }
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

    // MARK: - The sound (ADR-092, docs/DIRECT_SOUND.md)

    /// This phone's microphone goes in the offer it makes, in a slot of its
    /// own, with the camera's stream id so WebRTC keeps the two in step.
    private func addMySound(to connection: RTCPeerConnection) {
        guard let sound, !connection.transceivers.contains(where: { $0.mediaType == .audio && !$0.isStopped })
        else { return }
        let setup = RTCRtpTransceiverInit()
        setup.direction = .sendRecv
        setup.streamIds = ["linx"]
        if let transceiver = connection.addTransceiver(with: sound, init: setup) {
            putTheVoiceFirst(transceiver.sender)
        }
    }

    /// Their offer has a slot for the sound: this phone's microphone goes
    /// in it, as its camera does (`carryMyCamera`), and a slot of its own
    /// made for an offer it took back is closed.
    private func carryMySound(on connection: RTCPeerConnection) {
        guard let sound else { return }
        let audio = connection.transceivers.filter { $0.mediaType == .audio && !$0.isStopped }
        guard let theirs = audio.first(where: { !$0.mid.isEmpty }) else { return }
        for orphan in audio where orphan !== theirs && orphan.mid.isEmpty {
            orphan.sender.track = nil
            orphan.stopInternal()
        }
        guard theirs.sender.track == nil else { return }
        theirs.sender.track = sound
        theirs.sender.streamIds = ["linx"]
        theirs.setDirection(.sendRecv, error: nil)
        putTheVoiceFirst(theirs.sender)
    }

    /// The voice goes before the picture when the link is short of room,
    /// and never above the direct link's Opus ceiling.
    private func putTheVoiceFirst(_ sender: RTCRtpSender) {
        let parameters = sender.parameters
        for encoding in parameters.encodings {
            encoding.networkPriority = .high
            encoding.maxBitrateBps = NSNumber(value: SDPTweaks.directOpusMaxBitrate)
        }
        sender.parameters = parameters
    }

    /// Their sound on this link, kept off until it is the route played.
    private func findTheirSound(on connection: RTCPeerConnection) {
        let track = connection.transceivers.first { $0.mediaType == .audio && !$0.isStopped }?.receiver.track
        guard let track = track as? RTCAudioTrack else { return }
        theirSound = track
        track.isEnabled = soundRoute.playing == .direct
    }

    /// The other side said something about the sound.
    private func soundSaid(_ word: SoundWord) {
        let before = soundRoute.theyPlay
        soundRoute.heard(word)
        switch word {
        case .can:
            // It knows about the direct sound: this phone's microphone goes
            // on there, and it is told so.
            guard sound != nil else { return }
            theyCanTakeSound = true
            sound?.isEnabled = !muted
            send(PictureSignal(sound: .sending))
        case .sending:
            onMark?("Sound: theirs is on the direct link too")
        case .direct, .linx:
            guard soundRoute.theyPlay != before else { return }
            onMark?(word == .direct ? "Sound: they hear mine directly" : "Sound: they hear mine through Linx again")
            onTheyHear?(soundRoute.theyPlay)
        }
    }

    /// Reads the direct link's sound twice a second, and follows it.
    private func startListening() {
        soundWatch?.cancel()
        guard sound != nil else { return }
        soundWatch = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: DirectSound.every)
                guard let self, !Task.isCancelled, let connection = self.connection else { return }
                guard self.callUp else { continue }
                let report = await withCheckedContinuation { (done: CheckedContinuation<RTCStatisticsReport, Never>) in
                    connection.statistics { done.resume(returning: $0) }
                }
                guard !Task.isCancelled, connection === self.connection else { return }
                self.soundQuality =
                    SoundQuality(report: report)
                    ?? self.soundQuality
                self.heard(Self.soundPackets(in: report))
            }
        }
    }

    /// Their sound packets that have arrived on this link so far.
    nonisolated static func soundPackets(in report: RTCStatisticsReport) -> Int {
        report.statistics.values
            .filter { $0.type == "inbound-rtp" && ($0.values["kind"] as? String) == "audio" }
            .reduce(0) { $0 + (($1.values["packetsReceived"] as? NSNumber)?.intValue ?? 0) }
    }

    private func heard(_ packets: Int) {
        guard let route = soundRoute.reading(packets: packets) else { return }
        theirSound?.isEnabled = route == .direct
        onMark?(
            route == .direct
                ? "Sound: playing theirs from the direct link"
                : "Sound: theirs stopped on the direct link; playing it through Linx")
        onHearing?(route)
        send(PictureSignal(sound: route == .direct ? .direct : .linx))
    }

    /// The link is gone or made again: both routes are Asterisk's at once.
    private func soundGone(_ why: String) {
        soundWatch?.cancel()
        soundWatch = nil
        theyCanTakeSound = false
        sound?.isEnabled = false
        theirSound?.isEnabled = false
        theirSound = nil
        guard soundRoute.linkGone() else { return }
        onMark?("Sound: through Linx again (\(why))")
        onHearing?(.linx)
        onTheyHear?(.linx)
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
                if let track = tracks.remote { waitForTheirFirstFrame(on: track) }
            }
        }
        if !theirs {
            framesSeen = 0
            quietReadings = 0
        }
        said(theirs: theirs, theirPicture: theirs && video.theirPicture)
    }

    /// Their picture goes up on the screen the moment its first frame is
    /// decoded. The two-second reading (`read`) used to decide it, which
    /// put up to two seconds on every call's first picture.
    private func waitForTheirFirstFrame(on track: RTCVideoTrack) {
        guard firstFrame?.track !== track else { return }
        if let old = firstFrame { old.track.remove(old.watcher) }
        let watcher = FirstFrame { [weak self] in
            Task { @MainActor in self?.theirFirstFrame(on: track) }
        }
        firstFrame = (track, watcher)
        track.add(watcher)
    }

    private func theirFirstFrame(on track: RTCVideoTrack) {
        guard let first = firstFrame, first.track === track else { return }
        track.remove(first.watcher)
        guard !stopped, tracks.remote === track else { return }
        if !theirPictureMarked {
            theirPictureMarked = true
            onMark?("Their picture arrived")
        }
        quietReadings = 0
        said(theirs: true, theirPicture: true)
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
    /// bench, 2026-10-13). Video apps send less on such a route: the
    /// picture starts at 360p there and grows only as far as the room the
    /// link really shows, never by a blind try: never past 540p, or past
    /// 720p with **HD** on. Both phones on a call press HD and expect it
    /// to mean something wherever the link has room, TCP or not (owner,
    /// 2026-10-17: both pressed HD over TLS and the picture stayed 540p).
    static let overTCPStep = VideoQuality.low
    static let overTCPMost = VideoQuality.high
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
        startOverTCPSmall()
        onMark?(
            tcp
                ? "Picture over TCP: starts at \(quality.height)p"
                : "Picture off TCP: back to \(unsqueezed.height)p")
        send(PictureSignal(slowRoute: tcp))
        useQuality()
    }

    /// The least round trip the picture's route has had, and how many
    /// readings in a row it has been far above it.
    private var delayFloor: Double?
    private var delayedReadings = 0

    /// A queue building up on the way (a mobile network that holds what it
    /// can't send yet instead of dropping it): the round trip climbs well
    /// above what the route had, and the picture comes down a step before
    /// the delay reaches the sound. Two readings in a row (four seconds),
    /// so a satellite's moment of handing over (Starlink, every fifteen
    /// seconds) doesn't count, and never below a round trip a slow but
    /// steady link has anyway (a ship's satellite: 0.8 s).
    private func watchTheDelay(_ rtt: Double) {
        delayFloor = min(delayFloor ?? rtt, rtt)
        guard let floor = delayFloor, rtt > max(Self.delayAlways, floor * Self.delayTimes) else {
            delayedReadings = 0
            return
        }
        delayedReadings += 1
        guard delayedReadings >= 2, let smaller = VideoQuality(rawValue: quality.rawValue - 1) else { return }
        delayedReadings = 0
        startedSmall = false
        squeezed = smaller
        roomyReadings = 0
        steadyReadings = 0
        onMark?("Picture down to \(smaller.height)p: the delay climbed to \(Int(rtt * 1000)) ms")
        useQuality()
    }

    nonisolated static let delayAlways = 0.6
    nonisolated static let delayTimes = 3.0

    /// Onto a TCP route, either side's: the picture goes back to the step it
    /// starts at there, if it was bigger.
    private func startOverTCPSmall() {
        guard overTCP || theirsOverTCP, quality.rawValue > Self.overTCPStep.rawValue else { return }
        squeezed = Self.overTCPStep
        roomyReadings = 0
        steadyReadings = 0
    }

    /// The picture's route as last read.
    private var routeNow: String?

    private func useQuality() {
        camera?.use(quality)
        capWhatThisPhoneSends()
    }

    private func capWhatThisPhoneSends() {
        let most = quality.bitrate(av1: sendingAV1)
        for sender in connection?.senders ?? [] where sender.track?.kind == kRTCMediaStreamTrackKindVideo {
            let parameters = sender.parameters
            for encoding in parameters.encodings { encoding.maxBitrateBps = NSNumber(value: most) }
            // The size is Linx's to choose (`follow(room:)` steps it down on
            // a thin link and back up): left to WebRTC's own "balanced"
            // scaling, a direct call with 2 Mbit/s of room started at 180p
            // and took 28 s to climb to 720p after HD (the owner's 5G call
            // and the phone bench, 2026-10-14). Holding the size means a
            // short squeeze costs frames, not sharpness, until the ladder
            // steps down.
            parameters.degradationPreference = NSNumber(
                value: RTCDegradationPreference.maintainResolution.rawValue)
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
        var how = PictureReading()
        // The room to send in is the pair WebRTC is really using: several
        // pairs can be marked nominated at once, and reading whichever came
        // first gave a different route and a different room every few
        // seconds — the picture stepped down for nothing and HD took up to
        // 50 s to arrive (the owner's 5G call, 2026-10-14).
        let pair = Self.pairInUse(in: report)
        let room = (pair?.values["availableOutgoingBitrate"] as? NSNumber)?.intValue
        if let route = Self.route(of: pair, in: report), route != routeNow {
            onMark?(routeNow == nil ? "Picture route: \(route)" : "Picture route now: \(route)")
            routeNow = route
            routeFound(route)
        }
        for statistic in report.statistics.values {
            if statistic.type == "outbound-rtp", statistic.values["kind"] as? String == "video",
                let height = (statistic.values["frameHeight"] as? NSNumber)?.intValue, height > 0
            {
                sending(height, room: room)
            }
            if statistic.type == "outbound-rtp", statistic.values["kind"] as? String == "video" {
                how.mine(statistic.values)
                if let codec = statistic.values["codecId"] as? String,
                    let mime = report.statistics[codec]?.values["mimeType"] as? String
                {
                    how.codec = Self.codecName(mime)
                    codecSeen(mime, limit: statistic.values["qualityLimitationReason"] as? String)
                }
            }
            if statistic.type == "inbound-rtp", statistic.values["kind"] as? String == "video" {
                frames += (statistic.values["framesDecoded"] as? NSNumber)?.intValue ?? 0
                how.theirs(statistic.values)
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
        if framesSeen == 0, frames > 0, !theirPictureMarked {
            theirPictureMarked = true
            onMark?("Their picture arrived")
        }
        framesSeen = frames
        framesMost = max(framesMost, frames)
        if video.mine, let rtt = (pair?.values["currentRoundTripTime"] as? NSNumber)?.doubleValue, rtt > 0 {
            watchTheDelay(rtt)
        }
        if video.mine, let room { follow(room: room) }
        how.room = room
        tell(how)
    }

    /// Every ten seconds, for the first minute of a picture: how each way
    /// is going, in the call's report. "Stopping and starting" and "went
    /// back to low res" are what a person sees; these say which way and why.
    private func tell(_ how: PictureReading) {
        guard how.any else { return }
        readings += 1
        if let freezes = how.freezes { theirFreezes = freezes }
        guard readings % Self.readingsBetweenTold == 1, told < Self.mostTold else { return }
        told += 1
        onMark?(how.words(since: lastTold))
        lastTold = how
        let held = Self.heldWords()
        if held != lastHeld {
            lastHeld = held
            onMark?("This \(UIDevice.current.userInterfaceIdiom == .pad ? "iPad" : "iPhone") is \(held)")
        }
    }

    /// How the device is held, which is also which way round its camera's
    /// picture goes out and how the other side's fills this screen: a
    /// sideways picture filling an upright phone is cropped to a narrow
    /// slice of it (owner, 2026-10-17: the iPhone's view of the iPad was
    /// far softer than the iPad's of the iPhone, the same 540p both ways).
    /// Flat on a table, the screen's own way round says it.
    private static func heldWords() -> String {
        let device = UIDevice.current.orientation
        if device.isPortrait { return "upright" }
        if device.isLandscape { return "sideways" }
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        guard let screen = scene?.effectiveGeometry.interfaceOrientation else { return "held some way unknown" }
        return screen.isLandscape ? "flat, its screen sideways" : "flat, its screen upright"
    }

    private var readings = 0
    private var told = 0
    private var lastTold: PictureReading?
    private var lastHeld: String?
    /// Their picture's stops (WebRTC's "freezes") and how long, at the last reading.
    private var theirFreezes: (count: Int, seconds: Double)?
    private static let readingsBetweenTold = 5
    private static let mostTold = 6

    /// The picture's size follows the room the link has. A thin stretch
    /// takes it straight down to the step that fits; room for the next
    /// step up, for a few readings, takes it up one; and only a link too
    /// thin even for the smallest switches the camera off, so the sound
    /// keeps what it needs.
    ///
    /// Only the last of these used to move it: a picture held at 720p on a
    /// link with room for 160–400 kbit/s sent a few frames a second, soft
    /// and stopping and starting, for the rest of the call (the owner's 5G
    /// call after HD, 2026-10-14). The size is Linx's to hold
    /// (`capWhatThisPhoneSends`), so it must be Linx that brings it down.
    private func follow(room: Int) {
        // At the smallest size already, a thin reading is only the room
        // following what is sent (an AV1 picture at 240p reads under
        // `tooThin` on a fine link): the camera goes off only below
        // `tooThinEvenSmall`, and otherwise the size is tried up like any
        // other (the phone bench's squeeze, 2026-10-15: it stayed at 240p
        // for the rest of the call).
        if room < Self.tooThin, quality != .thin || room < Self.tooThinEvenSmall {
            roomyReadings = 0
            tightReadings = 0
            steadyReadings = 0
            thinReadings += 1
            guard thinReadings >= Self.thinReadingsAllowed else { return }
            thinReadings = 0
            if tryingFor > 0 {
                tryingFor = 0
                steadyWanted = min(steadyWanted * 2, Self.steadyMost)
            }
            if quality != .thin {
                startedSmall = false
                squeezed = .thin
                onMark?("Picture down to \(VideoQuality.thin.height)p: room for \(room / 1000) kbit/s")
                useQuality()
            } else if room < Self.tooThinEvenSmall {
                onTooExpensive?()
            }
            return
        }
        thinReadings = 0
        let fits = Self.step(for: room, under: unsqueezed, av1: sendingAV1)
        if fits < quality {
            roomyReadings = 0
            steadyReadings = 0
            tightReadings += 1
            guard tightReadings >= Self.tightReadingsAllowed else { return }
            tightReadings = 0
            if tryingFor > 0 {
                // A step up that the link couldn't carry: wait longer before
                // the next try.
                tryingFor = 0
                steadyWanted = min(steadyWanted * 2, Self.steadyMost)
            }
            startedSmall = false
            squeezed = fits
            onMark?("Picture down to \(fits.height)p: room for \(room / 1000) kbit/s")
            useQuality()
            return
        }
        tightReadings = 0
        if tryingFor > 0 {
            // A tried step that has held: the next try waits the least again.
            tryingFor -= 1
            if tryingFor == 0 { steadyWanted = Self.steadyFirst }
        }
        guard let squeezed, let bigger = VideoQuality(rawValue: squeezed.rawValue + 1) else {
            roomyReadings = 0
            steadyReadings = 0
            return
        }
        guard room >= bigger.bitrate(av1: sendingAV1) * 13 / 20 else {
            roomyReadings = 0
            // The room WebRTC reports never runs far ahead of what this
            // phone sends, so a picture squeezed small can't show room for
            // a bigger one: the owner's iPhone sat at 360p reading 185–199
            // kbit/s for half a minute after HD while the iPad, allowed 900,
            // read 1023 (2026-10-15). A steady stretch is tried one step up;
            // a link that can't carry it brings it down again in seconds,
            // and the next try waits twice as long.
            // Never over TCP, where a step too big for the link backs up
            // everything behind it before the room reading catches up.
            guard !(overTCP || theirsOverTCP) else { return }
            steadyReadings += 1
            guard steadyReadings >= steadyWanted else { return }
            steadyReadings = 0
            tryingFor = Self.trialReadings
            startedSmall = false
            self.squeezed = bigger.rawValue >= unsqueezed.rawValue ? nil : bigger
            onMark?("Picture trying \(quality.height)p: room for \(room / 1000) kbit/s")
            useQuality()
            return
        }
        roomyReadings += 1
        guard roomyReadings >= Self.roomyReadingsWanted else { return }
        roomyReadings = 0
        startedSmall = false
        self.squeezed = bigger.rawValue >= unsqueezed.rawValue ? nil : bigger
        onMark?("Picture back up to \(quality.height)p: room for \(room / 1000) kbit/s")
        useQuality()
    }

    /// AV1 first, then the rest as WebRTC lists them (H.264, the phone's
    /// own hardware one, leading), on every picture slot before a
    /// description is made: each side sends in the first codec the other
    /// lists that it has too, so two apps meet on AV1, and a side without
    /// it (a browser that lacks it) on H.264 as before. AV1 squeezes the
    /// same picture into about a third less data (owner, 2026-10-15: a
    /// sharper picture on a thin link), worked out by the phone's
    /// processor instead of its video hardware.
    private func preferCodecs(_ connection: RTCPeerConnection) {
        let offered = factory.rtpReceiverCapabilities(forKind: kRTCMediaStreamTrackKindVideo).codecs
        let codecs = Self.codecOrder(offered.map(\.mimeType), av1: av1Allowed).map { offered[$0] }
        for transceiver in connection.transceivers where transceiver.mediaType == .video && !transceiver.isStopped {
            try? transceiver.setCodecPreferences(codecs, error: ())
        }
    }

    /// The order of `mimeTypes` (as positions in it) this phone offers its
    /// picture in: AV1 first when it's allowed, otherwise left out, so
    /// neither side sends it.
    nonisolated static func codecOrder(_ mimeTypes: [String], av1: Bool) -> [Int] {
        let isAV1 = { (at: Int) in mimeTypes[at].lowercased() == "video/av1" }
        let rest = mimeTypes.indices.filter { !isAV1($0) }
        return av1 ? mimeTypes.indices.filter(isAV1) + rest : rest
    }

    nonisolated static func codecName(_ mime: String) -> String {
        let name = mime.split(separator: "/").last.map(String.init) ?? mime
        return name.uppercased() == "H264" ? "H.264" : name
    }

    /// What the picture is going out in, at each reading. AV1 is the
    /// processor's work: a phone that can't keep up with it (WebRTC says
    /// the processor is what holds the picture back, three readings
    /// running) goes back to H.264 for the rest of the call, offering
    /// again without AV1 so both ways change.
    private func codecSeen(_ mime: String, limit: String?) {
        let av1 = mime.lowercased() == "video/av1"
        if av1 != sendingAV1 || !codecMarked {
            codecMarked = true
            sendingAV1 = av1
            onMark?("Picture in \(Self.codecName(mime))")
            capWhatThisPhoneSends()
        }
        guard av1 else {
            busyReadings = 0
            return
        }
        if !av1Allowed {
            // Still AV1 after giving it up: the offer that leaves it out
            // couldn't go yet (a description of theirs was in the way).
            if let connection { Task { [weak self] in await self?.negotiate(connection) } }
            return
        }
        busyReadings = limit == "cpu" ? busyReadings + 1 : 0
        guard busyReadings >= Self.busyReadingsAllowed else { return }
        busyReadings = 0
        av1Allowed = false
        onMark?("Picture: this phone can't keep up with AV1, back to H.264")
        if let connection { Task { [weak self] in await self?.negotiate(connection) } }
    }

    /// The biggest step, no bigger than `top`, that a link with `room` bits
    /// a second to spare can carry: one whose most is no more than twice
    /// the room. The encoder works below a step's most when it has to;
    /// further below than that, a bigger picture is a worse one.
    nonisolated static func step(for room: Int, under top: VideoQuality, av1: Bool = false) -> VideoQuality {
        var step = top
        while step.bitrate(av1: av1) / 2 > room, let smaller = VideoQuality(rawValue: step.rawValue - 1) {
            step = smaller
        }
        return step
    }

    /// The size this phone's picture is really going out at, which is what
    /// WebRTC makes of the step asked for and the link it finds: **HD** asks
    /// for 720p, and only this says whether it got there (owner, 2026-10-14:
    /// "clicking on HD doesn't change anything"). Marked when it settles on a
    /// new size, two readings running, so a size passed through on the way
    /// up isn't written down.
    private func sending(_ height: Int, room: Int?) {
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
        if sizeMarks <= Self.mostSizeMarks {
            onMark?("Sending \(height)p" + (room.map { " (room \($0 / 1000) kbit/s)" } ?? ""))
        }
        onSending?(height)
    }

    private var sendingHeight = 0
    private var sendingCandidate: Int?
    private var sizeMarks = 0
    /// A call that wanders between sizes all its life fills its report with
    /// nothing else; the first few say what is needed.
    private static let mostSizeMarks = 12
    /// When the server says the call was answered (`CallLinkMessage.answeredMsAgo`).
    var onAnswered: ((Date) -> Void)?
    /// The size this phone's picture is going out at, for Call details.
    var onSending: ((Int) -> Void)?

    /// The step before any squeezing.
    private var unsqueezed: VideoQuality {
        if overTCP || theirsOverTCP { return hd ? Self.withHD : Self.overTCPMost }
        return hd ? Self.withHD : Self.standard
    }

    /// The step a thin link has taken the picture down to, if any.
    private var squeezed: VideoQuality?
    private var roomyReadings = 0
    private var tightReadings = 0
    /// Readings (two seconds apart) too tight for the size before it comes
    /// down to the one that fits.
    private static let tightReadingsAllowed = 2
    /// Below this even the smallest step goes off (bits a second).
    private static let tooThinEvenSmall = 30_000
    /// Readings (two seconds apart) with room for the next step before a
    /// step up: the link's room grows with what is sent, so the next step
    /// is wanted at about two thirds of its most, not at all of it.
    private static let roomyReadingsWanted = 3
    /// Steady readings (no tight one) at a squeezed size before trying the
    /// next step up anyway: from ten seconds, doubling after each try the
    /// link couldn't carry, to a minute at most.
    private var steadyReadings = 0
    private var steadyWanted = steadyFirst
    private static let steadyFirst = 5
    private static let steadyMost = 30
    /// Readings left in which a tried step that comes down counts as a
    /// failed try.
    private var tryingFor = 0
    private static let trialReadings = 4
    /// The most of their frames decoded on this connection.
    private var framesMost = 0
    /// Their first frame's watcher, on the track it watches.
    private var firstFrame: (track: RTCVideoTrack, watcher: FirstFrame)?
    private var theirPictureMarked = false

    /// The picture's route in the call's own words ("relay/udp → relay/udp"),
    /// once it has one.
    /// The candidate pair the picture is going over: the one its transport
    /// has selected, not whichever pair says it was nominated.
    static func pairInUse(in report: RTCStatisticsReport) -> RTCStatistics? {
        let all = report.statistics
        for statistic in all.values where statistic.type == "transport" {
            if let id = statistic.values["selectedCandidatePairId"] as? String, let pair = all[id] {
                return pair
            }
        }
        return nil
    }

    private static func route(of pair: RTCStatistics?, in report: RTCStatisticsReport) -> String? {
        let all = report.statistics
        guard
            let pair,
            pair.values["state"] as? String == "succeeded",
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
        if !now.theirs { now.theirPictureSeen = false }
        if now.theirPicture { now.theirPictureSeen = true }
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
        let kind = Self.kind(of: candidate.sdp)
        Task { @MainActor [weak self] in
            guard let self, connection === self.connection else { return }
            if !self.ourRouteMarked {
                // When this phone's first way in is ready: on a woken
                // iPhone the picture took ten seconds from the answer and
                // the reports couldn't say which part (2026-10-15).
                self.ourRouteMarked = true
                self.onMark?("Picture: our first route ready (\(kind))")
            }
            self.send(signal)
        }
    }

    /// "relay", "host"… from a route's SDP line.
    nonisolated static func kind(of sdp: String) -> String {
        let words = sdp.split(separator: " ")
        guard let at = words.firstIndex(of: "typ"), at + 1 < words.count else { return "?" }
        return String(words[at + 1])
    }

    nonisolated func peerConnection(_ connection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        if newState == .connected {
            // The route is read now rather than at the next two-second
            // reading: what it is decides the picture's size.
            Task { @MainActor [weak self] in
                guard let self, connection === self.connection else { return }
                let report = await withCheckedContinuation { (done: CheckedContinuation<RTCStatisticsReport, Never>) in
                    connection.statistics { done.resume(returning: $0) }
                }
                guard connection === self.connection else { return }
                self.read(report)
            }
        }
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

/// Says once, from WebRTC's own thread, that a track's first frame has
/// been decoded.
final class FirstFrame: NSObject, RTCVideoRenderer, @unchecked Sendable {
    private let lock = NSLock()
    private var fired = false
    private let then: @Sendable () -> Void

    init(then: @escaping @Sendable () -> Void) {
        self.then = then
    }

    func setSize(_ size: CGSize) {}

    func renderFrame(_ frame: RTCVideoFrame?) {
        guard frame != nil else { return }
        lock.lock()
        let first = !fired
        fired = true
        lock.unlock()
        if first { then() }
    }
}

/// A call's link on the phone's own line (`internal/siprelay`'s Side):
/// the call link's messages, carried on the websocket the app has had open
/// since it signed in. On the owner's iPhone on Etisalat 5G (2026-10-14)
/// every newly opened connection was slow for its first 20–30 s, while the
/// line answered in a tenth of a second; a call link of its own took 17–24 s
/// a message, and the picture came 45 s after the answer.
@MainActor final class LineLink: PictureLink {
    var onMessage: ((Data) -> Void)?
    var onClose: (() -> Void)?

    let callID: String
    private weak var line: SIPUserAgent?
    private var open = false

    init(callID: String, line: SIPUserAgent) {
        self.callID = callID
        self.line = line
    }

    func start() {
        open = true
        guard let line else {
            Task { @MainActor in self.lineClosed() }
            return
        }
        line.open(self)
    }

    func send(_ data: Data) { line?.send(data, on: self) }

    func stop() {
        open = false
        line?.close(self)
    }

    func received(_ data: Data) {
        guard open else { return }
        onMessage?(data)
    }

    /// Linx closed the link, or the line went.
    func lineClosed() {
        guard open else { return }
        open = false
        onClose?()
    }

    enum Message: Equatable {
        case message(Data)
        case closed
    }

    /// {"call":ID,<key>:value} for the line.
    static func frame(_ callID: String, _ key: String, _ value: Bool) -> String {
        let id = String(decoding: (try? JSONEncoder().encode(callID)) ?? Data("\"\"".utf8), as: UTF8.self)
        return "{\"call\":\(id),\"\(key)\":\(value)}"
    }

    /// {"call":ID,"open":true,"early":true}: opens the link, and says this
    /// phone sets its picture up while the call rings.
    static func opening(_ callID: String) -> String {
        let id = String(decoding: (try? JSONEncoder().encode(callID)) ?? Data("\"\"".utf8), as: UTF8.self)
        return "{\"call\":\(id),\"open\":true,\"early\":true}"
    }

    /// {"call":ID,"msg":<data>}: data is already JSON, sent as it is.
    static func frame(_ callID: String, msg data: Data) -> String {
        let id = String(decoding: (try? JSONEncoder().encode(callID)) ?? Data("\"\"".utf8), as: UTF8.self)
        return "{\"call\":\(id),\"msg\":" + String(decoding: data, as: UTF8.self) + "}"
    }

    /// {"call":ID,"msg":<data>,"sip":"<message>"}: a SIP message and a side
    /// message in one, so neither waits on the other (`siprelay`).
    static func frame(_ callID: String, msg data: Data, sip: String) -> String {
        let id = String(decoding: (try? JSONEncoder().encode(callID)) ?? Data("\"\"".utf8), as: UTF8.self)
        let text = String(decoding: (try? JSONEncoder().encode(sip)) ?? Data("\"\"".utf8), as: UTF8.self)
        return "{\"call\":\(id),\"msg\":" + String(decoding: data, as: UTF8.self) + ",\"sip\":" + text + "}"
    }

    /// A side message from Linx: which call, and what for it.
    static func parse(_ text: String) -> (String, Message)? {
        guard let object = try? JSONSerialization.jsonObject(with: Data(text.utf8)) as? [String: Any],
            let callID = object["call"] as? String
        else { return nil }
        if object["closed"] != nil { return (callID, .closed) }
        guard let msg = object["msg"], JSONSerialization.isValidJSONObject(msg),
            let data = try? JSONSerialization.data(withJSONObject: msg)
        else { return nil }
        return (callID, .message(data))
    }
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

/// One reading of the picture both ways, for the call's report.
struct PictureReading {
    var sentHeight: Int?
    var sentFPS: Double?
    var gotHeight: Int?
    var gotFPS: Double?
    var room: Int?
    var codec: String?
    /// How long the encoder takes over a frame, on average so far.
    var encodeMs: Int?
    var freezes: (count: Int, seconds: Double)?
    /// WebRTC's running totals each way — bytes, frames, and the sum of
    /// each frame's quantiser (how coarsely it was squeezed: lower is
    /// sharper) — turned into kbit/s and an average between two readings.
    /// The size alone said nothing of a 720p picture that came out soft
    /// (the owner's call after HD, 2026-10-15).
    var sent = Totals()
    var got = Totals()
    var at = Date()

    struct Totals {
        var bytes: Double?
        var frames: Double?
        var qpSum: Double?

        init(_ values: [String: NSObject] = [:], bytes: String = "", frames: String = "") {
            self.bytes = (values[bytes] as? NSNumber)?.doubleValue
            self.frames = (values[frames] as? NSNumber)?.doubleValue
            qpSum = (values["qpSum"] as? NSNumber)?.doubleValue
        }

        /// "900 kbit/s qp 34" over the time since `before`.
        func words(since before: Totals?, seconds: Double) -> String? {
            guard let bytes, let before, let was = before.bytes, seconds > 0, bytes >= was else { return nil }
            var line = "\(Int((bytes - was) * 8 / seconds / 1000)) kbit/s"
            if let frames, let qpSum, let framesWas = before.frames, let qpWas = before.qpSum, frames > framesWas {
                line += " qp \(Int(((qpSum - qpWas) / (frames - framesWas)).rounded()))"
            }
            return line
        }
    }

    var any: Bool { sentHeight != nil || gotHeight != nil }

    mutating func mine(_ values: [String: NSObject]) {
        sentHeight = (values["frameHeight"] as? NSNumber)?.intValue
        sentFPS = (values["framesPerSecond"] as? NSNumber)?.doubleValue
        sent = Totals(values, bytes: "bytesSent", frames: "framesEncoded")
        if let frames = (values["framesEncoded"] as? NSNumber)?.doubleValue, frames > 0,
            let seconds = (values["totalEncodeTime"] as? NSNumber)?.doubleValue
        {
            encodeMs = Int((seconds / frames * 1000).rounded())
        }
    }

    mutating func theirs(_ values: [String: NSObject]) {
        gotHeight = (values["frameHeight"] as? NSNumber)?.intValue
        gotFPS = (values["framesPerSecond"] as? NSNumber)?.doubleValue
        got = Totals(values, bytes: "bytesReceived", frames: "framesDecoded")
        if let count = (values["freezeCount"] as? NSNumber)?.intValue {
            freezes = (count, (values["totalFreezesDuration"] as? NSNumber)?.doubleValue ?? 0)
        }
    }

    var words: String { words(since: nil) }

    func words(since before: PictureReading?) -> String {
        let seconds = before.map { at.timeIntervalSince($0.at) } ?? 0
        func way(_ height: Int?, _ fps: Double?, _ totals: Totals, _ was: Totals?) -> String {
            guard let height else { return "none" }
            var line = "\(height)p \(Int((fps ?? 0).rounded())) fps"
            if let rate = totals.words(since: was, seconds: seconds) { line += " \(rate)" }
            return line
        }
        var mine = way(sentHeight, sentFPS, sent, before?.sent)
        if sentHeight != nil, let codec { mine += " \(codec)" }
        if sentHeight != nil, let encodeMs { mine += " \(encodeMs) ms a frame" }
        var line = "Picture: sending \(mine), getting \(way(gotHeight, gotFPS, got, before?.got))"
        if let room { line += ", room \(room / 1000) kbit/s" }
        return line
    }
}

extension SoundQuality {
    /// From a connection's statistics.
    init?(report: RTCStatisticsReport) {
        self.init(report.statistics.values.map { $0.values.merging(["type": $0.type as NSString]) { kept, _ in kept } })
    }
}
