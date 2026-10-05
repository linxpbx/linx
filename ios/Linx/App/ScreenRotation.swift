import UIKit

/// Which way round the app may turn (owner, 2026-10-04: "every single page
/// changes its orientation with the phone — it shouldn't").
///
/// A phone's pages don't turn. A keypad, a list of people or a page of
/// settings lying on its side is nobody's idea of a phone, and the iPhone's
/// own Phone app doesn't turn either. Two things are deliberately left
/// alone:
///
///   - **a video call** turns, because a face held sideways is the one case
///     where turning earns its keep, and that layout is already built
///     (ADR-076: the call's layout follows the shape of the screen, not the
///     device). A call with no picture doesn't turn: it is a name and a few
///     buttons, exactly like the Phone app's own call screen. Turning the
///     phone still sends the **picture** the right way up either way —
///     WebRTC tags each frame with the way the phone is being held, not
///     with the way the app is drawn — so this only decides what the person
///     holding it sees;
///   - **a big screen** — an iPad, or an iPhone Duo opened out — turns as it
///     always has. Those layouts are made for both ways round and for two
///     panels side by side, which is exactly what the owner asked not to
///     disturb.
///
/// iOS asks the app delegate this question, and only when something tells it
/// to look again, which is what `aCallIsUp` does as it changes.
@MainActor enum ScreenRotation {
    /// How wide a screen's shorter side must be, in points, to count as a
    /// big one. The largest iPhone is 440 points across; an iPad is 744 at
    /// its narrowest, and the iPhone Duo's inner screen is wider still when
    /// it is opened out.
    ///
    /// It is now only the fallback: where the phone itself says it has a
    /// fold across the screen (`screenIsDivided`, iOS 27.1), that answer is
    /// taken instead of this guess.
    nonisolated static let bigScreen: CGFloat = 700

    /// The phone has told the app where its fold is, which it only does
    /// when the screen is opened out — so this *is* a big screen, whatever
    /// its width works out to be (`Crease`, step 8).
    static var screenIsDivided = false {
        didSet {
            guard screenIsDivided != oldValue else { return }
            lookAgain()
        }
    }

    /// Whether a picture is on this phone's screen right now. The app sets
    /// it as video comes and goes; nothing else turns the phone.
    static var videoIsUp = false {
        didSet {
            guard videoIsUp != oldValue else { return }
            lookAgain()
        }
    }

    /// The rule itself, with nothing of UIKit in it so it can be tested.
    nonisolated static func allowed(
        idiom: UIUserInterfaceIdiom, shorterSide: CGFloat, videoIsUp: Bool, divided: Bool = false
    ) -> UIInterfaceOrientationMask {
        if idiom == .pad || divided || shorterSide >= bigScreen { return .all }
        // Upside down is left out on a phone, as it is everywhere in iOS:
        // the earpiece belongs at the top.
        return videoIsUp ? [.portrait, .landscapeLeft, .landscapeRight] : .portrait
    }

    /// What the app delegate answers for one window.
    static func allowed(on window: UIWindow?) -> UIInterfaceOrientationMask {
        let bounds = (window?.windowScene ?? anyScene)?.screen.bounds ?? .zero
        let shorterSide = bounds.isEmpty ? bigScreen : min(bounds.width, bounds.height)
        return allowed(
            idiom: UIDevice.current.userInterfaceIdiom, shorterSide: shorterSide,
            videoIsUp: videoIsUp, divided: screenIsDivided)
    }

    private static var anyScene: UIWindowScene? {
        UIApplication.shared.connectedScenes.lazy.compactMap { $0 as? UIWindowScene }.first
    }

    /// iOS keeps the last answer until it is told to ask again — without
    /// this, a phone turned sideways for a video call stays sideways once
    /// the camera goes off.
    private static func lookAgain() {
        for scene in UIApplication.shared.connectedScenes {
            guard let scene = scene as? UIWindowScene else { continue }
            for window in scene.windows {
                window.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
            }
        }
    }
}
