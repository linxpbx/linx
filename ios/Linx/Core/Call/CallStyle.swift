import Foundation

/// Whether this phone may hand its calls to the system (ADR-042, ADR-078).
///
/// Apple does not allow CallKit on iPhones in mainland China, so an app
/// there must not use it — and must not take a VoIP push either, because a
/// VoIP push that doesn't report a call to CallKit gets the app killed
/// (docs/PHASE2.md §14 item 1). Linx therefore does two different things:
///
/// - **Everywhere else:** PushKit wakes the app, CallKit rings the phone,
///   and the call works from the lock screen, a car and a headset.
/// - **In mainland China:** no PushKit and no CallKit. A call arrives as an
///   ordinary, time-sensitive notification; tapping it opens the app, which
///   rings on its own screen while the call is still held for it.
///
/// There is no API that asks Apple whether CallKit is allowed, so the
/// phone's own region is what decides, which is also what App Review goes
/// by.
enum CallStyle {
    /// The regions where a call stays inside the app.
    static let noCallKit: Set<String> = ["CN"]

    static var usesCallKit: Bool { !noCallKit.contains(region) }

    /// What the phone is set to, not where it happens to be: a visitor's
    /// phone keeps working the way it does at home.
    static var region: String {
        Locale.current.region?.identifier.uppercased() ?? ""
    }

    /// What the person is told, once, on the screen that says this phone is
    /// signed in. Nil where the system takes the calls, which is almost
    /// everywhere.
    static var inAppRingingNote: String? {
        guard !usesCallKit else { return nil }
        return """
            Apple doesn't allow calls on the lock screen in this country, so \
            Linx rings inside the app. Keep notifications on: a call arrives \
            as a notification you tap to answer.
            """
    }

    /// The real thing, or the in-app one where CallKit may not be used.
    ///
    /// Neither the screenshot harness nor the unit tests ever touch CallKit:
    /// a screenshot run must not put a call on a simulator's lock screen,
    /// and a test asks a stand-in system for everything (`FakeSystemCalls`)
    /// so that what it checks is the app's own behaviour and not iOS's.
    @MainActor static func systemCalls() -> any SystemCalls {
        #if DEBUG
            if Screen.launched != nil || underTest { return NoSystemCalls() }
        #endif
        guard usesCallKit else { return NoSystemCalls() }
        return CallKitCalls()
    }

    #if DEBUG
        /// Running inside `xcodebuild test`, which sets this for the host app.
        static var underTest: Bool {
            ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil
                || ProcessInfo.processInfo.environment["XCTestBundlePath"] != nil
        }
    #endif
}
