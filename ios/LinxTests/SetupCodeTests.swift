import Foundation
import Testing

@testable import Linx

/// The three ways a setup code reaches the app, and everything the app
/// refuses (`Core/SetupCode.swift`).
struct SetupCodeTests {
    @Test("a scanned or pasted setup link gives the server and the code")
    func goodLinks() throws {
        let code = try #require(SetupCode.link("https://pbx.example.com/set-up-phone#ey.J.token"))
        #expect(code.server.absoluteString == "https://pbx.example.com")
        #expect(code.token == "ey.J.token")
        #expect(code.code == nil)

        // A server on another port, and a link with spaces around it.
        let onPort = try #require(SetupCode.link("  https://pbx.example.com:8443/set-up-phone#abc\n"))
        #expect(onPort.server.absoluteString == "https://pbx.example.com:8443")
        #expect(onPort.token == "abc")
    }

    @Test("anything that isn't a Linx setup link is refused")
    func badLinks() {
        for bad in [
            "http://pbx.example.com/set-up-phone#abc",  // never plain http
            "https://pbx.example.com/set-up-phone",  // no code in it
            "https://pbx.example.com/#abc",  // not the setup page
            "https://pbx.example.com/sign-in#abc",
            "https://pbx.example.com/set-up-phone?code=abc#abc",  // a query it shouldn't have
            "https://someone@pbx.example.com/set-up-phone#abc",
            "linx://pbx.example.com/set-up-phone#abc",
            "ABCD2345",
            "",
        ] {
            #expect(SetupCode.link(bad) == nil, "accepted \(bad)")
        }
    }

    @Test("typing it by hand forgives case, spaces and dashes")
    func byHand() throws {
        let code = try #require(SetupCode.byHand(server: "pbx.example.com", code: "abcd-2345"))
        #expect(code.code == "ABCD2345")
        #expect(code.token == nil)
        #expect(code.server.absoluteString == "https://pbx.example.com")
        #expect(SetupCode.byHand(server: "https://pbx.example.com/", code: " abcd 2345 ")?.code == "ABCD2345")
        // Too short, too long, and characters that aren't in the alphabet.
        #expect(SetupCode.byHand(server: "pbx.example.com", code: "ABCD234") == nil)
        #expect(SetupCode.byHand(server: "pbx.example.com", code: "ABCD23456") == nil)
        #expect(SetupCode.byHand(server: "pbx.example.com", code: "ABCD2OI1") == nil)
    }

    @Test("an address someone types becomes https and nothing else")
    func addresses() {
        #expect(SetupCode.address(from: "pbx.example.com")?.absoluteString == "https://pbx.example.com")
        #expect(SetupCode.address(from: "HTTPS://PBX.Example.com/")?.absoluteString == "https://pbx.example.com")
        for bad in ["http://pbx.example.com", "pbx.example.com/sign-in", "localhost", "", "pbx example.com"] {
            #expect(SetupCode.address(from: bad) == nil, "accepted \(bad)")
        }
    }
}

/// Linx's times, as Go writes them.
struct LinxTimeTests {
    @Test("times come back with or without a fraction of a second")
    func parsing() throws {
        let plain = try #require(LinxTime.parse("2026-10-03T12:00:00Z"))
        var when = DateComponents()
        (when.year, when.month, when.day, when.hour) = (2026, 10, 3, 12)
        when.timeZone = TimeZone(identifier: "UTC")
        #expect(plain == Calendar(identifier: .gregorian).date(from: when))
        let nanoseconds = try #require(LinxTime.parse("2026-10-03T12:00:00.123456789Z"))
        #expect(nanoseconds == plain)
        #expect(LinxTime.parse("not a time") == nil)
    }
}
