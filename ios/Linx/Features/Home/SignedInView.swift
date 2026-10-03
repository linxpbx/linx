import SwiftUI

/// What the app shows once this phone is set up and signed in: who it is,
/// which extension it answers for, and that its phone line is ready.
///
/// The Calls, Team, Keypad and More screens arrive in build-order step 7
/// (`docs/PHASE2.md` §12); making and taking the calls is step 4b and the
/// ringing is steps 5 and 6. This screen is what a person sees in between,
/// and it is also where "sign out of this phone" lives.
struct SignedInView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: LinxSpace.s4) {
                Text("THIS PHONE")
                    .font(.footnote.weight(.semibold))
                    .kerning(0.8)
                    .foregroundStyle(LinxColor.accent)

                Text(model.identity?.personName ?? "Signed in")
                    .font(.largeTitle.weight(.bold))
                    .foregroundStyle(LinxColor.text)

                if let identity = model.identity {
                    Text("Extension \(identity.extensionNumber) · \(identity.deviceName)")
                        .font(.body)
                        .foregroundStyle(LinxColor.textMuted)
                }

                LinxCard {
                    HStack(alignment: .top, spacing: LinxSpace.s3) {
                        Image(systemName: model.line == nil ? "phone.badge.waveform" : "checkmark.circle")
                            .foregroundStyle(model.line == nil ? LinxColor.textMuted : LinxColor.Status.available)
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: LinxSpace.s1) {
                            Text(model.line == nil ? "Getting your phone line…" : "Your phone line is ready")
                                .font(.headline)
                                .foregroundStyle(LinxColor.text)
                            Text(
                                model.line == nil
                                    ? "The app asks Linx for it every time it starts."
                                    : "Calls arrive in the next part of the app."
                            )
                            .font(.subheadline)
                            .foregroundStyle(LinxColor.textMuted)
                        }
                    }
                }

                if let identity = model.identity {
                    LinxCard {
                        VStack(alignment: .leading, spacing: LinxSpace.s1) {
                            Text("Set up again by \(identity.setUpAgain, format: .dateTime.day().month().year())")
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(LinxColor.text)
                            Text(
                                "Only if this phone goes that long without being in touch, or the password of the Linx account changes. While you use it, it keeps itself going."
                            )
                            .font(.subheadline)
                            .foregroundStyle(LinxColor.textMuted)
                        }
                    }
                }

                if let problem = model.problem {
                    Text(problem)
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.end)
                }

                Button("Sign out of this phone") {
                    Task { await model.signOut() }
                }
                .buttonStyle(.linxSecondary)
                .padding(.top, LinxSpace.s2)

                Text(
                    "Signing out forgets this phone's key. If you've lost the phone, stop it in Linx on a computer: My account → My phones → I've lost it."
                )
                .font(.footnote)
                .foregroundStyle(LinxColor.textMuted)
            }
            .padding(.horizontal, LinxSpace.s6)
            .padding(.top, LinxSpace.s6)
            .frame(maxWidth: 640, alignment: .leading)
            .frame(maxWidth: .infinity)
        }
        .linxBackground()
    }
}

/// The screen for a phone Linx no longer knows: six months with no contact,
/// a changed password, or an admin stopped it (docs/PHASE2.md §9).
struct SetUpAgainView: View {
    @Environment(AppModel.self) private var model
    let reason: String

    var body: some View {
        VStack(alignment: .leading, spacing: LinxSpace.s4) {
            Spacer(minLength: 0)
            Image(systemName: "iphone.slash")
                .font(.largeTitle)
                .foregroundStyle(LinxColor.textMuted)
                .accessibilityHidden(true)
            Text("Set this phone up again")
                .font(.largeTitle.weight(.bold))
                .foregroundStyle(LinxColor.text)
            Text(reason)
                .font(.body)
                .foregroundStyle(LinxColor.textMuted)
            Text("Ask for a new setup code — a QR code or an emailed link — and scan it here.")
                .font(.body)
                .foregroundStyle(LinxColor.textMuted)
            Button("Start again") {
                Task { await model.signOut() }
            }
            .buttonStyle(.linxPrimary)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, LinxSpace.s6)
        .frame(maxWidth: 640, alignment: .leading)
        .frame(maxWidth: .infinity)
        .linxBackground()
    }
}

#Preview("Signed in") {
    SignedInView().environment(AppModel())
}

#Preview("Set up again") {
    SetUpAgainView(reason: "This phone is no longer set up. Set it up again.").environment(AppModel())
}
