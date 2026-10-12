import Foundation
import Security

// Why a set-up phone found itself not set up (owner, 2026-10-12: the iPhone
// showed its setup screen with nobody having signed it out, and nothing on
// the phone said how). The app now remembers that it was set up and the
// last time it was signed out on purpose, and when it starts without its
// sign-in it writes down what it found. That note goes to Linx as a record
// beside the phone's call records, once the phone is signed in again.

enum SignInTrail {
    private static let wasSetUpKey = "linx.wasSetUp"
    private static let lastSignOutKey = "linx.lastSignOut"
    private static let pendingKey = "linx.signInTrailPending"

    /// The phone is set up and signed in.
    static func setUp() {
        UserDefaults.standard.set(true, forKey: wasSetUpKey)
    }

    /// Someone signed this phone out, and which button they used.
    static func signedOut(from button: String, at date: Date = Date()) {
        UserDefaults.standard.set(false, forKey: wasSetUpKey)
        UserDefaults.standard.set("\(button), \(date.formatted(.iso8601))", forKey: lastSignOutKey)
    }

    /// The app started without its sign-in. Answers the note if this phone
    /// had been set up and nobody signed it out, and keeps it to send.
    @discardableResult
    static func missing(identity: OSStatus, key: OSStatus, why: String, at date: Date = Date()) -> String? {
        guard UserDefaults.standard.bool(forKey: wasSetUpKey) else { return nil }
        let lines = [
            "Linx found this phone's sign-in missing",
            "when: \(date.formatted(.iso8601))",
            "build: \(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?")",
            "what was found: \(why)",
            "keychain, sign-in: \(words(identity)); key: \(words(key))",
            "last signed out on purpose: \(UserDefaults.standard.string(forKey: lastSignOutKey) ?? "never")",
        ]
        let note = lines.joined(separator: "\n")
        UserDefaults.standard.set(note, forKey: pendingKey)
        return note
    }

    /// The note waiting to go to Linx, as a record.
    static var pending: CallReport? {
        guard let note = UserDefaults.standard.string(forKey: pendingKey) else { return nil }
        // Named by the moment it was written, so it is sent once however
        // often the app starts before it gets there.
        let when = note.split(separator: "\n").first { $0.hasPrefix("when: ") }?.dropFirst(6) ?? ""
        let id = "sign-in-missing-" + when
        return CallReport(
            callID: id, incoming: false, peer: "", startedAt: Date(), endedAt: nil, text: note, kind: .crash)
    }

    static func words(_ status: OSStatus) -> String {
        switch status {
        case errSecSuccess: return "there"
        case errSecItemNotFound: return "not there"
        case errSecInteractionNotAllowed: return "locked (the phone hasn't been unlocked since it started)"
        default: return "unreadable (\(status))"
        }
    }
}
