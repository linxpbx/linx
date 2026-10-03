import SwiftUI

/// This phone's own details, behind the person button on the home screen:
/// who it is for, which extension it answers, how its line is doing, and
/// "sign out of this phone".
///
/// The Calls, Team and More screens arrive in build-order step 7
/// (`docs/PHASE2.md` §12), and ringing a sleeping phone is steps 5 and 6.
struct SignedInView: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone

    private var ready: Bool { phone.status == .ready }

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
                        Image(systemName: ready ? "checkmark.circle" : "phone.badge.waveform")
                            .foregroundStyle(ready ? LinxColor.Status.available : LinxColor.textMuted)
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: LinxSpace.s1) {
                            Text(ready ? "Your phone line is ready" : "Getting your phone line…")
                                .font(.headline)
                                .foregroundStyle(LinxColor.text)
                            Text(
                                ready
                                    ? "Calls come and go on this phone while the app is open. Ringing when it's asleep comes next."
                                    : "The app asks Linx for it every time it starts, and never keeps a password."
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
    SignedInView().environment(AppModel()).environment(PhoneModel(line: { nil }))
}

#Preview("Set up again") {
    SetUpAgainView(reason: "This phone is no longer set up. Set it up again.").environment(AppModel())
}
