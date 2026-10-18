import Foundation
import Testing

@testable import Linx

/// The switch-over between Asterisk's route and the direct link for a call's
/// sound (ADR-092): every rule, without WebRTC.
struct DirectSoundTests {
    /// Readings of a packet count that grows by `step` each time.
    private func feed(_ sound: inout DirectSound, from: Int, readings: Int, step: Int = 25) -> [SoundRoute?] {
        (1...readings).map { sound.reading(packets: from + $0 * step) }
    }

    @Test("a call stays on Asterisk until the other side says its microphone is on the direct link")
    func waitsForTheirWord() {
        var sound = DirectSound()
        // Packets arrive (their microphone is still silent there): nothing moves.
        let fed = feed(&sound, from: 0, readings: 6)
        #expect(fed.allSatisfy { $0 == nil })
        #expect(sound.playing == .linx)
        sound.heard(.sending)
        // What came before their word is not counted.
        let r1 = sound.reading(packets: 175)
        #expect(r1 == nil)
        let r2 = sound.reading(packets: 200)
        #expect(r2 == .direct)
        #expect(sound.playing == .direct)
    }

    @Test("an older app or browser that says nothing is never played from the direct link")
    func olderSideStaysOnAsterisk() {
        var sound = DirectSound()
        sound.heard(.can)
        let fed = feed(&sound, from: 0, readings: 20)
        #expect(fed.allSatisfy { $0 == nil })
        #expect(sound.playing == .linx)
    }

    @Test("direct sound that stops for a second and a half goes back to Asterisk at once")
    func fallsBack() {
        var sound = DirectSound()
        sound.heard(.sending)
        _ = feed(&sound, from: 0, readings: 3)
        #expect(sound.playing == .direct)
        // Opus sends in silence too (DTX): a reading or two with nothing new
        // is a quiet moment, not a broken route.
        let r3 = sound.reading(packets: 75)
        #expect(r3 == nil)
        let r4 = sound.reading(packets: 75)
        #expect(r4 == nil)
        let r5 = sound.reading(packets: 76)
        #expect(r5 == nil)
        #expect(sound.playing == .direct)
        // Three readings with nothing: the route has stopped.
        let r6 = sound.reading(packets: 76)
        #expect(r6 == nil)
        let r7 = sound.reading(packets: 76)
        #expect(r7 == nil)
        let r8 = sound.reading(packets: 76)
        #expect(r8 == .linx)
    }

    @Test("after falling back, the direct link must be steady for five seconds before it is used again")
    func returnsOnlyWhenSteady() {
        var sound = DirectSound()
        sound.heard(.sending)
        _ = feed(&sound, from: 0, readings: 3)
        for _ in 0..<3 { _ = sound.reading(packets: 75) }
        #expect(sound.playing == .linx)
        let back = feed(&sound, from: 75, readings: DirectSound.toReturn)
        #expect(back.dropLast().allSatisfy { $0 == nil })
        #expect(back.last == .direct)
    }

    @Test("what the other side plays from decides whether Asterisk's copy of this phone's voice is quiet")
    func theirRoute() {
        var sound = DirectSound()
        #expect(sound.theyPlay == .linx)
        sound.heard(.direct)
        #expect(sound.theyPlay == .direct)
        sound.heard(.linx)
        #expect(sound.theyPlay == .linx)
    }

    @Test("a link that goes puts both routes back on Asterisk")
    func linkGone() {
        var sound = DirectSound()
        let gone = sound.linkGone()
        #expect(!gone, "nothing to change on a call that never went direct")
        sound.heard(.sending)
        sound.heard(.direct)
        _ = feed(&sound, from: 0, readings: 3)
        let goneNow = sound.linkGone()
        #expect(goneNow)
        #expect(sound.playing == .linx)
        #expect(sound.theyPlay == .linx)
        #expect(!sound.theySend)
    }

    @Test("the sound's words travel on the link the way the browser reads them")
    func words() throws {
        let data = try JSONEncoder().encode(PictureSignal(sound: .sending))
        #expect(String(decoding: data, as: UTF8.self) == #"{"sound":"sending"}"#)
        let back = try JSONDecoder().decode(PictureSignal.self, from: Data(#"{"sound":"direct","session":7}"#.utf8))
        #expect(back.sound == .direct)
        // A word this app doesn't know (a later version's) is not an error:
        // the message is read without it.
        let later = try JSONDecoder().decode(PictureSignal.self, from: Data(#"{"sound":"louder","session":7}"#.utf8))
        #expect(later.sound == nil)
        #expect(later.session == 7)
    }

    @Test("the record says how a route's sound arrived, leaving out silence filled in")
    func quality() throws {
        let stats: [[String: NSObject]] = [
            [
                "type": "inbound-rtp" as NSString, "kind": "audio" as NSString,
                "packetsReceived": 980 as NSNumber, "packetsLost": 20 as NSNumber,
                "totalSamplesReceived": 960_000 as NSNumber, "concealedSamples": 19_200 as NSNumber,
                "silentConcealedSamples": 9_600 as NSNumber,
                "jitterBufferDelay": 600 as NSNumber, "jitterBufferEmittedCount": 10_000 as NSNumber,
            ],
            ["type": "inbound-rtp" as NSString, "kind": "video" as NSString, "packetsReceived": 5000 as NSNumber],
        ]
        let quality = try #require(SoundQuality(stats))
        #expect(quality == SoundQuality(packets: 980, lost: 20, madeUpPerMille: 10, bufferMs: 60))
        #expect(quality.words == "980 packets, 20 lost (2.0%), 1.0% made up, jitter buffer 60 ms")
        #expect(SoundQuality([["type": "outbound-rtp" as NSString, "kind": "audio" as NSString]]) == nil)
    }
}
