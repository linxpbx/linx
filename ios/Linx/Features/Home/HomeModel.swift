import Foundation
import SwiftUI

// What the app's four tabs share (docs/PHASE2.md §7, build step 7): the team
// with what everyone is doing, this person's own status, and the two numbers
// on the tab bar — calls missed and voicemail unheard.
//
// It is all one websocket and two small requests. Nothing polls: Linx sends
// the team list whenever it changes, and when it says "a call ended" or "a
// voicemail arrived" the app asks for its own counts and nobody else's
// (docs/WEB.md §6).
//
// It runs only while the app is in front. A sleeping phone hears about a
// missed call or a new message from a push, not from a connection held open
// all night (docs/PHASE2.md §5).

@MainActor @Observable final class HomeModel {
    private(set) var members: [TeamMember] = []
    /// Whether what is on the screen is live this moment.
    private(set) var live = false
    /// Nothing has arrived yet: the list shows its waiting state rather than
    /// "nobody here".
    private(set) var loaded = false
    private(set) var missedCalls = 0
    private(set) var newVoicemail = 0
    /// This person's own status, as they last set it.
    private(set) var presence: Presence = .available
    private(set) var problem: String?

    /// Moves whenever Linx says call history or voicemail changed, so an
    /// open Calls or Voicemail list fetches itself again.
    private(set) var changed = 0

    private let access: LinxAccess?
    private var socket: TeamLive?
    private var running = false

    /// `watching` is the websocket. A test and a screenshot run ask for a
    /// model without one: what they check is what the screens do with what
    /// Linx said, not the connection itself.
    init(access: LinxAccess?, watching: Bool = true) {
        self.access = access
        guard let access, watching else { return }
        let socket = TeamLive(server: access.server, token: { try? await access.token() })
        socket.onList = { [weak self] list in self?.took(list) }
        socket.onConnected = { [weak self] live in self?.live = live }
        socket.onCountersMoved = { [weak self] in
            self?.changed += 1
            Task { [weak self] in await self?.refreshBadges() }
        }
        self.socket = socket
    }

    func start() {
        guard !running else { return }
        running = true
        socket?.start()
        Task { [weak self] in
            await self?.loadOnce()
            await self?.refreshBadges()
        }
    }

    func stop() {
        running = false
        socket?.stop()
        live = false
    }

    /// The first list, for a phone that has only just opened the app: the
    /// websocket sends one straight away, and this is what fills the screen
    /// if that connection is slow to come up.
    func loadOnce() async {
        guard !loaded, let access else { return }
        do {
            let list = try await access.client.team(token: try await access.token())
            guard !loaded else { return }
            took(list)
        } catch let error as LinxError {
            problem = error.words
        } catch {
            problem = "Linx couldn't be reached. Try again in a moment."
        }
    }

    private func took(_ list: TeamList) {
        members = list.items
        loaded = true
        problem = nil
    }

    /// The two numbers on the tab bar. Each is this person's own count, read
    /// from Linx — the websocket only ever says "something moved".
    func refreshBadges() async {
        guard let access else { return }
        guard let token = try? await access.token() else { return }
        if let missed = try? await access.client.missedCalls(token: token) { missedCalls = missed }
        if let unheard = try? await access.client.newVoicemail(token: token) { newVoicemail = unheard }
    }

    /// Opening Calls clears its badge, here and on every other device of
    /// this person's: what someone missed is kept per person, not per phone
    /// (docs/PHASE2.md §5).
    func openedCalls() async {
        guard missedCalls > 0, let access, let token = try? await access.token() else { return }
        missedCalls = 0
        try? await access.client.clearMissedCalls(token: token)
    }

    /// Available, Away or Do not disturb. Do not disturb also stops this
    /// person's extension ringing, which is why it says so on the screen.
    func setPresence(_ presence: Presence) async {
        guard let access else { return }
        let was = self.presence
        self.presence = presence
        do {
            try await access.client.setPresence(presence, token: try await access.token())
        } catch let error as LinxError {
            self.presence = was
            problem = error.words
        } catch {
            self.presence = was
            problem = "Linx couldn't be reached. Try again in a moment."
        }
    }

    /// This person's own row in the team list, which is where their status
    /// comes from when the app starts.
    func noticeMyOwnStatus(extensionNumber: String) {
        guard let mine = members.first(where: { $0.extensionNumber == extensionNumber }) else { return }
        switch mine.status {
        case .dnd: presence = .dnd
        case .away: presence = .away
        case .available, .onCall, .ringing: presence = .available
        case .offline: break
        }
    }

    #if DEBUG
        /// Made-up contents for the screenshot harness and the tests: no
        /// network, no websocket, no Linx at all.
        func pretend(members: [TeamMember], missed: Int = 0, voicemail: Int = 0) {
            self.members = members
            self.missedCalls = missed
            self.newVoicemail = voicemail
            loaded = true
            live = true
        }
    #endif
}
