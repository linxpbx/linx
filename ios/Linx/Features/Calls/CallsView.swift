import SwiftUI

// The Calls tab (ADR-070, docs/PHASE2.md §7): every call this person made,
// was rung for, answered or whose voicemail was reached — the same history
// the web app shows, from the same place (`GET /api/v1/me/calls`), so the
// two can never say different things about the same call.
//
// Opening it clears the badge for this person on every device they have,
// because what someone missed is kept per person, not per phone.

/// One call in the list, and which account it belongs to
/// (docs/MULTI_ACCOUNT.md §7). With one account `label` is nil and the row
/// shows exactly what it always showed.
struct AccountCall: Identifiable, Sendable, Equatable {
    let record: CallRecord
    /// The account this call is on, so ringing back goes out on the line it
    /// came in on. Nil only for the made-up calls in a screenshot run.
    let account: UUID?
    let label: String?

    /// Two Linx servers each give their calls an id of their own, so the id
    /// of a row in a merged list is the account's and the call's together.
    var id: String { "\(account?.uuidString ?? "")/\(record.id)" }
}

@MainActor @Observable final class CallsModel {
    /// Where the calls come from: one account's history, with the name to
    /// put beside its rows. Several of them merge into one list.
    struct Source {
        let account: UUID?
        let label: String?
        let access: LinxAccess
    }

    /// Every account's calls, newest first.
    private(set) var calls: [AccountCall] = []
    private(set) var loading = false
    private(set) var loaded = false
    private(set) var problem: String?
    /// The page after the one in hand, per account, when there is one.
    private var next: [String: String] = [:]
    /// What each account has given so far, kept apart so one account's
    /// next page replaces only its own rows.
    private var pages: [String: [AccountCall]] = [:]

    private let sources: [Source]

    init(sources: [Source]) {
        self.sources = sources
    }

    /// One account, which is every phone until somebody adds a second.
    convenience init(access: LinxAccess?) {
        self.init(sources: access.map { [Source(account: nil, label: nil, access: $0)] } ?? [])
    }

    private static func key(_ source: Source) -> String { source.account?.uuidString ?? "one" }

    private func merge() {
        calls = pages.values.flatMap { $0 }.sorted { $0.record.startedAt > $1.record.startedAt }
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

    /// The newest page from every account, merged. Asked for again
    /// whenever Linx says a call ended.
    ///
    /// One account being unreachable doesn't empty the list: the others are
    /// shown and the problem is said once, because a list that goes blank
    /// when a staging server is off is worse than one that is short.
    func load() async {
        guard !sources.isEmpty, !loading else { return }
        loading = true
        defer { loading = false }
        let digits = searching.filter { $0.isNumber || $0 == "+" }
        var trouble: String?
        for source in sources {
            do {
                let page = try await source.access.client.myCalls(
                    number: digits.isEmpty ? nil : digits, token: try await source.access.token())
                pages[Self.key(source)] = page.items.map {
                    AccountCall(record: $0, account: source.account, label: source.label)
                }
                next[Self.key(source)] = page.next
            } catch let error as LinxError {
                trouble = trouble ?? error.words
            } catch {
                trouble = trouble ?? "Linx couldn't be reached. Try again in a moment."
            }
        }
        merge()
        loaded = true
        problem = trouble
    }

    /// Takes a call out of this person's own list (the swipe, owner
    /// 2026-10-23). The row goes at once so the swipe feels like a swipe,
    /// and comes back with a word if Linx wouldn't have it. Linx keeps the
    /// call itself: this hides it from this person's list on every device
    /// they sign in on, and the company's record is untouched.
    func hide(_ call: AccountCall) async {
        let key = sources.first { $0.account == call.account }
        guard let source = key else { return }
        let before = pages[Self.key(source)] ?? []
        pages[Self.key(source)] = before.filter { $0.record.id != call.record.id }
        merge()
        do {
            try await source.access.client.hideCall(call.record.id, token: try await source.access.token())
            hidden.append(call)
        } catch let error as LinxError {
            pages[Self.key(source)] = before
            merge()
            problem = error.words
        } catch {
            pages[Self.key(source)] = before
            merge()
            problem = "Linx couldn't be reached, so that call is still in your list."
        }
    }

    /// The calls taken out in this sitting, newest last, so Undo knows what
    /// to put back.
    private(set) var hidden: [AccountCall] = []

    /// Undo: the last call taken out goes back in the list.
    func putTheLastOneBack() async {
        guard let call = hidden.popLast(),
            let source = sources.first(where: { $0.account == call.account })
        else { return }
        do {
            try await source.access.client.putCallBack(call.record.id, token: try await source.access.token())
            pages[Self.key(source), default: []].append(call)
            merge()
        } catch {
            problem = "Linx couldn't put that call back. It is still out of your list."
        }
    }

    /// One more page from every account that has one, when the person
    /// scrolls to the end.
    func loadMore() async {
        guard hasMore, !loading else { return }
        loading = true
        defer { loading = false }
        let digits = searching.filter { $0.isNumber || $0 == "+" }
        for source in sources {
            guard let before = next[Self.key(source)] else { continue }
            guard
                let page = try? await source.access.client.myCalls(
                    before: before, number: digits.isEmpty ? nil : digits,
                    token: try await source.access.token())
            else { continue }
            pages[Self.key(source), default: []] += page.items.map {
                AccountCall(record: $0, account: source.account, label: source.label)
            }
            next[Self.key(source)] = page.next
        }
        merge()
    }

    var hasMore: Bool { next.values.contains { $0 != nil } }

    #if DEBUG
        func pretend(_ calls: [CallRecord], label: String? = nil) {
            self.calls = calls.map { AccountCall(record: $0, account: nil, label: label) }
            loaded = true
        }
    #endif
}

struct CallsView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @Environment(\.horizontalSizeClass) private var horizontal
    @State private var calls: CallsModel?
    /// Whether the "Call removed from your list · Undo" bar is up.
    @State private var removed = false
    @State private var undo: Task<Void, Never>?
    @State private var onlyMissed = false
    @State private var search = ""
    /// Which call the right-hand side is showing. On a phone there is no
    /// right-hand side and picking one pushes it instead (step 8).
    @State private var chosen: AccountCall.ID?

    private var shown: [AccountCall] {
        var all = calls?.calls ?? []
        if onlyMissed { all = all.filter(\.record.missed) }
        let text = search.trimmingCharacters(in: .whitespaces).lowercased()
        // A number has already been searched for at the server; a name is
        // matched here, against what is on the screen.
        guard !text.isEmpty, !text.contains(where: \.isNumber) else { return all }
        return all.filter {
            $0.record.from.name.lowercased().contains(text) || $0.record.to.name.lowercased().contains(text)
        }
    }

    /// The phone line a row's call belongs to, which is the one a ring back
    /// goes out on: a call that came in on one account is never rung back
    /// on another (docs/MULTI_ACCOUNT.md §7).
    private func phone(for call: AccountCall) -> PhoneModel {
        guard let id = call.account, let account = model.account(id: id) else { return model.phone }
        return account.phone
    }

    /// All / Missed. In the toolbar on a phone, as it has always been; in
    /// the column itself on a big screen, where a segmented control beside
    /// the title squeezes "Calls" down to "C…".
    private var whichCalls: some View {
        Picker("Which calls", selection: $onlyMissed) {
            Text("All").tag(false)
            Text("Missed").tag(true)
        }
        .pickerStyle(.segmented)
    }

    var body: some View {
        NavigationSplitView {
            VStack(spacing: 0) {
                if horizontal == .regular {
                    whichCalls
                        .padding(.horizontal, LinxSpace.s4)
                        .padding(.bottom, LinxSpace.s2)
                }
                list
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
                if horizontal != .regular {
                    ToolbarItem(placement: .topBarTrailing) { whichCalls }
                }
            }
        } detail: {
            // One call, in full: who, which way it went, when, how long, and
            // the button to ring them back.
            if let call = shown.first(where: { $0.id == chosen }) {
                CallDetailView(call: call.record, label: call.label, account: call.account)
                    .environment(phone(for: call))
            } else {
                NothingPicked(
                    symbol: "clock", title: "No call picked",
                    words: "Pick a call to see who it was, when, and how long it lasted.")
            }
        }
        .picksTheFirstRow($chosen, first: { shown.first?.id }, when: horizontal == .regular)
        .task {
            if calls == nil {
                calls = CallsModel(sources: Screen.launched == nil ? model.callSources : [])
            }
            #if DEBUG
                if Screen.launched != nil {
                    calls?.pretend(Screen.sampleCalls)
                    return
                }
            #endif
            #if DEBUG
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

    private var list: some View {
        Group {
            if let calls, !calls.calls.isEmpty {
                List(selection: $chosen) {
                    ForEach(shown) { call in
                        CallRow(
                            call: call.record, label: call.label, account: call.account,
                            selected: chosen == call.id
                        )
                        .environment(phone(for: call))
                        // Red, because a swipe that deletes is red (owner,
                        // 2026-10-05). It takes the call out of **this
                        // person's** list only; Linx keeps its own record,
                        // which is what the owner asked for (2026-10-23).
                        .swipeActions(edge: .trailing) {
                            Button(role: .destructive) {
                                Task {
                                    await calls.hide(call)
                                    justRemoved()
                                }
                            } label: {
                                Label("Delete", systemImage: "trash")
                            }
                            .tint(LinxColor.end)
                        }
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
                .safeAreaInset(edge: .bottom) {
                    if removed {
                        HStack(spacing: LinxSpace.s3) {
                            Text("Call removed from your list")
                                .font(.subheadline)
                                .foregroundStyle(LinxColor.textMuted)
                            Spacer()
                            Button("Undo") {
                                Task {
                                    undo?.cancel()
                                    await calls.putTheLastOneBack()
                                    removed = false
                                }
                            }
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(LinxColor.accent)
                        }
                        .padding(.horizontal, LinxSpace.s4)
                        .padding(.vertical, LinxSpace.s3)
                        .background(LinxColor.surface)
                        .overlay(alignment: .top) { Divider() }
                    }
                }
            } else {
                CallsEmpty(loaded: calls?.loaded ?? false, problem: calls?.problem)
            }
        }
    }

    /// A call has just gone from the list: the offer to put it back stands
    /// for a few seconds, as the web's does after a routing change.
    private func justRemoved() {
        removed = true
        undo?.cancel()
        undo = Task {
            try? await Task.sleep(for: .seconds(Self.undoLasts))
            guard !Task.isCancelled else { return }
            removed = false
        }
    }

    nonisolated static let undoLasts = 10
}

/// One call: who, which way it went, when, and how long they talked.
struct CallRow: View {
    @Environment(PhoneModel.self) private var phone
    /// The app's accounts, so a ring back can move onto the one the call
    /// came in on. Optional on purpose: a row drawn on its own, by a
    /// screenshot renderer or a preview, has no app around it and must not
    /// bring the process down for the want of one.
    @Environment(AppModel.self) private var model: AppModel?
    let call: CallRecord
    /// Which account this call came through, shown only on a phone with
    /// more than one (docs/MULTI_ACCOUNT.md §7).
    var label: String?
    /// And which account that is: ringing back moves the app onto it
    /// (`AppModel.use(accountID:)`).
    var account: UUID?
    /// This row is the one open on the detail side, so iOS has filled it
    /// with the app's own brand colour and the ink has to change with it
    /// (step 8). On a phone nothing stays selected and this is always false.
    var selected = false

    private var callable: Bool {
        phone.status == .ready && phone.call == nil && !call.other.number.isEmpty
    }

    private var ink: Color {
        if selected { return LinxColor.onBrand }
        return call.missed ? LinxColor.end : LinxColor.text
    }
    private var quietInk: Color { selected ? LinxColor.onBrand.opacity(0.8) : LinxColor.textMuted }

    /// Rings them back on the account the call came in on, and moves the
    /// app onto it as it goes (owner, 2026-10-20).
    private func ringBack(withVideo video: Bool = false) {
        model?.use(accountID: account)
        phone.callNumber(
            call.other.number, name: call.other.name.isEmpty ? nil : call.other.name, withVideo: video)
    }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Image(systemName: symbol)
                .font(.body)
                .foregroundStyle(selected ? LinxColor.onBrand : (call.missed ? LinxColor.end : LinxColor.textMuted))
                .frame(width: 22)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(who)
                    .font(.body.weight(call.missed ? .semibold : .regular))
                    .foregroundStyle(ink)
                Text(words)
                    .font(.subheadline)
                    .foregroundStyle(quietInk)
                if let label {
                    Text(label)
                        .font(.caption)
                        .foregroundStyle(quietInk)
                        .accessibilityLabel("Through \(label)")
                }
            }
            Spacer(minLength: LinxSpace.s2)
            if callable {
                Button {
                    ringBack()
                } label: {
                    Image(systemName: "phone")
                        .font(.title3)
                        .foregroundStyle(selected ? LinxColor.onBrand : LinxColor.accent)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Call \(who) back")
                // Someone at Linx can be rung back with a picture too (owner,
                // 2026-10-17); an outside number can't show one.
                if call.direction == .internal {
                    Button {
                        ringBack(withVideo: true)
                    } label: {
                        Image(systemName: "video")
                            .font(.title3)
                            .foregroundStyle(selected ? LinxColor.onBrand : LinxColor.accent)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Video call \(who) back")
                }
            }
        }
        .padding(.vertical, LinxSpace.s1)
        .accessibilityElement(children: .combine)
    }

    private var who: String { CallWords.who(call) }

    private var symbol: String { CallWords.symbol(call) }

    /// "Missed · 14:32" or "Incoming · yesterday · 2:14".
    private var words: String {
        var parts = [CallWords.kind(call), CallsWhen.text(call.startedAt)]
        if call.talkSeconds > 0 {
            parts.append(CallView.length(seconds: call.talkSeconds))
        }
        if let group = call.ringGroup { parts.append(group) }
        return parts.joined(separator: " · ")
    }
}

/// The words a call is described in, in one place, so the list and the
/// detail beside it can never say different things about the same call.
enum CallWords {
    static func who(_ call: CallRecord) -> String {
        let party = call.other
        if !party.name.isEmpty { return party.name }
        return party.number.isEmpty ? "Number withheld" : party.number
    }

    static func symbol(_ call: CallRecord) -> String {
        if call.missed { return "phone.badge.waveform" }
        return call.outgoing ? "arrow.up.right" : "arrow.down.left"
    }

    static func kind(_ call: CallRecord) -> String {
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

/// One call on the right-hand side of the Calls tab: everything Linx keeps
/// about it, and the button to ring them back. On a phone the same screen
/// is pushed when a call is tapped.
struct CallDetailView: View {
    @Environment(PhoneModel.self) private var phone
    /// The app's accounts, so a ring back can move onto the one the call
    /// came in on. Optional on purpose: a row drawn on its own, by a
    /// screenshot renderer or a preview, has no app around it and must not
    /// bring the process down for the want of one.
    @Environment(AppModel.self) private var model: AppModel?
    let call: CallRecord
    /// Which account the call came through, on a phone with more than one.
    var label: String?
    /// And which account that is: ringing back moves the app onto it
    /// (`AppModel.use(accountID:)`).
    var account: UUID?

    private var who: String { CallWords.who(call) }

    private var callable: Bool {
        phone.status == .ready && phone.call == nil && !call.other.number.isEmpty
    }

    /// Rings them back on the account the call came in on, and moves the
    /// app onto it as it goes (owner, 2026-10-20).
    private func ringBack(withVideo video: Bool = false) {
        model?.use(accountID: account)
        phone.callNumber(
            call.other.number, name: call.other.name.isEmpty ? nil : call.other.name, withVideo: video)
    }

    var body: some View {
        ScrollView {
            VStack(spacing: LinxSpace.s5) {
                BigInitials(name: who)

                VStack(spacing: LinxSpace.s1) {
                    Text(who)
                        .font(.title.weight(.bold))
                        .foregroundStyle(call.missed ? LinxColor.end : LinxColor.text)
                        .multilineTextAlignment(.center)
                    if !call.other.name.isEmpty, !call.other.number.isEmpty {
                        Text(call.other.number)
                            .font(.body)
                            .foregroundStyle(LinxColor.textMuted)
                    }
                }

                LinxCard {
                    VStack(spacing: LinxSpace.s3) {
                        DetailLine(label: "What happened", value: CallWords.kind(call))
                        DetailLine(
                            label: "When",
                            value: call.startedAt.formatted(date: .abbreviated, time: .shortened))
                        if call.talkSeconds > 0 {
                            DetailLine(
                                label: "How long", value: CallView.length(seconds: call.talkSeconds))
                        }
                        if let group = call.ringGroup {
                            DetailLine(label: "Rang", value: group)
                        }
                        if let answered = call.answeredBy {
                            DetailLine(label: "Answered by", value: answered)
                        }
                        if let label {
                            // Which of this phone's accounts, so there is no
                            // doubt which line a ring back goes out on.
                            DetailLine(label: "Account", value: label)
                        }
                    }
                }

                if callable {
                    Button("Call \(who) back") {
                        ringBack()
                    }
                    .buttonStyle(.linxCall)
                    if call.direction == .internal {
                        Button {
                            ringBack(withVideo: true)
                        } label: {
                            Label("Video call", systemImage: "video.fill")
                        }
                        .buttonStyle(.linxPrimary)
                    }
                }

                Spacer(minLength: 0)
            }
            .padding(LinxSpace.s6)
            .frame(maxWidth: 520)
            .frame(maxWidth: .infinity)
        }
        .navigationTitle(who)
        .navigationBarTitleDisplayMode(.inline)
        .linxBackground()
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
