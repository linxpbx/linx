import Foundation

/// Which Apple this build's push tokens belong to (`docs/PHASE2.md` §5).
///
/// A token from a build signed for development only works on Apple's
/// sandbox, and one from TestFlight or the App Store only works on the
/// production service — send to the wrong one and Apple calls the token
/// bad. Linx keeps this per phone rather than per server, so a TestFlight
/// phone and a build straight from Xcode can both ring at the same time.
///
/// Nothing in iOS answers the question directly, so the app reads it out of
/// its own provisioning profile, which is where signing put it.
enum PushEnvironment {
    static let sandbox = "sandbox"
    static let production = "production"

    static let current: String = read()

    private static func read() -> String {
        #if targetEnvironment(simulator)
            // A simulator has no push service at all; sandbox is the honest
            // answer and nothing is ever delivered to it.
            return sandbox
        #else
            guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
                let data = try? Data(contentsOf: url),
                // A profile is signed binary with plain XML inside it, so it
                // is read as bytes rather than as text.
                let text = String(data: data, encoding: .isoLatin1)
            else {
                // No profile in the bundle at all: a store build.
                return production
            }
            return value(of: "aps-environment", in: text) == "development" ? sandbox : production
        #endif
    }

    /// One `<key>…</key><string>…</string>` out of a profile's XML. Written
    /// by hand because a profile is not a plist on its own, and pulling in a
    /// parser for one short string isn't worth it.
    static func value(of key: String, in text: String) -> String? {
        guard let after = text.range(of: "<key>\(key)</key>") else { return nil }
        let rest = text[after.upperBound...]
        guard let open = rest.range(of: "<string>"), let close = rest.range(of: "</string>"),
            open.upperBound <= close.lowerBound
        else { return nil }
        return String(rest[open.upperBound..<close.lowerBound])
    }
}
