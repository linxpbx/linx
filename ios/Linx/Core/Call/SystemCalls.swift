import Foundation

// A call as the *system* knows it (docs/PHASE2.md §5, §14 item 1). On a
// locked or sleeping phone the call doesn't belong to the app: it belongs to
// iOS, which rings with the person's own ringtone, shows it on the lock
// screen, puts it in the Phone app's Recents, and answers it from a car, a
// headset or a watch. CallKit is how an app joins that, and it is not
// optional: a VoIP push that doesn't report a call gets the app killed, and
// an app that keeps doing it stops being sent pushes at all.
//
// Everything a person does to a call therefore goes one way round:
//
//   the app's own buttons ──ask──▶ the system ──tell──▶ the app does it
//
// so the lock screen, CarPlay and the app's own screen can never disagree
// about what the call is doing. The two ends of that are behind
// `SystemCalls`: `CallKitCalls` is the real one, and `NoSystemCalls` is for
// where CallKit may not be used — mainland China (ADR-042, ADR-078) — where
// the app rings on its own screen instead and the same code path runs.

/// What a person (or a car, or a headset) asked of a call.
enum SystemCallRequest: Equatable, Sendable {
    /// Place a call. The system knows about it from the start, so it shows
    /// in Recents and a car can hang it up.
    case start(UUID, SIPPeer)
    case answer(UUID)
    case end(UUID)
    case mute(UUID, Bool)
    case tone(UUID, Character)

    var id: UUID {
        switch self {
        case .start(let id, _), .answer(let id), .end(let id), .mute(let id, _), .tone(let id, _):
            return id
        }
    }
}

/// How a call the system knew about finished.
enum SystemCallEnding: Equatable, Sendable {
    /// One side put the phone down: an ordinary finished call.
    case hungUp
    /// It rang and nobody took it (the caller gave up, or it went to
    /// voicemail): the phone shows a missed call.
    case missed
    /// It never got through at all.
    case failed
    /// Someone answered it on another of their phones.
    case answeredElsewhere
}

@MainActor protocol SystemCalls: AnyObject {
    /// The system telling the app to do something. Every button the person
    /// can press — in the app, on the lock screen, in a car — arrives here.
    var onRequest: ((SystemCallRequest) -> Void)? { get set }
    /// The system has handed the app the microphone and the speaker, or
    /// taken them back. Nothing of a call's sound may start before this
    /// (docs/PHASE2.md §14, "Audio path").
    var onAudio: ((Bool) -> Void)? { get set }
    /// The system forgot everything it knew (it restarted): whatever the
    /// app had is gone with it.
    var onReset: (() -> Void)? { get set }

    /// Reports a call coming in, and answers whether the system took it.
    /// This is the first thing that happens when a wake push arrives — no
    /// network, no database, nothing else first.
    func reportIncoming(id: UUID, from: SIPPeer) async -> Bool
    /// The caller's name, once the invitation itself says who it is.
    func rename(id: UUID, to peer: SIPPeer)
    /// Their phone is ringing (an outgoing call only).
    func reportRingingThere(id: UUID)
    /// The call is up, in both directions.
    func reportAnswered(id: UUID)
    /// The call is over, however it went.
    func reportEnded(id: UUID, _ ending: SystemCallEnding)
    /// Asks the system for something on the person's behalf. The answer
    /// comes back through `onRequest`, which is the only place the app acts.
    func ask(_ request: SystemCallRequest)
}

/// Where CallKit may not be used (mainland China, ADR-078), and for the
/// screenshot harness and the tests. The app rings on its own screen, and
/// what the person asks for is simply done — the same one path as the real
/// one, with nothing in the middle.
@MainActor final class NoSystemCalls: SystemCalls {
    var onRequest: ((SystemCallRequest) -> Void)?
    var onAudio: ((Bool) -> Void)?
    var onReset: (() -> Void)?

    func reportIncoming(id: UUID, from: SIPPeer) async -> Bool { true }
    func rename(id: UUID, to peer: SIPPeer) {}
    func reportRingingThere(id: UUID) {}
    func reportAnswered(id: UUID) {}
    func reportEnded(id: UUID, _ ending: SystemCallEnding) {}
    func ask(_ request: SystemCallRequest) { onRequest?(request) }
}
