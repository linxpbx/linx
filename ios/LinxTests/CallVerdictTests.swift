import Foundation
import Testing

@testable import Linx

// The verdict a phone puts at the top of the report it sends after a call
// (`CallVerdict`): what an admin reads beside Linx's own judgement.

struct CallVerdictTests {
    let start = Date(timeIntervalSince1970: 1_000_000)

    func diagnostics(_ steps: [(String, Double)]) -> CallDiagnostics {
        var d = CallDiagnostics()
        d.steps =
            [.init(what: "Call started", at: start)]
            + steps.map { .init(what: $0.0, at: start.addingTimeInterval($0.1)) }
        return d
    }

    @Test("a call that went well is Good, and one never answered has no verdict")
    func good() {
        let d = diagnostics([
            ("Answered", 5), ("First sound arrived", 5.5), ("Their picture stopped 0 times, 0.0 s in all", 60),
        ])
        #expect(CallVerdict.words(d, soundIn: 9000, soundOut: 9000) == "Good")
        #expect(CallVerdict.words(diagnostics([("Ringing", 1)]), soundIn: 0, soundOut: 0) == nil)
    }

    @Test("the owner's Etisalat call reads as Bad, with what went wrong")
    func etisalat() {
        let d = diagnostics([
            ("Answered", 8.3), ("Network held back what this phone sent for 2.3 s", 12), ("First sound arrived", 16.9),
            ("Camera on", 9), ("Their picture arrived", 16.2),
            ("Their picture stopped 8 times, 21.1 s in all", 87),
        ])
        let words = CallVerdict.words(d, soundIn: 18801, soundOut: 42181) ?? ""
        #expect(words.hasPrefix("Bad: "))
        #expect(words.contains("their sound took 9 s to arrive"))
        #expect(words.contains("their picture stopped 8 times, 21 s in all"))
        #expect(words.contains("held back what this phone sent 1 times"))
    }

    @Test("a picture they said they were sending and never sent is Bad")
    func noPicture() {
        let d = diagnostics([
            ("Answered", 3), ("First sound arrived", 3.5), ("Screen: video, theirs waiting", 9),
        ])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 5000) == "Bad: their picture never arrived")
    }

    @Test("a one-way video call is no fault: this camera on and theirs off is Good")
    func oneWayVideoIsFine() {
        // ADR-079: one side turning video on is perfectly normal, and the
        // other side's camera staying off is its person's choice. Judging
        // by "Camera on" made every such call read Bad (the owner's call of
        // 2026-10-22, where the far phone did receive this picture).
        let d = diagnostics([
            ("Answered", 3), ("First sound arrived", 3.5), ("Camera on", 20),
            ("Screen: video, theirs off", 20.1),
        ])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 5000) == "Good")
    }

    @Test("a voice call is Good: every call opens a picture link while it rings")
    func voiceCallIsGood() {
        // ADR-087 opens the link on every Linx-to-Linx call, so the link is
        // no evidence of a video call. This read "Bad: their picture never
        // arrived" on both of the owner's voice calls of 2026-10-22.
        let d = diagnostics([
            ("Picture link open, waiting for the answer", 1), ("Picture link open while ringing", 1.2),
            ("Answered", 3), ("First sound arrived", 3.5), ("Picture link ready (set up while ringing)", 3.6),
        ])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 5000) == "Good")
    }

    @Test("the sound is timed from the answer itself, not from the answer being made ready")
    func soundFromTheAnswer() {
        // An incoming call prepares its answer while it rings (seconds
        // before the person answers); measuring from that made every
        // answered call read "their sound took 4 s to arrive".
        let d = diagnostics([
            ("Answer ready", 0.8), ("Answered", 3.6), ("First sound arrived", 4.9),
        ])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 5000) == "Good")
        // A call with no answer at all still has no verdict.
        let unanswered = diagnostics([("Answer ready", 0.8), ("Ringing", 1)])
        #expect(CallVerdict.words(unanswered, soundIn: 0, soundOut: 0) == nil)
    }

    @Test("a phone that sent no sound is Bad; a few short stops are only problems")
    func silentAndStops() {
        let d = diagnostics([("Answered", 3), ("First sound arrived", 3.5)])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 0)?.hasPrefix("Bad: no sound went out") == true)
        let stops = diagnostics([("Answered", 3), ("Their picture stopped 3 times, 4.0 s in all", 30)])
        #expect(CallVerdict.words(stops, soundIn: 1, soundOut: 1)?.hasPrefix("Had problems: ") == true)
    }
}
