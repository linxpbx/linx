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

    @Test("a video call whose picture never came is Bad")
    func noPicture() {
        let d = diagnostics([("Answered", 3), ("Camera on", 3.2), ("First sound arrived", 3.5)])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 5000) == "Bad: their picture never arrived")
    }

    @Test("a phone that sent no sound is Bad; a few short stops are only problems")
    func silentAndStops() {
        let d = diagnostics([("Answered", 3), ("First sound arrived", 3.5)])
        #expect(CallVerdict.words(d, soundIn: 5000, soundOut: 0)?.hasPrefix("Bad: no sound went out") == true)
        let stops = diagnostics([("Answered", 3), ("Their picture stopped 3 times, 4.0 s in all", 30)])
        #expect(CallVerdict.words(stops, soundIn: 1, soundOut: 1)?.hasPrefix("Had problems: ") == true)
    }
}
