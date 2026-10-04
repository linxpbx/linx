import AVFoundation
import Foundation
@preconcurrency import WebRTC

// This phone's camera, for a 1:1 video call (docs/PHASE2.md §7). It runs
// only while the person has their video on: a call starts as sound, and the
// camera is switched on by hand and switched off again with the button, with
// the call, or when the network can't carry it.
//
// Small on purpose. 640×480 at 24 frames is a face on a phone screen, costs
// a fraction of what a "high definition" call costs, and goes through a
// one-core Linx server without trouble (CLAUDE.md, the low-bandwidth rule).

/// Whose camera is on, as the screen sees it.
struct CallVideo: Equatable, Sendable {
    /// This phone is sending a picture.
    var mine = false
    /// The other side is sending one.
    var theirs = false

    var on: Bool { mine || theirs }
}

/// The two pictures themselves, for the view to draw. They are references to
/// live WebRTC tracks, not copies of anything.
@MainActor @Observable final class VideoTracks {
    var local: RTCVideoTrack?
    var remote: RTCVideoTrack?
}

/// What a camera can't do, in words the screen shows as they are.
enum CameraTrouble: Error {
    case notAllowed
    case noCamera

    var words: String {
        switch self {
        case .notAllowed:
            return "Linx needs the camera for video calls. Turn it on in Settings → Linx → Camera."
        case .noCamera:
            return "This device has no camera, so it can send sound only."
        }
    }
}

@MainActor final class Camera {
    let track: RTCVideoTrack

    private let capturer: RTCCameraVideoCapturer
    private let source: RTCVideoSource
    /// Which camera a video call starts on — the person's own choice in
    /// Settings, remembered on this phone (`Settings`).
    private var front =
        UserDefaults.standard.object(forKey: Settings.startVideoCallsWithTheFrontCamera) as? Bool ?? true
    private var running = false

    /// What the camera is asked for. The far end sees whatever its own
    /// screen can show; nothing here goes above it.
    static let width = 640
    static let height = 480
    static let frameRate = 24

    init(factory: RTCPeerConnectionFactory) {
        source = factory.videoSource()
        source.adaptOutputFormat(toWidth: Int32(Self.width), height: Int32(Self.height), fps: Int32(Self.frameRate))
        capturer = RTCCameraVideoCapturer(delegate: source)
        track = factory.videoTrack(with: source, trackId: "linx-video")
    }

    /// Asks for the camera the first time and starts it. It answers nothing
    /// and throws what the person needs to be told.
    func start() async throws {
        guard !running else { return }
        guard await Self.allowed() else { throw CameraTrouble.notAllowed }
        guard let device = Self.device(front: front), let format = Self.format(for: device) else {
            throw CameraTrouble.noCamera
        }
        let fps = min(Self.frameRate, Int(format.videoSupportedFrameRateRanges.map(\.maxFrameRate).max() ?? 30))
        try await capturer.startCapture(with: device, format: format, fps: fps)
        running = true
    }

    func stop() {
        guard running else { return }
        running = false
        capturer.stopCapture()
    }

    /// The other camera: the one looking at the person, or the one looking
    /// at what they are looking at.
    func flip() {
        front.toggle()
        guard running, let device = Self.device(front: front), let format = Self.format(for: device) else { return }
        let fps = min(Self.frameRate, Int(format.videoSupportedFrameRateRanges.map(\.maxFrameRate).max() ?? 30))
        capturer.startCapture(with: device, format: format, fps: fps)
    }

    /// Whether this phone's own picture should be shown mirrored, as a
    /// mirror does — only the front camera is.
    var mirrored: Bool { front }

    // MARK: - Picking a camera and a format

    private static func allowed() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized: return true
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .video)
        default: return false
        }
    }

    private static func device(front: Bool) -> AVCaptureDevice? {
        let wanted: AVCaptureDevice.Position = front ? .front : .back
        let cameras = RTCCameraVideoCapturer.captureDevices()
        return cameras.first { $0.position == wanted } ?? cameras.first
    }

    /// The smallest format that is still at least what Linx asks for, so the
    /// camera itself does the shrinking rather than the processor.
    private static func format(for device: AVCaptureDevice) -> AVCaptureDevice.Format? {
        let formats = RTCCameraVideoCapturer.supportedFormats(for: device)
        let sizes = formats.map { format -> (AVCaptureDevice.Format, Int32, Int32) in
            let dimensions = CMVideoFormatDescriptionGetDimensions(format.formatDescription)
            return (format, dimensions.width, dimensions.height)
        }
        let big = sizes.filter { $0.1 >= Int32(width) && $0.2 >= Int32(height) }
        let pick = big.min { ($0.1 * $0.2) < ($1.1 * $1.2) } ?? sizes.max { ($0.1 * $0.2) < ($1.1 * $1.2) }
        return pick?.0
    }
}
