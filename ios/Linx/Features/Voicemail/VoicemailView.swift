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

@MainActor @Observable final class VoicemailModel {
    private(set) var messages: [VoicemailMessage] = []
    private(set) var boxes: [VoicemailBoxSummary] = []
    private(set) var loaded = false
    private(set) var problem: String?
    /// Which message is playing, and how far through it is.
    private(set) var playing: UUID?
    private(set) var busy: UUID?

    private let access: LinxAccess?
    private var player: AVAudioPlayer?
    private var listener: VoicemailEnded?
    private var usingTheSpeaker = false

    init(access: LinxAccess?) {
        self.access = access
    }

    func load() async {
        guard let access else { return }
        do {
            let list = try await access.client.voicemail(token: try await access.token())
            messages = list.items
            boxes = list.boxes
            loaded = true
            problem = nil
        } catch let error as LinxError {
            problem = error.words
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
        }
    }

    /// Whose box a message landed in, when it isn't this person's own.
    func box(_ message: VoicemailMessage) -> String? {
        guard let box = boxes.first(where: { $0.id == message.boxID }), !box.mine else { return nil }
        return box.owner
    }

    /// Plays a message, or stops the one that is playing. The first press
    /// fetches it; nothing is kept afterwards.
    func play(_ message: VoicemailMessage) async {
        if playing == message.id {
            stop()
            return
        }
        stop()
        guard let access else { return }
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
            await markHeard(message)
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

    private func markHeard(_ message: VoicemailMessage) async {
        guard !message.heardByMe, let access else { return }
        try? await access.client.markVoicemail(message.id, heard: true, token: try await access.token())
        if let at = messages.firstIndex(where: { $0.id == message.id }) {
            messages[at] = VoicemailMessage(
                id: message.id, boxID: message.boxID, callerNumber: message.callerNumber,
                callerName: message.callerName, receivedAt: message.receivedAt,
                durationMs: message.durationMs, heardByMe: true, heardBy: message.heardBy)
        }
    }

    /// Deleted for everyone who can see that box, which is what the web app
    /// says too — a ring group's message is the group's, not one person's.
    func delete(_ message: VoicemailMessage) async {
        guard let access else { return }
        if playing == message.id { stop() }
        do {
            try await access.client.deleteVoicemail(message.id, token: try await access.token())
            messages.removeAll { $0.id == message.id }
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
        func pretend(_ messages: [VoicemailMessage]) {
            self.messages = messages
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

    var body: some View {
        Group {
            if let voicemail, !voicemail.messages.isEmpty {
                List {
                    ForEach(voicemail.messages) { message in
                        VoicemailRow(message: message, voicemail: voicemail)
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
                voicemail = VoicemailModel(access: Screen.launched == nil ? model.linx : nil)
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
    let message: VoicemailMessage
    let voicemail: VoicemailModel

    private var playing: Bool { voicemail.playing == message.id }

    var body: some View {
        HStack(spacing: LinxSpace.s3) {
            Button {
                Task { await voicemail.play(message) }
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
        if let box = voicemail.box(message) { parts.insert(box, at: 0) }
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
