import SwiftUI
@preconcurrency import WebRTC

/// A call with a picture in it (`docs/PHASE2.md` §7). It is the same call as
/// before — the same line, the same sound, the same system call on the lock
/// screen — with video added to it, so turning the camera off leaves an
/// ordinary phone call behind rather than ending anything.
///
/// **The picture is the screen** (owner, 2026-10-14). Whoever has the big
/// picture fills it, on a phone and an iPad alike; who it is, the buttons and
/// the **…** sit over it and go away by themselves after a few seconds, and a
/// tap on the picture brings them back. A tap on the small picture swaps the
/// two over. Only a folding phone opened out still divides the call into two
/// panels, because nothing may be laid over its crease (`CallLayout`).
struct VideoCallView: View {
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    /// Where this phone folds, when it folds and can say so (`Crease`).
    @Environment(\.crease) private var crease
    let call: PhoneModel.Call
    let now: Date

    /// Whether the controls are over the picture. They go after
    /// `controlsStay` with nobody touching the screen.
    @State private var controlsShown = true
    /// Counts touches, so each one starts the wait to hide them again.
    @State private var touches = 0

    static let controlsStay: Duration = .seconds(5)

    var body: some View {
        GeometryReader { geometry in
            let shape = CallLayout.videoShape(size: geometry.size, horizontal: horizontal, crease: crease)
            Group {
                switch shape {
                case .tall, .wide:
                    overlaid(shape: shape, size: geometry.size)
                case .split(let sideBySide):
                    split(sideBySide: sideBySide, size: geometry.size)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .background(LinxColor.surfaceDark.ignoresSafeArea())
        .task(id: touches) {
            try? await Task.sleep(for: Self.controlsStay)
            guard !Task.isCancelled, mayHideControls else { return }
            withAnimation(.easeOut(duration: 0.3)) { controlsShown = false }
        }
    }

    /// The controls only go while there is a picture to look at instead,
    /// and nothing on top of the call is waiting for an answer.
    private var mayHideControls: Bool {
        showsAPicture && !phone.callDetailsAreOpen && !phone.addingPerson && phone.askAboutTheirVideo == nil
    }

    private var showsAPicture: Bool {
        switch big {
        case .mine: return call.video.mine
        case .theirs: return call.video.theirs && call.video.theirPicture
        }
    }

    private func touched() {
        touches += 1
        if !controlsShown { withAnimation(.easeOut(duration: 0.2)) { controlsShown = true } }
    }

    private func tappedThePicture() {
        if controlsShown, mayHideControls {
            withAnimation(.easeOut(duration: 0.2)) { controlsShown = false }
        } else {
            touched()
        }
    }

    /// Who has the big screen. The other person by default — they are who you
    /// are talking to — and this phone when the person taps to swap them
    /// (owner's ask, 2026-10-05), or when theirs isn't there to show.
    private var big: Whose {
        guard call.video.theirs else { return .mine }
        guard call.video.mine else { return .theirs }
        return phone.myPictureIsBig ? .mine : .theirs
    }

    /// What goes in the small tile: whichever picture the big screen hasn't
    /// got, when there is one. With a single camera on in the call, that one
    /// picture *is* the big screen and showing it twice is just smaller.
    private var tile: Whose? {
        guard call.video.theirs else { return nil }
        return big == .theirs ? .mine : .theirs
    }

    /// The camera switch rides on this phone's own picture; when that is
    /// the big one, it goes in the top bar instead.
    private var bigFlip: (() -> Void)? {
        guard big == .mine, call.video.mine else { return nil }
        return {
            phone.switchCamera()
            touched()
        }
    }

    private func swap() {
        phone.swapPictures()
        touched()
    }

    private func smallPicture(_ whose: Whose, width: CGFloat) -> some View {
        SmallPicture(
            whose: whose, call: call, tracks: phone.tracks, mirrored: phone.mirrorsMyVideo,
            flip: whose == .mine && call.video.mine
                ? {
                    phone.switchCamera(); touched()
                } : nil,
            tap: swap
        )
        .frame(width: width)
    }

    // MARK: - The picture fills the screen

    /// The person and the **…** along the top; the buttons along the bottom
    /// upright, or down the trailing edge on its side so they never cross
    /// somebody's face. The small picture stays where it is when the rest
    /// goes, as it does in every video call app.
    private func overlaid(shape: CallLayout.Shape, size: CGSize) -> some View {
        let tileWidth = CallLayout.selfViewWidth(for: shape, size: size)
        return ZStack {
            Picture(whose: big, call: call, tracks: phone.tracks, mirrored: phone.mirrorsMyVideo, fills: true)
                .ignoresSafeArea()
                .contentShape(.rect)
                .onTapGesture(perform: tappedThePicture)
                .accessibilityAddTraits(.isButton)
                .accessibilityHint(controlsShown ? "Hides the call's buttons" : "Shows the call's buttons")

            if controlsShown {
                // A soft shade at the top and bottom keeps white words and
                // buttons readable over a bright picture, with no box round
                // them.
                VStack(spacing: 0) {
                    LinearGradient(
                        colors: [LinxColor.surfaceDark.opacity(0.6), .clear], startPoint: .top, endPoint: .bottom
                    )
                    .frame(height: 180)
                    Spacer(minLength: 0)
                    LinearGradient(
                        colors: [.clear, LinxColor.surfaceDark.opacity(0.6)], startPoint: .top, endPoint: .bottom
                    )
                    .frame(height: 200)
                }
                .ignoresSafeArea()
                .allowsHitTesting(false)
                .transition(.opacity)
            }

            VStack(spacing: LinxSpace.s3) {
                if controlsShown {
                    TopBar(
                        call: call, now: now, theirCameraOff: !call.video.theirs && call.video.mine,
                        flip: bigFlip
                    )
                    .transition(.opacity)
                }
                HStack(alignment: .top) {
                    Spacer(minLength: 0)
                    if let tile { smallPicture(tile, width: tileWidth) }
                }
                Spacer(minLength: 0)
                if controlsShown, shape == .tall {
                    Buttons(call: call, across: true)
                        .padding(.bottom, LinxSpace.s4)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .padding(.horizontal, LinxSpace.s4)
            .padding(.top, LinxSpace.s2)

            if controlsShown, shape == .wide {
                Buttons(call: call, across: false)
                    .padding(LinxSpace.s4)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomTrailing)
                    .transition(.move(edge: .trailing).combined(with: .opacity))
            }
        }
        .simultaneousGesture(TapGesture().onEnded { if controlsShown { touched() } })
    }

    // MARK: - A folding phone opened out: two panels

    private func split(sideBySide: Bool, size: CGSize) -> some View {
        let panel = VStack(spacing: LinxSpace.s5) {
            TopBar(
                call: call, now: now, theirCameraOff: !call.video.theirs && call.video.mine, flip: bigFlip)
            if let tile {
                smallPicture(tile, width: CallLayout.selfViewWidth(for: .split(sideBySide: sideBySide), size: size))
            }
            Spacer(minLength: 0)
            Buttons(call: call, across: true)
        }
        .padding(LinxSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)

        let picture = Picture(
            whose: big, call: call, tracks: phone.tracks, mirrored: phone.mirrorsMyVideo, fills: false
        )
        .clipShape(.rect(cornerRadius: LinxRadius.lg))
        .padding(LinxSpace.s4)
        .onTapGesture(perform: swap)

        // The picture's share of the screen. On a folding phone this is the
        // fold itself, so the join between the two panels and the crease are
        // one and the same line.
        let share = CallLayout.pictureShare(
            for: .split(sideBySide: sideBySide), size: size, crease: crease)

        return Group {
            if sideBySide {
                HStack(spacing: 0) {
                    picture.frame(width: size.width * share)
                    panel
                }
            } else {
                VStack(spacing: 0) {
                    picture.frame(height: size.height * share)
                    panel
                }
            }
        }
    }
}

// MARK: - The pieces

/// Whose picture a screen is showing.
enum Whose: Equatable, Sendable { case mine, theirs }

extension CallVideo {
    /// What a picture's space says while there's nothing to draw in it.
    func waiting(_ whose: Whose) -> String {
        switch whose {
        case .mine: return "Starting your camera…"
        case .theirs:
            guard theirs else { return "Their camera is off." }
            return theirPictureSeen
                ? "Their picture has stopped. The call carries on." : "Their picture is on its way…"
        }
    }
}

/// A picture, big: whoever has the big screen (`VideoCallView.big`). By
/// default that is the other person — they are who you are talking to — and a
/// tap swaps the two over (owner's ask, 2026-10-05). Where only this phone's
/// camera is on, a one-way video call is an ordinary call and has to look like
/// one (ADR-079, the owner's condition), so this phone's own picture fills the
/// screen. With no picture to show at all it says why, over their initials.
private struct Picture: View {
    let whose: Whose
    let call: PhoneModel.Call
    let tracks: VideoTracks
    let mirrored: Bool
    /// Fill the screen (a phone) or fit inside a panel (a pad).
    let fills: Bool

    var body: some View {
        ZStack {
            LinxColor.surfaceDark
            if let track = showing {
                VideoPicture(track: track, fills: fills, mirrored: whose == .mine && mirrored)
            } else {
                VStack(spacing: LinxSpace.s3) {
                    Text(CallView.initials(of: call.peer.name))
                        .font(.system(size: 44, weight: .semibold))
                        .foregroundStyle(LinxColor.onSurfaceDark)
                        .frame(width: 120, height: 120)
                        .background(LinxColor.surface.opacity(0.18), in: .circle)
                        .accessibilityHidden(true)
                    Text(waitingWords)
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.75))
                        .multilineTextAlignment(.center)
                }
            }
        }
        .accessibilityLabel(Self.label(whose, call: call, showing: showing != nil))
    }

    private var showing: RTCVideoTrack? {
        switch whose {
        case .mine: return call.video.mine ? tracks.local : nil
        case .theirs: return call.video.theirs && call.video.theirPicture ? tracks.remote : nil
        }
    }

    private var waitingWords: String { call.video.waiting(whose) }

    static func label(_ whose: Whose, call: PhoneModel.Call, showing: Bool) -> String {
        switch whose {
        case .mine: return showing ? "Your camera" : "Your camera is off"
        case .theirs: return showing ? "\(call.peer.name)'s camera" : "No picture from \(call.peer.name)"
        }
    }
}

/// The other picture, in a small rounded tile. Tapping it puts it on the big
/// screen and sends the big one here.
private struct SmallPicture: View {
    let whose: Whose
    let call: PhoneModel.Call
    let tracks: VideoTracks
    let mirrored: Bool
    /// This phone's own picture carries the front/back camera switch.
    let flip: (() -> Void)?
    let tap: () -> Void

    var body: some View {
        ZStack(alignment: .bottomTrailing) {
            Button(action: tap) {
                ZStack {
                    LinxColor.surface.opacity(0.25)
                    if let track = showing {
                        VideoPicture(track: track, fills: true, mirrored: whose == .mine && mirrored)
                    } else {
                        Image(systemName: "video.slash")
                            .font(.title3)
                            .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
                    }
                }
                .aspectRatio(3.0 / 4.0, contentMode: .fit)
                .clipShape(.rect(cornerRadius: LinxRadius.md))
                .overlay {
                    RoundedRectangle(cornerRadius: LinxRadius.md)
                        .strokeBorder(LinxColor.onSurfaceDark.opacity(0.25))
                }
            }
            .accessibilityLabel(Picture.label(whose, call: call, showing: showing != nil))
            .accessibilityHint("Puts this picture on the big screen")

            if let flip { SwitchCameraButton(press: flip, small: true) }
        }
    }

    private var showing: RTCVideoTrack? {
        switch whose {
        case .mine: return call.video.mine ? tracks.local : nil
        case .theirs: return call.video.theirs && call.video.theirPicture ? tracks.remote : nil
        }
    }
}

/// The front or the back camera (owner, 2026-10-14): on this phone's own
/// picture, as in every video call app.
private struct SwitchCameraButton: View {
    let press: () -> Void
    var small = false

    var body: some View {
        Button(action: press) {
            Image(systemName: "arrow.triangle.2.circlepath.camera")
                .font(small ? .footnote.weight(.semibold) : .body.weight(.semibold))
                .foregroundStyle(LinxColor.onSurfaceDark)
                .frame(width: small ? 30 : 36, height: small ? 30 : 36)
                .background(LinxColor.surfaceDark.opacity(0.55), in: .circle)
                .frame(width: 44, height: 44)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Switch camera")
        .accessibilityHint("Changes between the front and back camera")
    }
}

/// Who it is and how long it has been going on the left, with **Encrypted ⓘ**
/// under them; **HD** and the **…** on the right (owner, 2026-10-14).
private struct TopBar: View {
    let call: PhoneModel.Call
    let now: Date
    /// This phone's camera is on and theirs isn't, so the big picture is this
    /// phone's own: say whose it is, or a person would think they were
    /// looking at a call that had gone wrong.
    var theirCameraOff = false
    /// The camera switch, when this phone's own picture is the big one and
    /// so has no small tile to carry it.
    var flip: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: LinxSpace.s1) {
            HStack(alignment: .top, spacing: LinxSpace.s2) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(call.peer.name)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(LinxColor.onSurfaceDark)
                        .lineLimit(1)
                        .minimumScaleFactor(0.7)
                    Text(CallView.length(since: call.answeredAt, to: now))
                        .font(.subheadline.monospacedDigit())
                        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.85))
                    EncryptedInfo(tight: true)
                }
                .shadow(color: LinxColor.surfaceDark.opacity(0.5), radius: 3)
                Spacer(minLength: 0)
                if call.video.mine { HDButton(on: call.hd) }
                if let flip { SwitchCameraButton(press: flip) }
                CallMoreMenu()
            }
            if theirCameraOff {
                Text("Their camera is off. This is yours — they can see you.")
                    .font(.caption)
                    .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.85))
                    .shadow(color: LinxColor.surfaceDark.opacity(0.5), radius: 3)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// The **HD** button (owner, 2026-10-12; made smaller 2026-10-14): bold
/// slanted letters in a rounded badge. On is solid and bright; off is an
/// outline, dimmed, so the two can be told apart at a glance in a bright
/// room or a dark one.
private struct HDButton: View {
    @Environment(PhoneModel.self) private var phone
    let on: Bool

    var body: some View {
        Button {
            phone.toggleHD()
        } label: {
            Text("HD")
                .font(.system(size: 11, weight: .black).italic())
                .foregroundStyle(on ? LinxColor.surfaceDark : LinxColor.onSurfaceDark.opacity(0.7))
                .padding(.horizontal, 5)
                .padding(.vertical, 1)
                .background {
                    let badge = RoundedRectangle(cornerRadius: 4)
                    if on {
                        badge.fill(LinxColor.onSurfaceDark)
                    } else {
                        badge.stroke(LinxColor.onSurfaceDark.opacity(0.7), lineWidth: 1.2)
                    }
                }
                .transformEffect(CGAffineTransform(a: 1, b: 0, c: -0.18, d: 1, tx: 2, ty: 0))
                .frame(minWidth: 44, minHeight: 44)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("HD")
        .accessibilityValue(on ? "On" : "Off")
        .accessibilityHint(on ? "Holds your picture at 540p to use less data" : "Lets your picture reach 720p")
    }
}

/// Everything a person can press in a video call. `across` lays them in a
/// row (upright, or in a panel); otherwise they stand in a column.
private struct Buttons: View {
    @Environment(PhoneModel.self) private var phone
    let call: PhoneModel.Call
    let across: Bool

    var body: some View {
        let buttons = Group {
            CallCircle(
                symbol: call.muted ? "mic.slash.fill" : "mic.fill", words: call.muted ? "Muted" : "Mute",
                on: call.muted
            ) { phone.toggleMute() }
            CallCircle(
                symbol: call.video.mine ? "video.fill" : "video.slash.fill",
                words: call.video.mine ? "Stop video" : "Start video", on: call.video.mine,
                waiting: phone.changingVideo
            ) { phone.toggleVideo() }
            .disabled(phone.changingVideo)
            CallCircle(
                symbol: call.speaker ? "speaker.wave.3.fill" : "speaker.fill", words: "Speaker",
                on: call.speaker
            ) { phone.toggleSpeaker() }
            CallCircle(symbol: "phone.down.fill", words: "Hang up", on: false, danger: true) {
                phone.hangUp()
            }
        }

        return Group {
            if across {
                HStack(spacing: LinxSpace.s5) { buttons }
            } else {
                VStack(spacing: LinxSpace.s4) { buttons }
            }
        }
        .padding(LinxSpace.s3)
        .background(LinxColor.surfaceDark.opacity(0.45), in: .capsule)
    }
}

struct CallCircle: View {
    let symbol: String
    let words: String
    let on: Bool
    var danger = false
    /// The phone is doing it and hasn't finished.
    var waiting = false
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            Group {
                if waiting {
                    ProgressView().progressViewStyle(.circular).tint(ink)
                } else {
                    Image(systemName: symbol).font(.title3).foregroundStyle(ink)
                }
            }
            .frame(width: 56, height: 56)
            .background(fill, in: .circle)
        }
        .accessibilityLabel(words)
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    private var fill: Color {
        if danger { return LinxColor.end }
        return on ? LinxColor.onSurfaceDark : LinxColor.surface.opacity(0.22)
    }

    private var ink: Color {
        if danger { return LinxColor.onEnd }
        return on ? LinxColor.surfaceDark : LinxColor.onSurfaceDark
    }
}

/// One WebRTC picture, drawn by the phone's own graphics hardware. SwiftUI
/// has nothing of its own for this, so it is iOS's view in a wrapper.
struct VideoPicture: UIViewRepresentable {
    let track: RTCVideoTrack
    /// Fill the space and crop, or fit inside it and letterbox.
    let fills: Bool
    /// A front camera shows people themselves the way a mirror does.
    let mirrored: Bool

    func makeUIView(context: Context) -> RTCMTLVideoView {
        let view = RTCMTLVideoView()
        view.videoContentMode = fills ? .scaleAspectFill : .scaleAspectFit
        view.backgroundColor = .clear
        track.add(view)
        context.coordinator.showing = track
        context.coordinator.view = view
        return view
    }

    func updateUIView(_ view: RTCMTLVideoView, context: Context) {
        view.videoContentMode = fills ? .scaleAspectFill : .scaleAspectFit
        view.transform = mirrored ? CGAffineTransform(scaleX: -1, y: 1) : .identity
        guard context.coordinator.showing !== track else { return }
        context.coordinator.showing?.remove(view)
        track.add(view)
        context.coordinator.showing = track
    }

    static func dismantleUIView(_ view: RTCMTLVideoView, coordinator: Coordinator) {
        coordinator.showing?.remove(view)
        coordinator.showing = nil
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    /// Which track this view is showing, so it is taken off that track
    /// again when the call ends or the picture changes. SwiftUI only ever
    /// touches a representable's coordinator on the main thread, and
    /// `dismantleUIView` below is a static call with no actor of its own,
    /// so this is deliberately plain.
    final class Coordinator: @unchecked Sendable {
        var showing: RTCVideoTrack?
        weak var view: RTCMTLVideoView?
    }
}
