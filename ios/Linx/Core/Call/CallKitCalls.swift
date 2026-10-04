import AVFoundation
import CallKit
import Foundation
import UIKit

// CallKit: handing a call to iOS so it can ring the phone, show it on the
// lock screen, answer it from a car or a headset, and list it in the Phone
// app's Recents (docs/PHASE2.md §5, §14 item 1).
//
// Two rules shape everything here:
//
//  1. **A VoIP push must report a call at once, every time.** iOS kills an
//     app that takes one without reporting a call, and stops delivering
//     pushes to an app that keeps doing it. So `reportIncoming` is the
//     first thing that happens on a push — before the token, the phone
//     line, or anything else that could be slow or fail.
//  2. **The system owns the call.** Answering, hanging up, muting and
//     keypad tones all arrive as actions from CallKit, whether the person
//     pressed the app's own button or the one on their steering wheel. The
//     app does the work and then fulfils the action, so the two screens can
//     never disagree.

@MainActor final class CallKitCalls: SystemCalls {
    var onRequest: ((SystemCallRequest) -> Void)?
    var onAudio: ((Bool) -> Void)?
    var onReset: (() -> Void)?

    private let provider: CXProvider
    private let controller = CXCallController()
    private let bridge = Bridge()

    init() {
        provider = CXProvider(configuration: Self.configuration)
        bridge.owner = self
        provider.setDelegate(bridge, queue: nil)
    }

    /// What the system shows and allows. One call at a time: this phone has
    /// one line, and a second caller hears busy or goes to voicemail, the
    /// same as the browser (hold and a second line are later build steps).
    private static var configuration: CXProviderConfiguration {
        // The name on the lock screen is the app's own (CFBundleDisplayName,
        // "Linx"): iOS takes it from the bundle and won't be told otherwise.
        let configuration = CXProviderConfiguration()
        // A Linx call can become a video call while it is up (the video
        // button, docs/PHASE2.md §7), so the system is told this app does
        // video. Every call still *rings* as a phone call: a picture is
        // added to a call, never rung as one.
        configuration.supportsVideo = true
        configuration.maximumCallGroups = 1
        configuration.maximumCallsPerCallGroup = 1
        // A number, and "generic" for an extension or a withheld number.
        configuration.supportedHandleTypes = [.phoneNumber, .generic]
        configuration.includesCallsInRecents = true
        if let icon = UIImage(named: "CallKitIcon")?.pngData() {
            configuration.iconTemplateImageData = icon
        }
        return configuration
    }

    // MARK: - Telling the system

    func reportIncoming(id: UUID, from: SIPPeer) async -> Bool {
        let update = CXCallUpdate()
        update.remoteHandle = Self.handle(for: from)
        update.localizedCallerName = from.name.isEmpty ? nil : from.name
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = true
        return await withCheckedContinuation { finished in
            provider.reportNewIncomingCall(with: id, update: update) { error in
                // A refusal is the system's business, not a fault: the
                // number is blocked, or the person is in a Focus that the
                // caller isn't allowed through. The call is simply not
                // taken, and the server rings their other phones.
                finished.resume(returning: error == nil)
            }
        }
    }

    func rename(id: UUID, to peer: SIPPeer) {
        let update = CXCallUpdate()
        update.remoteHandle = Self.handle(for: peer)
        update.localizedCallerName = peer.name.isEmpty ? nil : peer.name
        provider.reportCall(with: id, updated: update)
    }

    /// A picture went into this call, or came out of it. The system shows
    /// it as a video call from then on — in Recents, and on the lock screen
    /// if it is locked while the call is up.
    func reportVideo(id: UUID, on: Bool) {
        let update = CXCallUpdate()
        update.hasVideo = on
        provider.reportCall(with: id, updated: update)
    }

    func reportRingingThere(id: UUID) {
        provider.reportOutgoingCall(with: id, startedConnectingAt: nil)
    }

    func reportAnswered(id: UUID) {
        provider.reportOutgoingCall(with: id, connectedAt: nil)
    }

    func reportEnded(id: UUID, _ ending: SystemCallEnding) {
        provider.reportCall(with: id, endedAt: nil, reason: Self.reason(ending))
    }

    private static func reason(_ ending: SystemCallEnding) -> CXCallEndedReason {
        switch ending {
        case .hungUp: return .remoteEnded
        case .missed: return .unanswered
        case .failed: return .failed
        case .answeredElsewhere: return .answeredElsewhere
        }
    }

    private static func handle(for peer: SIPPeer) -> CXHandle {
        let number = peer.number.trimmingCharacters(in: .whitespaces)
        guard !number.isEmpty else { return CXHandle(type: .generic, value: "Number withheld") }
        // An extension is not a phone number, and iOS tries to pretty-print
        // one that claims to be: anything that isn't a dialable number goes
        // through as it is.
        let dialable = number.allSatisfy { $0.isNumber || "+*#".contains($0) }
        return CXHandle(type: dialable && number.count > 6 ? .phoneNumber : .generic, value: number)
    }

    // MARK: - Asking the system

    func ask(_ request: SystemCallRequest) {
        let action: CXAction
        switch request {
        case .start(let id, let peer):
            let start = CXStartCallAction(call: id, handle: Self.handle(for: peer))
            start.isVideo = false
            start.contactIdentifier = peer.name.isEmpty ? nil : peer.name
            action = start
        case .answer(let id):
            action = CXAnswerCallAction(call: id)
        case .end(let id):
            action = CXEndCallAction(call: id)
        case .mute(let id, let muted):
            action = CXSetMutedCallAction(call: id, muted: muted)
        case .tone(let id, let digit):
            let tone = CXPlayDTMFCallAction(call: id, digits: String(digit), type: .singleTone)
            action = tone
        }
        controller.request(CXTransaction(action: action)) { [weak self] error in
            guard error != nil else { return }
            // The system wouldn't have it (a call it has already forgotten,
            // or an emergency call in the way). Nothing happens to the call
            // here: the app finds out from SIP, as it does for everything
            // else that goes wrong.
            MainActor.assumeIsolated { self?.refused(request) }
        }
    }

    /// What to do when the system refuses. Nothing is left half-done: a
    /// person pressing Hang up has to hang up even if CallKit has already
    /// forgotten the call, and a call the system wouldn't start never began,
    /// so the app is told to end it and says so rather than sitting there.
    private func refused(_ request: SystemCallRequest) {
        switch request {
        case .start(let id, _), .answer(let id):
            onRequest?(.end(id))
        case .end, .mute, .tone:
            onRequest?(request)
        }
    }

    // MARK: - What the system says back

    fileprivate func deliver(_ request: SystemCallRequest) { onRequest?(request) }
    fileprivate func audio(_ on: Bool) { onAudio?(on) }
    fileprivate func reset() { onReset?() }

    /// CallKit's delegate calls arrive on the main queue, but the protocol
    /// itself promises nothing about where, so the conformance lives on its
    /// own small object and steps onto the main actor explicitly. Only
    /// plain values (a call's id, a digit) cross over.
    private final class Bridge: NSObject, CXProviderDelegate, @unchecked Sendable {
        weak var owner: CallKitCalls?

        func providerDidReset(_ provider: CXProvider) {
            MainActor.assumeIsolated { owner?.reset() }
        }

        func provider(_ provider: CXProvider, perform action: CXStartCallAction) {
            let id = action.callUUID
            let number = action.handle.value
            let name = action.contactIdentifier ?? number
            MainActor.assumeIsolated { owner?.deliver(.start(id, SIPPeer(name: name, number: number))) }
            action.fulfill()
        }

        func provider(_ provider: CXProvider, perform action: CXAnswerCallAction) {
            let id = action.callUUID
            MainActor.assumeIsolated { owner?.deliver(.answer(id)) }
            action.fulfill()
        }

        func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
            let id = action.callUUID
            MainActor.assumeIsolated { owner?.deliver(.end(id)) }
            action.fulfill()
        }

        func provider(_ provider: CXProvider, perform action: CXSetMutedCallAction) {
            let id = action.callUUID
            let muted = action.isMuted
            MainActor.assumeIsolated { owner?.deliver(.mute(id, muted)) }
            action.fulfill()
        }

        func provider(_ provider: CXProvider, perform action: CXPlayDTMFCallAction) {
            let id = action.callUUID
            let digits = action.digits
            MainActor.assumeIsolated {
                for digit in digits { owner?.deliver(.tone(id, digit)) }
            }
            action.fulfill()
        }

        func provider(_ provider: CXProvider, timedOutPerforming action: CXAction) {
            action.fulfill()
        }

        /// The system has given the app the microphone and the speaker.
        /// Nothing of the call's sound starts before this moment — which is
        /// also what keeps the sound from cutting out on an answered call
        /// (docs/PHASE2.md §14, "Audio path").
        func provider(_ provider: CXProvider, didActivate audioSession: AVAudioSession) {
            MainActor.assumeIsolated { owner?.audio(true) }
        }

        func provider(_ provider: CXProvider, didDeactivate audioSession: AVAudioSession) {
            MainActor.assumeIsolated { owner?.audio(false) }
        }
    }
}
