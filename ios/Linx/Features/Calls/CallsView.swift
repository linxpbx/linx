import SwiftUI

// The Calls tab (ADR-070, docs/PHASE2.md §7): every call this person made,
// was rung for, answered or whose voicemail was reached — the same history
// the web app shows, from the same place (`GET /api/v1/me/calls`), so the
// two can never say different things about the same call.
//
// Opening it clears the badge for this person on every device they have,
// because what someone missed is kept per person, not per phone.

@MainActor @Observable final class CallsModel {
    private(set) var calls: [CallRecord] = []
    private(set) var loading = false
    private(set) var loaded = false
    private(set) var problem: String?
    /// The page after the one in hand, when there is one.
    private var next: String?

    private let access: LinxAccess?

    init(access: LinxAccess?) {
        self.access = access
    }

    /// What is being searched for, if anything.
    private(set) var searching = ""

    /// A number searches every call Linx still keeps, because that is what
    /// the server can do; a name is matched against the calls in hand,
    /// because the server has no name search and inventing one here would
    /// mean pretending to look further than this screen can see.
    func search(_ text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        guard trimmed != searching else { return }
        searching = trimmed
        guard trimmed.contains(where: \.isNumber) else { return }
        await load()
    }

    /// The newest page. Asked for again whenever Linx says a call ended.
    func load() async {
        guard let access, !loading else { return }
        loading = true
        defer { loading = false }
        let digits = searching.filter { $0.isNumber || $0 == "+" }
        do {
            let page = try await access.client.myCalls(
                number: digits.isEmpty ? nil : digits, token: try await access.token())
            calls = page.items
            next = page.next
            loaded = true
            problem = nil
        } catch let error as LinxError {
            problem = error.words
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
        }
    }

    /// One more page, when the person scrolls to the end of this one.
    func loadMore() async {
        guard let access, let before = next, !loading else { return }
        loading = true
        defer { loading = false }
        let digits = searching.filter { $0.isNumber || $0 == "+" }
        guard
            let page = try? await access.client.myCalls(
                before: before, number: digits.isEmpty ? nil : digits, token: try await access.token())
        else { return }
        calls += page.items
        next = page.next
    }

    var hasMore: Bool { next != nil }

    #if DEBUG
        func pretend(_ calls: [CallRecord]) {
            self.calls = calls
            loaded = true
        }
    #endif
}

struct CallsView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @State private var calls: CallsModel?
    @State private var onlyMissed = false
    @State private var search = ""

    private var shown: [CallRecord] {
        var all = calls?.calls ?? []
        if onlyMissed { all = all.filter(\.missed) }
        let text = search.trimmingCharacters(in: .whitespaces).lowercased()
        // A number has already been searched for at the server; a name is
        // matched here, against what is on the screen.
        guard !text.isEmpty, !text.contains(where: \.isNumber) else { return all }
        return all.filter {
            $0.from.name.lowercased().contains(text) || $0.to.name.lowercased().contains(text)
        }
    }

    var body: some View {
        NavigationStack {
            Group {
                if let calls, !calls.calls.isEmpty {
                    List {
                        ForEach(shown) { call in
                            CallRow(call: call)
                        }
                        if calls.hasMore, !onlyMissed {
                            Button("Show older calls") {
                                Task { await calls.loadMore() }
                            }
                            .font(.subheadline)
                            .foregroundStyle(LinxColor.accent)
                        }
                    }
                    .listStyle(.plain)
                } else {
                    CallsEmpty(loaded: calls?.loaded ?? false, problem: calls?.problem)
                }
            }
            .navigationTitle("Calls")
            .searchable(
                text: $search, placement: .navigationBarDrawer(displayMode: .always),
                prompt: "Name or number"
            )
            .onChange(of: search) { _, text in
                Task { await calls?.search(text) }
            }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Picker("Which calls", selection: $onlyMissed) {
                        Text("All").tag(false)
                        Text("Missed").tag(true)
                    }
                    .pickerStyle(.segmented)
                }
            }
        }
        .task {
            if calls == nil { calls = CallsModel(access: Screen.launched == nil ? model.linx : nil) }
            #if DEBUG
                if Screen.launched != nil {
                    calls?.pretend(Screen.sampleCalls)
                    return
                }
            #endif
            await calls?.load()
            // Opening this screen is what clears the badge, here and on
            // every other device of this person's.
            await home.openedCalls()
        }
        .onChange(of: home.changed) {
            Task { await calls?.load() }
        }
    }
}

/// One call: who, which way it went, when, and how long they talked.
private struct CallRow: View {
    @Environment(PhoneModel.self) private var phone
    let call: CallRecord

    private var callable: Bool {
        phone.status == .ready && phone.call == nil && !call.other.number.isEmpty
    }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Image(systemName: symbol)
                .font(.body)
                .foregroundStyle(call.missed ? LinxColor.end : LinxColor.textMuted)
                .frame(width: 22)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(who)
                    .font(.body.weight(call.missed ? .semibold : .regular))
                    .foregroundStyle(call.missed ? LinxColor.end : LinxColor.text)
                Text(words)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
            }
            Spacer(minLength: LinxSpace.s2)
            if callable {
                Button {
                    phone.callNumber(call.other.number, name: call.other.name.isEmpty ? nil : call.other.name)
                } label: {
                    Image(systemName: "phone")
                        .font(.title3)
                        .foregroundStyle(LinxColor.accent)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Call \(who) back")
            }
        }
        .padding(.vertical, LinxSpace.s1)
        .accessibilityElement(children: .combine)
    }

    private var who: String {
        let party = call.other
        if !party.name.isEmpty { return party.name }
        return party.number.isEmpty ? "Number withheld" : party.number
    }

    private var symbol: String {
        if call.missed { return "phone.badge.waveform" }
        return call.outgoing ? "arrow.up.right" : "arrow.down.left"
    }

    /// "Missed · 14:32" or "Incoming · yesterday · 2:14".
    private var words: String {
        var parts = [kind, CallsWhen.text(call.startedAt)]
        if call.talkSeconds > 0 {
            parts.append(CallView.length(seconds: call.talkSeconds))
        }
        if let group = call.ringGroup { parts.append(group) }
        return parts.joined(separator: " · ")
    }

    private var kind: String {
        switch call.result {
        case "voicemail": return "Voicemail"
        case "busy": return "Busy"
        case "echo_test": return "Sound test"
        default: break
        }
        if call.missed { return "Missed" }
        return call.outgoing ? "Outgoing" : "Incoming"
    }

}

private struct CallsEmpty: View {
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
                Image(systemName: "clock")
                    .font(.largeTitle)
                    .foregroundStyle(LinxColor.textMuted)
                    .accessibilityHidden(true)
                Text("No calls yet")
                    .font(.headline)
                    .foregroundStyle(LinxColor.text)
                Text("Calls you make and take on any of your phones show up here.")
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
                    .multilineTextAlignment(.center)
            } else {
                ProgressView()
            }
        }
        .padding(LinxSpace.s6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .linxBackground()
    }
}

#Preview("Calls") {
    let model = AppModel()
    model.identity = Screen.sampleIdentity
    return CallsView()
        .environment(model)
        .environment(model.home)
        .environment(PhoneModel(line: { nil }))
}
