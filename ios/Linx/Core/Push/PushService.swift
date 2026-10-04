import Foundation
import PushKit
import UIKit
import UserNotifications

// How a sleeping phone hears about a call (docs/PHASE2.md §5, build step 6).
//
// There are two kinds of push and they are kept strictly apart:
//
//   - a **VoIP push** (PushKit) is only ever a real, ringing call, and the
//     app reports it to CallKit the instant it arrives. Nothing else ever
//     goes through this door: iOS kills an app that takes a VoIP push
//     without ringing a call (§14 item 1).
//   - an **ordinary notification** is a missed call, a new voicemail, and —
//     where CallKit may not be used (ADR-078) — a call that is ringing
//     right now, which the person taps to answer.
//
// The app tells Linx where Apple can reach it with `POST
// /api/v1/me/phone-push`, and Linx tells Apple nothing but the call's id,
// the caller's number and the time.

/// Where Apple can reach this phone.
struct PushTokens: Equatable, Sendable {
    /// The PushKit token, lower-case hex. Empty where CallKit may not be
    /// used, and empty until Apple has given the app one.
    var voip = ""
    /// The notification token, lower-case hex. Empty until the person
    /// allows notifications.
    var alert = ""
    var environment = PushEnvironment.current
    /// This phone can't be woken with a VoIP push, so a ringing call has to
    /// be announced instead (ADR-078).
    var callAlerts = false

    /// There is nothing to tell Linx until Apple has given the app at least
    /// one of the two.
    var worthSending: Bool { !voip.isEmpty || !alert.isEmpty }
}

@MainActor final class PushService {
    static let shared = PushService()

    /// A call is on its way: the call's id and the caller's number, which is
    /// all that was ever sent. The app has to ring before it does anything
    /// else, and `then` is Apple's own "I'm finished with this push".
    var onCall: ((_ call: String, _ from: String, _ then: @escaping () -> Void) -> Void)?
    /// New tokens for Linx. The app sends them whenever they change and
    /// whenever it signs in, because Apple hands out new ones freely.
    var onTokens: ((PushTokens) -> Void)?

    private var registry: PKPushRegistry?
    private let bridge = Bridge()
    private var tokens = PushTokens(callAlerts: !CallStyle.usesCallKit)
    private var askedAboutNotifications = false

    /// start runs once, as the app launches, and before anything slow:
    /// PushKit only delivers a push the app was already listening for.
    func start() {
        bridge.owner = self
        UNUserNotificationCenter.current().delegate = bridge
        guard CallStyle.usesCallKit else { return }
        let registry = PKPushRegistry(queue: nil)
        registry.delegate = bridge
        registry.desiredPushTypes = [.voIP]
        self.registry = registry
    }

    /// askAboutNotifications is the one permission prompt the app shows, and
    /// only once this phone is set up: a missed call, a new voicemail, and
    /// where CallKit may not be used, a call that is ringing. If the person
    /// says no, calls still ring — on this phone's own screen while the app
    /// is open — and nothing is sent to Apple for it.
    func askAboutNotifications() async {
        // Never from the background: a phone woken by a push has no one
        // looking at it, and the prompt belongs to the moment the person
        // finished setting the phone up.
        guard UIApplication.shared.applicationState == .active else { return }
        guard !askedAboutNotifications else { return }
        askedAboutNotifications = true
        let centre = UNUserNotificationCenter.current()
        let allowed = (try? await centre.requestAuthorization(options: [.alert, .sound, .badge])) ?? false
        guard allowed else { return }
        UIApplication.shared.registerForRemoteNotifications()
    }

    /// What the app has to tell Linx right now, if anything.
    var current: PushTokens? { tokens.worthSending ? tokens : nil }

    /// What iOS says about notifications for this app at this moment —
    /// what Settings shows the person, so "a missed call doesn't tell me"
    /// has an answer on the phone itself rather than a guess.
    func notificationState() async -> UNAuthorizationStatus {
        await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }

    /// The person pressed the button in Settings. iOS only ever shows its
    /// own prompt once, so after that this does the one thing left: it
    /// registers again, in case the answer was yes and the token never
    /// arrived. Settings sends the person to iOS's own screen when the
    /// answer was no.
    func askAgainAboutNotifications() async {
        askedAboutNotifications = false
        await askAboutNotifications()
        if await notificationState() == .authorized {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    // MARK: - What Apple hands over

    fileprivate func voipToken(_ data: Data?) {
        tokens.voip = Self.hex(data)
        publish()
    }

    func alertToken(_ data: Data?) {
        tokens.alert = Self.hex(data)
        publish()
    }

    private func publish() {
        guard tokens.worthSending else { return }
        onTokens?(tokens)
    }

    nonisolated static func hex(_ data: Data?) -> String {
        guard let data else { return "" }
        return data.map { String(format: "%02x", $0) }.joined()
    }

    /// What a push says, and nothing more: the call's id and the caller's
    /// number. A push without them is not a Linx call and is ignored.
    nonisolated static func call(in payload: [AnyHashable: Any]) -> (call: String, from: String)? {
        guard let linx = payload["linx"] as? [AnyHashable: Any] else { return nil }
        guard let call = linx["call"] as? String, !call.isEmpty else { return nil }
        return (call, linx["from"] as? String ?? "")
    }

    /// Whether a notification is the "a call is ringing" one, which the app
    /// never shows as a banner while it is already open — it rings instead.
    nonisolated static func isRingingCall(_ payload: [AnyHashable: Any]) -> Bool {
        (payload["linx"] as? [AnyHashable: Any])?["kind"] as? String == "call"
    }

    fileprivate func arrived(call: String, from: String, then done: @escaping () -> Void) {
        guard let onCall else {
            done()
            return
        }
        onCall(call, from, done)
    }

    /// PushKit and notifications both promise nothing about which thread
    /// they call back on, so the conformances live on their own small
    /// object and step onto the main actor explicitly. Only plain values
    /// cross over.
    private final class Bridge: NSObject, PKPushRegistryDelegate, UNUserNotificationCenterDelegate,
        @unchecked Sendable
    {
        weak var owner: PushService?

        func pushRegistry(
            _ registry: PKPushRegistry, didUpdate credentials: PKPushCredentials,
            for type: PKPushType
        ) {
            let token = credentials.token
            MainActor.assumeIsolated { owner?.voipToken(token) }
        }

        func pushRegistry(_ registry: PKPushRegistry, didInvalidatePushTokenFor type: PKPushType) {
            MainActor.assumeIsolated { owner?.voipToken(nil) }
        }

        /// A call. This is the one place in the app where speed is the whole
        /// point: the call is already ringing for the caller, and iOS is
        /// watching to see a call reported.
        func pushRegistry(
            _ registry: PKPushRegistry, didReceiveIncomingPushWith payload: PKPushPayload,
            for type: PKPushType, completion: @escaping () -> Void
        ) {
            nonisolated(unsafe) let done = completion
            let said = PushService.call(in: payload.dictionaryPayload)
            MainActor.assumeIsolated {
                guard let said else {
                    done()
                    return
                }
                owner?.arrived(call: said.call, from: said.from, then: done)
            }
        }

        // MARK: Ordinary notifications

        func userNotificationCenter(
            _ centre: UNUserNotificationCenter, willPresent notification: UNNotification
        ) async -> UNNotificationPresentationOptions {
            // A call that is ringing: the app is open, so it rings on its
            // own screen and a banner would only be in the way.
            if PushService.isRingingCall(notification.request.content.userInfo) { return [] }
            return [.banner, .sound, .list]
        }

        func userNotificationCenter(
            _ centre: UNUserNotificationCenter, didReceive response: UNNotificationResponse
        ) async {
            // Opening the app is the whole of it: it signs its line in as it
            // always does, and a call that is still ringing arrives on it.
        }
    }
}
