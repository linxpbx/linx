import SwiftUI

/// The keypad (`docs/ui/iOS · Keypad@1x.png`, in Cobalt rather than the
/// mockup's green): the number as it is typed, the keys, and the call button.
/// The Calls, Team and More tabs around it are build-order step 7.
struct KeypadView: View {
    @Environment(PhoneModel.self) private var phone

    private static let keys: [(String, String)] = [
        ("1", ""), ("2", "ABC"), ("3", "DEF"),
        ("4", "GHI"), ("5", "JKL"), ("6", "MNO"),
        ("7", "PQRS"), ("8", "TUV"), ("9", "WXYZ"),
        ("*", ""), ("0", "+"), ("#", ""),
    ]

    var body: some View {
        // Laid out like the iPhone's own Phone app (owner, 2026-10-04): big
        // keys, a wide margin down each side, and the whole keypad standing
        // in the middle of the screen rather than at the top with a hole
        // underneath, which is what a tall phone showed before.
        GeometryReader { screen in
            let inset = Self.sideInset(in: screen.size.width)
            let key = Self.keySize(in: screen.size, inset: inset)
            body(keySize: key, digit: key * 0.42, inset: inset)
        }
        .linxBackground()
    }

    /// The gap between two keys, and how far the keypad stands in from each
    /// side of the screen — a tenth of the width, as the Phone app does,
    /// and never less than the room a small phone has always had.
    static let keyGap: CGFloat = LinxSpace.s5

    static func sideInset(in width: CGFloat) -> CGFloat {
        max(LinxSpace.s6, min(width * 0.1, 64))
    }

    /// As big as the width allows, so three keys and two gaps fill the
    /// space between the margins — unless the screen is too short for four
    /// rows of them, which is what decides on a small phone.
    static func keySize(in screen: CGSize, inset: CGFloat) -> CGFloat {
        let byWidth = (screen.width - 2 * inset - 2 * keyGap) / 3
        let byHeight = (screen.height * 0.62 - 3 * keyGap - 96) / 4
        return max(68, min(byWidth, byHeight, 112))
    }

    @ViewBuilder private func body(keySize: CGFloat, digit: CGFloat, inset: CGFloat) -> some View {
        VStack(spacing: LinxSpace.s6) {
            Spacer(minLength: LinxSpace.s4)

            Text(phone.typed.isEmpty ? " " : phone.typed)
                .font(.system(size: 40, weight: .regular, design: .monospaced))
                .foregroundStyle(LinxColor.text)
                .lineLimit(1)
                .minimumScaleFactor(0.4)
                .accessibilityLabel(phone.typed.isEmpty ? "No number typed" : phone.typed)

            if let problem = phone.problem {
                Text(problem)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.end)
                    .multilineTextAlignment(.center)
            }

            Grid(horizontalSpacing: Self.keyGap, verticalSpacing: Self.keyGap) {
                ForEach(0..<4) { row in
                    GridRow {
                        ForEach(0..<3) { column in
                            let key = Self.keys[row * 3 + column]
                            KeypadKey(digit: key.0, letters: key.1, size: keySize, digitSize: digit) {
                                press(key.0)
                            }
                        }
                    }
                }
            }

            HStack {
                Button("Test my sound") { phone.callNumber(PhoneModel.echoTest) }
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.accent)
                    .frame(maxWidth: .infinity)

                Button {
                    // Nothing typed and a number rung before: fill it in
                    // rather than ring it, which is what every phone does
                    // and what stops a pocket from redialling anybody.
                    if phone.typed.isEmpty {
                        @Bindable var phone = phone
                        phone.typed = phone.lastDialled
                    } else {
                        phone.dial()
                    }
                } label: {
                    Image(systemName: "phone.fill")
                        .font(.title2)
                        .foregroundStyle(LinxColor.onCall)
                        .frame(width: keySize, height: keySize)
                        .background(LinxColor.call, in: .circle)
                }
                .disabled(phone.typed.isEmpty && phone.lastDialled.isEmpty)
                .opacity(phone.typed.isEmpty && phone.lastDialled.isEmpty ? 0.5 : 1)
                .accessibilityLabel(phone.typed.isEmpty ? "Redial \(phone.lastDialled)" : "Call")

                Button {
                    if !phone.typed.isEmpty { phone.typed.removeLast() }
                } label: {
                    Image(systemName: "delete.left")
                        .font(.title3)
                        .foregroundStyle(LinxColor.textMuted)
                        .frame(maxWidth: .infinity)
                }
                .opacity(phone.typed.isEmpty ? 0 : 1)
                .accessibilityLabel("Delete the last number")
            }
            .padding(.top, LinxSpace.s2)

            Spacer(minLength: LinxSpace.s4)
        }
        .padding(.horizontal, inset)
        .padding(.vertical, LinxSpace.s4)
        .frame(maxWidth: 560)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func press(_ digit: String) {
        @Bindable var phone = phone
        phone.typed.append(digit)
    }
}

/// One key: the digit, and the letters under it as a phone has always had.
private struct KeypadKey: View {
    let digit: String
    let letters: String
    /// Both set by the screen there is to fill (`KeypadView.keySize`).
    var size: CGFloat = 76
    var digitSize: CGFloat = 30
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            VStack(spacing: 0) {
                Text(digit)
                    .font(.system(size: digitSize, weight: .regular))
                    .foregroundStyle(LinxColor.text)
                Text(letters)
                    .font(.caption2.weight(.medium))
                    .kerning(1)
                    .foregroundStyle(LinxColor.textMuted)
                    .frame(height: 12)
            }
            .frame(width: size, height: size)
            .background(LinxColor.surface, in: .circle)
            .overlay { Circle().strokeBorder(LinxColor.border) }
            .frame(maxWidth: .infinity)
        }
        .accessibilityLabel(digit)
    }
}

/// Whether this phone can make a call, in one line at the top of the screen.
struct LineStatusPill: View {
    @Environment(PhoneModel.self) private var phone
    let extensionNumber: String

    var body: some View {
        HStack(spacing: LinxSpace.s2) {
            Circle()
                .fill(colour)
                .frame(width: 10, height: 10)
                .accessibilityHidden(true)
            Text(words)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(LinxColor.text)
        }
        .padding(.horizontal, LinxSpace.s4)
        .padding(.vertical, LinxSpace.s2)
        .background(LinxColor.surface, in: .capsule)
        .overlay { Capsule().strokeBorder(LinxColor.border) }
    }

    private var words: String {
        switch phone.status {
        case .ready: return "Ready · Ext \(extensionNumber)"
        case .starting: return "Getting your phone line…"
        case .reconnecting: return "Reconnecting…"
        case .unavailable: return "No phone line"
        }
    }

    private var colour: Color {
        switch phone.status {
        case .ready: return LinxColor.Status.available
        case .starting, .reconnecting: return LinxColor.Status.away
        case .unavailable: return LinxColor.Status.busy
        }
    }
}

#Preview("Keypad") {
    KeypadView().environment(PhoneModel(line: { nil }))
}
