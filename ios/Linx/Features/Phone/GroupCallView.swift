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
            ScrollView {
                LazyVGrid(columns: columns, spacing: LinxSpace.s2) {
                    ForEach(group.people) { person in
                        PersonTile(
                            person: person, picture: group.pictures[person.id], mirrored: phone.mirrorsMyVideo)
                    }
                }
                .padding(.horizontal, LinxSpace.s3)
            }
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

    private var columns: [GridItem] {
        let count = group.people.count <= 2 ? 1 : 2
        return Array(repeating: GridItem(.flexible(), spacing: LinxSpace.s2), count: count)
    }
}

/// One person: their picture, or their initials while they have none.
private struct PersonTile: View {
    let person: GroupPerson
    let picture: RTCVideoTrack?
    let mirrored: Bool

    var body: some View {
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
        .aspectRatio(3 / 4, contentMode: .fit)
        .clipShape(.rect(cornerRadius: LinxRadius.md))
        .overlay {
            RoundedRectangle(cornerRadius: LinxRadius.md)
                .strokeBorder(person.speaking ? LinxColor.call : .clear, lineWidth: 3)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(person.name + (person.video ? "" : ", camera off") + (person.sound ? "" : ", muted"))
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

/// Add person on a call between two Linx devices (ADR-088): on a call to a
/// desk phone or an outside number there is nobody it could add.
struct AddPersonButton: View {
    @Environment(PhoneModel.self) private var phone

    var body: some View {
        if phone.canAddPeople {
            Button {
                phone.addingPerson = true
            } label: {
                Label("Add person", systemImage: "person.badge.plus")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(LinxColor.onSurfaceDark)
                    .padding(.horizontal, LinxSpace.s3)
                    .padding(.vertical, LinxSpace.s2)
                    .background(LinxColor.surface.opacity(0.22), in: .capsule)
            }
        }
    }
}
