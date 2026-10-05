import SwiftUI

/// A call, in front of everything else (`docs/ui/iOS · Active call@1x.png`,
/// in Cobalt rather than the mockup's teal). Hold, transfer, park and record
/// are later build-order steps and aren't shown until they work.
///
/// The moment there is a picture in the call — either side's — it hands over
/// to `VideoCallView`, which lays itself out to the screen it is on. The
/// call underneath is the same one throughout.
struct CallView: View {
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    let call: PhoneModel.Call
    /// Counted up from the moment the call was answered.
    @State private var now = Date()
    @State private var keypadOpen = false
    @State private var detailsOpen = false

    private static let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        Group {
            if call.video.on {
                VideoCallView(call: call, now: now)
            } else {
                sound
            }
        }
        .onReceive(Self.tick) { now = $0 }
        // They turned their camera on and this phone's is off: ask once
        // (ADR-079, owner 2026-10-04). "Not now" leaves a one-way video
        // call, which carries on exactly as it is.
        .alert(
            theirVideoQuestion,
            isPresented: Binding(
                get: { phone.askAboutTheirVideo != nil },
                set: { if !$0 { phone.answeredAboutTheirVideo(turningMineOn: false) } })
        ) {
            Button("Turn mine on too") { phone.answeredAboutTheirVideo(turningMineOn: true) }
            Button("Not now", role: .cancel) { phone.answeredAboutTheirVideo(turningMineOn: false) }
        } message: {
            Text("You can see them either way. Turning your camera on lets them see you.")
        }
        // Why the call sounds the way it does. A call that connects and
        // carries no sound looks exactly like one that works, so the facts
        // are one tap away and can be copied out whole.
        .sheet(isPresented: $detailsOpen) {
            CallDetailsView(call: call, diagnostics: phone.diagnostics) { detailsOpen = false }
        }
    }

    private var theirVideoQuestion: String {
        guard let peer = phone.askAboutTheirVideo else { return "" }
        return "\(peer.name) turned their camera on"
    }

    /// A call with no picture in it: the screen the app has always had.
    private var sound: some View {
        VStack(spacing: LinxSpace.s5) {
            HStack {
                Button {
                    detailsOpen = true
                } label: {
                    ConnectionPill(call: call)
                }
                .accessibilityLabel("Call details")
                Spacer()
            }
            if let warning = warning {
                Button {
                    detailsOpen = true
                } label: {
                    CallWarning(words: warning)
                }
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
    }

    private var buttons: some View {
        // Four across a phone's width: the circles are 56 wide so even the
        // narrowest iPhone has room for them without scrolling sideways.
        HStack(spacing: LinxSpace.s4) {
            CallToggleButton(
                symbol: call.muted ? "mic.slash.fill" : "mic.fill", words: call.muted ? "Muted" : "Mute",
                on: call.muted
            ) { phone.toggleMute() }
            .disabled(call.phase != .active)
            .opacity(call.phase == .active ? 1 : 0.5)
            CallToggleButton(symbol: "circle.grid.3x3.fill", words: "Keypad", on: keypadOpen) {
                keypadOpen.toggle()
            }
            .disabled(call.phase != .active)
            .opacity(call.phase == .active ? 1 : 0.5)
            AudioRouteButton(
                onSpeaker: call.speaker, deviceName: phone.whereTheSoundGoes
            ) { phone.toggleSpeaker() }
            // A picture is added to the call that is already up: the call
            // itself never stops, and turning it off again leaves an
            // ordinary phone call (docs/PHASE2.md §7).
            CallToggleButton(
                symbol: "video.fill", words: phone.changingVideo ? "Starting…" : "Video", on: false,
                waiting: phone.changingVideo
            ) { phone.toggleVideo() }
            .disabled(call.phase != .active || phone.changingVideo)
            .opacity(call.phase == .active ? 1 : 0.5)
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

    /// What is wrong with this call, in a line, while it is still going on:
    /// no sound coming in, or whatever else the phone has to say. The call
    /// screen is the only place a person is looking at this point, which is
    /// why it is here and not only on the keypad.
    private var warning: String? {
        if phone.noSoundComingIn(at: now) {
            return "No sound is coming through. Tap to see why."
        }
        guard call.phase == .active, let problem = phone.problem else { return nil }
        return problem
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
        length(seconds: Int(now.timeIntervalSince(since ?? now)))
    }

    /// A length of time as a phone shows one: 2:14, or 1:02:14 for the rare
    /// call that runs over an hour.
    static func length(seconds: Int) -> String {
        let seconds = max(0, seconds)
        if seconds >= 3600 {
            return String(format: "%d:%02d:%02d", seconds / 3600, (seconds % 3600) / 60, seconds % 60)
        }
        return String(format: "%d:%02d", seconds / 60, seconds % 60)
    }
}

/// Something is wrong with the call that is going on, in one line that opens
/// the details.
private struct CallWarning: View {
    let words: String

    var body: some View {
        HStack(spacing: LinxSpace.s2) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.caption)
                .accessibilityHidden(true)
            Text(words)
                .font(.caption.weight(.medium))
                .multilineTextAlignment(.leading)
        }
        .foregroundStyle(LinxColor.onEnd)
        .padding(.horizontal, LinxSpace.s3)
        .padding(.vertical, LinxSpace.s2)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(LinxColor.end, in: .rect(cornerRadius: LinxRadius.md))
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
