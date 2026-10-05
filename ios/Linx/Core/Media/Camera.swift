import AVFoundation
import Foundation
@preconcurrency import WebRTC

// This phone's camera, for a 1:1 video call (docs/PHASE2.md §7). It runs
// only while the person has their video on: a call starts as sound, and the
// camera is switched on by hand and switched off again with the button, with
// the call, or when the network can't carry it.
//
// What it asks the camera for follows the link (`VideoQuality`, ADR-081): a
// call starts at 640×480, climbs as far as 720p where there is room for it,
// and comes down to a face on a thin one. The *scaling* is the video source's
// own and costs nothing; the camera itself is only restarted when a step needs
// more pixels than the format in use can give, which is why going up is the
// slow direction (CLAUDE.md, the low-bandwidth rule).

/// Whose camera is on, as the screen sees it.
///
/// `theirs` is what the other side **says** — their SDP, or a stream starting —
/// and it is what decides whether this is a call with a picture in it at all.
/// `theirPicture` is whether frames are really arriving, which decides only
/// what is drawn inside that screen. Keeping them apart is what stops the app
/// flipping between the video screen and the voice screen every few seconds
/// when a picture stutters or when the last frames of a camera just switched
/// off are still in the air (owner, 2026-10-05).
struct CallVideo: Equatable, Sendable {
    /// This phone is sending a picture.
    var mine = false
    /// The other side says they are sending one.
    var theirs = false
    /// And their frames are actually arriving.
    var theirPicture = false

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
    /// What is being asked for now, and what the running capture format can
    /// actually give.
    private(set) var quality: VideoQuality
    private var captured: VideoQuality?

    init(factory: RTCPeerConnectionFactory, quality: VideoQuality = .standard) {
        self.quality = quality
        source = factory.videoSource()
        source.adaptOutputFormat(
            toWidth: Int32(quality.width), height: Int32(quality.height), fps: Int32(quality.frameRate))
        capturer = RTCCameraVideoCapturer(delegate: source)
        // The camera uses the **call's** audio session and is never allowed
        // to configure it.
        //
        // WebRTC's capturer gives its capture session a session of its own
        // (`usesApplicationAudioSession = NO`), and starting one of those
        // takes the sound hardware away from the call: the AirPods went the
        // moment the camera came on and didn't even appear as something to
        // choose again (owner, 2026-10-05). A camera has no business
        // touching the sound of a call, so it borrows the call's session
        // and is forbidden from setting its category — which is the same
        // rule as everywhere else here: while CallKit owns the session,
        // nothing else configures it.
        capturer.captureSession.usesApplicationAudioSession = true
        capturer.captureSession.automaticallyConfiguresApplicationAudioSession = false
        track = factory.videoTrack(with: source, trackId: "linx-video")
    }

    /// Asks for the camera the first time and starts it. It answers nothing
    /// and throws what the person needs to be told.
    func start() async throws {
        guard !running else { return }
        guard await Self.allowed() else { throw CameraTrouble.notAllowed }
        guard let device = Self.device(front: front), let format = Self.format(for: device, at: quality) else {
            throw CameraTrouble.noCamera
        }
        try await capturer.startCapture(with: device, format: format, fps: fps(format))
        running = true
        captured = quality
    }

    /// Follow the link to a new step. Coming down, and going up to anything the
    /// running format can still give, is the source's own scaling and the
    /// picture doesn't blink; going up past it restarts the camera once.
    func use(_ quality: VideoQuality) async {
        self.quality = quality
        source.adaptOutputFormat(
            toWidth: Int32(quality.width), height: Int32(quality.height), fps: Int32(quality.frameRate))
        guard running, let captured, quality > captured else { return }
        guard let device = Self.device(front: front), let format = Self.format(for: device, at: quality) else {
            return
        }
        try? await capturer.startCapture(with: device, format: format, fps: fps(format))
        self.captured = quality
    }

    private func fps(_ format: AVCaptureDevice.Format) -> Int {
        min(quality.frameRate, Int(format.videoSupportedFrameRateRanges.map(\.maxFrameRate).max() ?? 30))
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
        guard running, let device = Self.device(front: front), let format = Self.format(for: device, at: quality)
        else { return }
        capturer.startCapture(with: device, format: format, fps: fps(format))
        captured = quality
    }

    /// Whether this phone's own picture should be shown mirrored, as a
    /// mirror does — only the front camera is.
    var mirrored: Bool { front }

    /// Whether the camera is using the call's own audio session and keeping
    /// its hands off it, which is what leaves a Bluetooth headset where it
    /// was when the camera goes on. A test holds this to it.
    var usesTheCallsAudioSession: Bool {
        capturer.captureSession.usesApplicationAudioSession
            && !capturer.captureSession.automaticallyConfiguresApplicationAudioSession
    }

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

    /// The smallest format that is still at least what this step asks for, so
    /// the camera itself does the shrinking rather than the processor.
    private static func format(for device: AVCaptureDevice, at quality: VideoQuality) -> AVCaptureDevice.Format? {
        let formats = RTCCameraVideoCapturer.supportedFormats(for: device)
        let sizes = formats.map { format -> (AVCaptureDevice.Format, Int32, Int32) in
            let dimensions = CMVideoFormatDescriptionGetDimensions(format.formatDescription)
            return (format, dimensions.width, dimensions.height)
        }
        let big = sizes.filter { $0.1 >= Int32(quality.width) && $0.2 >= Int32(quality.height) }
        let pick = big.min { ($0.1 * $0.2) < ($1.1 * $1.2) } ?? sizes.max { ($0.1 * $0.2) < ($1.1 * $1.2) }
        return pick?.0
    }
}
