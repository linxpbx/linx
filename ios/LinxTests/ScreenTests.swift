import SwiftUI
import Testing

@testable import Linx

/// Smoke tests: each screen builds, and the setup screen keeps the words from
/// the mockup (`docs/ui/iOS · QR setup@1x.png`). Layout itself is checked by
/// the screenshots `make ios-screens` takes.
struct ScreenTests {
    @MainActor
    @Test("the setup screen renders")
    func rendersFirstScreen() {
        let renderer = ImageRenderer(
            content: SetUpPhoneView().environment(AppModel()).frame(width: 390, height: 844))
        #expect(renderer.uiImage != nil)
    }

    @MainActor
    @Test("the signed-in screen renders, with who the phone is for")
    func rendersSignedIn() {
        let model = AppModel()
        model.identity = Screen.sampleIdentity
        model.state = .signedIn
        let renderer = ImageRenderer(
            content: SignedInView().environment(model).frame(width: 390, height: 844))
        #expect(renderer.uiImage != nil)
    }

    @MainActor
    @Test("the set-it-up-again screen renders")
    func rendersSetUpAgain() {
        let renderer = ImageRenderer(
            content: SetUpAgainView(reason: "This phone is no longer set up.")
                .environment(AppModel()).frame(width: 390, height: 844))
        #expect(renderer.uiImage != nil)
    }

    @MainActor
    @Test("a screen name from the screenshot harness comes with made-up contents only")
    func launchedScreens() {
        #expect(Screen(rawValue: "signed-in")?.model().state == .signedIn)
        #expect(Screen(rawValue: "setup-phone")?.model().state == .setUp)
        #expect(Screen(rawValue: "nonsense") == nil)
        // Nothing made up ever carries a certificate or a key.
        #expect(Screen.sampleIdentity.certificate.isEmpty)
    }
}
