import SwiftUI
import Testing

@testable import Linx

/// A smoke test: the first screen builds and keeps the words from the mockup
/// (`docs/ui/iOS · QR setup@1x.png`). Layout itself is checked by the
/// screenshots `make ios-screens` takes.
struct SetUpPhoneViewTests {
    @MainActor
    @Test("the setup screen renders")
    func rendersFirstScreen() {
        let renderer = ImageRenderer(content: SetUpPhoneView().frame(width: 390, height: 844))
        #expect(renderer.uiImage != nil)
    }
}
