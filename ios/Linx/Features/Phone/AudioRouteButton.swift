import AVFoundation
import AVKit
import SwiftUI
import UIKit

/// The sound button on the call screen, which behaves as the iPhone's own
/// Phone app does (owner, 2026-10-04: "why don't you use a similar behaviour
/// of the phone app?").
///
///   - With nothing else plugged in or paired, it is a plain **Speaker**
///     switch: on puts the call on the loudspeaker, off puts it back to the
///     earpiece. That is the whole of it, and it says "Speaker" either way —
///     a button that renames itself is not what anybody expects.
///   - With a headset, a car or anything else connected, there is more than
///     one answer, so it becomes the system's own picker — the same list
///     the Phone app shows — and wears the name of whatever the sound is
///     coming out of now.
///
/// The picker is the system's, which also means the system moves the sound:
/// nothing here fights it for the route.
struct AudioRouteButton: View {
    /// Whether the sound is on the loudspeaker now.
    let onSpeaker: Bool
    /// What the sound is coming out of, when it isn't the phone itself.
    let deviceName: String?
    let toggle: () -> Void

    var body: some View {
        if let deviceName {
            RoutePicker(words: deviceName)
        } else {
            CallToggleButton(
                symbol: onSpeaker ? "speaker.wave.3.fill" : "speaker.fill", words: "Speaker",
                on: onSpeaker, press: toggle)
        }
    }
}

/// The system's route picker, drawn as one of the call screen's own round
/// buttons so it doesn't look like a stranger among them.
private struct RoutePicker: View {
    let words: String

    var body: some View {
        VStack(spacing: LinxSpace.s2) {
            SystemRoutePicker()
                .frame(width: 56, height: 56)
                .background(LinxColor.surface.opacity(0.18), in: .circle)
            Text(words)
                .font(.caption)
                .lineLimit(1)
                .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
        }
        .accessibilityLabel("Where the sound comes out: \(words)")
    }
}

private struct SystemRoutePicker: UIViewRepresentable {
    func makeUIView(context: Context) -> AVRoutePickerView {
        let picker = AVRoutePickerView()
        picker.activeTintColor = UIColor(LinxColor.accent)
        picker.tintColor = UIColor(LinxColor.onSurfaceDark)
        picker.prioritizesVideoDevices = false
        return picker
    }

    func updateUIView(_ picker: AVRoutePickerView, context: Context) {}
}

/// One of the call screen's round buttons. It lives here because the sound
/// button and the others have to look identical.
struct CallToggleButton: View {
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
                    .frame(width: 56, height: 56)
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
