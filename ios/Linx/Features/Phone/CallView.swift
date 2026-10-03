import SwiftUI

/// A call, in front of everything else (`docs/ui/iOS · Active call@1x.png`,
/// in Cobalt rather than the mockup's teal). Hold, transfer, park, record and
/// video are later build-order steps and aren't shown until they work.
struct CallView: View {
    @Environment(PhoneModel.self) private var phone
    let call: PhoneModel.Call
    /// Counted up from the moment the call was answered.
    @State private var now = Date()
    @State private var keypadOpen = false

    private static let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        VStack(spacing: LinxSpace.s5) {
            HStack {
                ConnectionPill(call: call)
                Spacer()
            }
            Spacer(minLength: 0)

            Text(Self.initials(of: call.peer.name))
                .font(.system(size: 44, weight: .semibold))
                .foregroundStyle(LinxColor.onSurfaceDark)
                .frame(width: 120, height: 120)
                .background(LinxColor.surface.opacity(0.18), in: .circle)
                .accessibilityHidden(true)

            VStack(spacing: LinxSpace.s2) {
                Text(call.peer.name)
                    .font(.largeTitle.weight(.bold))
                    .foregroundStyle(LinxColor.onSurfaceDark)
                    .multilineTextAlignment(.center)
                if call.peer.name != call.peer.number {
                    Text(call.peer.number)
                        .font(.body)
                        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.7))
                }
                Text(what)
                    .font(.title3.monospacedDigit())
                    .foregroundStyle(LinxColor.onSurfaceDark)
                    .accessibilityLabel(whatSpoken)
            }

            Spacer(minLength: 0)

            // While it is still ringing there is nothing to mute or dial
            // into, so only Answer and Decline are shown.
            if call.phase != .ringing {
                if keypadOpen {
                    toneKeys
                } else {
                    buttons
                }
            }

            if call.phase == .ringing {
                HStack(spacing: LinxSpace.s10) {
                    RoundCallButton(
                        symbol: "phone.down.fill", words: "Decline", colour: LinxColor.end,
                        ink: LinxColor.onEnd
                    ) { phone.decline() }
                    RoundCallButton(
                        symbol: "phone.fill", words: "Answer", colour: LinxColor.call,
                        ink: LinxColor.onCall
                    ) { phone.answer() }
                }
                .padding(.bottom, LinxSpace.s6)
            } else {
                RoundCallButton(
                    symbol: "phone.down.fill", words: "Hang up", colour: LinxColor.end,
                    ink: LinxColor.onEnd
                ) { phone.hangUp() }
                .padding(.bottom, LinxSpace.s6)
            }
        }
        .padding(.horizontal, LinxSpace.s6)
        .padding(.top, LinxSpace.s5)
        .frame(maxWidth: 520)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(LinxColor.surfaceDark.ignoresSafeArea())
        .onReceive(Self.tick) { now = $0 }
    }

    private var buttons: some View {
        HStack(spacing: LinxSpace.s8) {
            CallToggle(
                symbol: call.muted ? "mic.slash.fill" : "mic.fill", words: call.muted ? "Muted" : "Mute",
                on: call.muted
            ) { phone.toggleMute() }
            .disabled(call.phase != .active)
            .opacity(call.phase == .active ? 1 : 0.5)
            CallToggle(symbol: "circle.grid.3x3.fill", words: "Keypad", on: keypadOpen) {
                keypadOpen.toggle()
            }
            .disabled(call.phase != .active)
            .opacity(call.phase == .active ? 1 : 0.5)
            CallToggle(
                symbol: call.speaker ? "speaker.wave.3.fill" : "speaker.fill", words: "Speaker",
                on: call.speaker
            ) { phone.toggleSpeaker() }
        }
        .padding(.bottom, LinxSpace.s4)
    }

    /// The keypad during a call: for a menu, an extension or a PIN.
    private var toneKeys: some View {
        VStack(spacing: LinxSpace.s3) {
            Grid(horizontalSpacing: LinxSpace.s6, verticalSpacing: LinxSpace.s3) {
                ForEach(0..<4) { row in
                    GridRow {
                        ForEach(0..<3) { column in
                            let digit = Array("123456789*0#")[row * 3 + column]
                            Button {
                                phone.sendTone(digit)
                            } label: {
                                Text(String(digit))
                                    .font(.title2)
                                    .foregroundStyle(LinxColor.onSurfaceDark)
                                    .frame(width: 60, height: 60)
                                    .background(LinxColor.surface.opacity(0.18), in: .circle)
                            }
                            .accessibilityLabel(String(digit))
                        }
                    }
                }
            }
            Button("Done") { keypadOpen = false }
                .font(.body)
                .foregroundStyle(LinxColor.accentOnDark)
        }
        .padding(.bottom, LinxSpace.s4)
    }

    private var what: String {
        switch call.phase {
        case .ringing: return "Calling you"
        case .calling: return call.ringingThere ? "Ringing…" : "Calling…"
        case .active: return Self.length(since: call.answeredAt, to: now)
        }
    }

    private var whatSpoken: String {
        call.phase == .active ? "In a call, \(what)" : what
    }

    static func initials(of name: String) -> String {
        let words = name.split(separator: " ").prefix(2)
        let letters = words.compactMap { $0.first.map(String.init) }.joined()
        return letters.isEmpty ? "?" : letters.uppercased()
    }

    static func length(since: Date?, to now: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(since ?? now)))
        return String(format: "%d:%02d", seconds / 60, seconds % 60)
    }
}

/// Encrypted, and how the sound is getting through.
private struct ConnectionPill: View {
    let call: PhoneModel.Call

    var body: some View {
        HStack(spacing: LinxSpace.s2) {
            Image(systemName: "lock.fill")
                .font(.caption)
                .accessibilityHidden(true)
            Text(words)
                .font(.caption.weight(.medium))
        }
        .foregroundStyle(LinxColor.onSurfaceDark)
        .padding(.horizontal, LinxSpace.s3)
        .padding(.vertical, LinxSpace.s2)
        .background(LinxColor.surface.opacity(0.18), in: .capsule)
    }

    /// Every Linx call is encrypted end to end over DTLS-SRTP; the rest says
    /// whether it is going straight there or through Linx's relay.
    private var words: String {
        guard let connection = call.connection else { return "Encrypted" }
        var out = "Encrypted · " + (connection.route == .direct ? "Direct" : "Relayed")
        if let rtt = connection.roundTripMs { out += " · \(rtt) ms" }
        return out
    }
}

private struct CallToggle: View {
    let symbol: String
    let words: String
    let on: Bool
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            VStack(spacing: LinxSpace.s2) {
                Image(systemName: symbol)
                    .font(.title3)
                    .foregroundStyle(on ? LinxColor.surfaceDark : LinxColor.onSurfaceDark)
                    .frame(width: 64, height: 64)
                    .background(
                        on ? LinxColor.onSurfaceDark : LinxColor.surface.opacity(0.18), in: .circle)
                Text(words)
                    .font(.caption)
                    .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
            }
        }
        .accessibilityLabel(words)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}

private struct RoundCallButton: View {
    let symbol: String
    let words: String
    let colour: Color
    let ink: Color
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            Image(systemName: symbol)
                .font(.title)
                .foregroundStyle(ink)
                .frame(width: 76, height: 76)
                .background(colour, in: .circle)
        }
        .accessibilityLabel(words)
    }
}

#Preview("In a call") {
    CallView(
        call: PhoneModel.Call(
            peer: SIPPeer(name: "Sara Haddad", number: "1024"), incoming: false, phase: .active,
            answeredAt: Date(timeIntervalSinceNow: -252),
            connection: MediaConnection(route: .direct, roundTripMs: 38, relayProtocol: nil, audioBytesIn: 1))
    )
    .environment(PhoneModel(line: { nil }))
}
