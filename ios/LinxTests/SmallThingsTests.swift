import Foundation
import Testing

@testable import Linx

// The five small things the owner asked for on 2026-10-04: redial, light and
// dark, searching call history, favourites in Team, and keeping Linx's calls
// out of the iPhone's own Phone app.

@MainActor
struct RedialTests {
    private static func line() -> PhoneModel.Line {
        PhoneModel.Line(
            line: PhoneLine(
                deviceID: UUID(), deviceName: "Sara's iPhone", sipUsername: "d_Ab12Cd34",
                password: "in-memory-only", sipURI: "sip:d_Ab12Cd34@sip.example.com",
                websocketPath: "/sip", displayName: "Sara Haddad", extensionNumber: "101",
                turn: PhoneLine.Turn(
                    urls: [], username: "u", credential: "c", expiresAt: Date(timeIntervalSinceNow: 3600))),
            server: URL(string: "https://pbx.example.com")!, token: "a-device-token")
    }

    private func ready() async throws -> (PhoneModel, FakeTransport, FakeSystemCalls) {
        let transport = FakeTransport()
        let system = FakeSystemCalls()
        let phone = PhoneModel(
            line: { Self.line() }, transport: { _ in transport }, media: { _ in FakeMedia() },
            calls: system)
        await phone.start()
        let first = try #require(transport.last("REGISTER"))
        var challenge = SIPMessage.response(401, "Unauthorized", to: first)
        challenge.add("WWW-Authenticate", "Digest realm=\"asterisk\", nonce=\"n1\", qop=\"auth\"")
        transport.asterisk(challenge)
        transport.asterisk(SIPMessage.response(200, "OK", to: transport.last("REGISTER")!))
        return (phone, transport, system)
    }

    @Test("the last number rung is remembered, and the sound test isn't a number")
    func remembersTheLastNumber() async throws {
        let (phone, _, _) = try await ready()
        #expect(phone.lastDialled.isEmpty)

        phone.typed = "1024"
        phone.dial()
        #expect(phone.lastDialled == "1024")
        phone.hangUp()

        // *43 is "Test my sound", not somebody's number: redial must not
        // offer it back.
        phone.callNumber(PhoneModel.echoTest)
        #expect(phone.lastDialled == "1024")
    }

    @Test("a phone with no line remembers nothing: there was no call")
    func nothingToRedial() async {
        let phone = PhoneModel(line: { nil }, transport: { _ in FakeTransport() }, media: { _ in FakeMedia() })
        await phone.start()
        phone.callNumber("1024")
        #expect(phone.lastDialled.isEmpty)
        phone.stop()
    }

    @Test("the Phone app setting is passed to the system, not acted on here")
    func recentsSetting() async throws {
        let (phone, _, system) = try await ready()
        phone.showCallsInThePhoneApp(false)
        #expect(system.inPhoneApp == false)
        phone.showCallsInThePhoneApp(true)
        #expect(system.inPhoneApp == true)
    }
}

@MainActor
struct FavouritesTests {
    private func store() -> UserDefaults {
        let store = UserDefaults(suiteName: "linx.tests.\(UUID().uuidString)")!
        return store
    }

    @Test("starring someone keeps them at the top, and survives a restart")
    func starring() {
        let store = store()
        let favourites = Favourites(store: store)
        #expect(!favourites.has("1024"))

        favourites.toggle("1024")
        #expect(favourites.has("1024"))
        // A second Favourites reads what the first wrote: this is what
        // "remembered on this phone" means.
        #expect(Favourites(store: store).has("1024"))

        let team = Screen.sampleTeam
        let parts = favourites.split(team)
        #expect(parts.favourites.map(\.extensionNumber) == ["1024"])
        #expect(parts.rest.count == team.count - 1)
        // Nobody is lost or shown twice.
        #expect(parts.favourites.count + parts.rest.count == team.count)

        favourites.toggle("1024")
        #expect(!favourites.has("1024"))
        #expect(Favourites(store: store).extensions.isEmpty)
    }
}

struct AppearanceTests {
    @Test("light, dark, and the phone's own setting")
    func threeChoices() {
        #expect(Appearance.allCases.count == 3)
        #expect(Appearance.system.scheme == nil)
        #expect(Appearance.light.scheme == .light)
        #expect(Appearance.dark.scheme == .dark)
        #expect(Appearance(rawValue: "dark") == .dark)
        // Anything else is the phone's own setting, which is the default.
        #expect(Appearance(rawValue: "nonsense") == nil)
    }
}
