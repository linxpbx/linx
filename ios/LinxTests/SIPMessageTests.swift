import Testing

@testable import Linx

// Reading and writing SIP (RFC 3261 §7). The app writes its own, so these
// check the shapes Asterisk actually sends and the ones Linx's relay lets
// the phone send back (internal/siprelay).

struct SIPMessageTests {
    @Test("a request is read and written back the same")
    func roundTrip() throws {
        let text = [
            "INVITE sip:101@pbx.example.com SIP/2.0",
            "Via: SIP/2.0/WSS abc.invalid;branch=z9hG4bK1;rport",
            "From: \"Sara Haddad\" <sip:d_Ab12@pbx.example.com>;tag=aaa",
            "To: <sip:101@pbx.example.com>",
            "Call-ID: xyz@abc.invalid",
            "CSeq: 1 INVITE",
            "Content-Type: application/sdp",
            "Content-Length: 5",
            "", "v=0\r\n",
        ].joined(separator: "\r\n")
        let message = try #require(SIPMessage(text: text))
        #expect(message.method == "INVITE")
        #expect(message.requestURI == "sip:101@pbx.example.com")
        #expect(message.callID == "xyz@abc.invalid")
        #expect(message.cseq?.number == 1)
        #expect(message.cseq?.method == "INVITE")
        #expect(message.fromTag == "aaa")
        #expect(message.toTag == nil)
        #expect(message.body == "v=0\r\n")
        // Written back, Content-Length tells the truth about the body.
        #expect(message.text.contains("Content-Length: \(message.body.utf8.count)\r\n"))
        #expect(message.text.hasPrefix("INVITE sip:101@pbx.example.com SIP/2.0\r\n"))
    }

    @Test("a response is read, and one is built with the headers that answer a request")
    func responses() throws {
        let invite = try #require(
            SIPMessage(
                text: """
                    INVITE sip:101@pbx.example.com SIP/2.0\r
                    Via: SIP/2.0/WSS a.invalid;branch=z9hG4bK1\r
                    From: <sip:102@pbx.example.com>;tag=theirs\r
                    To: <sip:101@pbx.example.com>\r
                    Call-ID: call-1\r
                    CSeq: 3 INVITE\r
                    \r

                    """))
        let ringing = SIPMessage.response(180, "Ringing", to: invite).withTag("mine")
        #expect(ringing.status == 180)
        #expect(ringing.first("Via") == "SIP/2.0/WSS a.invalid;branch=z9hG4bK1")
        #expect(ringing.first("Call-ID") == "call-1")
        #expect(ringing.first("CSeq") == "3 INVITE")
        #expect(ringing.toTag == "mine")
        // The tag is only ever put on once.
        #expect(ringing.withTag("other").toTag == "mine")
        #expect(ringing.text.hasPrefix("SIP/2.0 180 Ringing\r\n"))

        let read = try #require(SIPMessage(text: "SIP/2.0 486 Busy Here\r\nCall-ID: call-1\r\n\r\n"))
        #expect(read.status == 486)
        #expect(read.reason == "Busy Here")
    }

    @Test("one-letter header names and folded lines are understood")
    func compactAndFolded() throws {
        let message = try #require(
            SIPMessage(
                text: """
                    SIP/2.0 200 OK\r
                    f: <sip:102@pbx.example.com>;tag=x\r
                    i: call-2\r
                    Record-Route: <sip:1.2.3.4:8089;transport=ws;lr>\r
                    Contact: <sip:asterisk@1.2.3.4:8089\r
                     ;transport=ws>\r
                    \r

                    """))
        #expect(message.first("From") == "<sip:102@pbx.example.com>;tag=x")
        #expect(message.callID == "call-2")
        #expect(message.all("Record-Route").count == 1)
        #expect(message.first("Contact") == "<sip:asterisk@1.2.3.4:8089 ;transport=ws>")
    }

    @Test("who a header is about, in the forms Asterisk sends")
    func addresses() {
        #expect(SIPMessage.user(of: "\"Sara\" <sip:101@pbx.example.com>;tag=a") == "101")
        #expect(SIPMessage.user(of: "<sip:+97141230000@pbx.example.com>") == "+97141230000")
        #expect(SIPMessage.user(of: "sip:101@pbx.example.com;tag=a") == "101")
        #expect(SIPMessage.displayName(of: "\"Sara Haddad\" <sip:101@x>") == "Sara Haddad")
        #expect(SIPMessage.displayName(of: "<sip:101@x>") == nil)
        #expect(SIPMessage.uri(in: "\"Sara\" <sip:101@x;transport=ws>;tag=a") == "sip:101@x;transport=ws")
        #expect(SIPMessage.parameter("tag", in: "<sip:101@x>;tag=abc") == "abc")
        #expect(SIPMessage.parameter("tag", in: "<sip:101@x>") == nil)
    }

    @Test("set replaces every copy of a header, add keeps them all")
    func headers() {
        var message = SIPMessage.request("REGISTER", "sip:pbx.example.com")
        message.add("Via", "one")
        message.add("Via", "two")
        #expect(message.all("Via").count == 2)
        message.set("Via", "only")
        #expect(message.all("Via") == ["only"])
        message.remove("Via")
        #expect(message.first("Via") == nil)
    }

    @Test("nonsense is refused, and a keep-alive is not a message")
    func notMessages() {
        #expect(SIPMessage(text: "") == nil)
        #expect(SIPMessage(text: "\r\n\r\n") == nil)
        #expect(SIPMessage(text: "hello there") == nil)
        #expect(SIPMessage(text: "INVITE sip:1@x HTTP/1.1\r\n\r\n") == nil)
    }
}
