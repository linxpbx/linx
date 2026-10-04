import Foundation

// What the app's screens read out of Linx (docs/PHASE2.md §7, build step 7):
// the team, call history and voicemail. Every shape here is the one the web
// client already gets from the same endpoint (api/openapi.yaml), so the two
// clients can never drift apart about what a call or a message is — and only
// the fields the app actually shows are decoded.

/// One row of the Team list: an extension, whoever is on it, and what they
/// are doing this moment.
struct TeamMember: Decodable, Sendable, Identifiable, Equatable {
    /// What someone is doing. A call beats a chosen status, and `available`
    /// and `away` need a phone signed in somewhere — otherwise it's
    /// `offline`.
    enum Status: String, Decodable, Sendable {
        case onCall = "on_call"
        case ringing
        case dnd
        case away
        case available
        case offline
    }

    let extensionNumber: String
    let name: String
    let status: Status
    /// When the call started, for `on_call` and `ringing`.
    let since: Date?

    var id: String { extensionNumber }

    enum CodingKeys: String, CodingKey {
        case extensionNumber = "extension"
        case name, status, since
    }

    /// What the row says after the extension number.
    var words: String {
        switch status {
        case .onCall: return "On a call"
        case .ringing: return "Ringing"
        case .dnd: return "Do not disturb"
        case .away: return "Away"
        case .available: return "Available"
        case .offline: return "Offline"
        }
    }
}

/// The whole Team list, and the two counters that say "something changed,
/// ask again" for the Calls and Voicemail badges.
struct TeamList: Decodable, Sendable {
    let items: [TeamMember]
    let voicemail: Int64?
    let calls: Int64?
}

/// My own chosen status.
enum Presence: String, Sendable, CaseIterable {
    case available
    case away
    case dnd

    var words: String {
        switch self {
        case .available: return "Available"
        case .away: return "Away"
        case .dnd: return "Do not disturb"
        }
    }
}

/// Who was on the other end of a call in the history.
struct CallParty: Decodable, Sendable, Equatable {
    let number: String
    let name: String
}

/// One call in my history (ADR-070). The server works out whose call it was
/// and what became of it; the app only shows it.
struct CallRecord: Decodable, Sendable, Identifiable, Equatable {
    enum Direction: String, Decodable, Sendable { case `internal`, inbound, outbound }

    let id: UUID
    let direction: Direction
    let startedAt: Date
    let answeredAt: Date?
    let talkSeconds: Int
    /// Why it ended, in the server's own words ("answered", "voicemail", …).
    let result: String
    let missed: Bool
    let placedByMe: Bool?
    let from: CallParty
    let to: CallParty
    let ringGroup: String?
    let answeredBy: String?

    enum CodingKeys: String, CodingKey {
        case id, direction, result, missed, from, to
        case startedAt = "started_at"
        case answeredAt = "answered_at"
        case talkSeconds = "talk_seconds"
        case placedByMe = "placed_by_me"
        case ringGroup = "ring_group"
        case answeredBy = "answered_by"
    }

    /// I made this call. The server says so outright for my own history; a
    /// call out is mine either way.
    var outgoing: Bool { placedByMe ?? (direction == .outbound) }

    /// The other end of it: who to show, and who to ring back.
    var other: CallParty { outgoing ? to : from }
}

/// A page of call history, newest first.
struct CallHistoryPage: Decodable, Sendable {
    let items: [CallRecord]
    /// Pass as `before` for the page after this one; absent on the last.
    let next: String?
}

/// One voicemail message, without its audio (that is a second request, and
/// only for the one being listened to).
struct VoicemailMessage: Decodable, Sendable, Identifiable, Equatable {
    let id: UUID
    let boxID: UUID
    let callerNumber: String
    let callerName: String
    let receivedAt: Date
    let durationMs: Int
    let heardByMe: Bool
    let heardBy: String?

    enum CodingKeys: String, CodingKey {
        case id
        case boxID = "box_id"
        case callerNumber = "caller_number"
        case callerName = "caller_name"
        case receivedAt = "received_at"
        case durationMs = "duration_ms"
        case heardByMe = "heard_by_me"
        case heardBy = "heard_by"
    }

    /// Who left it, in one line: the name when the caller's provider or Linx
    /// gave one, otherwise the number.
    var who: String {
        if !callerName.isEmpty { return callerName }
        return callerNumber.isEmpty ? "Number withheld" : callerNumber
    }
}

/// A voicemail box the person may see: their own, or a ring group's they are
/// in. The app shows every one of them in one list and says which box a
/// message landed in when it isn't the person's own.
struct VoicemailBoxSummary: Decodable, Sendable, Identifiable, Equatable {
    let id: UUID
    let owner: String
    let mine: Bool
    let enabled: Bool

    enum CodingKeys: String, CodingKey { case id, owner, mine, enabled }
}

struct VoicemailList: Decodable, Sendable {
    let boxes: [VoicemailBoxSummary]
    let items: [VoicemailMessage]
}

extension LinxClient {
    // MARK: - The team

    func team(token: String) async throws -> TeamList {
        try await get("/api/v1/team", token: token)
    }

    func setPresence(_ presence: Presence, token: String) async throws {
        _ = try await change("/api/v1/me/presence", method: "PUT", body: ["presence": presence.rawValue], token: token)
    }

    // MARK: - Call history

    /// My calls, newest first. `before` is the `next` of the page before
    /// it, and `number` searches every call Linx still keeps rather than
    /// only the page in hand.
    func myCalls(limit: Int = 50, before: String? = nil, number: String? = nil, token: String) async throws
        -> CallHistoryPage
    {
        var query = ["limit": String(limit)]
        if let before { query["before"] = before }
        if let number, !number.isEmpty { query["number"] = number }
        return try await get("/api/v1/me/calls", query: query, token: token)
    }

    /// How many calls I have missed since I last looked — the Calls badge.
    func missedCalls(token: String) async throws -> Int {
        struct Count: Decodable { let missed: Int }
        let count: Count = try await get("/api/v1/me/missed-calls", token: token)
        return count.missed
    }

    /// Clears that badge, the moment the person opens Calls.
    func clearMissedCalls(token: String) async throws {
        _ = try await change("/api/v1/me/missed-calls", method: "DELETE", body: nil, token: token)
    }

    // MARK: - Voicemail

    func voicemail(token: String) async throws -> VoicemailList {
        try await get("/api/v1/voicemail", token: token)
    }

    /// How many messages nobody has heard — the Voicemail badge.
    func newVoicemail(token: String) async throws -> Int {
        struct Count: Decodable { let new: Int }
        let count: Count = try await get("/api/v1/me/voicemail-count", token: token)
        return count.new
    }

    /// One message's audio, as a WAV the phone can play. Nothing is kept on
    /// the phone: it is played out of memory and forgotten.
    func voicemailAudio(_ id: UUID, token: String) async throws -> Data {
        try await fetch("/api/v1/voicemail/\(id.uuidString.lowercased())/audio", accept: "audio/wav", token: token)
    }

    func markVoicemail(_ id: UUID, heard: Bool, token: String) async throws {
        _ = try await change(
            "/api/v1/voicemail/\(id.uuidString.lowercased())", method: "PATCH", body: ["heard": heard],
            contentType: "application/merge-patch+json", token: token)
    }

    func deleteVoicemail(_ id: UUID, token: String) async throws {
        _ = try await change(
            "/api/v1/voicemail/\(id.uuidString.lowercased())", method: "DELETE", body: nil, token: token)
    }
}
