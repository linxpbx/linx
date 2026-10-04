import SwiftUI

/// What a set-up phone shows: four tabs — **Calls**, **Team**, **Keypad**
/// and **More** (`docs/ui/iOS · Team & presence@1x.png`, in Cobalt rather
/// than the mockup's teal).
///
/// Four and no more. Meetings arrive with Phase 3 and Chat with Phase 4, and
/// neither gets a tab before it works: App Review refuses an app with
/// "coming soon" in it (`docs/PHASE2.md` §14 item 2).
///
/// On an iPad — and on an iPhone Duo's inner screen — the same four become a
/// sidebar beside the content, because the system turns them into one when
/// there is room (`sidebarAdaptable`).
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone
    @State private var tab: Tabs = Screen.launched?.tab ?? .keypad

    enum Tabs: String {
        case calls, team, keypad, more
    }

    var body: some View {
        @Bindable var model = model
        TabView(selection: $tab) {
            Tab("Calls", systemImage: "clock", value: Tabs.calls) {
                CallsView()
            }
            .badge(model.home.missedCalls)

            Tab("Team", systemImage: "person.2", value: Tabs.team) {
                TeamView()
            }

            Tab("Keypad", systemImage: "circle.grid.3x3", value: Tabs.keypad) {
                KeypadScreen()
            }

            Tab("More", systemImage: "ellipsis.circle", value: Tabs.more) {
                MoreView()
            }
            .badge(model.home.newVoicemail)
        }
        .tabViewStyle(.sidebarAdaptable)
        .tint(LinxColor.brandFill)
        .environment(model.home)
        .task {
            // A screenshot run has its contents already and talks to
            // nothing at all (`Screen`).
            guard Screen.launched == nil else { return }
            model.home.start()
        }
        .onDisappear { model.home.stop() }
    }
}

/// The keypad, with the line's state and this phone's details above it —
/// the screen the app had before there were tabs.
struct KeypadScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone
    /// Open from the start only for the screenshot that shows it
    /// (`ios/tools/screens.sh`); always closed in a release build.
    @State private var showThisPhone = Screen.launched == .thisPhone

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                LineStatusPill(extensionNumber: model.identity?.extensionNumber ?? "")
                Spacer()
                Button {
                    showThisPhone = true
                } label: {
                    Image(systemName: "person.crop.circle")
                        .font(.title2)
                        .foregroundStyle(LinxColor.textMuted)
                }
                .accessibilityLabel("This phone")
            }
            .padding(.horizontal, LinxSpace.s6)
            .padding(.top, LinxSpace.s5)

            KeypadView()
        }
        .linxBackground()
        .sheet(isPresented: $showThisPhone) {
            NavigationStack {
                SignedInView()
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { showThisPhone = false }
                        }
                    }
            }
        }
    }
}

/// Everything that isn't a call: voicemail and settings.
struct MoreView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @State private var path: [Page] = Screen.launched?.morePage.map { [$0] } ?? []

    /// The screens behind **More**.
    enum Page: Hashable {
        case voicemail
        case settings
    }

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    NavigationLink(value: Page.voicemail) {
                        Label {
                            HStack {
                                Text("Voicemail")
                                Spacer()
                                if home.newVoicemail > 0 {
                                    Text("\(home.newVoicemail)")
                                        .font(.footnote.weight(.semibold))
                                        .foregroundStyle(LinxColor.onBrand)
                                        .padding(.horizontal, LinxSpace.s2)
                                        .padding(.vertical, 2)
                                        .background(LinxColor.brandFill, in: .capsule)
                                        .accessibilityLabel("\(home.newVoicemail) new")
                                }
                            }
                        } icon: {
                            Image(systemName: "recordingtape")
                        }
                    }

                    NavigationLink(value: Page.settings) {
                        Label("Settings", systemImage: "gearshape")
                    }
                }

                Section {
                    LabeledContent("Extension", value: model.identity?.extensionNumber ?? "—")
                    LabeledContent("This phone", value: model.identity?.deviceName ?? "—")
                } header: {
                    Text(model.identity?.personName ?? "Signed in")
                }
            }
            .navigationTitle("More")
            .navigationDestination(for: Page.self) { page in
                switch page {
                case .voicemail: VoicemailView()
                case .settings: SettingsView()
                }
            }
        }
    }
}

#Preview("Home") {
    HomeView()
        .environment(AppModel())
        .environment(PhoneModel(line: { nil }))
}
