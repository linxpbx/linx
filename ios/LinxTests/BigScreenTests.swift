import SwiftUI
import Testing
import UIKit

@testable import Linx

/// Step 8's rules: where a fold is, what a call does with the room it has,
/// and what the app looks like on a screen with two columns on it
/// (`docs/PHASE2.md` §12 step 8).
///
/// Every one of these is a plain value in, a plain value out — no iOS 27.1
/// in any of them — so they run on the Xcode CI has as well as on the beta
/// that can actually fold a simulator.
struct CreaseTests {
    /// The unfolded iPhone Duo: a narrow band straight down the middle.
    static let duo = CGSize(width: 1024, height: 1080)
    static let downTheMiddle = CGRect(x: 504, y: 0, width: 16, height: 1080)

    @Test("a band down the middle of the screen is a fold")
    func readsAFoldThatRunsDown() {
        let crease = Crease.from(band: Self.downTheMiddle, in: Self.duo)
        #expect(crease?.runsDown == true)
        #expect(crease?.share(of: Self.duo) == 0.5)
    }

    @Test("a band across the screen is a fold the other way")
    func readsAFoldThatRunsAcross() {
        let band = CGRect(x: 0, y: 532, width: 1024, height: 16)
        let crease = Crease.from(band: band, in: Self.duo)
        #expect(crease?.runsDown == false)
        let share = crease?.share(of: Self.duo)
        #expect(share == 0.5, "the fold sits halfway down: \(String(describing: share))")
    }

    @Test("something that doesn't cross the screen is not a fold")
    func ignoresWhatIsNotAFold() {
        // A small rectangle in a corner — an occlusion, a sensor, anything.
        #expect(Crease.from(band: CGRect(x: 40, y: 40, width: 20, height: 60), in: Self.duo) == nil)
        #expect(Crease.from(band: .null, in: Self.duo) == nil)
        #expect(Crease.from(band: .zero, in: Self.duo) == nil)
    }

    @Test("a fold almost at the edge is no use to divide along")
    func ignoresAFoldAtTheEdge() {
        // A phone closing: the fold ends up hard against one side, and two
        // panels either side of it would leave one of them a sliver.
        let nearlyShut = CGRect(x: 40, y: 0, width: 16, height: 1080)
        let crease = Crease.from(band: nearlyShut, in: Self.duo)
        #expect(crease != nil)
        #expect(crease?.share(of: Self.duo) == nil)
    }
}

struct FoldedCallLayoutTests {
    /// A fold wins over the shape of the screen: even a screen the old rule
    /// would have called "tall" divides along the crease, because a button
    /// in the crease is a button nobody can press.
    @Test("a call divides along the fold, whatever shape the screen is")
    func thefoldDecides() {
        let size = CGSize(width: 700, height: 1200)
        let crease = Crease.from(band: CGRect(x: 0, y: 590, width: 700, height: 20), in: size)
        #expect(
            CallLayout.shape(size: size, horizontal: .regular, crease: crease)
                == .split(sideBySide: false))

        let duo = CreaseTests.duo
        let down = Crease.from(band: CreaseTests.downTheMiddle, in: duo)
        #expect(
            CallLayout.shape(size: duo, horizontal: .regular, crease: down) == .split(sideBySide: true))
    }

    @Test("with no fold to ask, the shape of the screen decides as before")
    func fallsBackToTheShape() {
        #expect(CallLayout.shape(size: CGSize(width: 393, height: 852), horizontal: .compact) == .tall)
        #expect(
            CallLayout.shape(size: CreaseTests.duo, horizontal: .regular) == .split(sideBySide: true))
    }

    @Test("the picture's panel ends where the fold is")
    func thePanelsMeetOnTheFold() {
        let duo = CreaseTests.duo
        let crease = Crease.from(band: CGRect(x: 614, y: 0, width: 16, height: 1080), in: duo)
        let share = CallLayout.pictureShare(for: .split(sideBySide: true), size: duo, crease: crease)
        #expect(abs(share - 622.0 / 1024.0) < 0.001)

        // An iPad has no fold, and the picture keeps the larger share.
        let pad = CGSize(width: 1376, height: 1032)
        #expect(CallLayout.pictureShare(for: .split(sideBySide: true), size: pad) == 0.62)
    }
}

struct CallBesideTheAppTests {
    @Test("a call covers a phone's screen")
    func aPhoneIsCovered() {
        // iPhone 18 Pro Max, upright and on its side.
        #expect(!CallLayout.callFitsBeside(size: CGSize(width: 440, height: 956), horizontal: .compact))
        #expect(!CallLayout.callFitsBeside(size: CGSize(width: 956, height: 440), horizontal: .compact))
    }

    @Test("a call stands beside the app on an iPad, either way round")
    func aPadKeepsItsList() {
        // iPad Pro 13-inch (M5).
        #expect(CallLayout.callFitsBeside(size: CGSize(width: 1032, height: 1376), horizontal: .regular))
        #expect(CallLayout.callFitsBeside(size: CGSize(width: 1376, height: 1032), horizontal: .regular))
        // An iPad squeezed into a narrow window is a phone again.
        #expect(!CallLayout.callFitsBeside(size: CGSize(width: 507, height: 1032), horizontal: .compact))
    }

    @Test("the call's column takes one leaf of a folding phone")
    func theCallTakesOneLeaf() {
        let duo = CreaseTests.duo
        let crease = Crease.from(band: CreaseTests.downTheMiddle, in: duo)
        #expect(CallLayout.callFitsBeside(size: duo, horizontal: .regular))
        #expect(CallLayout.callWidth(size: duo, crease: crease) == 512)
    }

    @Test("with no fold the call takes a sensible share of a big screen")
    func theCallTakesAShare() {
        let pad = CGSize(width: 1376, height: 1032)
        let width = CallLayout.callWidth(size: pad)
        #expect(width >= 380)
        #expect(width <= 560)
        #expect(width < pad.width / 2)
    }
}

/// Opening the phone out, and closing it again (owner, 2026-10-05: "there
/// is also the transition when you unfold the phone").
///
/// Unfolding is not one event the app is told about: the window grows, the
/// size class changes, and a moment later iOS says where the crease is.
/// Every rule below is asked the same question at each step of that, so a
/// half-finished fold can never leave a layout nobody designed.
struct UnfoldingTests {
    /// The Duo as it goes: shut, opened out, and opened out once the phone
    /// has said where its fold is.
    static let shut = CGSize(width: 466, height: 678)
    static let open = CreaseTests.duo
    static var fold: Crease? { Crease.from(band: CreaseTests.downTheMiddle, in: open) }

    @Test("a call covers the folded phone and takes one leaf of the opened one")
    func theCallFindsItsPlace() {
        #expect(!CallLayout.callFitsBeside(size: Self.shut, horizontal: .compact))
        // Opened out, before the crease has arrived: the call already
        // stands beside the app, on a sensible share of the screen.
        #expect(CallLayout.callFitsBeside(size: Self.open, horizontal: .regular))
        let guessed = CallLayout.callWidth(size: Self.open)
        #expect(guessed >= 380)
        // And once the fold is known, exactly one leaf of it.
        #expect(CallLayout.callWidth(size: Self.open, crease: Self.fold) == 512)
    }

    @Test("the call screen goes from one panel to two and back again")
    func theCallScreenFollows() {
        #expect(CallLayout.shape(size: Self.shut, horizontal: .compact) == .tall)
        // Opened out, the near-square rule already says two panels; the
        // crease then confirms it rather than changing it, so the screen
        // does not jump when the fold is reported a moment later.
        #expect(CallLayout.shape(size: Self.open, horizontal: .regular) == .split(sideBySide: true))
        #expect(
            CallLayout.shape(size: Self.open, horizontal: .regular, crease: Self.fold)
                == .split(sideBySide: true))
        // Shut again: back to one panel, with any stale crease ignored
        // because it no longer crosses this screen.
        let stale = Crease.from(band: CreaseTests.downTheMiddle, in: Self.shut)
        #expect(stale == nil)
        #expect(CallLayout.shape(size: Self.shut, horizontal: .compact, crease: stale) == .tall)
    }

    @Test("the fold is reported once, not on every layout pass")
    func theFoldIsReportedOnChange() {
        // The same band in the same screen is the same fold: a view is laid
        // out constantly and a phone folds rarely, which is why the probe
        // only speaks when the answer changes.
        let first = Crease.from(band: CreaseTests.downTheMiddle, in: Self.open)
        let again = Crease.from(band: CreaseTests.downTheMiddle, in: Self.open)
        #expect(first == again)
        let moved = Crease.from(band: CGRect(x: 600, y: 0, width: 16, height: 1080), in: Self.open)
        #expect(first != moved)
    }
}

@MainActor
struct FoldedRotationTests {
    @Test("a phone opened out turns like a tablet, without guessing at its width")
    func anOpenedPhoneTurns() {
        // Narrower than the old 700-point guess, but the phone has said it
        // has a fold across it, so it is a big screen.
        #expect(
            ScreenRotation.allowed(idiom: .phone, shorterSide: 640, videoIsUp: false, divided: true)
                == .all)
        // Folded shut, the same phone is a phone and stays upright.
        #expect(
            ScreenRotation.allowed(idiom: .phone, shorterSide: 440, videoIsUp: false, divided: false)
                == .portrait)
    }
}
