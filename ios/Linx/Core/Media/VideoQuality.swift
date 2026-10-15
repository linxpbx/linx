import Foundation

// The steps of picture quality a call's camera can be asked for (ADR-081).
// The picture travels on its own direct link (ADR-087), where WebRTC's own
// congestion control adapts it end to end; these are the ceilings it works
// under — 540p normally, 720p with the person's **HD** button — and what the
// camera's source scales to. Sound comes first: when the link can't hold a
// picture at all, the camera goes off (`DirectPicture`).

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

    /// Bits a second for this step when the picture goes in AV1, which
    /// carries a small picture in about two thirds of H.264's data, so a
    /// thin link gets a bigger one. At 540p and 720p it gets all of
    /// H.264's: a phone encodes AV1 in its fastest mode, which saves far
    /// less there, and 720p at two thirds came out full size but soft
    /// (the owner's call after HD, 2026-10-15).
    func bitrate(av1: Bool) -> Int { av1 && self < .high ? bitrate * 2 / 3 : bitrate }

    /// In the words people use for a picture, for the Call details screen.
    var words: String { "\(height)p" }

    static func < (a: VideoQuality, b: VideoQuality) -> Bool { a.rawValue < b.rawValue }
}
