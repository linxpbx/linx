import Testing

@testable import Linx

// Answering Asterisk's challenge (RFC 3261 §22 / RFC 2617).

struct SIPDigestTests {
    @Test("a challenge is read, quotes and all")
    func readsChallenge() throws {
        let challenge = try #require(
            SIPChallenge.parse(
                "Digest realm=\"asterisk\", nonce=\"16a8f4\", opaque=\"op\", algorithm=MD5, qop=\"auth,auth-int\"",
                proxy: false))
        #expect(challenge.realm == "asterisk")
        #expect(challenge.nonce == "16a8f4")
        #expect(challenge.opaque == "op")
        #expect(challenge.qop == "auth")
        #expect(!challenge.stale)
    }

    @Test("a stale nonce says so, and anything but MD5 digest is refused")
    func refusals() {
        #expect(SIPChallenge.parse("Digest realm=\"a\", nonce=\"n\", stale=true", proxy: false)?.stale == true)
        #expect(SIPChallenge.parse("Digest realm=\"a\", nonce=\"n\", algorithm=SHA-256", proxy: false) == nil)
        #expect(SIPChallenge.parse("Basic realm=\"a\"", proxy: false) == nil)
        #expect(SIPChallenge.parse("Digest realm=\"a\"", proxy: false) == nil)
    }

    @Test("the answer is the one RFC 2617 works out by hand")
    func knownAnswer() throws {
        // RFC 2617 §3.5's example, which every digest implementation checks
        // itself against.
        let challenge = try #require(
            SIPChallenge.parse(
                "Digest realm=\"testrealm@host.com\", qop=\"auth,auth-int\", "
                    + "nonce=\"dcd98b7102dd2f0e8b11d0f600bfb0c093\", opaque=\"5ccc069c403ebaf9f0171e9517f40e41\"",
                proxy: false))
        let header = SIPDigest.authorization(
            challenge: challenge, username: "Mufasa", password: "Circle Of Life", method: "GET",
            uri: "/dir/index.html", nonceCount: 1, cnonce: "0a4f113b")
        #expect(header.contains("response=\"6629fae49393a05397450978507c4ef1\""))
        #expect(header.contains("nc=00000001"))
        #expect(header.contains("cnonce=\"0a4f113b\""))
        #expect(header.contains("opaque=\"5ccc069c403ebaf9f0171e9517f40e41\""))
        #expect(header.contains("qop=auth"))
    }

    @Test("a challenge without qop is answered the old way")
    func withoutQop() throws {
        let challenge = try #require(SIPChallenge.parse("Digest realm=\"asterisk\", nonce=\"n\"", proxy: false))
        let header = SIPDigest.authorization(
            challenge: challenge, username: "d_Ab12", password: "secret", method: "REGISTER",
            uri: "sip:pbx.example.com", nonceCount: 1)
        let ha1 = SIPDigest.md5("d_Ab12:asterisk:secret")
        let ha2 = SIPDigest.md5("REGISTER:sip:pbx.example.com")
        #expect(header.contains("response=\"\(SIPDigest.md5("\(ha1):n:\(ha2)"))\""))
        #expect(!header.contains("qop="))
        #expect(!header.contains("secret"))
    }
}
