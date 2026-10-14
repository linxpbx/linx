import SwiftUI

/// "Encrypted ⓘ" at the top of a call. Every Linx call is encrypted end to
/// end over DTLS-SRTP; how it is getting through — direct or relayed, the
/// round trip, what the relay answered — is behind the ⓘ, in Call details
/// (owner, 2026-10-14: the route and the milliseconds in a grey box were
/// too much for a call screen).
struct EncryptedInfo: View {
    @Environment(PhoneModel.self) private var phone
    /// Under a name and a time, where a full-height row leaves a gap.
    var tight = false

    var body: some View {
        Button {
            phone.callDetailsAreOpen = true
        } label: {
            HStack(spacing: LinxSpace.s1) {
                Image(systemName: "lock.fill").font(.caption2)
                Text("Encrypted").font(.caption.weight(.medium))
                Image(systemName: "info.circle").font(.footnote)
            }
            .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.85))
            .frame(minHeight: tight ? 28 : 44)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Encrypted")
        .accessibilityHint("Opens the call details")
    }
}

/// The **…** at the top right of a call: adding a person, and the call's
/// details (owner, 2026-10-14: Add person off the screen, into a menu).
struct CallMoreMenu: View {
    @Environment(PhoneModel.self) private var phone

    var body: some View {
        Menu {
            if phone.canAddPeople {
                Button {
                    phone.addingPerson = true
                } label: {
                    Label("Add person", systemImage: "person.badge.plus")
                }
            }
            Button {
                phone.callDetailsAreOpen = true
            } label: {
                Label("Call details", systemImage: "info.circle")
            }
        } label: {
            Image(systemName: "ellipsis")
                .font(.body.weight(.semibold))
                .foregroundStyle(LinxColor.onSurfaceDark)
                .frame(width: 36, height: 36)
                .background(LinxColor.surface.opacity(0.22), in: .circle)
                .frame(width: 44, height: 44)
                .contentShape(.rect)
        }
        .accessibilityLabel("More")
    }
}
