import SwiftUI
import UIKit

/// Settings (`docs/PHASE2.md` §7): the few things that are this phone's own
/// — its status, how calls arrive here, what the camera does — and the way
/// out. Everything about the *account* (its password, its authenticator, its
/// passkeys) stays in the web app on a computer, because a phone's token is
/// a client's and never an administrator's (owner, 2026-10-03).
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @AppStorage(Settings.startVideoCallsWithTheFrontCamera) private var frontCamera = true
    @AppStorage(Settings.appearance) private var appearance = Appearance.system.rawValue
    @AppStorage(Settings.showCallsInThePhoneApp) private var inPhoneApp = true
    @State private var signingOut = false

    private var ready: Bool { phone.status == .ready }

    var body: some View {
        List {
            Section("My status") {
                Picker(
                    "My status",
                    selection: Binding(
                        get: { home.presence },
                        set: { choice in
                            Task { await home.setPresence(choice) }
                        })
                ) {
                    ForEach(Presence.allCases, id: \.self) { presence in
                        Text(presence.words).tag(presence)
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                Text("Do not disturb stops your extension ringing anywhere. Callers go to your voicemail.")
                    .font(.footnote)
                    .foregroundStyle(LinxColor.textMuted)
            }

            Section("This phone's line") {
                LabeledContent("Status", value: lineWords)
                LabeledContent("Extension", value: model.identity?.extensionNumber ?? "—")
                LabeledContent("Name", value: model.identity?.deviceName ?? "—")
                if let identity = model.identity {
                    LabeledContent(
                        "Set up again by",
                        value: identity.setUpAgain.formatted(.dateTime.day().month().year()))
                }
            }

            Section {
                Toggle("Start video calls with the front camera", isOn: $frontCamera)
                Text(
                    "Video is added to a call that is already up: press the video button during a call, or the video button beside someone in Team. Linx keeps the picture small so the sound never suffers for it, and turns it off by itself if the connection gets too slow."
                )
                .font(.footnote)
                .foregroundStyle(LinxColor.textMuted)
            } header: {
                Text("Video")
            }

            Section {
                Picker("Appearance", selection: $appearance) {
                    ForEach(Appearance.allCases, id: \.self) { choice in
                        Text(choice.words).tag(choice.rawValue)
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
            } header: {
                Text("Appearance")
            }

            Section {
                Toggle("Show Linx calls in the Phone app", isOn: $inPhoneApp)
                    .onChange(of: inPhoneApp) { _, on in phone.showCallsInThePhoneApp(on) }
                Text(
                    "On, a Linx call sits in the iPhone's own Recents beside your ordinary calls, and you can tap one to ring it back from there. Off, Linx keeps its calls to itself — the Calls tab still has every one of them. Calls already made stay where they are."
                )
                .font(.footnote)
                .foregroundStyle(LinxColor.textMuted)
            } header: {
                Text("Your call history")
            }

            Section("How calls arrive here") {
                Text(CallStyle.inAppRingingNote ?? Self.lockScreenNote)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
                Button("Notification settings") {
                    guard let url = URL(string: UIApplication.openSettingsURLString) else { return }
                    UIApplication.shared.open(url)
                }
                .foregroundStyle(LinxColor.accent)
            }

            if let problem = model.problem ?? home.problem ?? phone.problem {
                Section { Text(problem).font(.subheadline).foregroundStyle(LinxColor.end) }
            }

            Section {
                Button("Sign out of this phone", role: .destructive) { signingOut = true }
            } footer: {
                Text(
                    "Signing out forgets this phone's key. If you've lost the phone, stop it in Linx on a computer: My account → My phones → I've lost it."
                )
            }
        }
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog(
            "Sign out of this phone?", isPresented: $signingOut, titleVisibility: .visible
        ) {
            Button("Sign out", role: .destructive) {
                Task { await model.signOut() }
            }
            Button("Keep me signed in", role: .cancel) {}
        } message: {
            Text("This phone will stop ringing, and you'll need a new setup code to use it again.")
        }
    }

    private var lineWords: String {
        switch phone.status {
        case .ready: return "Ready"
        case .starting: return "Getting your phone line…"
        case .reconnecting: return "Reconnecting…"
        case .unavailable: return "No phone line"
        }
    }

    private static let lockScreenNote = """
        Calls ring on the lock screen like any other call, even when the app \
        is closed — that's what the Linx app is for. Keep notifications on \
        so a missed call and a new voicemail reach you too.
        """
}

/// What this phone remembers about itself, between runs. Nothing here is a
/// secret, and nothing here is anybody's but this phone's: the account's own
/// settings live in Linx (docs/PHASE2.md §7).
enum Settings {
    static let startVideoCallsWithTheFrontCamera = "linx.video.front-camera"
    static let appearance = "linx.appearance"
    /// Whether a Linx call also goes into the iPhone's own Phone app, under
    /// Recents, beside the person's ordinary calls.
    static let showCallsInThePhoneApp = "linx.calls.in-phone-app"
    /// The extensions this phone keeps at the top of Team.
    static let favourites = "linx.team.favourites"
}

/// Light, dark, or whatever the phone itself is set to — the same choice the
/// web app has on every page (owner, 2026-09-30), kept per phone.
enum Appearance: String, CaseIterable {
    case system
    case light
    case dark

    var words: String {
        switch self {
        case .system: return "Match this device"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }

    var scheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}

#Preview("Settings") {
    let model = AppModel()
    model.identity = Screen.sampleIdentity
    return NavigationStack { SettingsView() }
        .environment(model)
        .environment(model.home)
        .environment(PhoneModel(line: { nil }))
}
