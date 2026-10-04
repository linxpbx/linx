import SwiftUI
import UIKit

/// The one thing SwiftUI's own lifecycle can't do: be ready for a push
/// before any screen exists (docs/PHASE2.md §5, build step 6).
///
/// PushKit only delivers a call to an app that was already listening, and
/// when a push wakes a phone whose app was closed, iOS launches it with no
/// window at all — so registering has to happen here, at the very first
/// moment of the app's life, and not in a view's `task`.
@MainActor final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        // A screenshot run talks to nothing at all, and must never put a
        // call on a simulator's lock screen.
        guard Screen.launched == nil else { return true }
        AppModel.shared.listenForCalls()
        return true
    }

    /// Apple's token for ordinary notifications: a missed call, a new
    /// voicemail, and where CallKit may not be used, a call that is ringing
    /// (ADR-078). It only ever arrives after the person has said yes.
    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken token: Data
    ) {
        PushService.shared.alertToken(token)
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // Nothing to tell the person: calls still ring while the app is
        // open, and iOS tries again by itself next time the app runs.
        PushService.shared.alertToken(nil)
    }
}
