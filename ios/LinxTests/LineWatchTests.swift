import Testing

@testable import Linx

/// Spotting a line to Linx that has stopped answering (ADR-097), without a
/// network: the rules alone.
struct LineWatchTests {
    @Test("a line with nothing owed on it is never read")
    func nothingOwed() {
        var watch = LineWatch()
        #expect(!watch.waiting)
        for _ in 0...LineWatch.goneAfter { #expect(watch.reading() == .nothing) }
    }

    @Test("an answer that comes back leaves the line alone")
    func answered() {
        var watch = LineWatch()
        watch.sent()
        #expect(watch.waiting)
        #expect(watch.reading() == .nothing)
        watch.heard()
        #expect(!watch.waiting)
        for _ in 0...LineWatch.goneAfter { #expect(watch.reading() == .nothing) }
    }

    @Test("two seconds with nothing back asks the line outright, once")
    func asksOnce() {
        var watch = LineWatch()
        watch.sent()
        for _ in 1..<LineWatch.askAfter { #expect(watch.reading() == .nothing) }
        #expect(watch.reading() == .ask)
        // Asking again every half second would be a flood, and tells
        // nothing new: one asking, then the line is judged.
        for _ in (LineWatch.askAfter + 1)..<LineWatch.goneAfter { #expect(watch.reading() == .nothing) }
        #expect(watch.reading() == .stalled)
        #expect(!watch.waiting, "judged: there is nothing more owed on this line")
    }

    @Test("an answer to the asking is enough: the line stays")
    func theAskingIsAnswered() {
        var watch = LineWatch()
        watch.sent()
        for _ in 1..<LineWatch.askAfter { _ = watch.reading() }
        #expect(watch.reading() == .ask)
        watch.heard()
        for _ in 0...LineWatch.goneAfter { #expect(watch.reading() == .nothing) }
    }

    @Test("a second request while one is already owed doesn't start the clock again")
    func oneClock() {
        var watch = LineWatch()
        watch.sent()
        for _ in 1..<LineWatch.askAfter {
            watch.sent()
            #expect(watch.reading() == .nothing)
        }
        #expect(watch.reading() == .ask)
    }

    @Test("a line that went owes nothing")
    func lineWent() {
        var watch = LineWatch()
        watch.sent()
        watch.lineWent()
        #expect(!watch.waiting)
        for _ in 0...LineWatch.goneAfter { #expect(watch.reading() == .nothing) }
    }

    @Test("only the requests Linx answers are waited for")
    func whatIsAnswered() {
        for method in ["INVITE", "REGISTER", "BYE", "CANCEL", "UPDATE"] {
            #expect(LineWatch.answered(method), "\(method) is answered")
        }
        // An ACK answers a 200 OK; nothing answers it.
        #expect(!LineWatch.answered("ACK"))
        // A response has no method of its own, and nothing of Linx's is
        // owed for one: a 180 Ringing waits on the person being rung.
        #expect(!LineWatch.answered(nil))
    }
}
