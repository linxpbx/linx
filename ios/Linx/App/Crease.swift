import SwiftUI
import UIKit

/// Where a folding phone's crease runs across the screen, as the phone
/// itself reports it (`docs/PHASE2.md` §12 step 8).
///
/// Until now the app inferred the crease from the shape of the screen: a
/// near-square screen was taken to be an iPhone Duo opened out, with its
/// fold down the middle. That was a guess, and a guess is wrong the moment
/// a window is resized or a future phone folds the other way. iOS 27.1 asks
/// the device instead — `UIView.reservedRegions(kind: .division)` — and this
/// is that answer, in the plainest shape the layouts need.
///
/// It is a plain value with nothing of iOS 27.1 in it, so every layout rule
/// that uses it can be tested on any Xcode, including the one CI has.
struct Crease: Equatable, Sendable {
    /// The band the fold occupies, in the view's own coordinates, with the
    /// margins the system asks to be kept clear included.
    let band: CGRect
    /// True when the fold runs top to bottom, so it divides the screen into
    /// a left half and a right half.
    let runsDown: Bool

    /// The middle of the fold along the axis it divides.
    var middle: CGFloat { runsDown ? band.midX : band.midY }

    /// Where a two-panel layout should divide, as a share of the screen, so
    /// that the join lands on the fold and no button sits across it.
    /// Nothing is returned for a fold so close to an edge that one panel
    /// would be useless — a screen folded nearly shut, or a crease the app
    /// only partly overlaps.
    func share(of size: CGSize) -> CGFloat? {
        let whole = runsDown ? size.width : size.height
        guard whole > 0 else { return nil }
        let share = middle / whole
        guard share >= 0.25, share <= 0.75 else { return nil }
        return share
    }

    /// The crease as iOS reports it, or nothing when the region is not one
    /// the layouts can use: inactive, or so thin and off to one side that it
    /// is not a fold across this view at all.
    static func from(band: CGRect, in size: CGSize) -> Crease? {
        guard !band.isNull, !band.isEmpty, size.width > 0, size.height > 0 else { return nil }
        // A fold that runs down the screen is a tall, narrow band; one that
        // runs across it is short and wide.
        let runsDown = band.height >= band.width
        let along = runsDown ? band.height : band.width
        let whole = runsDown ? size.height : size.width
        // It has to cross the view, or it is something else entirely.
        guard along >= whole * 0.8 else { return nil }
        return Crease(band: band, runsDown: runsDown)
    }
}

extension EnvironmentValues {
    /// The fold across the screen this view is on, when there is one and the
    /// phone can say where it is. `nil` on every other device, and on iOS
    /// before 27.1.
    @Entry var crease: Crease?
}

extension View {
    /// Puts the fold this view lies across into the environment, so the
    /// layouts below can divide themselves along it. A no-op on an iOS
    /// older than 27.1 and on an Xcode whose SDK doesn't know about folds —
    /// everything below then falls back to the shape of the screen, exactly
    /// as it did before.
    ///
    /// `isTheWholeScreen` is set by the one at the root of the app, which
    /// is also what tells `ScreenRotation` that this screen is a big one: a
    /// phone opened out turns like a tablet, and a guess at its width is no
    /// longer needed to know that.
    func readsTheCrease(isTheWholeScreen: Bool = false) -> some View {
        modifier(ReadsTheCrease(isTheWholeScreen: isTheWholeScreen))
    }
}

private struct ReadsTheCrease: ViewModifier {
    let isTheWholeScreen: Bool
    @State private var crease: Crease?

    func body(content: Content) -> some View {
        content
            .environment(\.crease, crease)
            .onChange(of: crease, initial: true) { _, fold in
                guard isTheWholeScreen else { return }
                ScreenRotation.screenIsDivided = fold != nil
            }
            .background {
                #if LINX_FOLD_SDK
                    if #available(iOS 27.1, *) {
                        CreaseProbe(crease: $crease).accessibilityHidden(true)
                    }
                #endif
            }
    }
}

#if LINX_FOLD_SDK
    /// A view of nothing at all, there only to ask iOS where the fold is.
    /// It sits behind the content and fills it, so what it is told is in
    /// the content's own coordinates.
    @available(iOS 27.1, *)
    private struct CreaseProbe: UIViewRepresentable {
        @Binding var crease: Crease?

        func makeUIView(context: Context) -> CreaseProbeView {
            let view = CreaseProbeView()
            view.isUserInteractionEnabled = false
            tell(view)
            return view
        }

        /// The binding is handed over again on every update: the one a view
        /// was made with belongs to that pass and goes stale.
        func updateUIView(_ view: CreaseProbeView, context: Context) { tell(view) }

        private func tell(_ view: CreaseProbeView) {
            let binding = $crease
            view.found = { found in
                // SwiftUI is told during layout, so the change waits for the
                // next turn of the run loop rather than redrawing from
                // inside `layoutSubviews`.
                Task { @MainActor in
                    guard binding.wrappedValue != found else { return }
                    binding.wrappedValue = found
                }
            }
        }
    }

    /// The view itself. A fold appears, moves and goes away as the phone is
    /// opened and closed, and each of those lays the view out again.
    @available(iOS 27.1, *)
    private final class CreaseProbeView: UIView {
        var found: ((Crease?) -> Void)?
        /// What was last reported. A view is laid out often and a phone
        /// folds rarely, so SwiftUI is only told when the answer changes.
        private var last: Crease??

        override func layoutSubviews() {
            super.layoutSubviews()
            look()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            look()
        }

        private func look() {
            let regions = reservedRegions(kind: .division).filter(\.isActive)
            // More than one fold is not a thing any phone does today; if it
            // ever is, the widest band is the one worth laying out around.
            let widest = regions.max { a, b in
                a.frame.width * a.frame.height < b.frame.width * b.frame.height
            }
            let crease = widest.flatMap { Crease.from(band: $0.frame, in: bounds.size) }
            guard last != .some(crease) else { return }
            last = .some(crease)
            found?(crease)
        }
    }
#endif
