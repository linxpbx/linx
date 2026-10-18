import AVFoundation
import Foundation
import UIKit
@preconcurrency import WebRTC

// This phone's camera, for a 1:1 video call. It runs only while the person
// has their video on: switched on by the button (or by a video call), and off
// again with the button, with the call, or when the network can't carry it.
// Its picture goes to the other device on the call's direct link
// (`DirectPicture`, ADR-087).
//
// The camera captures at 720p and the video source scales that to the step
// in use (540p, or 720p with **HD**); WebRTC's own congestion control adapts
// below that, end to end. Scaling costs nothing and never blinks.

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
    /// Whether the other side can show a picture at all: nil until the
    /// call link says, false for a desk phone or an outside number.
    var available: Bool?
    /// This phone is sending a picture.
    var mine = false
    /// The other side says they are sending one.
    var theirs = false
    /// And their frames are actually arriving.
    var theirPicture = false
    /// Their frames have arrived since they last turned their camera on:
    /// until then their picture is on its way, not stopped (the owner saw
    /// "Their picture has stopped" while it was still loading, 2026-10-15).
    var theirPictureSeen = false

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
    /// The step being sent now.
    private(set) var quality: VideoQuality

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
        // The camera keeps going while the call is in its small window over
        // another app (`CallPictureInPicture`): iOS 18 allows it to an app
        // with the `voip` background mode, set before the session starts.
        let session = capturer.captureSession
        if session.isMultitaskingCameraAccessSupported {
            session.beginConfiguration()
            session.isMultitaskingCameraAccessEnabled = true
            session.commitConfiguration()
        }
        track = factory.videoTrack(with: source, trackId: "linx-video")
    }

    /// Asks for the camera the first time and starts it. It answers nothing
    /// and throws what the person needs to be told.
    func start() async throws {
        guard !running else { return }
        #if targetEnvironment(simulator)
            // A simulator has no camera: a moving test picture stands in,
            // so two simulators can prove a video call end to end
            // (ios/tools/bench.sh).
            testPicture = TestPicture(source: source, quality: Self.capturedAt)
            testPicture?.start()
            running = true
            use(quality)
            return
        #endif
        guard await Self.allowed() else { throw CameraTrouble.notAllowed }
        guard let device = Self.device(front: front), let format = Self.format(for: device, at: Self.capturedAt)
        else {
            throw CameraTrouble.noCamera
        }
        try await capturer.startCapture(with: device, format: format, fps: fps(format))
        running = true
        use(quality)
        Self.centerStageOnce(device)
    }

    /// The camera always runs at the top step, and every step — HD on or
    /// off included — is the source's own scaling of it. Restarting the
    /// camera to go up a step blanked the picture for a moment, which is
    /// what tapping HD did (2026-10-12).
    static let capturedAt = VideoQuality.hd

    /// Follow the link to a new step: the picture is scaled, the camera is
    /// left alone, and nothing blinks.
    func use(_ quality: VideoQuality) {
        self.quality = quality
        source.adaptOutputFormat(
            toWidth: Int32(quality.width), height: Int32(quality.height), fps: Int32(quality.frameRate))
    }

    private func fps(_ format: AVCaptureDevice.Format) -> Int {
        min(Self.capturedAt.frameRate, Int(format.videoSupportedFrameRateRanges.map(\.maxFrameRate).max() ?? 30))
    }

    func stop() {
        guard running else { return }
        running = false
        #if targetEnvironment(simulator)
            testPicture?.stop()
            testPicture = nil
            return
        #endif
        capturer.stopCapture()
    }

    #if targetEnvironment(simulator)
        private var testPicture: TestPicture?
    #endif

    /// The other camera: the one looking at the person, or the one looking
    /// at what they are looking at.
    func flip() {
        front.toggle()
        guard running, let device = Self.device(front: front),
            let format = Self.format(for: device, at: Self.capturedAt)
        else { return }
        capturer.startCapture(with: device, format: format, fps: fps(format))
    }

    /// Whether this phone's own picture should be shown mirrored, as a
    /// mirror does — only the front camera is.
    var mirrored: Bool { front }

    /// Whether the camera is using the call's own audio session and keeping
    /// its hands off it, which is what leaves a Bluetooth headset where it
    /// was when the camera goes on. A test holds this to it.
    /// Whether the camera may keep running while another app is in front.
    var keepsGoingInTheSmallWindow: Bool { capturer.captureSession.isMultitaskingCameraAccessEnabled }

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
        if front, centerStageWanted, let stage = centerStageCamera() { return stage }
        let cameras = RTCCameraVideoCapturer.captureDevices()
        return cameras.first { $0.position == wanted } ?? cameras.first
    }

    // MARK: - Center Stage (owner, 2026-10-12)

    /// On an iPad the front camera keeps the person in the middle of the
    /// picture as they move (Center Stage), on from the first video call.
    /// The person can still switch it off in Control Center → Video Effects,
    /// and that choice holds: the app turns it on once, not at every start.
    private static var centerStageWanted: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    private static var centerStageTurnedOn = false

    /// The front camera that can do Center Stage. WebRTC's own list has only
    /// the wide-angle cameras, and on an iPad the one that can is often the
    /// ultra-wide.
    private static func centerStageCamera() -> AVCaptureDevice? {
        AVCaptureDevice.DiscoverySession(
            deviceTypes: [.builtInUltraWideCamera, .builtInWideAngleCamera, .builtInTrueDepthCamera],
            mediaType: .video, position: .front
        ).devices.first { device in device.formats.contains(where: \.isCenterStageSupported) }
    }

    private static func centerStageOnce(_ device: AVCaptureDevice) {
        guard centerStageWanted, !centerStageTurnedOn, device.position == .front,
            device.activeFormat.isCenterStageSupported
        else { return }
        centerStageTurnedOn = true
        AVCaptureDevice.centerStageControlMode = .cooperative
        AVCaptureDevice.isCenterStageEnabled = true
    }

    /// The smallest format that is still at least what this step asks for, so
    /// the camera itself does the shrinking rather than the processor.
    private static func format(for device: AVCaptureDevice, at quality: VideoQuality) -> AVCaptureDevice.Format? {
        var formats = RTCCameraVideoCapturer.supportedFormats(for: device)
        // Only a format that can do Center Stage, on a camera that has one.
        let stage = formats.filter(\.isCenterStageSupported)
        if centerStageWanted, device.position == .front, !stage.isEmpty { formats = stage }
        let sizes = formats.map { format -> (AVCaptureDevice.Format, Int32, Int32) in
            let dimensions = CMVideoFormatDescriptionGetDimensions(format.formatDescription)
            return (format, dimensions.width, dimensions.height)
        }
        let big = sizes.filter { $0.1 >= Int32(quality.width) && $0.2 >= Int32(quality.height) }
        let pick = big.min { ($0.1 * $0.2) < ($1.1 * $1.2) } ?? sizes.max { ($0.1 * $0.2) < ($1.1 * $1.2) }
        return pick?.0
    }
}

#if targetEnvironment(simulator)
    /// What a simulator sends instead of a camera: a frame whose colour moves
    /// on every tick, at the size and rate a camera would give.
    @MainActor final class TestPicture {
        private let source: RTCVideoSource
        private let feeder: RTCVideoCapturer
        private let width: Int
        private let height: Int
        private let fps: Int
        private var timer: Timer?
        private var tick = 0

        init(source: RTCVideoSource, quality: VideoQuality) {
            self.source = source
            feeder = RTCVideoCapturer(delegate: source)
            width = quality.width
            height = quality.height
            fps = 15
        }

        func start() {
            timer = Timer.scheduledTimer(withTimeInterval: 1 / Double(fps), repeats: true) { [weak self] _ in
                MainActor.assumeIsolated { self?.frame() }
            }
        }

        func stop() {
            timer?.invalidate()
            timer = nil
        }

        private func frame() {
            var buffer: CVPixelBuffer?
            CVPixelBufferCreate(
                kCFAllocatorDefault, width, height, kCVPixelFormatType_420YpCbCr8BiPlanarFullRange,
                [kCVPixelBufferIOSurfacePropertiesKey: [:]] as CFDictionary, &buffer)
            guard let buffer else { return }
            tick += 1
            CVPixelBufferLockBaseAddress(buffer, [])
            for plane in 0..<2 {
                guard let base = CVPixelBufferGetBaseAddressOfPlane(buffer, plane) else { continue }
                let rows = CVPixelBufferGetHeightOfPlane(buffer, plane)
                let stride = CVPixelBufferGetBytesPerRowOfPlane(buffer, plane)
                let value = UInt8(truncatingIfNeeded: plane == 0 ? (tick * 4) : (128 + tick))
                memset(base, Int32(value), rows * stride)
                // A bar that moves, so the encoder has something to send.
                if plane == 0 {
                    let x = (tick * 8) % max(1, stride - 40)
                    for row in 0..<rows { memset(base + row * stride + x, 255, 40) }
                }
            }
            CVPixelBufferUnlockBaseAddress(buffer, [])
            let frame = RTCVideoFrame(
                buffer: RTCCVPixelBuffer(pixelBuffer: buffer), rotation: ._0,
                timeStampNs: Int64(DispatchTime.now().uptimeNanoseconds))
            source.capturer(feeder, didCapture: frame)
        }
    }
#endif
