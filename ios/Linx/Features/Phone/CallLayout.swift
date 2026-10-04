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
// Build-order step 8 replaces the near-square rule below with the fold's own
// geometry (`ReservedRegion`, `ArrangementView`, iOS 27.1), which says where
// the crease really is instead of inferring it from the shape. Until then
// this keeps every button on one side of it.

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

    static func shape(size: CGSize, horizontal: UserInterfaceSizeClass?) -> Shape {
        if horizontal == .regular, size.width >= roomForTwoPanels, size.height >= depthForTwoPanels {
            // Near-square counts as side by side: that is the unfolded Duo,
            // and its crease runs down the middle.
            return .split(sideBySide: size.width >= size.height * 0.9)
        }
        return size.height > size.width ? .tall : .wide
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
