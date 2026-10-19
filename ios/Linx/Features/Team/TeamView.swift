import SwiftUI

/// The Team tab (`docs/ui/iOS · Team & presence@1x.png`, in Cobalt): every
/// extension, who is on it, what they are doing this moment, and a button to
/// ring them — or to ring them and turn the camera on.
///
/// It is live: Linx sends the whole list again whenever anything in it
/// changes, so "On a call" goes grey the moment they put the phone down
/// (`HomeModel`). Favourites and departments belong to a later phase and so
/// are not shown — a filter that filters nothing is a placeholder
/// (`docs/PHASE2.md` §14 item 2).
struct TeamView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    @State private var search = ""
    /// Starred people, kept on this phone. The one list the app has
    /// (`HomeView`), so the keypad's speed dial shows the same stars.
    @Environment(Favourites.self) private var favourites
    /// Whose card the right-hand side is showing (step 8). On a phone there
    /// is no right-hand side and picking somebody pushes their card.
    @State private var chosen: TeamMember.ID?

    private var mine: String { model.identity?.extensionNumber ?? "" }

    /// One account's team: the people on it, who this phone is there, and
    /// the name of the section they go under. Two companies' people are
    /// never one directory, so each account keeps its own section
    /// (docs/MULTI_ACCOUNT.md §7).
    @MainActor private struct Team: Identifiable {
        let account: Account
        nonisolated var id: UUID { account.id }
        var members: [TeamMember] { account.home.members }
        var mine: String { account.identity?.extensionNumber ?? "" }
    }

    private var teams: [Team] {
        let signedIn = model.accounts.filter { $0.identity != nil }
        guard !signedIn.isEmpty else { return [Team(account: model.current)] }
        return signedIn.map(Team.init)
    }

    private func shown(_ team: Team) -> [TeamMember] {
        let text = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !text.isEmpty else { return team.members }
        return team.members.filter {
            $0.name.lowercased().contains(text) || $0.extensionNumber.contains(text)
        }
    }

    /// Everybody on screen, across every account, in the order they are
    /// shown: what "pick the first one" and "who is chosen" are read from.
    private var everyone: [(team: Team, member: TeamMember)] {
        teams.flatMap { team in shown(team).map { (team, $0) } }
    }

    /// One person, with the star that keeps them at the top of this phone's
    /// own list. A star belongs to the account the person is on.
    @ViewBuilder private func row(_ member: TeamMember, on team: Team) -> some View {
        let account = team.account.accountID
        let starred = favourites.has(member.extensionNumber, on: account)
        TeamRow(member: member, isMe: member.extensionNumber == team.mine, selected: chosen == member.id)
            .environment(team.account.phone)
            .swipeActions(edge: .leading) {
                Button(starred ? "Unstar" : "Favourite", systemImage: starred ? "star.slash" : "star") {
                    favourites.toggle(member.extensionNumber, on: account)
                }
                // Amber, the colour of the star itself. Not the brand blue:
                // on a phone, blue down the side of a row means "mark read
                // or unread" and nothing else (owner, 2026-10-05).
                .tint(LinxColor.star)
            }
    }

    /// A section's heading. With one account it is what it always was —
    /// "Favourites", or nothing at all. With several, the account's name
    /// goes on it, because the same extension number means two different
    /// people on two different systems.
    private func sectionTitle(_ what: String?, _ team: Team) -> String {
        guard model.hasSeveralAccounts, let label = team.account.label else { return what ?? "" }
        guard let what else { return label }
        return "\(what) · \(label)"
    }

    var body: some View {
        NavigationSplitView {
            Group {
                if everyone.isEmpty {
                    TeamEmpty(loaded: home.loaded, problem: home.problem)
                } else {
                    List(selection: $chosen) {
                        ForEach(teams) { team in
                            let parts = favourites.split(shown(team), on: team.account.accountID)
                            // With one account these are the two sections
                            // the tab has always had; with several, each
                            // account's people are its own, under its name.
                            if !parts.favourites.isEmpty {
                                Section(sectionTitle("Favourites", team)) {
                                    ForEach(parts.favourites) { member in
                                        row(member, on: team)
                                    }
                                }
                            }
                            if !parts.rest.isEmpty {
                                Section(sectionTitle(nil, team)) {
                                    ForEach(parts.rest) { member in
                                        row(member, on: team)
                                    }
                                }
                            }
                        }
                    }
                    .listStyle(.plain)
                }
            }
            .navigationTitle("Team")
            // On the stack rather than the list: iOS puts the field under
            // the title that way, as the mockup has it.
            .searchable(
                text: $search, placement: .navigationBarDrawer(displayMode: .always), prompt: "Name or extension"
            )
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { PresenceButton() }
            }
        } detail: {
            if let picked = everyone.first(where: { $0.member.id == chosen }) {
                PersonView(
                    member: picked.member, isMe: picked.member.extensionNumber == picked.team.mine,
                    favourites: favourites, account: picked.team.account.accountID,
                    label: picked.team.account.label
                )
                .environment(picked.team.account.phone)
            } else {
                NothingPicked(
                    symbol: "person.2", title: "Nobody picked",
                    words: "Pick somebody to see what they're doing and ring them.")
            }
        }
        // Never yourself: your own card has nothing to press on it, and it
        // is not what somebody opening Team came to see.
        .picksTheFirstRow(
            $chosen,
            first: {
                everyone.first(where: { $0.member.extensionNumber != $0.team.mine })?.member.id
            },
            when: horizontal == .regular
        )
        .task {
            // Each account watches for its own entry in its own team.
            for team in teams { team.account.home.noticeMyOwnStatus(extensionNumber: team.mine) }
        }
    }
}

/// One person on the right-hand side of the Team tab: who they are, what
/// they are doing this moment, and the two ways to reach them. It is live,
/// like the list beside it — "On a call" goes the moment they put the phone
/// down.
struct PersonView: View {
    @Environment(PhoneModel.self) private var phone
    let member: TeamMember
    let isMe: Bool
    let favourites: Favourites
    /// Which account this person is on, so their star is their own and the
    /// call goes out on the line they are reachable on.
    var account: UUID?
    /// The account's name, shown only on a phone with more than one.
    var label: String?

    private var callable: Bool { !isMe && phone.status == .ready && phone.call == nil }
    private var starred: Bool { favourites.has(member.extensionNumber, on: account) }

    var body: some View {
        ScrollView {
            VStack(spacing: LinxSpace.s5) {
                BigInitials(name: member.name)

                VStack(spacing: LinxSpace.s2) {
                    Text(isMe ? "\(member.name) (you)" : member.name)
                        .font(.title.weight(.bold))
                        .foregroundStyle(LinxColor.text)
                        .multilineTextAlignment(.center)
                    HStack(spacing: LinxSpace.s2) {
                        Circle()
                            .fill(PresenceDot.colour(for: member.status))
                            .frame(width: 10, height: 10)
                            .accessibilityHidden(true)
                        Text(what)
                            .font(.body)
                            .foregroundStyle(LinxColor.textMuted)
                    }
                }

                LinxCard {
                    VStack(spacing: LinxSpace.s3) {
                        DetailLine(label: "Extension", value: member.extensionNumber)
                        DetailLine(label: "Right now", value: what)
                    }
                }

                if !isMe {
                    VStack(spacing: LinxSpace.s3) {
                        Button {
                            phone.callNumber(member.extensionNumber, name: member.name)
                        } label: {
                            Label("Call \(member.name)", systemImage: "phone.fill")
                        }
                        .buttonStyle(.linxCall)
                        .disabled(!callable)

                        Button {
                            phone.callNumber(member.extensionNumber, name: member.name, withVideo: true)
                        } label: {
                            Label("Video call", systemImage: "video.fill")
                        }
                        .buttonStyle(.linxPrimary)
                        .disabled(!callable)

                        Button(starred ? "Remove from favourites" : "Add to favourites") {
                            favourites.toggle(member.extensionNumber, on: account)
                        }
                        .font(.subheadline)
                        .foregroundStyle(LinxColor.accent)
                    }
                }

                Spacer(minLength: 0)
            }
            .padding(LinxSpace.s6)
            .frame(maxWidth: 520)
            .frame(maxWidth: .infinity)
        }
        .navigationTitle(member.name)
        .navigationBarTitleDisplayMode(.inline)
        .linxBackground()
    }

    /// "On a call · 04:12", the same words the row uses.
    private var what: String {
        var out = member.words
        if let since = member.since, member.status == .onCall || member.status == .ringing {
            out += " · " + CallView.length(since: since, to: Date())
        }
        return out
    }
}

/// My own status, and what it does: Do not disturb stops this person's
/// extension ringing anywhere, which is worth saying on the button itself.
private struct PresenceButton: View {
    @Environment(HomeModel.self) private var home

    var body: some View {
        Menu {
            Picker(
                "My status",
                selection: Binding(
                    get: { home.presence },
                    set: { choice in
                        Task { await home.setPresence(choice) }
                    })
            ) {
                ForEach(Presence.allCases, id: \.self) { presence in
                    Text(presence == .dnd ? "Do not disturb · nothing rings" : presence.words).tag(presence)
                }
            }
        } label: {
            HStack(spacing: LinxSpace.s2) {
                Circle()
                    .fill(PresenceDot.colour(for: home.presence))
                    .frame(width: 10, height: 10)
                Text(home.presence.words)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(LinxColor.text)
            }
            .padding(.horizontal, LinxSpace.s3)
            .padding(.vertical, LinxSpace.s2)
            .background(LinxColor.surface, in: .capsule)
            .overlay { Capsule().strokeBorder(LinxColor.border) }
        }
        .accessibilityLabel("My status, \(home.presence.words)")
    }
}

private struct TeamRow: View {
    @Environment(PhoneModel.self) private var phone
    let member: TeamMember
    let isMe: Bool
    /// This row is the one open on the detail side, so iOS has filled it
    /// with the app's own brand colour and the ink has to change with it
    /// (step 8). On a phone nothing stays selected and this is always false.
    var selected = false

    private var callable: Bool { !isMe && phone.status == .ready && phone.call == nil }
    private var ink: Color { selected ? LinxColor.onBrand : LinxColor.text }
    private var quietInk: Color { selected ? LinxColor.onBrand.opacity(0.8) : LinxColor.textMuted }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Initials(name: member.name, status: member.status)
            VStack(alignment: .leading, spacing: 2) {
                Text(isMe ? "\(member.name) (you)" : member.name)
                    .font(.body.weight(.medium))
                    .foregroundStyle(ink)
                Text(words)
                    .font(.subheadline)
                    .foregroundStyle(quietInk)
            }
            Spacer(minLength: LinxSpace.s2)
            if !isMe {
                Button {
                    phone.callNumber(member.extensionNumber, name: member.name)
                } label: {
                    Image(systemName: "phone.fill")
                        .font(.title3)
                        .foregroundStyle(callable ? (selected ? LinxColor.onBrand : LinxColor.call) : quietInk)
                }
                .buttonStyle(.borderless)
                .disabled(!callable)
                .accessibilityLabel("Call \(member.name)")

                Button {
                    phone.callNumber(member.extensionNumber, name: member.name, withVideo: true)
                } label: {
                    Image(systemName: "video.fill")
                        .font(.title3)
                        .foregroundStyle(callable ? (selected ? LinxColor.onBrand : LinxColor.brandFill) : quietInk)
                }
                .buttonStyle(.borderless)
                .disabled(!callable)
                .accessibilityLabel("Video call \(member.name)")
            }
        }
        .padding(.vertical, LinxSpace.s1)
    }

    /// "1024 · On a call · 04:12" — the extension, what they are doing, and
    /// how long they have been doing it when that is a call.
    private var words: String {
        var out = member.extensionNumber + " · " + member.words
        if let since = member.since, member.status == .onCall || member.status == .ringing {
            out += " · " + CallView.length(since: since, to: Date())
        }
        return out
    }
}

/// A circle with someone's initials, and the dot that says what they are
/// doing. The dot carries no meaning on its own: the row says it in words
/// beside it, for anyone who can't tell the colours apart.
private struct Initials: View {
    let name: String
    let status: TeamMember.Status

    var body: some View {
        Text(CallView.initials(of: name))
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(LinxColor.text)
            .frame(width: 40, height: 40)
            .background(LinxColor.surface, in: .circle)
            .overlay { Circle().strokeBorder(LinxColor.border) }
            .overlay(alignment: .bottomTrailing) {
                Circle()
                    .fill(PresenceDot.colour(for: status))
                    .frame(width: 12, height: 12)
                    .overlay { Circle().strokeBorder(LinxColor.bg, lineWidth: 2) }
            }
            .accessibilityHidden(true)
    }
}

/// The one place the status colours are decided (docs/ui/DESIGN_TOKENS.md).
enum PresenceDot {
    static func colour(for status: TeamMember.Status) -> Color {
        switch status {
        case .onCall, .ringing: return LinxColor.Status.busy
        case .dnd: return LinxColor.Status.busy
        case .away: return LinxColor.Status.away
        case .available: return LinxColor.Status.available
        case .offline: return LinxColor.textMuted
        }
    }

    static func colour(for presence: Presence) -> Color {
        switch presence {
        case .available: return LinxColor.Status.available
        case .away: return LinxColor.Status.away
        case .dnd: return LinxColor.Status.busy
        }
    }
}

private struct TeamEmpty: View {
    let loaded: Bool
    let problem: String?

    var body: some View {
        VStack(spacing: LinxSpace.s3) {
            if let problem {
                Text(problem)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
                    .multilineTextAlignment(.center)
            } else if loaded {
                Text("Nobody else has an extension yet.")
                    .font(.body)
                    .foregroundStyle(LinxColor.textMuted)
            } else {
                ProgressView()
            }
        }
        .padding(LinxSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .linxBackground()
    }
}

// The preview's made-up team is debug-only, like the screenshot harness's.
#if DEBUG
    #Preview("Team") {
        let model = AppModel()
        model.identity = Screen.sampleIdentity
        model.home.pretend(members: Screen.sampleTeam)
        return TeamView()
            .environment(model)
            .environment(model.home)
            .environment(Favourites())
            .environment(PhoneModel(line: { nil }))
    }
#endif
