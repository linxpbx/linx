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
    @State private var search = ""
    @State private var favourites = Favourites()

    private var mine: String { model.identity?.extensionNumber ?? "" }

    private var shown: [TeamMember] {
        let text = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !text.isEmpty else { return home.members }
        return home.members.filter {
            $0.name.lowercased().contains(text) || $0.extensionNumber.contains(text)
        }
    }

    /// One person, with the star that keeps them at the top of this phone's
    /// own list.
    @ViewBuilder private func row(_ member: TeamMember) -> some View {
        let starred = favourites.has(member.extensionNumber)
        TeamRow(member: member, isMe: member.extensionNumber == mine)
            .swipeActions(edge: .leading) {
                Button(starred ? "Unstar" : "Favourite", systemImage: starred ? "star.slash" : "star") {
                    favourites.toggle(member.extensionNumber)
                }
                .tint(LinxColor.brandFill)
            }
    }

    var body: some View {
        NavigationStack {
            Group {
                if home.members.isEmpty {
                    TeamEmpty(loaded: home.loaded, problem: home.problem)
                } else {
                    List {
                        let parts = favourites.split(shown)
                        if !parts.favourites.isEmpty {
                            Section("Favourites") {
                                ForEach(parts.favourites) { member in
                                    row(member)
                                }
                            }
                        }
                        Section {
                            ForEach(parts.rest) { member in
                                row(member)
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
        }
        .task {
            home.noticeMyOwnStatus(extensionNumber: mine)
        }
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

    private var callable: Bool { !isMe && phone.status == .ready && phone.call == nil }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Initials(name: member.name, status: member.status)
            VStack(alignment: .leading, spacing: 2) {
                Text(isMe ? "\(member.name) (you)" : member.name)
                    .font(.body.weight(.medium))
                    .foregroundStyle(LinxColor.text)
                Text(words)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
            }
            Spacer(minLength: LinxSpace.s2)
            if !isMe {
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

                Button {
                    phone.callNumber(member.extensionNumber, name: member.name, withVideo: true)
                } label: {
                    Image(systemName: "video")
                        .font(.title3)
                        .foregroundStyle(callable ? LinxColor.accent : LinxColor.textMuted)
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
            .environment(PhoneModel(line: { nil }))
    }
#endif
