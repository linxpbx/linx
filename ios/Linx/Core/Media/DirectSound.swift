import Foundation

// The sound of a Linx-to-Linx call on the direct link (ADR-092,
// docs/DIRECT_SOUND.md). The call starts on Asterisk's route and stays
// connected there; this decides, from what actually arrives, when this
// phone plays the other side's sound from the direct link instead, and when
// it goes back. No WebRTC in here, so every rule is tested on its own.

/// Which route a call's sound is on.
enum SoundRoute: String, Sendable, Equatable {
    /// Through Asterisk, as every call starts.
    case linx
    /// Device to device, on the picture's link.
    case direct
}

/// What the two sides tell each other about the sound, on the call link
/// (`PictureSignal.sound`). An app or a browser that doesn't know about it
/// says nothing, and is never sent sound on the direct link: it would play
/// it on top of Asterisk's and every word would be heard twice.
enum SoundWord: String, Codable, Sendable {
    /// "I can take the sound on the direct link." Sent once a pairing is up.
    case can
    /// "My microphone is on the direct link now." Sent on hearing `can`.
    case sending
    /// "I'm playing your sound from the direct link": the other side may
    /// quiet what it sends through Asterisk.
    case direct
    /// "I'm playing your sound from Asterisk": it must be sent there again.
    case linx
}

/// The switch-over, one reading at a time (every `DirectSound.every`).
struct DirectSound: Equatable, Sendable {
    /// How often the direct link's sound is read.
    static let every: Duration = .milliseconds(500)
    /// Readings with new sound before this phone plays the direct link: a
    /// second of it arriving steadily.
    static let toSwitch = 2
    /// Readings with nothing new before it goes back to Asterisk. Opus sends
    /// a packet at least every 400 ms even in silence (DTX), so a second and
    /// a half with none means the route has stopped, not that nobody spoke.
    static let toFallBack = 3
    /// Readings with new sound before going direct again after falling
    /// back: five seconds, so a route that comes and goes isn't followed.
    static let toReturn = 10

    /// The other side said its microphone is on the direct link.
    private(set) var theySend = false
    /// Which route this phone plays the other side's sound from.
    private(set) var playing: SoundRoute = .linx
    /// Whether the other side plays this phone's sound from the direct link,
    /// so what goes through Asterisk can be quiet.
    private(set) var theyPlay: SoundRoute = .linx
    private var lastPackets: Int?
    private var arriving = 0
    private var quiet = 0
    private var fellBack = false

    /// The other side said something about the sound.
    mutating func heard(_ word: SoundWord) {
        switch word {
        case .can: break
        case .sending:
            // What arrived before this was silence (their microphone was
            // still off on this link): counted afresh from here.
            theySend = true
            arriving = 0
        case .direct: theyPlay = .direct
        case .linx: theyPlay = .linx
        }
    }

    /// The direct link's sound packets from the other side so far. The
    /// answer is the route to play from now on, when it changes.
    mutating func reading(packets: Int) -> SoundRoute? {
        let new = lastPackets.map { packets > $0 } ?? false
        lastPackets = packets
        if new {
            arriving += 1
            quiet = 0
        } else {
            quiet += 1
            arriving = 0
        }
        switch playing {
        case .linx:
            guard theySend, arriving >= (fellBack ? Self.toReturn : Self.toSwitch) else { return nil }
            playing = .direct
            return .direct
        case .direct:
            guard quiet >= Self.toFallBack else { return nil }
            playing = .linx
            fellBack = true
            return .linx
        }
    }

    /// The link went (a new pairing, the call moving into a group): back to
    /// Asterisk at once, both ways. The answer says whether anything changed.
    mutating func linkGone() -> Bool {
        let changed = playing == .direct || theyPlay == .direct
        self = DirectSound()
        return changed
    }
}

/// How well a route's sound arrived, from WebRTC's own count of it: what the
/// call record says at the end, for each route, so the phone bench can
/// compare the direct link with Asterisk's route (docs/DIRECT_SOUND.md §6).
struct SoundQuality: Equatable, Sendable {
    var packets = 0
    var lost = 0
    /// Sound made up to cover what didn't arrive, out of all played, in
    /// thousandths; silence filled in during DTX isn't counted.
    var madeUpPerMille = 0
    /// How long sound waited in the jitter buffer on average, in ms.
    var bufferMs = 0

    init(packets: Int = 0, lost: Int = 0, madeUpPerMille: Int = 0, bufferMs: Int = 0) {
        self.packets = packets
        self.lost = lost
        self.madeUpPerMille = madeUpPerMille
        self.bufferMs = bufferMs
    }

    /// From one connection's statistics (`inbound-rtp`, audio).
    init?(_ values: [[String: NSObject]]) {
        let audio = values.filter { $0["type"] as? String == "inbound-rtp" && $0["kind"] as? String == "audio" }
        guard !audio.isEmpty else { return nil }
        func sum(_ key: String) -> Double { audio.reduce(0) { $0 + (($1[key] as? NSNumber)?.doubleValue ?? 0) } }
        packets = Int(sum("packetsReceived"))
        lost = max(0, Int(sum("packetsLost")))
        let samples = sum("totalSamplesReceived")
        let madeUp = sum("concealedSamples") - sum("silentConcealedSamples")
        madeUpPerMille = samples > 0 ? Int((max(0, madeUp) / samples * 1000).rounded()) : 0
        let emitted = sum("jitterBufferEmittedCount")
        bufferMs = emitted > 0 ? Int((sum("jitterBufferDelay") / emitted * 1000).rounded()) : 0
    }

    var words: String {
        let share = packets + lost > 0 ? Double(lost) / Double(packets + lost) * 100 : 0
        return "\(packets) packets, \(lost) lost (\(String(format: "%.1f", share))%), "
            + "\(String(format: "%.1f", Double(madeUpPerMille) / 10))% made up, jitter buffer \(bufferMs) ms"
    }
}
