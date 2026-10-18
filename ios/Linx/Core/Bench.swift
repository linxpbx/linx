import Foundation

// The test bench (ios/tools/bench.sh): two simulators that set themselves
// up, call each other and hang up with nobody touching them, under the
// networks a phone really meets — Linx's relay only, no UDP at all (China).
// Every switch is a launch argument, read in debug builds only; a release
// build has none of them.

enum Bench {
    #if DEBUG
        private static var defaults: UserDefaults { .standard }

        /// A setup link to redeem when the app starts with no phone set up.
        static var setupLink: String? { defaults.string(forKey: "LinxBenchSetup") }
        /// A number to call once the line is up, once.
        static var call: String? { defaults.string(forKey: "LinxBenchCall") }
        /// Whether that call is a video call.
        static var video: Bool { defaults.bool(forKey: "LinxBenchVideo") }
        /// How long after the line is up that call is made, in seconds
        /// (0: two): longer when the phone it rings starts after this one.
        static var callAfter: Int {
            let after = defaults.integer(forKey: "LinxBenchCallAfter")
            return after > 0 ? after : 2
        }
        /// Answer whatever rings, a second after it starts.
        static var answer: Bool { defaults.bool(forKey: "LinxBenchAnswer") }
        /// How long it rings before that answer, in seconds (0: one).
        static var ringFor: Int { max(1, defaults.integer(forKey: "LinxBenchRingFor")) }
        /// Hang up this many seconds after the call is answered (0: never).
        static var hangUpAfter: Int { defaults.integer(forKey: "LinxBenchHangUpAfter") }
        /// Every route through Linx's relay, as on a mobile network.
        static var relayOnly: Bool { defaults.bool(forKey: "LinxBenchRelayOnly") }
        /// No UDP at all: the relay over TLS on 443 only, as in China.
        static var noUDP: Bool { defaults.bool(forKey: "LinxBenchNoUDP") }
        /// Ring and answer through CallKit, as an iPhone outside China does;
        /// without it the bench takes China's in-app path (ADR-078).
        static var callKit: Bool { defaults.bool(forKey: "LinxBenchCallKit") }
        /// Press **HD** five seconds into the call.
        static var hd: Bool { defaults.bool(forKey: "LinxBenchHD") }
        /// The picture set up only after the answer, as before build 35:
        /// what the bench's slow links are measured against.
        static var noEarly: Bool { defaults.bool(forKey: "LinxBenchNoEarly") }
        /// This phone is on mobile data (`MobileData`): its picture goes
        /// through the relay alone, its sound by every route as usual.
        static var mobileData: Bool { defaults.bool(forKey: "LinxBenchMobileData") }
        /// The sound stays on Asterisk's route all call, as before ADR-092:
        /// what the direct sound is measured against.
        static var noDirectSound: Bool { defaults.bool(forKey: "LinxBenchNoDirectSound") }

        static var on: Bool { setupLink != nil || call != nil || answer }
    #else
        static let setupLink: String? = nil
        static let call: String? = nil
        static let video = false
        static let callAfter = 2
        static let noEarly = false
        static let mobileData = false
        static let noDirectSound = false
        static let answer = false
        static let ringFor = 1
        static let hangUpAfter = 0
        static let relayOnly = false
        static let noUDP = false
        static let callKit = false
        static let hd = false
        static let on = false
    #endif

    /// The relay's addresses as the bench wants them: without the UDP ones
    /// when there is no UDP.
    static func relayURLs(_ urls: [String]) -> [String] {
        guard noUDP else { return urls }
        return urls.filter { $0.hasPrefix("turns:") }
    }
}
