import Foundation

// How good a picture a call sends, and who decides (ADR-081, owner's ask
// 2026-10-05: "adaptive based on connection speed — HD on a fast link, less on
// a slow one, and drop the video altogether when it starts hurting the call").
//
// Three rules, in this order:
//   1. **Sound comes first.** When the link can't hold a picture at all the
//      camera goes off and the call carries on as a phone call.
//   2. **Never more than the road allows.** A relayed call can't use more than
//      Linx's relay will carry for one call, whatever the phone's own link
//      could manage: asking the relay for more doesn't slow the picture down,
//      it loses packets and freezes it (owner, 2026-10-05).
//   3. **Down at once, up slowly.** A picture too big for the link is what
//      breaks the voice, so it comes down on the first bad reading; going up
//      waits until the link has had room to spare for a quarter of a minute,
//      so a call doesn't flicker between sizes.

/// One step of picture quality: what the camera is asked for and what the
/// encoder is allowed to spend on it.
enum VideoQuality: Int, CaseIterable, Comparable, Sendable {
    /// A face, and little else — for a train, a lift or a bad hotel.
    case thin = 0
    case low
    /// What every call starts at: known to work on almost any link, including
    /// the first seconds of a mobile call before anything is measured.
    case standard
    case high
    /// 720p. Only on a link with real room, and only where the picture doesn't
    /// have to go through Linx's relay (or through a relay that allows it).
    case hd

    var width: Int {
        switch self {
        case .thin: return 320
        case .low: return 480
        case .standard: return 640
        case .high: return 960
        case .hd: return 1280
        }
    }

    var height: Int {
        switch self {
        case .thin: return 240
        case .low: return 360
        case .standard: return 480
        case .high: return 540
        case .hd: return 720
        }
    }

    var frameRate: Int {
        switch self {
        case .thin: return 15
        case .low: return 20
        case .standard: return 24
        case .high, .hd: return 30
        }
    }

    /// Bits a second for the picture at this step.
    var bitrate: Int {
        switch self {
        case .thin: return 200_000
        case .low: return 350_000
        case .standard: return 600_000
        case .high: return 900_000
        case .hd: return 1_500_000
        }
    }

    /// What the link has to have spare before this step is worth asking for:
    /// the step itself and a third again, so a picture isn't raised onto a
    /// link that only just fits it.
    var needs: Int { bitrate * 4 / 3 }

    /// In the words people use for a picture, for the Call details screen.
    var words: String { "\(height)p" }

    var lower: VideoQuality? { VideoQuality(rawValue: rawValue - 1) }
    var higher: VideoQuality? { VideoQuality(rawValue: rawValue + 1) }

    static func < (a: VideoQuality, b: VideoQuality) -> Bool { a.rawValue < b.rawValue }

    /// The most a call may use. Everything on a route straight to the other
    /// side; on a relayed one, what the relay will carry for one call, less the
    /// voice and the weight of the packets themselves. A server too old to say
    /// (`budget` nil) is held to `standard`, which every Linx relay has always
    /// carried.
    static func ceiling(route: MediaConnection.Route, relay budget: Int?) -> VideoQuality {
        guard route == .relayed else { return .hd }
        guard let budget, budget > 0 else { return .standard }
        // 15% for RTP, SRTP, UDP and TURN's own headers, and room for the
        // voice beside the picture.
        let room = budget * 85 / 100 - SDPTweaks.opusMaxBitrate * 2
        return allCases.last { $0.bitrate <= room } ?? .thin
    }
}

/// Which step a call is on, and when to move. It keeps no WebRTC in it at all,
/// so the rules can be read and tested on their own.
struct VideoLadder {
    /// Readings of room to spare before the picture is made better. Readings
    /// come every three seconds while a picture is going out, so this is about
    /// ten seconds of a link that is genuinely good — long enough that a lift,
    /// a lorry or a mast handover never makes the picture jump about.
    static let steadyReadings = 3
    /// Readings saying the link is too tight before the picture is made
    /// smaller. Two, not one: a single reading is wrong too often to act on.
    static let tightReadings = 2
    /// Readings ignored when a camera has just come on, for going **down**
    /// only. A link's room is measured from what is flowing on it, and for the
    /// first few seconds of a picture that measurement is still catching up —
    /// so a perfectly good link reads as a poor one and the picture would be
    /// made worse the moment it appeared (owner, 2026-10-05: "it shows my
    /// actual camera with the same quality but then... lower quality").
    static let settlingReadings = 2

    private(set) var quality: VideoQuality
    private var good = 0
    private var tight = 0
    private var settling = Self.settlingReadings

    init(start: VideoQuality = .standard) { quality = start }

    /// The step to move to, or nil to stay where it is. `spare` is what the
    /// link says it has room for; `ceiling` is the most this call may use.
    mutating func reading(spare: Int?, ceiling: VideoQuality) -> VideoQuality? {
        if quality > ceiling {
            reset()
            quality = ceiling
            return ceiling
        }
        guard let spare else { return nil }
        if spare < quality.bitrate {
            good = 0
            if settling > 0 {
                settling -= 1
                return nil
            }
            tight += 1
            guard tight >= Self.tightReadings, let lower = quality.lower else { return nil }
            reset()
            quality = lower
            return lower
        }
        tight = 0
        if settling > 0 { settling -= 1 }
        guard let higher = quality.higher, higher <= ceiling, spare >= higher.needs else {
            good = 0
            return nil
        }
        good += 1
        guard good >= Self.steadyReadings else { return nil }
        reset()
        quality = higher
        return higher
    }

    /// The counts start again whenever the step changes: each step is judged on
    /// its own readings, not the one before's.
    private mutating func reset() {
        good = 0
        tight = 0
        settling = 0
    }
}
