import SwiftUI

/// Setting this phone up, to the mockup `docs/ui/iOS · QR setup@1x.png`.
///
/// Three ways in, all carrying the same one-time code (docs/PHASE2.md §4):
/// scan the QR code, paste the link from the email, or type the server's
/// address and the 8 characters. The phone makes its own key as it finishes,
/// and nobody's password is in any of it.
struct SetUpPhoneView: View {
    @Environment(AppModel.self) private var model

    @State private var camera = CameraAccess.none
    @State private var byHand = false
    @State private var pasteLink = false
    /// Stops a second scan while the first one is being set up.
    @State private var scanned = false

    var body: some View {
        // Everything fits on the screen at normal text sizes; at the largest
        // accessibility sizes the same layout scrolls instead.
        ViewThatFits(in: .vertical) {
            // The viewfinder takes whatever room is left over…
            content(viewfinderHeight: nil)
            // …and at the largest accessibility text sizes the screen scrolls
            // with a smaller one instead.
            ScrollView { content(viewfinderHeight: 220) }
        }
        .safeAreaInset(edge: .bottom) {
            VStack(spacing: LinxSpace.s3) {
                Button("Paste setup link") { pasteLink = true }
                    .buttonStyle(.linxPrimary)
                Button("Set it up by hand") { byHand = true }
                    .buttonStyle(.linxSecondary)
            }
            .padding(.horizontal, LinxSpace.s6)
            .padding(.top, LinxSpace.s4)
            .padding(.bottom, LinxSpace.s2)
            .frame(maxWidth: 640)
            .frame(maxWidth: .infinity)
            .background(LinxColor.bg)
        }
        .linxBackground()
        .task {
            camera = await CameraAccess.ask()
        }
        .sheet(isPresented: $byHand) {
            ByHandView { code in use(code) }
        }
        .sheet(isPresented: $pasteLink) {
            PasteLinkView { code in use(code) }
        }
    }

    private func use(_ code: SetupCode) {
        Task { await model.setUp(with: code) }
    }

    /// A scanned code is a setup link. Anything else is somebody else's QR
    /// code, and the screen says so rather than sending it to Linx.
    private func scan(_ text: String) {
        guard !scanned else { return }
        guard let code = SetupCode.link(text) else {
            model.problem = "That isn't a Linx setup code. Scan the code on the Linx page that made it."
            return
        }
        scanned = true
        use(code)
    }

    private func content(viewfinderHeight: CGFloat?) -> some View {
        VStack(alignment: .leading, spacing: LinxSpace.s4) {
            Text("STEP 1 OF 3")
                .font(.footnote.weight(.semibold))
                .kerning(0.8)
                .foregroundStyle(LinxColor.accent)

            Text("Set up your extension")
                .font(.largeTitle.weight(.bold))
                .foregroundStyle(LinxColor.text)

            Text("Scan the QR code from your portal or setup email.")
                .font(.body)
                .foregroundStyle(LinxColor.textMuted)

            ScanFrame(camera: camera, onCode: scan)
                .frame(height: viewfinderHeight)
                .frame(maxHeight: viewfinderHeight == nil ? .infinity : nil)
                .padding(.top, LinxSpace.s2)

            LinxCard {
                HStack(alignment: .top, spacing: LinxSpace.s3) {
                    Image(systemName: "lock")
                        .foregroundStyle(LinxColor.textMuted)
                        .accessibilityHidden(true)
                    Text(
                        "The code works once and expires in 10 minutes. Your phone creates its own security key. No password is stored in the code."
                    )
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
                }
            }

            if let problem = model.problem {
                Text(problem)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.end)
                    .accessibilityAddTraits(.isHeader)
            }

            Spacer(minLength: 0)
        }
        .padding(.horizontal, LinxSpace.s6)
        .padding(.top, LinxSpace.s6)
        .frame(maxWidth: 640, alignment: .leading)
        .frame(maxWidth: .infinity)
    }
}

/// The viewfinder: the camera when there is one, behind four corner brackets
/// and a scan line. Without a camera (the simulator, or the person said no)
/// the same frame says what to do instead.
private struct ScanFrame: View {
    let camera: CameraAccess
    let onCode: @MainActor @Sendable (String) -> Void

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: LinxRadius.lg)
                .fill(LinxColor.surfaceDark)
                // In dark mode the panel is the same colour as the page behind it,
                // so it needs an outline to read as a frame.
                .overlay {
                    RoundedRectangle(cornerRadius: LinxRadius.lg).strokeBorder(LinxColor.border)
                }
            if camera == .available {
                CodeScanner(onCode: onCode)
                    .clipShape(.rect(cornerRadius: LinxRadius.lg))
            } else {
                Text(
                    camera == .denied
                        ? "Linx can't use the camera. Turn it on in Settings, or use one of the two ways below."
                        : "No camera on this device. Use one of the two ways below."
                )
                .font(.subheadline)
                .multilineTextAlignment(.center)
                .foregroundStyle(LinxColor.onSurfaceDark)
                .padding(LinxSpace.s6)
            }
            Brackets()
                .stroke(LinxColor.onSurfaceDark, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                .padding(LinxSpace.s8)
            if camera == .available {
                Rectangle()
                    .fill(LinxColor.accentOnDark)
                    .frame(height: 2)
                    .padding(.horizontal, LinxSpace.s8)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 220)
        .accessibilityElement()
        .accessibilityLabel("Camera viewfinder")
    }
}

/// Corner brackets, each one a sixth of the shorter side.
private struct Brackets: Shape {
    func path(in rect: CGRect) -> Path {
        let arm = min(rect.width, rect.height) / 4
        var path = Path()
        for (corner, dx, dy) in [
            (CGPoint(x: rect.minX, y: rect.minY), 1.0, 1.0),
            (CGPoint(x: rect.maxX, y: rect.minY), -1.0, 1.0),
            (CGPoint(x: rect.minX, y: rect.maxY), 1.0, -1.0),
            (CGPoint(x: rect.maxX, y: rect.maxY), -1.0, -1.0),
        ] {
            path.move(to: CGPoint(x: corner.x + arm * dx, y: corner.y))
            path.addLine(to: corner)
            path.addLine(to: CGPoint(x: corner.x, y: corner.y + arm * dy))
        }
        return path
    }
}

#Preview {
    SetUpPhoneView().environment(AppModel())
}
