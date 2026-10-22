import Foundation

/// What this phone makes of a call once it is over: Good, Had problems or
/// Bad, with the reasons in words, at the top of the report it sends Linx.
/// An admin reads it beside Linx's own judgement of the same call (Calls →
/// Call quality, internal/callquality), so a call that went wrong is known
/// about even when nobody says so (owner, 2026-10-16).
enum CallVerdict {
    /// The verdict line's words, or nil for a call that was never answered.
    static func words(_ diagnostics: CallDiagnostics, soundIn: Int?, soundOut: Int?) -> String? {
        let steps = diagnostics.steps
        let start = steps.first?.at
        func at(_ what: String) -> Double? {
            guard let start, let step = steps.first(where: { $0.what == what }) else { return nil }
            return step.at.timeIntervalSince(start)
        }
        // The moment the call was really answered — the 200 OK, either way
        // round (`CallDiagnostics.answered`). Not "Answer ready", which is
        // when the answer was *prepared*, seconds earlier on an incoming
        // call: measuring from that made every answered call read as though
        // its sound was 4 s late (the owner's calls, 2026-10-22).
        guard let answered = at(CallDiagnostics.answered) else { return nil }
        var bad: [String] = []
        var problems: [String] = []
        if soundOut == 0 { bad.append("no sound went out from this phone") }
        if soundIn == 0 { bad.append("no sound came in") }
        if let sound = at("First sound arrived") {
            let late = sound - answered
            if late >= 6 {
                bad.append(String(format: "their sound took %.0f s to arrive", late))
            } else if late >= 3 {
                problems.append(String(format: "their sound took %.0f s to arrive", late))
            }
        }
        // A picture is missing only when the other side said it was sending
        // one. Every Linx-to-Linx call opens a picture link while it rings
        // (ADR-087), and a one-way video call is perfectly normal (ADR-079),
        // so neither of those is a fault: judging by the link made every
        // voice call read "Bad" (the owner's calls, 2026-10-22).
        if steps.contains(where: { Self.theySaidSending($0.what) }),
            !steps.contains(where: { $0.what == "Their picture arrived" })
        {
            bad.append("their picture never arrived")
        }
        if let stopped = steps.lazy.compactMap({ Self.stopped($0.what) }).last {
            let words = String(format: "their picture stopped %d times, %.0f s in all", stopped.times, stopped.seconds)
            if stopped.seconds >= 10 {
                bad.append(words)
            } else if stopped.seconds >= 3 || stopped.times >= 3 {
                problems.append(words)
            }
        }
        let held = steps.filter { $0.what.hasPrefix("Network held back") }.count
        if held > 0 { problems.append("the network held back what this phone sent \(held) times") }
        if diagnostics.movedToRelay { problems.append("moved onto the relay to get its sound through") }
        if steps.contains(where: { $0.what.hasPrefix("Sound taken by the app") }) {
            problems.append("iOS never handed the call its sound")
        }
        if !bad.isEmpty { return "Bad: " + (bad + problems).joined(separator: "; ") }
        if !problems.isEmpty { return "Had problems: " + problems.joined(separator: "; ") }
        return "Good"
    }

    /// Whether a step is the screen saying the other side announced a
    /// picture of its own (`PhoneModel`'s "Screen: video, theirs …").
    static func theySaidSending(_ what: String) -> Bool {
        what == "Screen: video, theirs waiting" || what == "Screen: video, theirs shown"
    }

    /// "Their picture stopped 8 times, 21.1 s in all" read back.
    static func stopped(_ what: String) -> (times: Int, seconds: Double)? {
        let prefix = "Their picture stopped "
        guard what.hasPrefix(prefix) else { return nil }
        let words = what.dropFirst(prefix.count).split(separator: " ")
        guard words.count >= 3, let times = Int(words[0]), let seconds = Double(words[2]) else { return nil }
        return (times, seconds)
    }
}
