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
/// there is room (`sidebarAdaptable`), and each tab is then a **list beside
/// a detail** rather than a phone's column floated in the middle of a
/// 13-inch screen (step 8, ADR-076).
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone
    @State private var tab: Tabs = Screen.launched?.tab ?? .keypad
    /// Starred people, kept on this phone. One list for the whole app: Team
    /// stars them and the keypad's speed dial reads the same stars, so the
    /// two can't drift apart until the app is next started.
    @State private var favourites = Favourites()

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
        .environment(favourites)
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
///
/// On a screen with room for two columns the keypad is not left floating in
/// the middle of it (ADR-076): the starred people stand beside it, which is
/// what the buttons down the side of a desk phone have always been for.
struct KeypadScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    /// Open from the start only for the screenshot that shows it
    /// (`ios/tools/screens.sh`); always closed in a release build.
    @State private var showThisPhone = Screen.launched == .thisPhone

    var body: some View {
        Group {
            if horizontal == .regular {
                NavigationSplitView {
                    SpeedDial()
                        .navigationTitle("Favourites")
                } detail: {
                    pad
                }
            } else {
                pad
            }
        }
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

    private var pad: some View {
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
    }
}

/// The starred people, and the last number rung, beside the keypad on a
/// screen with room for them. Tapping one **puts the number on the keypad**
/// rather than ringing it, for the same reason redial does: a pocket, or a
/// tablet picked up off a desk, must not be able to ring anybody.
struct SpeedDial: View {
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @Environment(Favourites.self) private var favourites

    private var starred: [TeamMember] { favourites.split(home.members).favourites }

    var body: some View {
        List {
            if !phone.lastDialled.isEmpty {
                Section("Last number") {
                    Button {
                        fill(phone.lastDialled)
                    } label: {
                        Label(phone.lastDialled, systemImage: "arrow.counterclockwise")
                            .foregroundStyle(LinxColor.text)
                    }
                }
            }

            Section {
                if starred.isEmpty {
                    Text("Star somebody in Team and they stand here, beside the keypad.")
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.textMuted)
                } else {
                    ForEach(starred) { member in
                        SpeedDialRow(member: member, fill: { fill(member.extensionNumber) })
                    }
                }
            }
        }
        .listStyle(.sidebar)
    }

    private func fill(_ number: String) {
        @Bindable var phone = phone
        phone.typed = number
    }
}

private struct SpeedDialRow: View {
    @Environment(PhoneModel.self) private var phone
    let member: TeamMember
    let fill: () -> Void

    private var callable: Bool { phone.status == .ready && phone.call == nil }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Button(action: fill) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(member.name)
                        .font(.body.weight(.medium))
                        .foregroundStyle(LinxColor.text)
                    Text(member.extensionNumber + " · " + member.words)
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.textMuted)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel("Put \(member.name)'s extension on the keypad")

            Button {
                phone.callNumber(member.extensionNumber, name: member.name)
            } label: {
                Image(systemName: "phone")
                    .font(.title3)
                    .foregroundStyle(callable ? LinxColor.accent : LinxColor.textMuted)
            }
            .buttonStyle(.borderless)
            .disabled(!callable)
            .accessibilityLabel("Call \(member.name)")
        }
        .padding(.vertical, LinxSpace.s1)
    }
}

/// Everything that isn't a call: voicemail and settings. On a big screen
/// the two stand in a list with the chosen one open beside them, like every
/// other tab (step 8).
struct MoreView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(\.horizontalSizeClass) private var horizontal
    @State private var page: Page? = Screen.launched?.morePage

    /// The screens behind **More**.
    enum Page: Hashable {
        case voicemail
        case settings
    }

    var body: some View {
        NavigationSplitView {
            List(selection: $page) {
                Section {
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
                    .tag(Page.voicemail)

                    Label("Settings", systemImage: "gearshape")
                        .tag(Page.settings)
                }

                Section {
                    LabeledContent("Extension", value: model.identity?.extensionNumber ?? "—")
                    LabeledContent("This phone", value: model.identity?.deviceName ?? "—")
                } header: {
                    Text(model.identity?.personName ?? "Signed in")
                }
            }
            .navigationTitle("More")
        } detail: {
            NavigationStack {
                switch page {
                case .voicemail: VoicemailView()
                case .settings: SettingsView()
                case nil:
                    NothingPicked(
                        symbol: "ellipsis.circle", title: "Nothing picked",
                        words: "Pick Voicemail or Settings.")
                }
            }
        }
        .picksTheFirstRow($page, first: { Page.voicemail }, when: horizontal == .regular)
    }
}

#Preview("Home") {
    HomeView()
        .environment(AppModel())
        .environment(PhoneModel(line: { nil }))
}
