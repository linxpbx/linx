import AVFoundation
import Foundation
@preconcurrency import WebRTC

// The sound of a call: Google's WebRTC, the same engine the browser uses
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

    private let turn: PhoneLine.Turn?
    private var connection: RTCPeerConnection?
    private var audio: RTCAudioTrack?
    private var gathered: CheckedContinuation<Void, Never>?
    private var gatheringLimit: Task<Void, Never>?
    private var watch: Task<Void, Never>?
    private var stopped = false

    /// Asterisk doesn't take candidates one at a time, so the phone holds the
    /// offer until it has them; a relay that can't be reached would otherwise
    /// hold the call for a long time (the same rule as the browser's).
    private static let gatherLimit: Duration = .seconds(3)
    private static let afterRelayCandidate: Duration = .milliseconds(300)
    private static let readRouteEvery: Duration = .seconds(5)

    init(turn: PhoneLine.Turn?) {
        self.turn = turn
        super.init()
    }

    // MARK: - SIPCallMedia

    func offer() async throws -> String {
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
        let tweaked = SDPTweaks.preferOpusFecDtx(local)
        try await setLocal(RTCSessionDescription(type: .offer, sdp: tweaked))
        await waitForCandidates()
        guard let full = connection.localDescription?.sdp else { throw MediaTrouble.noOffer }
        return SDPTweaks.preferOpusFecDtx(full)
    }

    func answer(to offer: String) async throws -> String {
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
        try await setLocal(RTCSessionDescription(type: .answer, sdp: SDPTweaks.preferOpusFecDtx(local)))
        await waitForCandidates()
        guard let full = connection.localDescription?.sdp else { throw MediaTrouble.noAnswer }
        capWhatThisPhoneSends()
        startWatching()
        return SDPTweaks.preferOpusFecDtx(full)
    }

    func accept(answer: String) async throws {
        try await setRemote(RTCSessionDescription(type: .answer, sdp: answer))
        capWhatThisPhoneSends()
        startWatching()
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
        gatheringLimit?.cancel()
        gatheringLimit = nil
        gathered?.resume()
        gathered = nil
        audio = nil
        connection?.close()
        connection = nil
        Self.releaseAudioSession()
    }

    /// Puts the call on the loudspeaker, or back on the earpiece.
    func setSpeaker(_ on: Bool) {
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        defer { session.unlockForConfiguration() }
        try? session.overrideOutputAudioPort(on ? .speaker : .none)
    }

    // MARK: - The peer connection

    private func start() throws -> RTCPeerConnection {
        if let connection { return connection }
        Self.prepareAudioSession()
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

    /// Caps what this phone sends, whatever the other side's SDP allows.
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

    /// Reads how the sound is getting through, every few seconds — no more
    /// often, because a phone's battery pays for it.
    private func startWatching() {
        watch?.cancel()
        watch = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                if let connection = await self.readConnection() {
                    self.onConnection?(connection)
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
        return MediaConnection(
            route: relayed ? .relayed : .direct,
            roundTripMs: rtt.map { Int(($0 * 1000).rounded()) },
            relayProtocol: relayed ? local?.values["relayProtocol"] as? String : nil,
            audioBytesIn: audioBytesIn)
    }

    // MARK: - One engine for the whole app

    /// WebRTC's factory is expensive to make and cheap to keep, so the app
    /// makes one. Audio only in this slice: video is build-order step 7, and
    /// leaving the video encoders out keeps the app smaller and idler.
    private static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory()
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
    /// other apps ducked, and Bluetooth headsets allowed. CallKit takes this
    /// over in build-order step 6, where the session is activated by the
    /// system instead (docs/PHASE2.md §14).
    private static func prepareAudioSession() {
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        defer { session.unlockForConfiguration() }
        do {
            try session.setCategory(
                .playAndRecord, mode: .voiceChat,
                options: [.allowBluetoothHFP, .allowBluetoothA2DP, .duckOthers])
            try session.setActive(true)
        } catch {
            // Nothing to do about it here: the call goes on without sound and
            // the person hears nothing, which the in-call screen shows.
        }
    }

    private static func releaseAudioSession() {
        let session = RTCAudioSession.sharedInstance()
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
