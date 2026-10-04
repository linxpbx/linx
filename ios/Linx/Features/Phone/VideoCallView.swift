import SwiftUI
@preconcurrency import WebRTC

/// A call with a picture in it (`docs/PHASE2.md` §7). It is the same call as
/// before — the same line, the same sound, the same system call on the lock
/// screen — with video added to it, so turning the camera off leaves an
/// ordinary phone call behind rather than ending anything.
///
/// The layout follows the screen, not the device (`CallLayout`): upright, on
/// its side, an iPad, and an iPhone Duo folded and unfolded.
struct VideoCallView: View {
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    let call: PhoneModel.Call
    let now: Date

    var body: some View {
        GeometryReader { geometry in
            let shape = CallLayout.shape(size: geometry.size, horizontal: horizontal)
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
    }

    // MARK: - A phone: the picture fills the screen

    /// Upright, the buttons lie along the bottom; on its side they stand in
    /// a column down the trailing edge, so they never cross the middle of
    /// somebody's face and never sit under a notch.
    private func overlaid(shape: CallLayout.Shape, size: CGSize) -> some View {
        ZStack(alignment: shape == .wide ? .topLeading : .top) {
            TheirPicture(call: call, tracks: phone.tracks, fills: true)
                .ignoresSafeArea()

            VStack(spacing: LinxSpace.s2) {
                Who(call: call, now: now, big: false)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, LinxSpace.s5)
            .padding(.top, LinxSpace.s3)
            .frame(maxWidth: shape == .wide ? size.width * 0.5 : .infinity, alignment: .leading)

            if shape == .wide {
                HStack(alignment: .bottom) {
                    MyPicture(mirrored: phone.mirrorsMyVideo, tracks: phone.tracks, on: call.video.mine)
                        .frame(width: CallLayout.selfViewWidth(for: shape, size: size))
                    Spacer(minLength: 0)
                    Buttons(call: call, across: false)
                }
                .padding(LinxSpace.s4)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
            } else {
                VStack(spacing: 0) {
                    HStack {
                        Spacer(minLength: 0)
                        MyPicture(mirrored: phone.mirrorsMyVideo, tracks: phone.tracks, on: call.video.mine)
                            .frame(width: CallLayout.selfViewWidth(for: shape, size: size))
                    }
                    .padding(.top, LinxSpace.s10)
                    .padding(.horizontal, LinxSpace.s4)
                    Spacer(minLength: 0)
                    Buttons(call: call, across: true)
                        .padding(.bottom, LinxSpace.s6)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
    }

    // MARK: - An iPad, or a Duo opened out: two panels

    private func split(sideBySide: Bool, size: CGSize) -> some View {
        let panel = VStack(spacing: LinxSpace.s5) {
            Who(call: call, now: now, big: true)
            MyPicture(mirrored: phone.mirrorsMyVideo, tracks: phone.tracks, on: call.video.mine)
                .frame(width: CallLayout.selfViewWidth(for: .split(sideBySide: sideBySide), size: size))
            Buttons(call: call, across: true)
        }
        .padding(LinxSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)

        let picture = TheirPicture(call: call, tracks: phone.tracks, fills: false)
            .clipShape(.rect(cornerRadius: LinxRadius.lg))
            .padding(LinxSpace.s4)

        return Group {
            if sideBySide {
                HStack(spacing: 0) {
                    picture.frame(width: size.width * 0.62)
                    panel
                }
            } else {
                VStack(spacing: 0) {
                    picture.frame(height: size.height * 0.6)
                    panel
                }
            }
        }
    }
}

// MARK: - The pieces

/// The other side's picture, or — until the first frame arrives, or while
/// only this phone has its camera on — their initials, which is what a
/// person needs to see is still the right call.
private struct TheirPicture: View {
    let call: PhoneModel.Call
    let tracks: VideoTracks
    /// Fill the screen (a phone) or fit inside a panel (a pad).
    let fills: Bool

    var body: some View {
        ZStack {
            LinxColor.surfaceDark
            if call.video.theirs, let track = tracks.remote {
                VideoPicture(track: track, fills: fills, mirrored: false)
            } else {
                VStack(spacing: LinxSpace.s3) {
                    Text(CallView.initials(of: call.peer.name))
                        .font(.system(size: 44, weight: .semibold))
                        .foregroundStyle(LinxColor.onSurfaceDark)
                        .frame(width: 120, height: 120)
                        .background(LinxColor.surface.opacity(0.18), in: .circle)
                        .accessibilityHidden(true)
                    Text(call.video.mine ? "They can see you. Their camera is off." : "Their camera is off.")
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.75))
                        .multilineTextAlignment(.center)
                }
            }
        }
    }
}

/// This phone's own picture, in a small rounded tile.
private struct MyPicture: View {
    let mirrored: Bool
    let tracks: VideoTracks
    let on: Bool

    var body: some View {
        ZStack {
            LinxColor.surface.opacity(0.25)
            if on, let track = tracks.local {
                VideoPicture(track: track, fills: true, mirrored: mirrored)
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
        .accessibilityLabel(on ? "Your camera" : "Your camera is off")
    }
}

/// Who it is, how long it has been going, and how the call is getting
/// through — the same three facts the sound-only screen shows.
private struct Who: View {
    let call: PhoneModel.Call
    let now: Date
    let big: Bool

    var body: some View {
        VStack(alignment: big ? .center : .leading, spacing: LinxSpace.s1) {
            Text(call.peer.name)
                .font(big ? .largeTitle.weight(.bold) : .headline)
                .foregroundStyle(LinxColor.onSurfaceDark)
                .lineLimit(2)
                .minimumScaleFactor(0.7)
            Text(CallView.length(since: call.answeredAt, to: now))
                .font(.subheadline.monospacedDigit())
                .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
            ConnectionWords(call: call)
        }
        .frame(maxWidth: .infinity, alignment: big ? .center : .leading)
        .padding(.horizontal, LinxSpace.s3)
        .padding(.vertical, LinxSpace.s2)
        .background(big ? Color.clear : LinxColor.surfaceDark.opacity(0.45), in: .rect(cornerRadius: LinxRadius.md))
    }
}

private struct ConnectionWords: View {
    let call: PhoneModel.Call

    var body: some View {
        HStack(spacing: LinxSpace.s1) {
            Image(systemName: "lock.fill").font(.caption2).accessibilityHidden(true)
            Text(words).font(.caption)
        }
        .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
    }

    private var words: String {
        guard let connection = call.connection else { return "Encrypted" }
        var out = "Encrypted · " + (connection.route == .direct ? "Direct" : "Relayed")
        if let rtt = connection.roundTripMs { out += " · \(rtt) ms" }
        return out
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
                words: call.video.mine ? "Stop video" : "Start video", on: call.video.mine
            ) { phone.toggleVideo() }
            CallCircle(symbol: "arrow.trianglehead.2.clockwise.rotate.90.camera", words: "Flip camera", on: false) {
                phone.switchCamera()
            }
            .disabled(!call.video.mine)
            .opacity(call.video.mine ? 1 : 0.4)
            CallCircle(
                symbol: call.speaker ? "speaker.wave.3.fill" : "speaker.fill",
                words: phone.audioRoute.isEmpty ? "Speaker" : phone.audioRoute,
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
        .padding(across ? LinxSpace.s3 : LinxSpace.s3)
        .background(LinxColor.surfaceDark.opacity(0.45), in: .capsule)
    }
}

private struct CallCircle: View {
    let symbol: String
    let words: String
    let on: Bool
    var danger = false
    let press: () -> Void

    var body: some View {
        Button(action: press) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(ink)
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
