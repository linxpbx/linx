import SwiftUI

/// Step 1 of setting up a phone, to the mockup `docs/ui/iOS · QR setup@1x.png`.
///
/// The skeleton draws the screen; build-order step 4 (`docs/PHASE2.md` §12) wires
/// up the camera, the Secure Enclave key and the two other ways in.
struct SetUpPhoneView: View {
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
                Button("Open setup link from email") {}
                    .buttonStyle(.linxPrimary)
                Button("Set it up by hand") {}
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

            ScanFrame()
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

            Spacer(minLength: 0)
        }
        .padding(.horizontal, LinxSpace.s6)
        .padding(.top, LinxSpace.s6)
        .frame(maxWidth: 640, alignment: .leading)
        .frame(maxWidth: .infinity)
    }
}

/// The viewfinder: four corner brackets and a scan line on the dark surface.
/// Step 4 puts the live camera behind it.
private struct ScanFrame: View {
    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: LinxRadius.lg)
                .fill(LinxColor.surfaceDark)
                // In dark mode the panel is the same colour as the page behind it,
                // so it needs an outline to read as a frame.
                .overlay {
                    RoundedRectangle(cornerRadius: LinxRadius.lg).strokeBorder(LinxColor.border)
                }
            Brackets()
                .stroke(LinxColor.onSurfaceDark, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                .padding(LinxSpace.s8)
            Rectangle()
                .fill(LinxColor.accentOnDark)
                .frame(height: 2)
                .padding(.horizontal, LinxSpace.s8)
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
    SetUpPhoneView()
}
