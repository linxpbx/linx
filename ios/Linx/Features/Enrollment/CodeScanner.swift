import AVFoundation
import SwiftUI

// The camera behind the viewfinder on the setup screen (docs/PHASE2.md §4).
// It reads one QR code and stops: the code works once, and the app has no
// other use for the camera in this slice. There is no camera in the
// simulator, so the screen falls back to its drawn frame, which is also what
// the screenshots show.

/// Whether this device can scan at all, and whether the person has said yes.
enum CameraAccess {
    case available
    case denied
    case none

    static var current: CameraAccess {
        // The simulator's "camera" is a moving test pattern, and running a
        // capture session on it brings the app down; screenshots and tests
        // use the drawn frame, which says what to do instead.
        #if targetEnvironment(simulator)
            return .none
        #endif
        guard AVCaptureDevice.default(for: .video) != nil else { return .none }
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .denied, .restricted: return .denied
        default: return .available
        }
    }

    /// ask puts up iOS's own "Linx would like to use the camera" the first
    /// time, and answers what the person chose.
    static func ask() async -> CameraAccess {
        #if targetEnvironment(simulator)
            return .none
        #endif
        guard AVCaptureDevice.default(for: .video) != nil else { return .none }
        if AVCaptureDevice.authorizationStatus(for: .video) == .notDetermined {
            return await AVCaptureDevice.requestAccess(for: .video) ? .available : .denied
        }
        return current
    }
}

/// The live camera, reading QR codes.
struct CodeScanner: UIViewRepresentable {
    /// Called on the main actor with each code read, until the view goes.
    let onCode: @MainActor @Sendable (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    func makeUIView(context: Context) -> PreviewView {
        let view = PreviewView()
        context.coordinator.start(in: view)
        return view
    }

    func updateUIView(_ view: PreviewView, context: Context) {}

    static func dismantleUIView(_ view: PreviewView, coordinator: Coordinator) {
        coordinator.stop()
    }

    /// A view whose layer is the camera's preview, so it resizes with the
    /// frame it sits in.
    final class PreviewView: UIView {
        override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }
        var previewLayer: AVCaptureVideoPreviewLayer { layer as! AVCaptureVideoPreviewLayer }
    }

    /// The camera's own object. It is `@unchecked Sendable` because the one
    /// thing it owns, the capture session, is only ever touched on `work` (or
    /// on the main actor before the session starts), which is exactly what
    /// AVFoundation asks for.
    final class Coordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate, @unchecked Sendable {
        private let session = AVCaptureSession()
        private let onCode: @MainActor @Sendable (String) -> Void
        private let work = DispatchQueue(label: "com.linxpbx.app.scanner")

        init(onCode: @escaping @MainActor @Sendable (String) -> Void) {
            self.onCode = onCode
        }

        @MainActor func start(in view: PreviewView) {
            guard let camera = AVCaptureDevice.default(for: .video),
                let input = try? AVCaptureDeviceInput(device: camera)
            else {
                return
            }
            session.beginConfiguration()
            if session.canAddInput(input) { session.addInput(input) }
            let output = AVCaptureMetadataOutput()
            if session.canAddOutput(output) {
                session.addOutput(output)
                output.setMetadataObjectsDelegate(self, queue: work)
                output.metadataObjectTypes = [.qr]
            }
            session.commitConfiguration()
            view.previewLayer.session = session
            view.previewLayer.videoGravity = .resizeAspectFill
            work.async { [self] in session.startRunning() }
        }

        func stop() {
            work.async { [self] in
                if session.isRunning { session.stopRunning() }
            }
        }

        func metadataOutput(
            _ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject],
            from connection: AVCaptureConnection
        ) {
            guard let code = objects.compactMap({ $0 as? AVMetadataMachineReadableCodeObject }).first,
                let text = code.stringValue
            else {
                return
            }
            let onCode = self.onCode
            Task { @MainActor in onCode(text) }
        }
    }
}
