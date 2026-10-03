import Testing
import UIKit

@testable import Linx

/// The app's colours must be exactly design/tokens.json, in both appearances.
/// `LinxTokenReference` and the asset catalog are generated from that one file by
/// `make tokens`, so this catches a hand-edited catalog or a stale generated file.
struct DesignTokensTests {
    @Test("every colour set matches design/tokens.json, light and dark")
    func colorSetsMatchTokens() throws {
        #expect(!LinxTokenReference.colors.isEmpty)
        for (name, hex) in LinxTokenReference.colors {
            for (style, want) in [(UIUserInterfaceStyle.light, hex.light), (.dark, hex.dark)] {
                let traits = UITraitCollection(userInterfaceStyle: style)
                let color = try #require(
                    UIColor(named: name, in: .main, compatibleWith: traits),
                    "colour set \(name) is missing from the asset catalog")
                #expect(
                    color.resolvedColor(with: traits).hexString == want.uppercased(),
                    "colour set \(name) in \(style == .light ? "light" : "dark")")
            }
        }
    }

    @Test("the spacing scale and radii are the ones in the design tokens")
    func scaleValues() {
        #expect(LinxSpace.s1 == 4)
        #expect(LinxSpace.s4 == 16)
        #expect(LinxSpace.s10 == 40)
        #expect(LinxRadius.sm == 8)
        #expect(LinxRadius.md == 12)
        #expect(LinxRadius.lg == 16)
    }
}

private extension UIColor {
    /// "#RRGGBB" in sRGB, which is how the tokens are written.
    var hexString: String {
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        getRed(&r, green: &g, blue: &b, alpha: &a)
        let byte = { (v: CGFloat) in Int((v * 255).rounded()) }
        return String(format: "#%02X%02X%02X", byte(r), byte(g), byte(b))
    }
}
