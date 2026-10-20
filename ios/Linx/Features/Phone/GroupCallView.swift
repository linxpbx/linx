import SwiftUI
@preconcurrency import WebRTC

// A group call (ADR-088, docs/VIDEO_CALLS.md §4): everyone in it, in a grid,
// with their picture or their initials, and the same buttons as a video
// call. Add person rings one more in, from the Team list.

struct GroupCallView: View {
    @Environment(PhoneModel.self) private var phone
    let call: PhoneModel.Call
    let group: GroupPeople

    var body: some View {
        VStack(spacing: LinxSpace.s4) {
            Text(heading)
                .font(.body)
                .foregroundStyle(LinxColor.onSurfaceDark.opacity(0.8))
                .padding(.top, LinxSpace.s4)
            people
                .padding(.horizontal, LinxSpace.s3)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            HStack(spacing: LinxSpace.s4) {
                CallCircle(
                    symbol: call.muted ? "mic.slash.fill" : "mic.fill", words: call.muted ? "Muted" : "Mute",
                    on: call.muted
                ) { phone.toggleMute() }
                CallCircle(
                    symbol: group.camera ? "video.fill" : "video.slash.fill",
                    words: group.camera ? "Stop video" : "Start video", on: group.camera,
                    waiting: phone.changingVideo
                ) { phone.toggleVideo() }
                .disabled(phone.changingVideo)
                CallCircle(symbol: "person.badge.plus", words: "Add person", on: false) { phone.addingPerson = true }
                    .disabled(group.people.count >= 8)
                CallCircle(symbol: "phone.down.fill", words: "End", on: false, danger: true) { phone.hangUp() }
            }
            .padding(.bottom, LinxSpace.s5)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(LinxColor.surfaceDark.ignoresSafeArea())
        // Another app in front carries the group on in a small window, with
        // the first other person's picture in it (`CallPictureInPicture`).
        .background {
            PictureInPictureSource(
                window: phone.smallWindow,
                theirs: group.people.first { !$0.me && $0.video && group.pictures[$0.id] != nil }
                    .flatMap { group.pictures[$0.id] },
                mine: group.camera ? group.people.first(where: \.me).flatMap { group.pictures[$0.id] } : nil,
                mirrored: phone.mirrorsMyVideo)
        }
    }

    private var heading: String {
        switch group.phase {
        case .joining: return "Joining the group call…"
        case .ended(let why): return why
        case .joined:
            let n = group.people.count
            return "Group call · \(n) \(n == 1 ? "person" : "people")"
        }
    }

    /// Everyone in the call. **Two people are laid out as a call between
    /// two** — the other person filling the space, this phone's own picture
    /// small in the corner, a tap to swap them — because that is what the
    /// call is once a third person has dropped out (owner, 2026-10-20: two
    /// tiles one above the other that had to be scrolled). Three to eight
    /// share a grid worked out from the space there is, so a group call
    /// never scrolls.
    @ViewBuilder private var people: some View {
        if group.people.count <= 2, let other = group.people.first(where: { !$0.me }) {
            twoUp(other: other)
        } else {
            grid
        }
    }

    private func twoUp(other: GroupPerson) -> some View {
        let me = group.people.first(where: \.me)
        let big = phone.myPictureIsBig ? (me ?? other) : other
        let small = phone.myPictureIsBig ? other : me
        return ZStack(alignment: .topTrailing) {
            tile(for: big)
                .contentShape(.rect)
                .onTapGesture { phone.swapPictures() }
            if let small, small.id != big.id {
                tile(for: small, aspect: 3.0 / 4.0)
                    .frame(width: 104)
                    .padding(LinxSpace.s3)
                    .contentShape(.rect)
                    .onTapGesture { phone.swapPictures() }
            }
        }
    }

    private var grid: some View {
        GeometryReader { space in
            let count = group.people.count
            let columns = GroupGrid.columns(people: count)
            let size = GroupGrid.tile(people: count, in: space.size, spacing: LinxSpace.s2)
            VStack(spacing: LinxSpace.s2) {
                ForEach(0..<GroupGrid.rows(people: count), id: \.self) { row in
                    HStack(spacing: LinxSpace.s2) {
                        ForEach(group.people.indices.filter { $0 / columns == row }, id: \.self) { index in
                            tile(for: group.people[index])
                                .frame(width: size.width, height: size.height)
                        }
                    }
                }
            }
            .frame(width: space.size.width, height: space.size.height)
        }
    }

    private func tile(for person: GroupPerson, aspect: CGFloat? = nil) -> some View {
        PersonTile(
            person: person, picture: group.pictures[person.id], mirrored: phone.mirrorsMyVideo,
            flip: flip(for: person), aspect: aspect)
    }

    /// The front/back camera switch rides on this phone's own picture, which
    /// is where a call between two has it too (owner, 2026-10-14) — a group
    /// call had no way to switch cameras at all (owner, 2026-10-20).
    private func flip(for person: GroupPerson) -> (() -> Void)? {
        guard person.me, group.camera else { return nil }
        return { phone.switchCamera() }
    }
}

/// How a group call's tiles are laid out: everyone fits the space the screen
/// gives them, because a group call never scrolls (owner, 2026-10-20).
enum GroupGrid {
    /// Two to a row. One person on their own — this phone while it is still
    /// joining — has the space to themselves.
    static func columns(people: Int) -> Int { people <= 1 ? 1 : 2 }

    static func rows(people: Int) -> Int {
        let columns = columns(people: people)
        return max(1, (people + columns - 1) / columns)
    }

    /// One tile's size: what is left of the space after the gaps between
    /// them, shared out.
    static func tile(people: Int, in space: CGSize, spacing: CGFloat) -> CGSize {
        let columns = CGFloat(columns(people: people))
        let rows = CGFloat(rows(people: people))
        return CGSize(
            width: max(1, (space.width - spacing * (columns - 1)) / columns),
            height: max(1, (space.height - spacing * (rows - 1)) / rows))
    }
}

/// One person: their picture, or their initials while they have none.
private struct PersonTile: View {
    let person: GroupPerson
    let picture: RTCVideoTrack?
    let mirrored: Bool
    /// This phone's own tile carries the front/back camera switch.
    var flip: (() -> Void)?
    /// Kept to this shape, or filling the space it is given when there is
    /// none — the grid works its own tiles out.
    var aspect: CGFloat?

    var body: some View {
        sized
            .clipShape(.rect(cornerRadius: LinxRadius.md))
            .overlay {
                RoundedRectangle(cornerRadius: LinxRadius.md)
                    .strokeBorder(person.speaking ? LinxColor.call : .clear, lineWidth: 3)
            }
            .overlay(alignment: .bottomTrailing) {
                if let flip { SwitchCameraButton(press: flip, small: true) }
            }
            .accessibilityElement(children: .combine)
            .accessibilityLabel(
                person.name + (person.video ? "" : ", camera off") + (person.sound ? "" : ", muted"))
    }

    /// Kept to a shape when one is asked for — the small picture in the
    /// corner — and otherwise **filling** the space it is given, because
    /// the grid and the two-person layout work their own sizes out. Left to
    /// take its own ideal size a tile shrank to its contents and the screen
    /// came out half empty, with the rows pushed apart (2026-10-20).
    @ViewBuilder private var sized: some View {
        if let aspect {
            stack.aspectRatio(aspect, contentMode: .fit)
        } else {
            stack.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var stack: some View {
        ZStack(alignment: .bottomLeading) {
            LinxColor.surface.opacity(0.18)
            if person.video, let picture {
                VideoPicture(track: picture, fills: true, mirrored: person.me && mirrored)
            } else {
                Text(Self.initials(person.name))
                    .font(.title.weight(.semibold))
                    .foregroundStyle(LinxColor.onSurfaceDark)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            Text(person.name)
                .font(.caption)
                .lineLimit(1)
                .padding(.horizontal, LinxSpace.s2)
                .padding(.vertical, 2)
                .foregroundStyle(LinxColor.onSurfaceDark)
                .background(LinxColor.surfaceDark.opacity(0.6), in: .capsule)
                .padding(LinxSpace.s2)
        }
    }

    static func initials(_ name: String) -> String {
        name.split(separator: " ").prefix(2).compactMap(\.first).map(String.init).joined().uppercased()
    }
}

/// Add person: the Team list, and one tap rings them into the call.
struct AddPersonSheet: View {
    @Environment(PhoneModel.self) private var phone
    let done: () -> Void
    @State private var members: [TeamMember]?
    @State private var search = ""
    @State private var problem: String?
    @State private var busy = false

    var body: some View {
        NavigationStack {
            List {
                if let problem {
                    Text(problem).foregroundStyle(LinxColor.end)
                }
                ForEach(shown) { member in
                    Button {
                        Task { await add(member) }
                    } label: {
                        HStack {
                            Text(member.name).foregroundStyle(LinxColor.text)
                            Spacer()
                            Text(member.extensionNumber).font(.caption).foregroundStyle(LinxColor.textMuted)
                        }
                    }
                    .disabled(busy)
                }
            }
            .overlay {
                if members == nil { ProgressView() }
            }
            .searchable(text: $search, prompt: "Name or extension")
            .navigationTitle("Add person")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel", action: done) }
            }
        }
        .task { members = await phone.teamToAdd() }
    }

    private var shown: [TeamMember] {
        let q = search.trimmingCharacters(in: .whitespaces).lowercased()
        return (members ?? []).filter {
            q.isEmpty || $0.name.lowercased().contains(q) || $0.extensionNumber.hasPrefix(q)
        }
    }

    private func add(_ member: TeamMember) async {
        busy = true
        problem = await phone.addPerson(member.extensionNumber)
        busy = false
        if problem == nil { done() }
    }
}
