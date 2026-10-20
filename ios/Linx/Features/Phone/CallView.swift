import SwiftUI
@preconcurrency import WebRTC

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

    private static let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        @Bindable var phone = phone
        // One container, whichever screen is inside it. The switch between the
        // call's two screens used to change this view's identity, and anything
        // attached here — the question below above all — went with it (owner,
        // 2026-10-05: the question "comes and disappears in less than a
        // second"). A ZStack that stays put while its child changes keeps them
        // alive.
        ZStack {
            if let group = phone.group {
                GroupCallView(call: call, group: group)
            } else if call.video.on {
                VideoCallView(call: call, now: now)
            } else {
                sound
            }
        }
        .onReceive(Self.tick) { now = $0 }
        // Switching to another app carries the call on in a small window
        // (`CallPictureInPicture`). It hangs here, off the call itself,
        // rather than off either screen inside it: a moment with no picture
        // — which is exactly what switching Wi-Fi off mid-call causes, since
        // the picture's link is made again — took the video screen away and
        // the window with it, and iOS only ever opens that window as an app
        // goes to the background, so a person already in another app had no
        // moment left for it to come back in (owner, 2026-10-20).
        .background {
            PictureInPictureSource(
                window: phone.smallWindow,
                theirs: smallWindowPictures.theirs, mine: smallWindowPictures.mine,
                mirrored: phone.mirrorsMyVideo)
        }
        // They turned their camera on and this phone's is off: ask once
        // (ADR-079, owner 2026-10-04). "Not now" leaves a one-way video
        // call, which carries on exactly as it is.
        //
        // **Only the buttons answer it.** A binding that answered "Not now"
        // whenever SwiftUI set it false answered the question for the person:
        // a picture arriving brings several changes at once — the screen, the
        // loudspeaker, the system's own call — and any one of them taking the
        // alert down counted as "no" before anybody could read it. The two
        // buttons clear the question themselves, which is what takes the alert
        // away; nothing else may.
        .alert(
            theirVideoQuestion,
            isPresented: Binding(get: { phone.askAboutTheirVideo != nil }, set: { _ in })
        ) {
            Button("Turn mine on too") { phone.answeredAboutTheirVideo(turningMineOn: true) }
            Button("Not now", role: .cancel) { phone.answeredAboutTheirVideo(turningMineOn: false) }
        } message: {
            Text("You can see them either way. Turning your camera on lets them see you.")
        }
        // Why the call sounds the way it does. A call that connects and
        // carries no sound looks exactly like one that works, so the facts
        // are one tap away and can be copied out whole.
        .sheet(isPresented: $phone.callDetailsAreOpen) {
            CallDetailsView(call: call, diagnostics: phone.diagnostics) {
                phone.callDetailsAreOpen = false
            }
        }
        .sheet(isPresented: $phone.addingPerson) {
            AddPersonSheet { phone.addingPerson = false }
        }
    }

    /// What the small window shows: in a group call the first other person
    /// who has a picture, with this phone's own small in the corner; in a
    /// call between two, their picture and this phone's own.
    private var smallWindowPictures: (theirs: RTCVideoTrack?, mine: RTCVideoTrack?) {
        if let group = phone.group {
            return (
                group.people.first { !$0.me && $0.video && group.pictures[$0.id] != nil }
                    .flatMap { group.pictures[$0.id] },
                group.camera ? group.people.first(where: \.me).flatMap { group.pictures[$0.id] } : nil
            )
        }
        return (
            call.video.theirs ? phone.tracks.remote : nil,
            call.video.mine ? phone.tracks.local : nil
        )
    }

    private var theirVideoQuestion: String {
        guard let peer = phone.askAboutTheirVideo else { return "" }
        return "\(peer.name) turned their camera on"
    }

    /// A call with no picture in it: the screen the app has always had.
    private var sound: some View {
        VStack(spacing: LinxSpace.s5) {
            HStack {
                EncryptedInfo()
                Spacer()
                CallMoreMenu()
            }
            if let warning = warning {
                Button {
                    phone.callDetailsAreOpen = true
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
                if let account = phone.accountLabel {
                    // Which of this phone's accounts the call is on, so
                    // there is never a doubt which company you are speaking
                    // as (docs/MULTI_ACCOUNT.md §7). Nothing at all with one
                    // account.
                    Text(account)
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.7))
                        .accessibilityLabel("On your \(account) account")
                }
            }

            Spacer(minLength: 0)

            // While it is still ringing there is nothing to mute or dial
            // into, so only Answer and Decline are shown.
            if call.phase != .ringing {
                if phone.callKeypadIsOpen {
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
                    // A video call is answered with the Cobalt camera button,
                    // so it can't be mistaken for a voice call (owner, 2026-10-12).
                    RoundCallButton(
                        symbol: call.videoCall ? "video.fill" : "phone.fill", words: "Answer",
                        colour: call.videoCall ? LinxColor.brandFill : LinxColor.call,
                        ink: call.videoCall ? LinxColor.onBrand : LinxColor.onCall
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
            CallToggleButton(symbol: "circle.grid.3x3.fill", words: "Keypad", on: phone.callKeypadIsOpen) {
                phone.callKeypadIsOpen.toggle()
            }
            .disabled(call.phase != .active)
            .opacity(call.phase == .active ? 1 : 0.5)
            AudioRouteButton(
                onSpeaker: call.speaker, deviceName: phone.whereTheSoundGoes
            ) { phone.toggleSpeaker() }
            // The picture has its own link to the other device (ADR-087):
            // the call itself never stops, and turning it off again leaves
            // an ordinary phone call. A desk phone or an outside number
            // can't show one, so the button is off for those.
            let noPicture = call.video.available == false
            CallToggleButton(
                symbol: "video.fill", words: phone.changingVideo ? "Starting…" : "Video", on: false,
                waiting: phone.changingVideo
            ) { phone.toggleVideo() }
            .disabled(call.phase != .active || phone.changingVideo || noPicture)
            .opacity(call.phase == .active && !noPicture ? 1 : 0.5)
            .accessibilityHint(noPicture ? "Their phone can't show a picture." : "")
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
            Button("Done") { phone.callKeypadIsOpen = false }
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
        case .ringing: return call.videoCall ? "Video calling you" : "Calling you"
        case .calling:
            if call.videoCall { return call.ringingThere ? "Ringing for a video call…" : "Video calling…" }
            return call.ringingThere ? "Ringing…" : "Calling…"
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
