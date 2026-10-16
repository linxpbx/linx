import CoreTelephony
import Foundation
import Synchronization

/// The mobile networks this phone has seen holding back what it sends over
/// UDP, so a call on one of them starts on the secure route straight away
/// (`WebRTCMedia.moveToSecureRoute`, `UploadWatch`).
///
/// A network is known by the SIM carrying the phone's data: the owner's
/// iPhone has an Etisalat line, which holds UDP back for the first 30–45 s
/// of a call, and a du line, which doesn't (2026-10-16). Starting every
/// mobile call on the secure route would cost du its picture — a TCP route
/// stalls on a lossy link where UDP doesn't — so only the line that was
/// caught doing it is remembered, for a week; after that its next call
/// finds out again. Nothing is ever remembered as "fine": a quiet check is
/// no proof that a call won't be held.
enum HeldNetworks {
    static let remembered: TimeInterval = 7 * 24 * 3600
    private static let key = "heldNetworks"

    /// The SIM carrying mobile data now, while the phone is on mobile data.
    static var here: String? {
        guard MobileData.now else { return nil }
        if Bench.mobileData { return "bench" }
        return CTTelephonyNetworkInfo().dataServiceIdentifier
    }

    /// Whether the network the phone is on now holds UDP back.
    static var holdingHere: Bool {
        guard let here else { return false }
        return holds(here, at: Date())
    }

    static func holds(_ network: String, at now: Date) -> Bool {
        if Bench.on { return benchHeld.withLock { $0 } }
        guard let at = seen()[network] else { return false }
        return now.timeIntervalSince(at) < remembered
    }

    /// The network the phone is on now held UDP back.
    static func remember() {
        guard let here else { return }
        if Bench.on {
            benchHeld.withLock { $0 = true }
            return
        }
        var all = seen().filter { Date().timeIntervalSince($0.value) < remembered }
        all[here] = Date()
        UserDefaults.standard.set(all.mapValues(\.timeIntervalSince1970), forKey: key)
    }

    /// On the bench, what is remembered lasts as long as the app does, so
    /// one case can't change the next (each starts the app afresh).
    private static let benchHeld = Mutex(Bench.heldBefore)

    private static func seen() -> [String: Date] {
        guard let stored = UserDefaults.standard.dictionary(forKey: key) as? [String: Double] else { return [:] }
        return stored.mapValues { Date(timeIntervalSince1970: $0) }
    }
}
