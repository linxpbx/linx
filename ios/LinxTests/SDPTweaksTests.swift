import Testing

@testable import Linx

// The Opus settings the phone sends, which are the browser's settings
// (web/src/phone/sdp.ts): a bad network has to degrade the same way in both.

struct SDPTweaksTests {
    private static let offer = """
        v=0\r
        o=- 1 1 IN IP4 127.0.0.1\r
        m=audio 9 UDP/TLS/RTP/SAVPF 111 0 8\r
        a=rtpmap:111 opus/48000/2\r
        a=rtpmap:0 PCMU/8000\r

        """

    @Test("Opus is asked for FEC, DTX, mono and a ceiling")
    func addsSettings() {
        let out = SDPTweaks.preferOpusFecDtx(Self.offer)
        #expect(out.contains("a=fmtp:111 useinbandfec=1;usedtx=1;stereo=0;sprop-stereo=0;maxaveragebitrate=24000"))
        // Nothing else moved, and the line endings are untouched.
        #expect(out.contains("a=rtpmap:0 PCMU/8000"))
        #expect(out.contains("\r\n"))
        #expect(SDPTweaks.opusMaxBitrate == 24000)
    }

    @Test("settings already there are corrected, not repeated")
    func replacesSettings() {
        let withFmtp = Self.offer.replacingOccurrences(
            of: "a=rtpmap:111 opus/48000/2\r\n",
            with: "a=rtpmap:111 opus/48000/2\r\na=fmtp:111 minptime=10;useinbandfec=0;stereo=1\r\n")
        let out = SDPTweaks.preferOpusFecDtx(withFmtp)
        #expect(out.contains("minptime=10"))
        #expect(out.contains("useinbandfec=1"))
        #expect(!out.contains("useinbandfec=0"))
        #expect(!out.contains("stereo=1"))
        #expect(out.components(separatedBy: "a=fmtp:111").count == 2)
    }

    @Test("an SDP with no Opus is left exactly as it was")
    func leavesOthersAlone() {
        let noOpus = "v=0\r\nm=audio 9 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
        #expect(SDPTweaks.preferOpusFecDtx(noOpus) == noOpus)
    }
}
