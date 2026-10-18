import AVKit
import SwiftUI
@preconcurrency import WebRTC

// A video call goes on in a small window over other apps (owner, 2026-10-18):
// switching away from Linx during a video call shrinks it into the system's
// Picture in Picture window, both pictures still moving. Swiping the window
// to the edge of the screen hides it and closing it leaves the call going as
// sound alone; tapping it comes back to the call.
//
// iOS lets an app with the `voip` background mode keep its camera running
// in that window from iOS 18 (`AVCaptureSession.isMultitaskingCameraAccess-
// Supported`), so this phone's picture keeps going out as well. Nothing here
// needs anything from Apple beyond what Linx already has.
//
// The window draws with `AVSampleBufferDisplayLayer`, which goes on drawing
// while the app is in the background, where WebRTC's own Metal view is not
// allowed to (Apple's "Adopting Picture in Picture for video calls"). The
// pictures are only fed to it from the moment the app is about to leave the
// screen until it is back, so a call on the screen pays nothing for it.

/// The small window for this call. One for the whole app, kept by
/// `PhoneModel`, so a call screen rebuilt by a turn or a fold keeps it.
@MainActor final class CallPictureInPicture: NSObject, AVPictureInPictureControllerDelegate {
    /// What the call's record is told (`SIPCallMedia.mark`).
    var onMark: ((String) -> Void)?

    private var controller: AVPictureInPictureController?
    private let content = AVPictureInPictureVideoCallViewController()
    private let window = PictureInPictureContent()
    private weak var source: UIView?
    private var theirs: RTCVideoTrack?
    private var mine: RTCVideoTrack?
    private var mirrored = true
    /// The window is up.
    private(set) var isActive = false
    /// The pictures are being fed to the window's layers.
    private var feeding = false
    private var watchers: [NSObjectProtocol] = []

    override init() {
        super.init()
        content.view.addSubview(window)
        window.frame = content.view.bounds
        window.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        content.preferredContentSize = CGSize(width: 9, height: 16)
        window.theirs.feed.onShape = { [weak self] size in self?.theirShape(size) }
        let centre = NotificationCenter.default
        watchers = [
            centre.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) {
                [weak self] _ in MainActor.assumeIsolated { self?.mayLeave() }
            },
            // In front again, even with something of iOS's still over it
            // (a permission question keeps an app from being active).
            centre.addObserver(forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main) {
                [weak self] _ in MainActor.assumeIsolated { self?.back() }
            },
            centre.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) {
                [weak self] _ in MainActor.assumeIsolated { self?.back() }
            },
            centre.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main) {
                [weak self] _ in MainActor.assumeIsolated { self?.gone() }
            },
        ]
    }

    /// Whether this device can show a call in a small window at all.
    static var possible: Bool { AVPictureInPictureController.isPictureInPictureSupported() }

    /// The call screen's view, which the window grows out of and goes back
    /// into. Each new one replaces the last.
    func use(source view: UIView) {
        guard Self.possible else {
            if source == nil { onMark?("This device can't show a small window") }
            source = view
            return
        }
        guard source !== view else { return }
        source = view
        let from = AVPictureInPictureController.ContentSource(
            activeVideoCallSourceView: view, contentViewController: content)
        if let controller {
            controller.contentSource = from
            return
        }
        let controller = AVPictureInPictureController(contentSource: from)
        controller.canStartPictureInPictureAutomaticallyFromInline = theirs != nil || mine != nil
        controller.delegate = self
        self.controller = controller
        onMark?("Small window ready")
    }

    /// The call screen's view went away. If no other takes its place by the
    /// next turn of the run loop, the call has no picture any more and the
    /// window goes with it.
    func lost(source view: UIView) {
        guard source === view else { return }
        Task { @MainActor [weak self] in
            guard let self, self.source == nil || self.source === view, self.source?.window == nil else { return }
            self.end()
        }
    }

    /// The pictures to show: theirs big, and mine small in a corner; mine
    /// big when it is the only one.
    func show(theirs: RTCVideoTrack?, mine: RTCVideoTrack?, mirrored: Bool) {
        guard theirs !== self.theirs || mine !== self.mine || mirrored != self.mirrored else { return }
        // No picture at all (a group call with every camera off): nothing to
        // show, so no window either; the call goes on as sound.
        controller?.canStartPictureInPictureAutomaticallyFromInline = theirs != nil || mine != nil
        let wasFeeding = feeding
        if wasFeeding { stopFeeding() }
        self.theirs = theirs
        self.mine = mine
        self.mirrored = mirrored
        if wasFeeding { startFeeding() }
    }

    /// The call is over, or has no picture left in it: no window, and none
    /// started by accident as it goes (Apple's warning about releasing the
    /// controller during a call).
    func end() {
        controller?.canStartPictureInPictureAutomaticallyFromInline = false
        if controller?.isPictureInPictureActive == true { controller?.stopPictureInPicture() }
        stopFeeding()
        controller = nil
        source = nil
        theirs = nil
        mine = nil
    }

    // MARK: - Feeding the window

    /// The app is about to leave the screen (another app, the home screen,
    /// or Control Center coming down): the window may start, so it gets
    /// the pictures now and has one ready when it opens.
    private func mayLeave() {
        guard controller != nil else { return }
        startFeeding()
    }

    /// Back on the screen: the call screen is there again, so the window
    /// goes, as FaceTime's does.
    private func back() {
        if isActive {
            onMark?("Back in the app: closing the small window")
            closeTheWindow(tries: 5)
            return
        }
        stopFeeding()
    }

    /// Asked as the app comes back, iOS sometimes leaves the window up, so
    /// it is asked again a moment later while it still is.
    private func closeTheWindow(tries: Int) {
        guard isActive, tries > 0, let controller else { return }
        controller.stopPictureInPicture()
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(600))
            self?.closeTheWindow(tries: tries - 1)
        }
    }

    /// The app has left the screen. If a call with a picture didn't go into
    /// its window, the record says why.
    private func gone() {
        guard let controller, !isActive else { return }
        guard controller.canStartPictureInPictureAutomaticallyFromInline else {
            onMark?("Left the app with no picture for a small window")
            return
        }
        let possible = controller.isPictureInPicturePossible
        let shown = source?.window != nil
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard let self, !self.isActive, self.controller === controller else { return }
            self.onMark?("Left the app with no small window (possible \(possible), source on screen \(shown))")
        }
    }

    private func startFeeding() {
        guard !feeding else { return }
        feeding = true
        let big = theirs ?? mine
        let small = theirs == nil ? nil : mine
        window.theirs.mirrored = big === mine && mirrored
        window.mine.mirrored = mirrored
        window.showsMine = small != nil
        if let big { big.add(window.theirs.feed) }
        if let small { small.add(window.mine.feed) }
        feedingBig = big
        feedingSmall = small
    }

    private var feedingBig: RTCVideoTrack?
    private var feedingSmall: RTCVideoTrack?

    private func stopFeeding() {
        guard feeding else { return }
        feeding = false
        feedingBig?.remove(window.theirs.feed)
        feedingSmall?.remove(window.mine.feed)
        feedingBig = nil
        feedingSmall = nil
    }

    /// The window takes the shape of the big picture.
    private func theirShape(_ size: CGSize) {
        guard size.width > 0, size.height > 0 else { return }
        content.preferredContentSize = size
    }

    // MARK: - AVPictureInPictureControllerDelegate

    nonisolated func pictureInPictureControllerWillStartPictureInPicture(_ controller: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            startFeeding()
            window.theirs.feed.frames = 0
        }
    }

    nonisolated func pictureInPictureControllerDidStartPictureInPicture(_ controller: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            isActive = true
            onMark?("Small window on")
        }
    }

    nonisolated func pictureInPictureController(
        _ controller: AVPictureInPictureController, failedToStartPictureInPictureWithError error: any Error
    ) {
        let words = error.localizedDescription
        MainActor.assumeIsolated {
            stopFeeding()
            onMark?("Small window couldn't open: \(words)")
        }
    }

    nonisolated func pictureInPictureControllerDidStopPictureInPicture(_ controller: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            isActive = false
            onMark?("Small window off after \(window.theirs.feed.frames) frames")
            if UIApplication.shared.applicationState == .active { stopFeeding() }
        }
    }

    nonisolated func pictureInPictureController(
        _ controller: AVPictureInPictureController,
        restoreUserInterfaceForPictureInPictureStopWithCompletionHandler completionHandler: @escaping (Bool) -> Void
    ) {
        // The call screen is still there behind the window: nothing to put
        // back.
        completionHandler(true)
    }
}

/// What the small window shows: the big picture filling it, and this
/// phone's own small in a corner while both cameras are on.
final class PictureInPictureContent: UIView {
    let theirs = SampleBufferPicture()
    let mine = SampleBufferPicture()
    var showsMine = false {
        didSet {
            mine.isHidden = !showsMine
            setNeedsLayout()
        }
    }

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        addSubview(theirs)
        addSubview(mine)
        mine.isHidden = true
        mine.layer.cornerRadius = 6
        mine.clipsToBounds = true
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not from a storyboard") }

    override func layoutSubviews() {
        super.layoutSubviews()
        theirs.frame = bounds
        // A quarter of the window's shorter side, upright, in the bottom
        // corner, as FaceTime's small window has it.
        let short = min(bounds.width, bounds.height)
        let width = max(36, short * 0.3)
        let size = CGSize(width: width, height: width * 4 / 3)
        let margin: CGFloat = 6
        mine.frame = CGRect(
            x: bounds.maxX - size.width - margin, y: bounds.maxY - size.height - margin,
            width: size.width, height: size.height)
    }
}

/// One picture drawn by `AVSampleBufferDisplayLayer`, turned the way the
/// sending phone was held.
final class SampleBufferPicture: UIView {
    let feed = SampleBufferFeed()
    var mirrored = false {
        didSet { setNeedsLayout() }
    }
    private var rotation: RTCVideoRotation = ._0

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        layer.addSublayer(feed.layer)
        feed.layer.videoGravity = .resizeAspectFill
        feed.onRotation = { [weak self] rotation in
            self?.rotation = rotation
            self?.setNeedsLayout()
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not from a storyboard") }

    override func layoutSubviews() {
        super.layoutSubviews()
        let sideways = rotation == ._90 || rotation == ._270
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        feed.layer.bounds = CGRect(
            origin: .zero,
            size: sideways ? CGSize(width: bounds.height, height: bounds.width) : bounds.size)
        feed.layer.position = CGPoint(x: bounds.midX, y: bounds.midY)
        var turn = CGAffineTransform(rotationAngle: CGFloat(rotation.rawValue) * .pi / 180)
        if mirrored { turn = turn.concatenating(CGAffineTransform(scaleX: -1, y: 1)) }
        feed.layer.setAffineTransform(turn)
        CATransaction.commit()
    }
}

/// Hands WebRTC's frames to a sample-buffer layer. WebRTC calls it on its
/// own decoding thread; a frame that is already the hardware's own buffer
/// goes as it is, and any other (AV1 and VP8 decode to plain planes) is
/// copied into one first.
final class SampleBufferFeed: NSObject, RTCVideoRenderer, @unchecked Sendable {
    let layer = AVSampleBufferDisplayLayer()
    /// The picture turned, on the main thread.
    var onRotation: (@MainActor (RTCVideoRotation) -> Void)?
    /// The picture's shape as it is seen, on the main thread.
    var onShape: (@MainActor (CGSize) -> Void)?
    /// Frames drawn, for the call's record.
    var frames = 0

    private let lock = NSLock()
    private var rotation: RTCVideoRotation?
    private var shape: CGSize = .zero
    private var pool: CVPixelBufferPool?
    private var poolSize = (0, 0)

    func setSize(_ size: CGSize) {}

    func renderFrame(_ frame: RTCVideoFrame?) {
        guard let frame, let pixels = pixels(of: frame) else { return }
        lock.lock()
        frames += 1
        let turned = rotation != frame.rotation
        rotation = frame.rotation
        let sideways = frame.rotation == ._90 || frame.rotation == ._270
        let seen = CGSize(
            width: CGFloat(sideways ? frame.height : frame.width),
            height: CGFloat(sideways ? frame.width : frame.height))
        let reshaped = seen != shape
        shape = seen
        lock.unlock()
        if turned {
            let rotation = frame.rotation
            DispatchQueue.main.async { [weak self] in MainActor.assumeIsolated { self?.onRotation?(rotation) } }
        }
        if reshaped {
            DispatchQueue.main.async { [weak self] in MainActor.assumeIsolated { self?.onShape?(seen) } }
        }
        enqueue(pixels)
    }

    private func enqueue(_ pixels: CVPixelBuffer) {
        var format: CMVideoFormatDescription?
        CMVideoFormatDescriptionCreateForImageBuffer(
            allocator: nil, imageBuffer: pixels, formatDescriptionOut: &format)
        guard let format else { return }
        var timing = CMSampleTimingInfo(
            duration: .invalid, presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid)
        var sample: CMSampleBuffer?
        CMSampleBufferCreateReadyWithImageBuffer(
            allocator: nil, imageBuffer: pixels, formatDescription: format, sampleTiming: &timing,
            sampleBufferOut: &sample)
        guard let sample else { return }
        if let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: true),
            CFArrayGetCount(attachments) > 0
        {
            let first = unsafeBitCast(CFArrayGetValueAtIndex(attachments, 0), to: CFMutableDictionary.self)
            CFDictionarySetValue(
                first, Unmanaged.passUnretained(kCMSampleAttachmentKey_DisplayImmediately).toOpaque(),
                Unmanaged.passUnretained(kCFBooleanTrue).toOpaque())
        }
        let renderer = layer.sampleBufferRenderer
        if renderer.status == .failed || renderer.requiresFlushToResumeDecoding { renderer.flush() }
        renderer.enqueue(sample)
    }

    /// The frame as a buffer the layer can show.
    private func pixels(of frame: RTCVideoFrame) -> CVPixelBuffer? {
        if let buffer = frame.buffer as? RTCCVPixelBuffer, !buffer.requiresCropping() {
            return buffer.pixelBuffer
        }
        return Self.copy(frame.buffer.toI420(), into: &pool, size: &poolSize)
    }

    /// Plain Y, U and V planes copied into the two planes the layer takes.
    static func copy(
        _ planes: any RTCI420BufferProtocol, into pool: inout CVPixelBufferPool?, size: inout (Int, Int)
    ) -> CVPixelBuffer? {
        let width = Int(planes.width)
        let height = Int(planes.height)
        guard width > 0, height > 0 else { return nil }
        if pool == nil || size != (width, height) {
            let attributes: [CFString: Any] = [
                kCVPixelBufferPixelFormatTypeKey: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
                kCVPixelBufferWidthKey: width,
                kCVPixelBufferHeightKey: height,
                kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary,
            ]
            pool = nil
            CVPixelBufferPoolCreate(nil, nil, attributes as CFDictionary, &pool)
            size = (width, height)
        }
        guard let pool else { return nil }
        var made: CVPixelBuffer?
        CVPixelBufferPoolCreatePixelBuffer(nil, pool, &made)
        guard let out = made else { return nil }
        CVPixelBufferLockBaseAddress(out, [])
        defer { CVPixelBufferUnlockBaseAddress(out, []) }
        guard let lumaOut = CVPixelBufferGetBaseAddressOfPlane(out, 0)?.assumingMemoryBound(to: UInt8.self),
            let chromaOut = CVPixelBufferGetBaseAddressOfPlane(out, 1)?.assumingMemoryBound(to: UInt8.self)
        else { return nil }
        let lumaStride = CVPixelBufferGetBytesPerRowOfPlane(out, 0)
        let chromaStride = CVPixelBufferGetBytesPerRowOfPlane(out, 1)
        let y = planes.dataY
        let u = planes.dataU
        let v = planes.dataV
        let yStride = Int(planes.strideY)
        let uStride = Int(planes.strideU)
        let vStride = Int(planes.strideV)
        for row in 0..<height {
            memcpy(lumaOut + row * lumaStride, y + row * yStride, width)
        }
        let chromaWidth = (width + 1) / 2
        for row in 0..<(height + 1) / 2 {
            let line = chromaOut + row * chromaStride
            let uRow = u + row * uStride
            let vRow = v + row * vStride
            for column in 0..<chromaWidth {
                line[2 * column] = uRow[column]
                line[2 * column + 1] = vRow[column]
            }
        }
        return out
    }
}

/// The call screen's own view, given to the small window to grow out of.
/// It draws nothing.
struct PictureInPictureSource: UIViewRepresentable {
    let window: CallPictureInPicture
    let theirs: RTCVideoTrack?
    let mine: RTCVideoTrack?
    let mirrored: Bool

    func makeUIView(context: Context) -> UIView {
        let view = UIView()
        view.isUserInteractionEnabled = false
        view.backgroundColor = .clear
        window.use(source: view)
        return view
    }

    func updateUIView(_ view: UIView, context: Context) {
        window.use(source: view)
        window.show(theirs: theirs, mine: mine, mirrored: mirrored)
    }

    static func dismantleUIView(_ view: UIView, coordinator: Coordinator) {
        coordinator.window?.lost(source: view)
    }

    func makeCoordinator() -> Coordinator {
        let coordinator = Coordinator()
        coordinator.window = window
        return coordinator
    }

    final class Coordinator {
        weak var window: CallPictureInPicture?
    }
}
