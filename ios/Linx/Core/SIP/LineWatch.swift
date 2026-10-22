import Foundation

// A line to Linx that has stopped answering (ADR-097). A mobile network can
// hold a TCP connection open and carry nothing on it: the socket is not
// closed, so nothing tells the app anything, and what the phone sends is
// quietly lost. The owner's call from China at 13:13:48 on 2026-10-22 went
// exactly that way — the INVITE never reached Linx (it answered the CANCEL
// twenty seconds later with "Call/Transaction Does Not Exist"), no 100
// Trying ever came back, and a backlog of six older messages then arrived
// inside 400 ms, which is a stall flushing. The person sat listening to
// nothing.
//
// So the line is watched: after this phone asks Linx for something, Linx
// must say *something* back. If it doesn't, the line is asked outright (a
// REGISTER, which is always answered). If that is not answered either, the
// line is not there any more, whatever the socket believes, and is opened
// again. No WebRTC and no networking in here, so every rule is tested.

/// Watching one line, one reading at a time (every `LineWatch.every`).
struct LineWatch: Equatable, Sendable {
    /// How often the line is looked at.
    static let every: Duration = .milliseconds(500)
    /// Readings with nothing heard after this phone asked for something
    /// before the line is asked outright: two seconds. A 100 Trying comes
    /// back in well under a second, even from China (the owner's calls show
    /// 200 ms round trips).
    static let askAfter = 4
    /// And the reading at which a line that answered neither has stopped:
    /// four seconds in all. Long enough that a slow answer is never
    /// mistaken for a stall, short enough that a lost call is dialled again
    /// while the person is still waiting for it to ring.
    static let goneAfter = 8

    /// What to do about the line now.
    enum Next: Equatable {
        case nothing
        /// Ask Linx outright whether it is there.
        case ask
        /// It isn't: open the line again.
        case stalled
    }

    /// Whether an answer is owed.
    private(set) var waiting = false
    private var quiet = 0
    private var asked = false

    /// This phone asked Linx for something that it answers.
    mutating func sent() {
        guard !waiting else { return }
        waiting = true
        quiet = 0
        asked = false
    }

    /// Something arrived on the line. Anything at all will do: what is being
    /// tested is whether the line still carries, not whether the answer was
    /// the hoped-for one.
    mutating func heard() {
        waiting = false
        quiet = 0
        asked = false
    }

    /// One reading.
    mutating func reading() -> Next {
        guard waiting else { return .nothing }
        quiet += 1
        if quiet >= Self.goneAfter {
            waiting = false
            return .stalled
        }
        guard quiet >= Self.askAfter, !asked else { return .nothing }
        asked = true
        return .ask
    }

    /// The line has gone or been replaced: nothing is owed on it.
    mutating func lineWent() { self = LineWatch() }

    /// The requests whose answers prove the line still carries. ACK
    /// answers nothing and is answered by nothing, so it is left out, and
    /// this is asked only of requests this phone sends — a response of its
    /// own waits on the person at the other end, not on Linx.
    static func answered(_ method: String?) -> Bool {
        switch method {
        case "INVITE", "REGISTER", "BYE", "CANCEL", "UPDATE": return true
        default: return false
        }
    }
}
