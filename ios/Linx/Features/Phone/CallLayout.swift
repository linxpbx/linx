import SwiftUI

// How a call lays itself out on whatever screen it finds itself on
// (docs/PHASE2.md §8). One decision, made from the size the window actually
// has — never from "which device is this" — so it is right on every iPhone,
// on an iPad, in a window an iPad resizes while the call is up, and on an
// iPhone Duo on both of its screens, folded and unfolded.
//
//   tall   a phone held upright, and the Duo's outer screen: the picture
//          fills the screen and the buttons sit along the bottom, where a
//          thumb is.
//   wide   a phone or a pad turned on its side: the picture fills the
//          screen and the buttons stand in a column down the trailing edge,
//          out of the middle of the picture and away from the camera notch.
//   split  an iPad, or the Duo opened out: the picture takes one half and
//          everything else the other, so nothing a person presses sits in
//          the middle of a screen that folds, and nobody has to reach
//          across a tablet to hang up.
//
// A folding phone now says where its fold really is (`Crease`, iOS 27.1),
// and when it does, that answer wins: the two panels divide along the fold
// itself. Where there is no fold to ask — every iPad, every ordinary phone,
// and any iOS older than 27.1 — the shape of the screen decides, as it
// always has.

enum CallLayout {
    enum Shape: Equatable {
        case tall
        case wide
        /// Two panels. `sideBySide` when the screen is as wide as it is
        /// tall or wider (an unfolded Duo, an iPad on its side); `overUnder`
        /// when it is clearly taller (an iPad held upright).
        case split(sideBySide: Bool)

        var isSplit: Bool { if case .split = self { return true } else { return false } }
    }

    /// Enough room across for two panels that are both worth having.
    static let roomForTwoPanels: CGFloat = 640
    /// And enough room down them: a phone on its side is wide but shallow,
    /// and a column of buttons in half of it would be a column of nothing.
    static let depthForTwoPanels: CGFloat = 500

    static func shape(size: CGSize, horizontal: UserInterfaceSizeClass?, crease: Crease? = nil) -> Shape {
        // A fold across the screen is the one thing that must not be laid
        // over, however small the screen is: the panels divide along it
        // whatever the shape says.
        if let crease, crease.share(of: size) != nil {
            return .split(sideBySide: crease.runsDown)
        }
        if horizontal == .regular, size.width >= roomForTwoPanels, size.height >= depthForTwoPanels {
            // Near-square counts as side by side: that is an unfolded Duo
            // whose fold the phone hasn't told us about, and its crease
            // runs down the middle.
            return .split(sideBySide: size.width >= size.height * 0.9)
        }
        return size.height > size.width ? .tall : .wide
    }

    /// How much of the screen the picture's panel takes. The fold decides it
    /// when there is one, so the join and the crease are the same line;
    /// otherwise the picture takes the larger share, because it is what
    /// people are looking at.
    static func pictureShare(for shape: Shape, size: CGSize, crease: Crease? = nil) -> CGFloat {
        guard case .split(let sideBySide) = shape else { return 1 }
        if let share = crease?.share(of: size), crease?.runsDown == sideBySide {
            return share
        }
        return sideBySide ? 0.62 : 0.6
    }

    /// How wide a screen has to be before a call stands beside the app
    /// instead of covering it. An iPad held upright (834 points) is wide
    /// enough for a list and a call; an iPad mini upright, or an iPad in a
    /// narrow window beside another app, is not, and there the call covers
    /// the screen as it does on a phone.
    static let roomForACallBeside: CGFloat = 820

    static func callFitsBeside(size: CGSize, horizontal: UserInterfaceSizeClass?) -> Bool {
        horizontal == .regular && size.width >= roomForACallBeside
    }

    /// How wide the call's own column is. A fold decides it when there is
    /// one, so the call takes one leaf of the phone and the app the other.
    static func callWidth(size: CGSize, crease: Crease? = nil) -> CGFloat {
        if let crease, crease.runsDown, let share = crease.share(of: size) {
            return size.width * (1 - share)
        }
        return max(380, min(size.width * 0.42, 560))
    }

    /// How big this phone's own picture is shown, as a share of the screen's
    /// shorter side. Small: it is there to check the camera is pointing the
    /// right way, not to watch yourself.
    static func selfViewWidth(for shape: Shape, size: CGSize) -> CGFloat {
        switch shape {
        case .tall, .wide: return min(size.width, size.height) * 0.28
        case .split: return min(size.width, size.height) * 0.34
        }
    }
}
