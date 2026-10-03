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
        VStack(spacing: LinxSpace.s4) {
            Text(phone.typed.isEmpty ? " " : phone.typed)
                .padding(.top, LinxSpace.s8)
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

            Grid(horizontalSpacing: LinxSpace.s5, verticalSpacing: LinxSpace.s4) {
                ForEach(0..<4) { row in
                    GridRow {
                        ForEach(0..<3) { column in
                            let key = Self.keys[row * 3 + column]
                            KeypadKey(digit: key.0, letters: key.1) { press(key.0) }
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
                    phone.dial()
                } label: {
                    Image(systemName: "phone.fill")
                        .font(.title2)
                        .foregroundStyle(LinxColor.onCall)
                        .frame(width: 72, height: 72)
                        .background(LinxColor.call, in: .circle)
                }
                .disabled(phone.typed.isEmpty)
                .opacity(phone.typed.isEmpty ? 0.5 : 1)
                .accessibilityLabel("Call")

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

            Spacer(minLength: 0)
        }
        .padding(.horizontal, LinxSpace.s6)
        .padding(.vertical, LinxSpace.s5)
        .frame(maxWidth: 480)
        .frame(maxWidth: .infinity)
        .linxBackground()
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
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            VStack(spacing: 0) {
                Text(digit)
                    .font(.system(size: 30, weight: .regular))
                    .foregroundStyle(LinxColor.text)
                Text(letters)
                    .font(.caption2.weight(.medium))
                    .kerning(1)
                    .foregroundStyle(LinxColor.textMuted)
                    .frame(height: 12)
            }
            .frame(maxWidth: .infinity)
            .frame(height: 76)
            .background(LinxColor.surface, in: .circle)
            .overlay { Circle().strokeBorder(LinxColor.border) }
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
