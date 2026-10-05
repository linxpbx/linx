import SwiftUI

// What a tab looks like when there is room for more than one column
// (`docs/PHASE2.md` §12 step 8; ADR-076, the owner's condition on
// 2026-10-04: "the iPad must look like an iPad app, not a stretched iPhone
// one").
//
// Every tab is a **list beside a detail**, the way Mail and Messages are: the
// list stays put on the left and what you picked fills the right, instead of
// a phone's single column pushed and popped in the middle of a 13-inch
// screen. iOS collapses the same layout back into one pushed column on a
// phone, so there is one set of screens and no "which device is this"
// anywhere in them.

/// The empty right-hand side, before anything is picked. Said in the same
/// plain words the rest of the app uses, and in the app's own colours
/// rather than the system's.
struct NothingPicked: View {
    let symbol: String
    let title: String
    let words: String

    var body: some View {
        VStack(spacing: LinxSpace.s3) {
            Image(systemName: symbol)
                .font(.system(size: 44))
                .foregroundStyle(LinxColor.textMuted)
                .accessibilityHidden(true)
            Text(title)
                .font(.title3.weight(.semibold))
                .foregroundStyle(LinxColor.text)
            Text(words)
                .font(.subheadline)
                .foregroundStyle(LinxColor.textMuted)
                .multilineTextAlignment(.center)
        }
        .padding(LinxSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .linxBackground()
    }
}

/// A line of the detail side: a plain label and what it says.
struct DetailLine: View {
    let label: String
    let value: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: LinxSpace.s3) {
            Text(label)
                .font(.subheadline)
                .foregroundStyle(LinxColor.textMuted)
            Spacer(minLength: LinxSpace.s4)
            Text(value)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(LinxColor.text)
                .multilineTextAlignment(.trailing)
        }
        .accessibilityElement(children: .combine)
    }
}

/// Somebody's initials, big, at the top of a detail pane.
struct BigInitials: View {
    let name: String

    var body: some View {
        Text(CallView.initials(of: name))
            .font(.system(size: 36, weight: .semibold))
            .foregroundStyle(LinxColor.text)
            .frame(width: 96, height: 96)
            .background(LinxColor.surface, in: .circle)
            .overlay { Circle().strokeBorder(LinxColor.border) }
            .accessibilityHidden(true)
    }
}

extension View {
    /// Picks the first row for the detail side when a tab opens on a screen
    /// with two columns on it, so nobody is shown an empty half. On a phone
    /// there is only ever one column and nothing is picked, because that
    /// would push a screen the person never asked for.
    func picksTheFirstRow<ID: Hashable>(
        _ chosen: Binding<ID?>, first: @escaping () -> ID?, when regular: Bool
    ) -> some View {
        modifier(PicksTheFirstRow(chosen: chosen, first: first, regular: regular))
    }
}

private struct PicksTheFirstRow<ID: Hashable>: ViewModifier {
    @Binding var chosen: ID?
    let first: () -> ID?
    let regular: Bool

    func body(content: Content) -> some View {
        content
            .onChange(of: regular, initial: true) { _, _ in pick() }
            .onChange(of: first()) { _, _ in pick() }
    }

    private func pick() {
        guard regular, chosen == nil, let first = first() else { return }
        chosen = first
    }
}
