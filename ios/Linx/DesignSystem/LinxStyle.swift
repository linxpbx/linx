import SwiftUI

// The look of the app, all of it from design/tokens.json by way of
// Linx/Generated/DesignTokens.swift. No colour or radius is ever written by hand.
// Type is the system font with Dynamic Type (docs/ui/DESIGN_TOKENS.md).

/// A filled primary button: Cobalt, white text, full width.
struct LinxPrimaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .foregroundStyle(LinxColor.onBrand)
            .frame(maxWidth: .infinity)
            .padding(.vertical, LinxSpace.s4)
            .background(LinxColor.brandFill, in: .rect(cornerRadius: LinxRadius.md))
            .opacity(configuration.isPressed ? 0.85 : 1)
    }
}

/// An outlined button for the quieter choice next to it.
struct LinxSecondaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body)
            .foregroundStyle(LinxColor.text)
            .frame(maxWidth: .infinity)
            .padding(.vertical, LinxSpace.s4)
            .background(LinxColor.surface, in: .rect(cornerRadius: LinxRadius.md))
            .overlay {
                RoundedRectangle(cornerRadius: LinxRadius.md).strokeBorder(LinxColor.border)
            }
            .opacity(configuration.isPressed ? 0.85 : 1)
    }
}

extension ButtonStyle where Self == LinxPrimaryButtonStyle {
    static var linxPrimary: LinxPrimaryButtonStyle { LinxPrimaryButtonStyle() }
}

extension ButtonStyle where Self == LinxSecondaryButtonStyle {
    static var linxSecondary: LinxSecondaryButtonStyle { LinxSecondaryButtonStyle() }
}

/// A card: surface colour, hairline border, panel radius.
struct LinxCard<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        content
            .padding(LinxSpace.s4)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(LinxColor.surface, in: .rect(cornerRadius: LinxRadius.md))
            .overlay {
                RoundedRectangle(cornerRadius: LinxRadius.md).strokeBorder(LinxColor.border)
            }
    }
}

extension View {
    /// The app background, behind every screen.
    func linxBackground() -> some View {
        background(LinxColor.bg.ignoresSafeArea())
    }
}
