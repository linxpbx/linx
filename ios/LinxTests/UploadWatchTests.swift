import Foundation
import Testing

@testable import Linx

// A network that holds back what a phone sends over UDP (the owner's iPhone
// on Etisalat, 2026-10-16: 2.3 s holds every 3–4 s for the first 40 s of a
// call) is told apart from one that merely loses or delays a little, and the
// call moves onto the secure route on the second hold (`UploadWatch`,
// `StallCounter`). Nothing here touches a network.

struct UploadWatchTests {
    let start = Date(timeIntervalSince1970: 1_000_000)

    /// Questions every 0.25 s, held for `hold` from `from`, answered in a
    /// burst when the hold ends; the verdicts in order, ignoring `.fine`.
    func run(holds: [(from: Double, hold: Double)], until end: Double, rtt: Double = 0.05)
        -> [StallCounter.Verdict]
    {
        var counter = StallCounter(stall: UploadWatch.stall, within: UploadWatch.within)
        var events: [(at: Double, sent: Double)] = []
        var t = 0.0
        while t < end {
            var arrives = t + rtt
            for h in holds where t >= h.from && t < h.from + h.hold { arrives = h.from + h.hold + rtt }
            events.append((arrives, t))
            t += 0.25
        }
        events.sort { $0.at < $1.at }
        var verdicts: [StallCounter.Verdict] = []
        for e in events {
            let v = counter.answered(after: e.at - e.sent, now: start.addingTimeInterval(e.at))
            if v != .fine { verdicts.append(v) }
        }
        return verdicts
    }

    @Test("Etisalat's pattern: the first hold is noted, the second moves the call")
    func etisalat() {
        let verdicts = run(holds: [(5, 2.3), (8.3, 2.3), (11.6, 2.3)], until: 15)
        #expect(verdicts.count == 3)
        guard case .stalled(let first) = verdicts[0] else {
            Issue.record("the first hold should only be noted")
            return
        }
        #expect(first > 2.2 && first < 2.5)
        guard case .keepsStalling = verdicts[1] else {
            Issue.record("the second hold should move the call")
            return
        }
    }

    @Test("a slow but steady link never stalls: a ship's satellite at 0.8 s")
    func satellite() {
        #expect(run(holds: [], until: 60, rtt: 0.8).isEmpty)
    }

    @Test("one hold is not a pattern, and two far apart are not either")
    func oneHold() {
        let one = run(holds: [(5, 2.3)], until: 30)
        #expect(one.count == 1)
        if case .keepsStalling = one.first { Issue.record("one hold moved the call") }
        let apart = run(holds: [(5, 2.3), (45, 2.3)], until: 50)
        #expect(apart.allSatisfy { if case .keepsStalling = $0 { false } else { true } })
    }

    @Test("a short hiccup is not a stall")
    func hiccup() {
        #expect(run(holds: [(5, 0.6), (8, 0.6), (11, 0.6)], until: 15).isEmpty)
    }

    @Test("the relay's UDP address is found, and a STUN answer is matched to its question")
    func addressAndAnswers() {
        let urls = ["turns:turn.example.com:443?transport=tcp", "turn:turn.example.com:443?transport=udp"]
        #expect(UploadWatch.udpRelay(in: urls)?.host == "turn.example.com")
        #expect(UploadWatch.udpRelay(in: urls)?.port == 443)
        #expect(UploadWatch.udpRelay(in: ["turn:10.0.0.1"])?.port == 3478)
        #expect(UploadWatch.udpRelay(in: ["turns:turn.example.com:443?transport=tcp"]) == nil)
        let (request, id) = UploadWatch.bindingRequest()
        #expect(request.count == 20 && request[0] == 0 && request[1] == 1)
        var answer = Data([0x01, 0x01, 0x00, 0x0C, 0x21, 0x12, 0xA4, 0x42]) + id
        answer += Data(count: 12)
        #expect(UploadWatch.answeredID(answer) == id)
        #expect(UploadWatch.answeredID(request) == nil)
    }

    @Test("the side line takes whole STUN answers off a TCP stream, however it arrives")
    func sideLineFraming() {
        let (_, first) = UploadWatch.bindingRequest()
        let (_, second) = UploadWatch.bindingRequest()
        func answer(_ id: Data) -> Data {
            Data([0x01, 0x01, 0x00, 0x0C, 0x21, 0x12, 0xA4, 0x42]) + id + Data(count: 12)
        }
        let stream = answer(first) + answer(second)
        // In two pieces, the cut inside the second answer.
        var buffer = Data(stream.prefix(40))
        let got = SideLine.messages(from: &buffer)
        #expect(got.count == 1 && UploadWatch.answeredID(got[0]) == first)
        #expect(buffer.count == 8)
        buffer += stream.dropFirst(40)
        let rest = SideLine.messages(from: &buffer)
        #expect(rest.count == 1 && UploadWatch.answeredID(rest[0]) == second)
        #expect(buffer.isEmpty)
    }

    @Test("the busy line's question is one whole STUN message carrying its padding")
    func busyLinePadding() {
        let (bytes, id) = SideLine.request(padding: 1000)
        #expect(bytes.count == 20 + 4 + 1000)
        #expect(Int(bytes[2]) << 8 | Int(bytes[3]) == 1004)
        #expect(bytes[20] == 0x80 && bytes[21] == 0xA0)
        #expect(Data(bytes[8..<20]) == id)
        var buffer = bytes
        #expect(SideLine.messages(from: &buffer).count == 1 && buffer.isEmpty)
        #expect(SideLine.request(padding: 0).bytes.count == 20)
    }
}
