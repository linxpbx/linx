import AVFoundation
import Foundation
import SwiftUI

// Voicemail (ADR-069, docs/PHASE2.md §7): the messages in this person's own
// box and in any ring group's box they are in — the same list the web app
// shows, from the same place.
//
// A message's audio is fetched only when someone presses play, is played out
// of memory, and is never written to the phone: a voicemail is somebody's
// voice, and the fewer places it exists the better.

/// One message in the list, and which account it is on
/// (docs/MULTI_ACCOUNT.md §7). With one account `label` is nil and the row
/// shows exactly what it always showed.
struct AccountVoicemail: Identifiable, Sendable, Equatable {
    let message: VoicemailMessage
    let account: UUID?
    let label: String?

    /// Each Linx gives its messages an id of its own, so a row in a merged
    /// list is named by the account and the message together.
    var id: String { "\(account?.uuidString ?? "")/\(message.id)" }
}

@MainActor @Observable final class VoicemailModel {
    /// One account's voicemail, with the name to put beside its rows.
    struct Source {
        let account: UUID?
        let label: String?
        let access: LinxAccess
    }

    private(set) var messages: [AccountVoicemail] = []
    private(set) var loaded = false
    private(set) var problem: String?
    /// Which message is playing, and how far through it is.
    private(set) var playing: UUID?
    private(set) var busy: UUID?

    private let sources: [Source]
    /// Each account's boxes, so "whose box is this" is asked of the Linx the
    /// message is on and never of another.
    private var boxesByAccount: [String: [VoicemailBoxSummary]] = [:]
    private var player: AVAudioPlayer?
    private var listener: VoicemailEnded?
    private var usingTheSpeaker = false

    init(sources: [Source]) {
        self.sources = sources
    }

    /// One account, which is every phone until somebody adds a second.
    convenience init(access: LinxAccess?) {
        self.init(sources: access.map { [Source(account: nil, label: nil, access: $0)] } ?? [])
    }

    private static func key(_ account: UUID?) -> String { account?.uuidString ?? "one" }

    private func access(for account: UUID?) -> LinxAccess? {
        sources.first { $0.account == account }?.access
    }

    /// Every account's messages, newest first. One account being
    /// unreachable leaves the others on the screen and says so once.
    func load() async {
        guard !sources.isEmpty else { return }
        var all: [AccountVoicemail] = []
        var trouble: String?
        for source in sources {
            do {
                let list = try await source.access.client.voicemail(token: try await source.access.token())
                boxesByAccount[Self.key(source.account)] = list.boxes
                all += list.items.map {
                    AccountVoicemail(message: $0, account: source.account, label: source.label)
                }
            } catch let error as LinxError {
                trouble = trouble ?? error.words
            } catch {
                trouble = trouble ?? "Linx couldn't be reached. Try again in a moment."
            }
        }
        messages = all.sorted { $0.message.receivedAt > $1.message.receivedAt }
        loaded = true
        problem = trouble
    }

    /// Whose box a message landed in, when it isn't this person's own.
    func box(_ row: AccountVoicemail) -> String? {
        let boxes = boxesByAccount[Self.key(row.account)] ?? []
        guard let box = boxes.first(where: { $0.id == row.message.boxID }), !box.mine else { return nil }
        return box.owner
    }

    /// Plays a message, or stops the one that is playing. The first press
    /// fetches it; nothing is kept afterwards.
    func play(_ row: AccountVoicemail) async {
        let message = row.message
        if playing == message.id {
            stop()
            return
        }
        stop()
        guard let access = access(for: row.account) else { return }
        busy = message.id
        defer { busy = nil }
        do {
            let wav = try await access.client.voicemailAudio(message.id, token: try await access.token())
            try Self.readyToPlay()
            usingTheSpeaker = true
            let player = try AVAudioPlayer(data: wav)
            let listener = VoicemailEnded { [weak self] in
                Task { @MainActor in self?.stop() }
            }
            player.delegate = listener
            self.listener = listener
            self.player = player
            player.play()
            playing = message.id
            await markHeard(row)
        } catch let error as LinxError {
            problem = error.words
        } catch {
            problem = "That message wouldn't play on this phone."
        }
    }

    func stop() {
        player?.stop()
        player = nil
        listener = nil
        playing = nil
        guard usingTheSpeaker else { return }
        usingTheSpeaker = false
        // Hand the speaker back, so music or a podcast the person had on
        // carries on where it left off.
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    private func markHeard(_ row: AccountVoicemail) async {
        let message = row.message
        guard !message.heardByMe, let access = access(for: row.account) else { return }
        try? await access.client.markVoicemail(message.id, heard: true, token: try await access.token())
        if let at = messages.firstIndex(where: { $0.id == row.id }) {
            messages[at] = AccountVoicemail(
                message: VoicemailMessage(
                    id: message.id, boxID: message.boxID, callerNumber: message.callerNumber,
                    callerName: message.callerName, receivedAt: message.receivedAt,
                    durationMs: message.durationMs, heardByMe: true, heardBy: message.heardBy),
                account: row.account, label: row.label)
        }
    }

    /// Deleted for everyone who can see that box, which is what the web app
    /// says too — a ring group's message is the group's, not one person's.
    func delete(_ row: AccountVoicemail) async {
        let message = row.message
        guard let access = access(for: row.account) else { return }
        if playing == message.id { stop() }
        do {
            try await access.client.deleteVoicemail(message.id, token: try await access.token())
            messages.removeAll { $0.id == row.id }
        } catch let error as LinxError {
            problem = error.words
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
        }
    }

    /// A voicemail is listened to out loud, on the loudspeaker, like a
    /// message on an answering machine — and it must not leave the phone
    /// silent for the next call, so the session is put back afterwards.
    private static func readyToPlay() throws {
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playback, mode: .default, options: [])
        try session.setActive(true)
    }

    #if DEBUG
        func pretend(_ messages: [VoicemailMessage], label: String? = nil) {
            self.messages = messages.map {
                AccountVoicemail(message: $0, account: nil, label: label)
            }
            loaded = true
        }
    #endif
}

/// AVAudioPlayer still wants an object with a delegate method on it. It is
/// deliberately not on the main actor: AVFoundation calls this from its own
/// queue, and all it does is hop back.
private final class VoicemailEnded: NSObject, AVAudioPlayerDelegate, @unchecked Sendable {
    private let finished: @Sendable () -> Void

    init(finished: @escaping @Sendable () -> Void) {
        self.finished = finished
    }

    func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully: Bool) {
        finished()
    }
}

struct VoicemailView: View {
    @Environment(AppModel.self) private var model
    @Environment(HomeModel.self) private var home
    @Environment(PhoneModel.self) private var phone
    @State private var voicemail: VoicemailModel?

    /// The phone line a message belongs to, which is the one a ring back
    /// goes out on: a message left on one account is never answered from
    /// another (docs/MULTI_ACCOUNT.md §7).
    private func phone(for row: AccountVoicemail) -> PhoneModel {
        guard let id = row.account, let account = model.account(id: id) else { return phone }
        return account.phone
    }

    var body: some View {
        Group {
            if let voicemail, !voicemail.messages.isEmpty {
                List {
                    ForEach(voicemail.messages) { message in
                        VoicemailRow(row: message, voicemail: voicemail)
                            .environment(phone(for: message))
                            .swipeActions {
                                // Red, always, and Linx's own red rather
                                // than whichever one the system reaches for:
                                // a swipe that deletes something is the one
                                // colour nobody may have to think about
                                // (owner, 2026-10-05).
                                Button("Delete", systemImage: "trash", role: .destructive) {
                                    Task { await voicemail.delete(message) }
                                }
                                .tint(LinxColor.end)
                            }
                    }
                }
                .listStyle(.plain)
            } else {
                VoicemailEmpty(loaded: voicemail?.loaded ?? false, problem: voicemail?.problem)
            }
        }
        .navigationTitle("Voicemail")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if voicemail == nil {
                voicemail = VoicemailModel(sources: Screen.launched == nil ? model.voicemailSources : [])
            }
            #if DEBUG
                if Screen.launched != nil {
                    voicemail?.pretend(Screen.sampleVoicemail)
                    return
                }
            #endif
            await voicemail?.load()
            await home.refreshBadges()
        }
        .onChange(of: home.changed) {
            Task { await voicemail?.load() }
        }
        .onDisappear { voicemail?.stop() }
    }
}

private struct VoicemailRow: View {
    @Environment(PhoneModel.self) private var phone
    let row: AccountVoicemail
    let voicemail: VoicemailModel

    private var message: VoicemailMessage { row.message }
    private var playing: Bool { voicemail.playing == message.id }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Button {
                Task { await voicemail.play(row) }
            } label: {
                ZStack {
                    Circle()
                        .fill(LinxColor.surface)
                        .overlay { Circle().strokeBorder(LinxColor.border) }
                    if voicemail.busy == message.id {
                        ProgressView()
                    } else {
                        Image(systemName: playing ? "stop.fill" : "play.fill")
                            .font(.body)
                            .foregroundStyle(LinxColor.accent)
                    }
                }
                .frame(width: 40, height: 40)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(playing ? "Stop" : "Play the message from \(message.who)")

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: LinxSpace.s2) {
                    if !message.heardByMe {
                        Circle()
                            .fill(LinxColor.brandFill)
                            .frame(width: 8, height: 8)
                            .accessibilityLabel("New")
                    }
                    Text(message.who)
                        .font(.body.weight(message.heardByMe ? .regular : .semibold))
                        .foregroundStyle(LinxColor.text)
                }
                Text(words)
                    .font(.subheadline)
                    .foregroundStyle(LinxColor.textMuted)
            }
            Spacer(minLength: LinxSpace.s2)
            if phone.status == .ready, phone.call == nil, !message.callerNumber.isEmpty {
                Button {
                    phone.callNumber(message.callerNumber, name: message.callerName.isEmpty ? nil : message.callerName)
                } label: {
                    Image(systemName: "phone")
                        .font(.title3)
                        .foregroundStyle(LinxColor.accent)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Call \(message.who) back")
            }
        }
        .padding(.vertical, LinxSpace.s1)
    }

    private var words: String {
        var parts = [CallsWhen.text(message.receivedAt), CallView.length(seconds: message.durationMs / 1000)]
        if let box = voicemail.box(row) { parts.insert(box, at: 0) }
        // Which account the message is on, on a phone with more than one.
        if let label = row.label { parts.append(label) }
        if let heardBy = message.heardBy, !message.heardByMe { parts.append("Heard by \(heardBy)") }
        return parts.joined(separator: " · ")
    }
}

private struct VoicemailEmpty: View {
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
                Image(systemName: "recordingtape")
                    .font(.largeTitle)
                    .foregroundStyle(LinxColor.textMuted)
                    .accessibilityHidden(true)
                Text("No messages")
                    .font(.headline)
                    .foregroundStyle(LinxColor.text)
                Text("When someone leaves you a voicemail it appears here, and this phone gets a quiet notification.")
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

/// When something happened, the way a phone's own lists say it.
enum CallsWhen {
    static func text(_ at: Date) -> String {
        let calendar = Calendar.current
        if calendar.isDateInToday(at) { return at.formatted(date: .omitted, time: .shortened) }
        if calendar.isDateInYesterday(at) { return "Yesterday" }
        if let week = calendar.date(byAdding: .day, value: -6, to: Date()), at > week {
            return at.formatted(.dateTime.weekday(.wide))
        }
        return at.formatted(.dateTime.day().month(.abbreviated))
    }
}

#Preview("Voicemail") {
    let model = AppModel()
    model.identity = Screen.sampleIdentity
    return NavigationStack { VoicemailView() }
        .environment(model)
        .environment(model.home)
        .environment(PhoneModel(line: { nil }))
}
